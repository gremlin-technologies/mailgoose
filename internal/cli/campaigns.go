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

// campaign is the API's Campaign.
type campaign struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	EmailID    *string `json:"email_id"`
	Recipients struct {
		All  bool `json:"all"`
		Tags []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tags"`
	} `json:"recipients"`
	RecipientCount *int       `json:"recipient_count,omitempty"`
	SendAt         *time.Time `json:"send_at"`
	Status         string     `json:"status"`
	Version        int        `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type campaignPage struct {
	Data       []campaign `json:"data"`
	NextCursor *string    `json:"next_cursor"`
}

func campaignIDArg(arg string) (string, error) {
	if !domainIDPattern.MatchString(arg) {
		return "", usageError(fmt.Sprintf("%q isn't a campaign id.", arg), "`mailgoose campaign list` shows the account's campaigns and their ids.")
	}
	return arg, nil
}

func campaignPath(id string) string { return "/v1/campaigns/" + url.PathEscape(id) }

// sendAtLayouts are the local-time forms --send-at takes besides RFC 3339.
var sendAtLayouts = []string{"2006-01-02 15:04", "2006-01-02T15:04"}

// parseSendAt reads --send-at: "now" for when launched (nil), an RFC 3339
// time, or a local date and time.
func parseSendAt(v string) (*time.Time, error) {
	if strings.EqualFold(v, "now") {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	for _, layout := range sendAtLayouts {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return &t, nil
		}
	}
	return nil, usageError(fmt.Sprintf("--send-at %q isn't a time.", v),
		`Use now, a time like 2026-12-01T09:00:00Z, or a local "2026-12-01 09:00".`)
}

// recipientFlags pick who a campaign goes to.
type recipientFlags struct {
	all  bool
	tags []string
}

func (f *recipientFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.all, "all", false, "send to every subscribed contact")
	cmd.Flags().StringArrayVar(&f.tags, "tag", nil, "send to subscribed contacts with this tag (name or id); repeat for any of several")
}

// body is the RecipientsInput for the flags, or nil when neither is given.
func (a *App) recipientsBody(ctx context.Context, f *recipientFlags) (map[string]any, error) {
	switch {
	case f.all && len(f.tags) > 0:
		return nil, usageError("Pass --all or --tag, not both.", "")
	case f.all:
		return map[string]any{"all": true}, nil
	case len(f.tags) == 0:
		return nil, nil
	}
	ids := make([]string, 0, len(f.tags))
	for _, t := range f.tags {
		id, err := a.tagID(ctx, t)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return map[string]any{"tag_ids": ids}, nil
}

func (c campaign) audience() string {
	if c.Recipients.All {
		return "every subscribed contact"
	}
	if len(c.Recipients.Tags) == 0 {
		return "nobody (its tags were deleted; pick recipients with --all or --tag)"
	}
	names := make([]string, len(c.Recipients.Tags))
	for i, t := range c.Recipients.Tags {
		names[i] = t.Name
	}
	return "subscribed contacts tagged " + strings.Join(names, " or ")
}

func (c campaign) sendTime() string {
	if c.SendAt == nil {
		return "when launched"
	}
	return c.SendAt.Local().Format("2006-01-02 15:04 MST")
}

func (a *App) printCampaign(c campaign, verb string) {
	if a.jsonOut {
		a.printJSON(c)
		return
	}
	if verb != "" {
		fmt.Fprintf(a.Stdout, "%s campaign %q (%s), version %d.\n", verb, c.Name, c.ID, c.Version)
	}
	email := "none: its email was deleted; pick one with --email"
	if c.EmailID != nil {
		email = *c.EmailID
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if verb == "" {
		fmt.Fprintf(tw, "Name:\t%s\n", c.Name)
		fmt.Fprintf(tw, "ID:\t%s\n", c.ID)
		fmt.Fprintf(tw, "Status:\t%s\n", c.Status)
		fmt.Fprintf(tw, "Version:\t%d\n", c.Version)
	}
	fmt.Fprintf(tw, "Email:\t%s\n", email)
	fmt.Fprintf(tw, "To:\t%s\n", c.audience())
	if c.RecipientCount != nil {
		fmt.Fprintf(tw, "Recipients now:\t%d\n", *c.RecipientCount)
	}
	fmt.Fprintf(tw, "Send:\t%s\n", c.sendTime())
	_ = tw.Flush()
}

func (a *App) campaignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "campaign",
		Aliases: []string{"campaigns"},
		Short:   "Set up campaigns: an email, who gets it and when",
		Long: `A campaign sends one email to every subscribed contact (--all) or to the
subscribed contacts with any of some tags (--tag), when it's launched or at a
set time (--send-at). Unconfirmed, unsubscribed, bounced and complained
contacts never get it.

A campaign starts as a draft. Its status then moves through scheduled,
sending and sent; cancelled, paused and failed are the other ends. Only
draft and scheduled campaigns can be changed, and only drafts deleted.`,
	}

	var limit int
	var cursor, status string
	list := calls("listCampaigns", &cobra.Command{
		Use:   "list",
		Short: "List campaigns, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if status != "" {
				q.Set("status", status)
			}
			path := "/v1/campaigns"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page campaignPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No campaigns. Make one with `mailgoose campaign create --name <name> --email <email-id> --all`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tSEND\tUPDATED")
			for _, c := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.ID, c.Name, c.Status, c.sendTime(), c.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore campaigns: mailgoose campaign list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "campaigns per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	list.Flags().StringVar(&status, "status", "", "only this status: draft, scheduled, sending, sent, cancelled, paused or failed")

	var name, emailID, sendAt string
	var cr recipientFlags
	create := calls("createCampaign", &cobra.Command{
		Use:   "create",
		Short: "Make a draft campaign",
		Long: `Makes a draft campaign. It needs a name, an email (--email) and recipients:
