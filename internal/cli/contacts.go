package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// contact is the API's Contact.
type contact struct {
	ID              string    `json:"id"`
	Email           string    `json:"email"`
	Tags            []tagRef  `json:"tags"`
	FirstName       *string   `json:"first_name"`
	LastName        *string   `json:"last_name"`
	Status          string    `json:"status"`
	Source          string    `json:"source"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	StatusChangedAt time.Time `json:"status_changed_at"`
}

type tagRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type contactPage struct {
	Data       []contact `json:"data"`
	NextCursor *string   `json:"next_cursor"`
}

func (c contact) name() string {
	var parts []string
	for _, p := range []*string{c.FirstName, c.LastName} {
		if p != nil {
			parts = append(parts, *p)
		}
	}
	return strings.Join(parts, " ")
}

// contactID turns a contact's address or id into its id, looking the
// address up.
func (a *App) contactID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	email := strings.ToLower(strings.TrimSpace(arg))
	q := url.Values{"email": {email}, "limit": {"100"}}
	for {
		var page contactPage
		if err := a.call(ctx, http.MethodGet, "/v1/contacts?"+q.Encode(), nil, &page, true); err != nil {
			return "", err
		}
		// The filter matches addresses containing the text, so pick the exact one.
		for _, c := range page.Data {
			if c.Email == email {
				return c.ID, nil
			}
		}
		if page.NextCursor == nil {
			return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no contact %s.", arg),
				Hint: "`mailgoose contacts list --email <text>` searches addresses."}
		}
		q.Set("cursor", *page.NextCursor)
	}
}

func (c contact) tagNames() string {
	names := make([]string, len(c.Tags))
	for i, t := range c.Tags {
		names[i] = t.Name
	}
	return strings.Join(names, ", ")
}

func (a *App) printContact(c contact) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	name := c.name()
	if name == "" {
		name = "(none)"
	}
	tags := c.tagNames()
	if tags == "" {
		tags = "(none)"
	}
	fmt.Fprintf(tw, "ID\t%s\nEmail\t%s\nName\t%s\nStatus\t%s (since %s)\nTags\t%s\nSource\t%s\nAdded\t%s\n",
		c.ID, c.Email, name, c.Status, c.StatusChangedAt.Local().Format("2006-01-02 15:04"), tags, c.Source,
		c.CreatedAt.Local().Format("2006-01-02 15:04"))
	_ = tw.Flush()
}

const contactStatuses = "unconfirmed, subscribed, unsubscribed, bounced or complained"

func (a *App) contactsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "contacts",
		Aliases: []string{"contact"},
		Short:   "Add, find, change and delete contacts",
		Long: `Contacts are the people campaigns go to; only subscribed ones receive them.
An address or a contact's id works wherever a contact is asked for.

Statuses: unconfirmed (no permission yet), subscribed, unsubscribed, and
bounced or complained, which come from Amazon SES. A contact that
unsubscribed, bounced or complained can't be subscribed again here; the
person can do it through a signup form. Deleting such a contact keeps only a
hash of its address, so adding it again keeps its status.`,
	}

	var first, last, status string
	add := calls("createContact", &cobra.Command{
		Use:   "add <email>",
		Short: "Add a contact",
		Example: `  mailgoose contacts add pat@example.com --first-name Pat --last-name Lee
  mailgoose contacts add sam@example.com --status unconfirmed`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"email": args[0]}
			if first != "" {
				body["first_name"] = first
			}
			if last != "" {
				body["last_name"] = last
			}
			if status != "" {
				body["status"] = status
			}
			var c contact
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/contacts", body, &c, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(c)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Added %s (%s), %s.\n", c.Email, c.ID, c.Status)
			if status != "" && c.Status != status {
				fmt.Fprintf(a.Stdout, "The address was deleted earlier after it %s, so it keeps that status.\n", c.Status)
			}
			return nil
		},
	})
	add.Flags().StringVar(&first, "first-name", "", "first name")
	add.Flags().StringVar(&last, "last-name", "", "last name")
	add.Flags().StringVar(&status, "status", "", "subscribed (default), unconfirmed or unsubscribed")

	var fEmail, fName, fStatus, fTag, cursor string
	var limit int
	list := callsAll([]string{"listContacts", "listTags"}, &cobra.Command{
		Use:   "list",
		Short: "List contacts, newest first",
		Example: `  mailgoose contacts list --status subscribed
  mailgoose contacts list --email example.org --name smith
  mailgoose contacts list --tag vip`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for k, v := range map[string]string{"email": fEmail, "name": fName, "status": fStatus, "cursor": cursor} {
				if v != "" {
					q.Set(k, v)
				}
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if fTag != "" {
				id, err := a.tagID(cmd.Context(), fTag)
				if err != nil {
					return err
				}
				q.Set("tag_id", id)
			}
			path := "/v1/contacts"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page contactPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No contacts match.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tEMAIL\tNAME\tSTATUS\tTAGS\tADDED")
			for _, c := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Email, c.name(), c.Status, c.tagNames(), c.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore contacts: run the same command with --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().StringVar(&fEmail, "email", "", "only addresses containing this")
	list.Flags().StringVar(&fName, "name", "", "only names containing this")
	list.Flags().StringVar(&fStatus, "status", "", "only this status: "+contactStatuses)
	list.Flags().StringVar(&fTag, "tag", "", "only contacts with this tag (name or id)")
	list.Flags().IntVar(&limit, "limit", 0, "contacts per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	get := callsAll([]string{"getContact", "listContacts"}, &cobra.Command{
		Use:     "get <email-or-id>",
		Short:   "Show one contact",
		Example: `  mailgoose contacts get pat@example.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.contactID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var c contact
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/contacts/"+url.PathEscape(id), nil, &c, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(c)
				return nil
			}
			a.printContact(c)
			return nil
		},
	})

	var uEmail, uFirst, uLast, uStatus string
	update := callsAll([]string{"updateContact", "listContacts"}, &cobra.Command{
		Use:   "update <email-or-id>",
		Short: "Change a contact's address, name or status",
		Long: `Changes only the flags given. An empty --first-name or --last-name removes it.
--status takes subscribed, unconfirmed or unsubscribed.`,
		Example: `  mailgoose contacts update pat@example.com --last-name Lee
  mailgoose contacts update pat@example.com --status unsubscribed`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("email") {
				body["email"] = uEmail
			}
			for flag, field := range map[string]string{"first-name": "first_name", "last-name": "last_name"} {
				if cmd.Flags().Changed(flag) {
					v, _ := cmd.Flags().GetString(flag)
					if v == "" {
						body[field] = nil
					} else {
						body[field] = v
					}
				}
			}
			if cmd.Flags().Changed("status") {
				body["status"] = uStatus
			}
			if len(body) == 0 {
				return usageError("Nothing to change.", "Pass at least one of --email, --first-name, --last-name or --status.")
			}
			id, err := a.contactID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var c contact
			if err := a.call(cmd.Context(), http.MethodPatch, "/v1/contacts/"+url.PathEscape(id), body, &c, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(c)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Updated %s.\n\n", c.Email)
			a.printContact(c)
			return nil
		},
	})
	update.Flags().StringVar(&uEmail, "email", "", "new address")
	update.Flags().StringVar(&uFirst, "first-name", "", "first name; empty removes it")
	update.Flags().StringVar(&uLast, "last-name", "", "last name; empty removes it")
	update.Flags().StringVar(&uStatus, "status", "", "subscribed, unconfirmed or unsubscribed")

	var yes bool
	del := callsAll([]string{"deleteContact", "listContacts"}, &cobra.Command{
		Use:   "delete <email-or-id>",
		Short: "Delete a contact for good",
		Long: `Deletes the contact and everything about it. If it unsubscribed, bounced or
complained, a hash of its address stays on the suppression list.`,
		Example: `  mailgoose contacts delete pat@example.com --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (deleting can't be undone)")
			}
			id, err := a.contactID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/contacts/"+url.PathEscape(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"id": id, "deleted": true})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted %s.\n", args[0])
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm; deleting can't be undone")

	cmd.AddCommand(add, list, get, update, del)
	return cmd
}
