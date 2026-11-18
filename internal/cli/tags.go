package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// tag is the API's Tag.
type tag struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	ContactCount int       `json:"contact_count"`
	CreatedAt    time.Time `json:"created_at"`
}

type tagPage struct {
	Data       []tag   `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

// tagID turns a tag's name or id into its id, looking the name up.
func (a *App) tagID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	var page tagPage
	if err := a.call(ctx, http.MethodGet, "/v1/tags?"+url.Values{"name": {arg}}.Encode(), nil, &page, true); err != nil {
		return "", err
	}
	if len(page.Data) == 0 {
		return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no tag %q.", arg),
			Hint: "`mailgoose tags list` shows the tags, and `mailgoose tags create` makes one."}
	}
	return page.Data[0].ID, nil
}

// selectionFlags pick the contacts a tag change applies to.
type selectionFlags struct {
	email, name, status, tag string
	all                      bool
}

func (f *selectionFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.email, "email", "", "every contact whose address contains this")
	cmd.Flags().StringVar(&f.name, "name", "", "every contact whose name contains this")
	cmd.Flags().StringVar(&f.status, "status", "", "every contact with this status")
	cmd.Flags().StringVar(&f.tag, "tag", "", "every contact with this other tag (name or id)")
	cmd.Flags().BoolVar(&f.all, "all", false, "every contact in the account")
}

// body builds the ContactSelection for the contacts named in args, or for
// the filter flags.
func (a *App) selectionBody(ctx context.Context, f *selectionFlags, args []string) (map[string]any, error) {
	filtered := f.email != "" || f.name != "" || f.status != "" || f.tag != ""
	switch {
	case len(args) > 0 && (filtered || f.all):
		return nil, usageError("Name contacts or use filter flags, not both.", "")
	case len(args) > 0:
		ids := make([]string, 0, len(args))
		for _, arg := range args {
			id, err := a.contactID(ctx, arg)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		return map[string]any{"contact_ids": ids}, nil
	case !filtered && !f.all:
		return nil, usageError("Say which contacts.",
			"Name them (address or id), or pass --email, --name, --status, --tag, or --all for every contact.")
	}
	filter := map[string]any{}
	for k, v := range map[string]string{"email": f.email, "name": f.name, "status": f.status} {
		if v != "" {
			filter[k] = v
		}
	}
	if f.tag != "" {
		id, err := a.tagID(ctx, f.tag)
		if err != nil {
			return nil, err
		}
		filter["tag_id"] = id
	}
	return map[string]any{"filter": filter}, nil
}

func (a *App) tagsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "tags",
		Aliases: []string{"tag"},
		Short:   "Create tags and put them on contacts",
		Long: `Tags group contacts, and a campaign can go to the contacts with any of
chosen tags. A tag's name or id works wherever a tag is asked for; names are
unique ignoring case.`,
	}

	var limit int
	var cursor string
	list := calls("listTags", &cobra.Command{
		Use:   "list",
		Short: "List tags with how many contacts have each",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/tags"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page tagPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No tags. Make one with `mailgoose tags create <name>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tCONTACTS")
			for _, t := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%d\n", t.ID, t.Name, t.ContactCount)
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore tags: mailgoose tags list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "tags per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	create := calls("createTag", &cobra.Command{
		Use:     "create <name>",
		Short:   "Make a tag",
		Example: `  mailgoose tags create newsletter`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var t tag
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/tags", map[string]string{"name": args[0]}, &t, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(t)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Created tag %q (%s).\n", t.Name, t.ID)
			return nil
		},
	})

	rename := callsAll([]string{"renameTag", "listTags"}, &cobra.Command{
		Use:     "rename <tag> <new-name>",
		Short:   "Rename a tag; contacts keep it",
		Example: `  mailgoose tags rename newsletter weekly`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.tagID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var t tag
			if err := a.call(cmd.Context(), http.MethodPatch, "/v1/tags/"+url.PathEscape(id), map[string]string{"name": args[1]}, &t, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(t)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Renamed %q to %q.\n", args[0], t.Name)
			return nil
		},
	})

	var yes bool
	del := callsAll([]string{"deleteTag", "listTags"}, &cobra.Command{
		Use:     "delete <tag>",
		Short:   "Delete a tag and take it off every contact and draft campaign",
		Example: `  mailgoose tags delete newsletter --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (the tag comes off every contact)")
			}
			id, err := a.tagID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var out struct {
				ContactsUntagged int `json:"contacts_untagged"`
				CampaignsUpdated int `json:"campaigns_updated"`
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/tags/"+url.PathEscape(id), nil, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted %q. It came off %d contacts and %d campaigns.\n", args[0], out.ContactsUntagged, out.CampaignsUpdated)
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	change := func(use, op, path, short, verb string) *cobra.Command {
		var sel selectionFlags
		c := callsAll([]string{op, "listTags", "listContacts"}, &cobra.Command{
			Use:   use + " <tag> [<contact>...]",
			Short: short,
			Long: short + `. Name contacts by address or id, or pick them with
--email, --name, --status or --tag (combined), or --all.`,
			Example: fmt.Sprintf(`  mailgoose tags %[1]s vip pat@example.com sam@example.com
  mailgoose tags %[1]s customers --email @example.com --status subscribed`, use),
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				body, err := a.selectionBody(cmd.Context(), &sel, args[1:])
				if err != nil {
					return err
				}
				id, err := a.tagID(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				var out struct {
					Matched int `json:"matched"`
					Changed int `json:"changed"`
				}
				if err := a.call(cmd.Context(), http.MethodPost, "/v1/tags/"+url.PathEscape(id)+path, body, &out, true); err != nil {
					return err
				}
				if a.jsonOut {
					a.printJSON(out)
					return nil
				}
				fmt.Fprintf(a.Stdout, "%s %q: %d contacts changed of %d selected.\n", verb, args[0], out.Changed, out.Matched)
				return nil
			},
		})
		sel.register(c)
		return c
	}

	cmd.AddCommand(list, create, rename, del,
		change("add", "tagContacts", "/contacts", "Put a tag on contacts", "Tagged"),
		change("remove", "untagContacts", "/contacts/remove", "Take a tag off contacts", "Untagged"))
	return cmd
}