--all, or one or more --tag. Without --send-at it goes out when launched.
Nothing is sent until it's launched.`,
		Example: `  mailgoose campaign create --name "Spring sale" --email 3f1c... --all
  mailgoose campaign create --name "VIP preview" --email 3f1c... --tag VIP --tag Wholesale --send-at "2026-12-01 09:00"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return needFlag("name", "A name")
			}
			if emailID == "" {
				return needFlag("email", "The email to send (`mailgoose email list` shows them)")
			}
			recipients, err := a.recipientsBody(cmd.Context(), &cr)
			if err != nil {
				return err
			}
			if recipients == nil {
				return usageError("Say who gets it.", "Pass --all for every subscribed contact, or --tag for contacts with a tag.")
			}
			body := map[string]any{"name": name, "email_id": emailID, "recipients": recipients}
			if sendAt != "" {
				at, err := parseSendAt(sendAt)
				if err != nil {
					return err
				}
				body["send_at"] = at
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/campaigns", body, &c, true); err != nil {
				return err
			}
			a.printCampaign(c, "Created")
			return nil
		},
	})
	create.Flags().StringVar(&name, "name", "", "the campaign's name, for you (required)")
	create.Flags().StringVar(&emailID, "email", "", "the id of the email to send (required)")
	create.Flags().StringVar(&sendAt, "send-at", "", `when to send: a time, or now for when launched (the default)`)
	cr.register(create)

	get := calls("getCampaign", &cobra.Command{
		Use:   "get <campaign-id>",
		Short: "Show a campaign and how many it would reach now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodGet, campaignPath(id), nil, &c, true); err != nil {
				return err
			}
			a.printCampaign(c, "")
			return nil
		},
	})

	var newName, newEmail, newSendAt string
	var ur recipientFlags
	var version int
	update := calls("updateCampaign", &cobra.Command{
		Use:   "update <campaign-id>",
		Short: "Change a draft or scheduled campaign",
		Long: `Changes the parts given and leaves the rest. --all or --tag replaces the
recipients. --send-at now sends it when launched. With --version the change
is refused if the campaign changed since you read it.`,
		Example: `  mailgoose campaign update 3f1c... --tag VIP --send-at 2026-12-01T09:00:00Z
  mailgoose campaign update 3f1c... --name "Spring sale, final" --version 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			flags := cmd.Flags()
			body := map[string]any{}
			if flags.Changed("name") {
				body["name"] = newName
			}
			if flags.Changed("email") {
				body["email_id"] = newEmail
			}
			if flags.Changed("send-at") {
				at, err := parseSendAt(newSendAt)
				if err != nil {
					return err
				}
				body["send_at"] = at
			}
			recipients, err := a.recipientsBody(cmd.Context(), &ur)
			if err != nil {
				return err
			}
			if recipients != nil {
				body["recipients"] = recipients
			}
			if len(body) == 0 {
				return usageError("Nothing to change.", "Pass --name, --email, --all, --tag or --send-at.")
			}
			if flags.Changed("version") {
				body["version"] = version
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodPatch, campaignPath(id), body, &c, true); err != nil {
				return err
			}
			a.printCampaign(c, "Updated")
			return nil
		},
	})
	update.Flags().StringVar(&newName, "name", "", "a new name")
	update.Flags().StringVar(&newEmail, "email", "", "the id of another email to send")
	update.Flags().StringVar(&newSendAt, "send-at", "", "when to send: a time, or now for when launched")
	update.Flags().IntVar(&version, "version", 0, "the version you read; the change fails if the campaign changed since")
	ur.register(update)

	var yes bool
	del := calls("deleteCampaign", &cobra.Command{
		Use:     "delete <campaign-id>",
		Short:   "Delete a draft campaign",
		Example: `  mailgoose campaign delete 3f1c... --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			if !yes {
				return needFlag("yes", "Confirmation (deleting can't be undone)")
			}
			if err := a.call(cmd.Context(), http.MethodDelete, campaignPath(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"deleted": id})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted campaign %s.\n", id)
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	var cc recipientFlags
	count := calls("countCampaignRecipients", &cobra.Command{
		Use:   "count",
		Short: "Count who --all or --tag would reach now",
		Example: `  mailgoose campaign count --all
  mailgoose campaign count --tag VIP --tag Wholesale`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			recipients, err := a.recipientsBody(cmd.Context(), &cc)
			if err != nil {
				return err
			}
			if recipients == nil {
				return usageError("Say whom to count.", "Pass --all, or --tag for contacts with a tag.")
			}
			var out struct {
				RecipientCount int `json:"recipient_count"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/campaigns/recipient-count", map[string]any{"recipients": recipients}, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "%d subscribed contacts.\n", out.RecipientCount)
			return nil
		},
	})
	cc.register(count)

	cmd.AddCommand(list, create, get, update, del, count)
	return cmd
}
