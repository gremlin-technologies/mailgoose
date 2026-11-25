package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// contactField is the API's ContactField.
type contactField struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type contactFieldPage struct {
	Data       []contactField `json:"data"`
	NextCursor *string        `json:"next_cursor"`
}

// fieldID turns a contact field's id, key or name into its id, looking the
// key or name up (a name ignoring case).
func (a *App) fieldID(ctx context.Context, arg string) (string, error) {
	if domainIDPattern.MatchString(arg) {
		return arg, nil
	}
	q := url.Values{"limit": {"100"}}
	for {
		var page contactFieldPage
		if err := a.call(ctx, http.MethodGet, "/v1/contact-fields?"+q.Encode(), nil, &page, true); err != nil {
			return "", err
		}
		for _, f := range page.Data {
			if f.Key == arg || strings.EqualFold(f.Name, strings.TrimSpace(arg)) {
				return f.ID, nil
			}
		}
		if page.NextCursor == nil {
			return "", &Error{Exit: ExitNotFound, Code: "not_found", Message: fmt.Sprintf("This account has no contact field %q.", arg),
				Hint: "`mailgoose fields list` shows the fields, and `mailgoose fields create` makes one."}
		}
		q.Set("cursor", *page.NextCursor)
	}
}

// fieldValues parses repeated --field key=value flags into a contact's
// fields. With clear, "key=" sends null, which clears the value; without,
// an empty value is left out.
func fieldValues(flags []string, clear bool) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range flags {
		k, v, ok := strings.Cut(f, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, usageError(fmt.Sprintf("--field %q isn't key=value.", f),
				"Pass --field company_size=50; `mailgoose fields list` shows the keys.")
		}
		switch {
		case v != "":
			out[k] = v
		case clear:
			out[k] = nil
		}
	}
	return out, nil
}

func (a *App) fieldsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "fields",
		Aliases: []string{"field"},
		Short:   "Define your own contact fields",
		Long: `Contact fields are text fields of your own beside email, name and tags,
such as company or plan. Each has a name, its label and its column's header
in an export, and a key worked out from the name ("Company size" is
company_size), which contacts use: mailgoose contacts add pat@example.com
--field company_size=50. Each field is a merge tag in emails by its key,
with an optional fallback for contacts with no value: {{company_size|a few}}.
A rename keeps the key and every value, so emails keep working. A field's
id, key or name works wherever a field is asked for.`,
	}

	var limit int
	var cursor string
	list := calls("listContactFields", &cobra.Command{
		Use: "list",
		Example: `  mailgoose fields list
  mailgoose fields list --json`,
		Short: "List contact fields in the order they were made",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			var page contactFieldPage
			if err := a.call(cmd.Context(), http.MethodGet, withQuery("/v1/contact-fields", q), nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No contact fields. Make one with `mailgoose fields create <name>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKEY\tNAME")
			for _, f := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", f.ID, f.Key, f.Name)
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore fields: mailgoose fields list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "fields per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	create := calls("createContactField", &cobra.Command{
		Use:     "create <name>",
		Short:   "Make a contact field",
		Example: `  mailgoose fields create "Company size"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var f contactField
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/contact-fields", map[string]string{"name": args[0]}, &f, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(f)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Created field %q with the key %s (%s).\n", f.Name, f.Key, f.ID)
			return nil
		},
	})

	rename := callsAll([]string{"renameContactField", "listContactFields"}, &cobra.Command{
		Use:     "rename <field> <new-name>",
		Short:   "Rename a contact field; its key and values stay",
		Example: `  mailgoose fields rename company_size "Team size"`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.fieldID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var f contactField
			if err := a.call(cmd.Context(), http.MethodPatch, "/v1/contact-fields/"+url.PathEscape(id), map[string]string{"name": args[1]}, &f, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(f)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Renamed %q to %q; its key is still %s.\n", args[0], f.Name, f.Key)
			return nil
		},
	})

	var yes bool
	del := callsAll([]string{"deleteContactField", "listContactFields"}, &cobra.Command{
		Use:   "delete <field>",
		Short: "Delete a contact field and every contact's value for it",
		Long: `Deletes the field and every contact's value for it, which can't be undone.

A field whose merge tag, such as {{company_size}}, is in the email of a
draft or scheduled campaign isn't deleted: the answer is field_in_use, naming
those emails. Take the tag out of them, or give their campaigns another
email, first.`,
		Example: `  mailgoose fields delete company_size --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (every contact's value for the field is deleted too, and can't be got back)")
			}
			id, err := a.fieldID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var out struct {
				ValuesDeleted int `json:"values_deleted"`
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/contact-fields/"+url.PathEscape(id), nil, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted %q and its values on %d contacts.\n", args[0], out.ValuesDeleted)
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm; the field's values are deleted too")

	cmd.AddCommand(list, create, rename, del)
	return cmd
}

// sortedFieldKeys is a contact's field keys in order, for printing.
func sortedFieldKeys(fields map[string]string) []string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
