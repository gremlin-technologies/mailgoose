package cli

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"
)

// exportCmd is `mailgoose export`: the account's contacts as CSV.
func (a *App) exportCmd() *cobra.Command {
	var output string
	var filters contactFilterFlags
	cmd := callsAll([]string{"exportContacts", "listTags"}, &cobra.Command{
		Use:   "export",
		Short: "Export contacts to a CSV file",
		Long: `Exports the account's contacts as CSV, oldest first: every contact, or those
matching the filters. Columns: email, first_name, last_name, status, tags,
source, created_at, status_changed_at and permission_confirmed (true or false
for imported contacts: whether the import confirmed permission to email
them). "mailgoose import" reads the file back as the same contacts and tags.
The filters are those of "mailgoose contacts list", engagement ones included.

The CSV goes to standard output, or to the file --output names. A cell
starting with =, +, - or @ gets a ' in front, so a spreadsheet doesn't run it
as a formula; import takes it off again.`,
		Example: `  mailgoose export --output contacts.csv
  mailgoose export --tag vip > vip.csv
  mailgoose export --status subscribed --output subscribed.csv --json
  mailgoose export --never-opened --output never-opened.csv`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			q, err := filters.query(ctx, a)
			if err != nil {
				return err
			}
			path := withQuery("/v1/contacts/export", q)
			if output == "" {
				return a.download(ctx, path, a.Stdout)
			}

			n, err := a.saveCSV(ctx, path, output)
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"file": output, "contacts": n})
				return nil
			}
			noun := "contacts"
			if n == 1 {
				noun = "contact"
			}
			fmt.Fprintf(a.Stdout, "Exported %d %s to %s.\n", n, noun, output)
			return nil
		},
	})
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the CSV to this file instead of standard output")
	filters.register(cmd)
	return cmd
}

// saveCSV downloads the CSV at path to the file output and returns how many
// rows it has after the header. The file holds people's data, so only its
// owner can read it; a download cut short leaves no file behind.
func (a *App) saveCSV(ctx context.Context, path, output string) (int, error) {
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the file the user named
	if err != nil {
		return 0, usageError("Can't write "+output+": "+err.Error(), "Pass a path you can write to, or leave out --output for standard output.")
	}
	err = a.download(ctx, path, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(output)
		return 0, err
	}
	return countCSVRows(output)
}

// countCSVRows counts the rows after the header of the CSV file at path.
func countCSVRows(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // the file the command just wrote
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	n := -1
	for {
		_, err := r.Read()
		if errors.Is(err, io.EOF) {
			return max(n, 0), nil
		}
		if err != nil {
			return 0, fmt.Errorf("read %s back: %w", path, err)
		}
		n++
	}
}

// contactDataCmd is `mailgoose contacts export-data`.
func (a *App) contactDataCmd() *cobra.Command {
	return callsAll([]string{"getContactData", "listContacts"}, &cobra.Command{
		Use:   "export-data <email-or-id>",
		Short: "Show everything stored about one contact, as JSON",
		Long: `For a person's request to see their data: prints, as JSON, the contact, when
each tag was put on it, the import that added it and whether that import
confirmed permission to email them, and import rows with the address that
weren't added. "mailgoose contacts delete <email> --erase --yes" removes all
of it.`,
		Example: `  mailgoose contacts export-data pat@example.com > pat.json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.contactID(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			var data json.RawMessage
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/contacts/"+url.PathEscape(id)+"/data", nil, &data, true); err != nil {
				return err
			}
			a.printJSON(data)
			return nil
		},
	})
}
