package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// maxImportFile matches the API's limit on an import's file.
const maxImportFile = 10 << 20

// importJob is the API's Import.
type importJob struct {
	ID            string   `json:"id"`
	FileName      *string  `json:"file_name"`
	Status        string   `json:"status"`
	ContactStatus string   `json:"contact_status"`
	Tags          []tagRef `json:"tags"`
	Columns       struct {
		Email     string  `json:"email"`
		FirstName *string `json:"first_name"`
		LastName  *string `json:"last_name"`
		Status    *string `json:"status"`
		Tags      *string `json:"tags"`
	} `json:"columns"`
	TotalRows        int            `json:"total_rows"`
	ProcessedRows    int            `json:"processed_rows"`
	Imported         int            `json:"imported"`
	Skipped          int            `json:"skipped"`
	SkippedByReason  map[string]int `json:"skipped_by_reason"`
	Review           string         `json:"review"`
	HoldReason       *string        `json:"hold_reason"`
	HoldMessage      *string        `json:"hold_message"`
	ListSource       *string        `json:"list_source"`
	ListSourceDetail *string        `json:"list_source_detail"`
	Checks           struct {
		InvalidRows         int `json:"invalid_rows"`
		RoleAddresses       int `json:"role_addresses"`
		DisposableAddresses int `json:"disposable_addresses"`
	} `json:"checks"`
	Error      *string    `json:"error"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type importPage struct {
	Data       []importJob `json:"data"`
	NextCursor *string     `json:"next_cursor"`
}

type skippedRow struct {
	Row    int    `json:"row"`
	Email  string `json:"email"`
	Reason string `json:"reason"`
}

// importResult is `import status --json`: the import and every skipped row.
type importResult struct {
	importJob
	SkippedRows []skippedRow `json:"skipped_rows"`
}

func (j importJob) finished() bool { return j.Status == "done" || j.Status == "failed" }

// skipReasons are the API's skip reasons, in the order the CLI shows them.
var skipReasons = []struct{ code, text string }{
	{"invalid_email", "not an email address"},
	{"invalid_row", "row couldn't be read"},
	{"duplicate", "repeats an earlier row"},
	{"exists", "already a contact (left as it is)"},
	{"suppressed", "unsubscribed, bounced or complained before"},
	{"over_limit", "over the free plan's 500 contacts"},
}

// listSources are the API's list sources, in the order the CLI shows them.
var listSources = []struct{ code, text string }{
	{"form", "signed up on my site or form"},
	{"customers", "customers who bought from me"},
	{"personal", "people I know personally"},
	{"export", "another email tool (an export)"},
	{"other", "other, described with --list-source-detail"},
}

func listSourceText(code string) string {
	for _, s := range listSources {
		if s.code == code {
			return s.text
		}
	}
	return code
}

// listSourceHelp lists the list sources for errors and help.
func listSourceHelp() string {
	var b strings.Builder
	for _, s := range listSources {
		fmt.Fprintf(&b, "\n  %-10s %s", s.code, s.text)
	}
	return b.String()
}

// skippedRows fetches every skipped row of an import.
func (a *App) skippedRows(ctx context.Context, id string) ([]skippedRow, error) {
	out := []skippedRow{}
	q := url.Values{"limit": {"100"}}
	for {
		var page struct {
			Data       []skippedRow `json:"data"`
			NextCursor *string      `json:"next_cursor"`
		}
		if err := a.call(ctx, http.MethodGet, "/v1/imports/"+url.PathEscape(id)+"/skipped-rows?"+q.Encode(), nil, &page, true); err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		if page.NextCursor == nil {
			return out, nil
		}
		q.Set("cursor", *page.NextCursor)
	}
}

// printImport shows an import: where it stands, then its skipped rows when
// it has finished.
func (a *App) printImport(ctx context.Context, j importJob) error {
	var rows []skippedRow
	if j.finished() && j.Skipped > 0 {
		var err error
		if rows, err = a.skippedRows(ctx, j.ID); err != nil {
			return err
		}
	}
	if a.jsonOut {
		if rows == nil {
			rows = []skippedRow{}
		}
		a.printJSON(importResult{importJob: j, SkippedRows: rows})
		return nil
	}

	name := ""
	if j.FileName != nil {
		name = " (" + *j.FileName + ")"
	}
	switch j.Status {
	case "queued":
		fmt.Fprintf(a.Stdout, "Import %s%s is waiting to start: %d rows.\n", j.ID, name, j.TotalRows)
	case "running":
		fmt.Fprintf(a.Stdout, "Import %s%s is running: %d of %d rows so far, %d added.\n", j.ID, name, j.ProcessedRows, j.TotalRows, j.Imported)
	case "failed":
		fmt.Fprintf(a.Stdout, "Import %s%s failed after %d of %d rows, with %d contacts added.\n", j.ID, name, j.ProcessedRows, j.TotalRows, j.Imported)
		if j.Error != nil {
			fmt.Fprintln(a.Stdout, *j.Error)
		}
	default:
		fmt.Fprintf(a.Stdout, "Import %s%s is done: %d of %d rows added as %s contacts, %d skipped.\n",
			j.ID, name, j.Imported, j.TotalRows, j.ContactStatus, j.Skipped)
	}
	if j.ListSource != nil {
		src := listSourceText(*j.ListSource)
		if j.ListSourceDetail != nil {
			src = "other: " + *j.ListSourceDetail
		}
		fmt.Fprintf(a.Stdout, "List source: %s.\n", src)
	}
	if c := j.Checks; c.InvalidRows+c.RoleAddresses+c.DisposableAddresses > 0 {
		fmt.Fprintf(a.Stdout, "Checks: %d invalid rows, %d role addresses (like info@), %d disposable-domain addresses.\n",
			c.InvalidRows, c.RoleAddresses, c.DisposableAddresses)
	}
	if j.Review == "held" {
		msg := "mailgoose checks it before these contacts get campaigns."
		if j.HoldMessage != nil {
			msg = *j.HoldMessage
		}
		fmt.Fprintf(a.Stdout, "Held for review. %s Its contacts won't receive campaigns until mailgoose approves it.\n", msg)
	}
	if j.Status == "queued" || j.Status == "running" {
		fmt.Fprintf(a.Stdout, "Check again with: mailgoose import status %s\n", j.ID)
		return nil
	}
	if len(rows) == 0 {
		return nil
	}
	fmt.Fprintln(a.Stdout)
	for _, r := range skipReasons {
		if n := j.SkippedByReason[r.code]; n > 0 {
			fmt.Fprintf(a.Stdout, "  %d %s (%s)\n", n, r.text, r.code)
		}
	}
	fmt.Fprintln(a.Stdout)
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ROW\tEMAIL\tREASON")
	for _, r := range rows {
		email := strings.ReplaceAll(r.Email, "\n", " ")
		if email == "" {
			email = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", r.Row, email, r.Reason)
	}
	return tw.Flush()
}

// readImportFile reads the CSV at path, or standard input for "-".
func (a *App) readImportFile(path string) ([]byte, error) {
	var rd io.Reader
	if path == "-" {
		rd = a.Stdin
	} else {
		f, err := os.Open(path) //nolint:gosec // the file the user named
		if err != nil {
			return nil, usageError("Can't read "+path+": "+err.Error(), "Pass the path of a CSV file, or - for standard input.")
		}
		defer func() { _ = f.Close() }()
		rd = f
	}
	data, err := io.ReadAll(io.LimitReader(rd, maxImportFile+1))
	if err != nil {
		return nil, usageError("Can't read "+path+": "+err.Error(), "Pass the path of a CSV file, or - for standard input.")
	}
	if len(data) > maxImportFile {
		return nil, usageError(fmt.Sprintf("%s is over %d MB.", path, maxImportFile>>20), "Split it into smaller files and import each.")
	}
	return data, nil
}

// importFields are what --map can name, with the API's names.
var importFields = map[string]string{
	"email": "email", "first_name": "first_name", "first-name": "first_name", "last_name": "last_name", "last-name": "last_name",
	"status": "status", "tags": "tags",
}

func (a *App) importCmd() *cobra.Command {
	var maps, tags []string
	var listSource, listSourceDetail string
	var permission, wait bool
	cmd := callsAll([]string{"createImport", "getImport", "listImportSkippedRows"}, &cobra.Command{
		Use:   "import <file.csv>",
		Short: "Import contacts from a CSV file",
		Long: `Imports contacts from a CSV file. The first row is the header. The email,
first name and last name columns are found from common headers ("Email",
"Email Address", "First Name", "Surname" ...); name any others with --map.
Commas, semicolons and tabs all work. Pass - to read the file from standard
input. A file from "mailgoose export" imports as the same contacts and tags.

--list-source is required: say where these people came from.` + listSourceHelp() + `
With other, describe it in --list-source-detail. The source and the time are
kept as each added contact's consent record.

Contacts are added unconfirmed, and don't receive campaigns, unless you pass
--permission-confirmed to say you have these people's permission to email
them. Then they're added subscribed. A "status" column can only hold
contacts back: unsubscribed, bounced and complained rows keep that status,
and unconfirmed rows stay unconfirmed. A "tags" column puts the tags it lists,
separated by commas, on each row's contact, creating tags the account lacks.

Rows with a bad address, a repeat of an earlier row, an address already in the
account, an address that unsubscribed, bounced or complained before, or one
past the free plan's 500 contacts are skipped and listed by
"mailgoose import status". Existing contacts aren't changed.

The file is checked as it's imported: rows that aren't addresses, role
addresses (info@, sales@ ...) and addresses at disposable domains are counted
in the report. The import runs in the background; --wait waits for it to
finish. It's held for review when too many rows are invalid, role or
disposable, or many of its addresses are on lists other accounts imported; so
is an account's first import of more than 2,000 rows, or one that would take
it past its contact limit. A held import's contacts are added but not mailed
until mailgoose approves it, and "mailgoose import status" says why it's held.`,
		Example: `  mailgoose import contacts.csv --list-source form --permission-confirmed --wait
  mailgoose import export.csv --list-source export --map email="E-mail" --map first_name=Vorname --tag newsletter
  mailgoose import fair.csv --list-source other --list-source-detail "Signed up at our trade show stand"
  mailgoose import status <id>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if strings.TrimSpace(listSource) == "" {
				return usageError("--list-source is required: say where these contacts came from.",
					"Pass one of:"+listSourceHelp())
			}
			body["list_source"] = strings.TrimSpace(listSource)
			if strings.TrimSpace(listSourceDetail) != "" {
				body["list_source_detail"] = listSourceDetail
			}
			columns := map[string]string{}
			for _, m := range maps {
				field, header, ok := strings.Cut(m, "=")
				api, known := importFields[strings.ToLower(strings.TrimSpace(field))]
				if !ok || !known || strings.TrimSpace(header) == "" {
					return usageError(fmt.Sprintf("--map %q isn't field=header.", m),
						"The fields are email, first_name, last_name, status and tags, as in --map email=\"Email Address\".")
				}
				columns[api] = header
			}
			data, err := a.readImportFile(args[0])
			if err != nil {
				return err
			}
			body["csv"] = string(data)
			if args[0] != "-" {
				body["file_name"] = filepath.Base(args[0])
			}
			if len(columns) > 0 {
				body["columns"] = columns
			}
			if len(tags) > 0 {
				body["tags"] = tags
			}
			body["permission_confirmed"] = permission

			ctx := cmd.Context()
			var j importJob
			if err := a.call(ctx, http.MethodPost, "/v1/imports", body, &j, true); err != nil {
				return err
			}
			for wait && !j.finished() {
				if err := a.Sleep(ctx, time.Second); err != nil {
					return err
				}
				if err := a.call(ctx, http.MethodGet, "/v1/imports/"+url.PathEscape(j.ID), nil, &j, true); err != nil {
					return err
				}
			}
			if err := a.printImport(ctx, j); err != nil {
				return err
			}
			if !a.jsonOut && !permission {
				fmt.Fprintln(a.Stdout, "\nContacts are added unconfirmed, so they won't receive campaigns. If you have these people's permission to email them, import with --permission-confirmed.")
			}
			return nil
		},
	})
	cmd.Flags().StringArrayVar(&maps, "map", nil, "field=header, naming the column a field comes from (email, first_name, last_name, status, tags); repeatable")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag (name or id) to put on every added contact; repeatable; it must exist")
	cmd.Flags().BoolVar(&permission, "permission-confirmed", false, "you have these people's permission to email them: add them subscribed, not unconfirmed")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the import to finish and show the result")
	cmd.Flags().StringVar(&listSource, "list-source", "", "required: where these contacts came from: form, customers, personal, export or other")
	cmd.Flags().StringVar(&listSourceDetail, "list-source-detail", "", "with --list-source other, a few words on where these contacts came from")

	status := callsAll([]string{"getImport", "listImportSkippedRows"}, &cobra.Command{
		Use:     "status <id>",
		Example: `  mailgoose import status 3f1c... --json`,
		Short:   "Show how an import is going, and every row it skipped",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var j importJob
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/imports/"+url.PathEscape(args[0]), nil, &j, true); err != nil {
				return err
			}
			return a.printImport(cmd.Context(), j)
		},
	})

	var cursor string
	var limit int
	list := calls("listImports", &cobra.Command{
		Use:     "list",
		Example: `  mailgoose import list`,
		Short:   "List imports, newest first",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			path := "/v1/imports"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page importPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No imports yet.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tFILE\tSTATUS\tROWS\tADDED\tSKIPPED\tREVIEW\tSTARTED")
			for _, j := range page.Data {
				file := "-"
				if j.FileName != nil {
					file = *j.FileName
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", j.ID, file, j.Status, j.TotalRows, j.Imported, j.Skipped, j.Review,
					j.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore: mailgoose import list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	list.Flags().IntVar(&limit, "limit", 0, "imports per page, 1 to 100")

	cmd.AddCommand(status, list)
	return cmd
}
