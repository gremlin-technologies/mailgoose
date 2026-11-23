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
	Kind          string   `json:"kind"`
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
	ExistingTagged     int `json:"existing_tagged"`
	ExistingSuppressed int `json:"existing_suppressed"`
	// Mailchimp is a Mailchimp import's upload and emails.
	Mailchimp *struct {
		BundleID string            `json:"bundle_id"`
		Emails   mailchimpProgress `json:"emails"`
	} `json:"mailchimp"`
	WaitingNote *string    `json:"waiting_note"`
	Error       *string    `json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type importPage struct {
	Data       []importJob `json:"data"`
	NextCursor *string     `json:"next_cursor"`
}

type skippedRow struct {
	Row    int    `json:"row"`
	Email  string `json:"email"`
	Reason string `json:"reason"`
	// Source is where a Mailchimp import's row came from.
	Source *string `json:"source"`
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
	{"over_limit", "over the free plan's 1,000 contacts"},
	{"not_subscribed", "never subscribed in Mailchimp (transactional)"},
	{"archived", "archived in Mailchimp"},
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
	case "waiting":
		fmt.Fprintf(a.Stdout, "Import %s%s is waiting for the paid plan: %d of %d rows so far, %d added.\n", j.ID, name, j.ProcessedRows, j.TotalRows, j.Imported)
		if j.WaitingNote != nil {
			fmt.Fprintln(a.Stdout, *j.WaitingNote)
		}
		fmt.Fprintf(a.Stdout, "To cancel it: mailgoose import cancel %s\n", j.ID)
	case "cancelled":
		fmt.Fprintf(a.Stdout, "Import %s%s was cancelled while it waited: %d of %d rows were done, %d added. The contacts it took off the list stay off.\n",
			j.ID, name, j.ProcessedRows, j.TotalRows, j.Imported)
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
	if j.ExistingTagged+j.ExistingSuppressed > 0 {
		fmt.Fprintf(a.Stdout, "Existing contacts: %d gained tags, %d came off the list.\n", j.ExistingTagged, j.ExistingSuppressed)
	}
	if j.Mailchimp != nil {
		a.printMailchimpEmails(j.Mailchimp.Emails)
	}
	if j.Status == "queued" || j.Status == "running" || j.Status == "waiting" {
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
		reason := r.Reason
		if r.Source != nil {
			reason += " (" + strings.ReplaceAll(*r.Source, "\t", " ") + ")"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\n", r.Row, email, reason)
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

// importColumns turns --map flags into an import's columns. field= with
// nothing after it leaves that field out of the import.
func importColumns(maps []string) (map[string]string, error) {
	columns := map[string]string{}
	for _, m := range maps {
		field, header, ok := strings.Cut(m, "=")
		api, known := importFields[strings.ToLower(strings.TrimSpace(field))]
		if !ok || !known || (api == "email" && strings.TrimSpace(header) == "") {
			return nil, usageError(fmt.Sprintf("--map %q isn't field=header.", m),
				"The fields are email, first_name, last_name, status and tags, as in --map email=\"Email Address\". Leave the header empty, as in --map status=, to not import a field; email can't be left out.")
		}
		if strings.TrimSpace(header) == "" {
			header = ""
		}
		columns[api] = header
	}
	return columns, nil
}

// fileLabel is the import's file name for the list: "pasted" for a CSV
// import without one, as for contacts pasted in or read from standard input
// (MG-125), and "-" for any other.
func (j importJob) fileLabel() string {
	switch {
	case j.FileName != nil:
		return *j.FileName
	case j.Kind == "csv":
		return "pasted"
	}
	return "-"
}

// importPreview is the API's ImportPreview.
type importPreview struct {
	Separator      string `json:"separator"`
	Rows           int    `json:"rows"`
	HasHeaderGuess bool   `json:"has_header_guess"`
	Columns        []struct {
		Position int      `json:"position"`
		Header   string   `json:"header"`
		Name     string   `json:"name"`
		Field    *string  `json:"field"`
		Samples  []string `json:"samples"`
	} `json:"columns"`
	Problem *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"problem"`
}

// importPreviewCmd is "mailgoose import preview".
func (a *App) importPreviewCmd() *cobra.Command {
	var maps []string
	var noHeader bool
	cmd := calls("previewImport", &cobra.Command{
		Use:   "preview <file.csv>",
		Short: "Show how a CSV file would be imported, without importing it",
		Long: `Reads a CSV file the way "mailgoose import" would, with the same --map
flags, and changes nothing. It shows the separator, how many contact rows
there are, and each column with its first values and the field it becomes.
Columns that aren't matched to a field aren't imported.

Name a column whose header is blank or repeated by its position, as in
--map first_name=#3. --map status= (with nothing after =) leaves a field out,
even when a column's header looks like it.

When the first row is a contact rather than a header, as with rows copied
from a spreadsheet, pass --no-header: columns are then named by position
(#1, #2 ...) and the email column is the one with an address in the first
row. The preview says when the first row looks like a contact.

An agent importing for someone should show them this before importing, and
let them confirm or change which column becomes which field.`,
		Example: `  mailgoose import preview contacts.csv
  mailgoose import preview export.csv --map email="E-mail" --map first_name=#3 --map status=
  pbpaste | mailgoose import preview - --no-header
  pbpaste | mailgoose import preview - --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			columns, err := importColumns(maps)
			if err != nil {
				return err
			}
			data, err := a.readImportFile(args[0])
			if err != nil {
				return err
			}
			body := map[string]any{"csv": string(data)}
			if len(columns) > 0 {
				body["columns"] = columns
			}
			if noHeader {
				body["has_header"] = false
			}
			var p importPreview
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/imports/preview", body, &p, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(p)
				return nil
			}
			return a.printImportPreview(p, noHeader)
		},
	})
	cmd.Flags().StringArrayVar(&maps, "map", nil, "field=header or field=#position, naming the column a field comes from; field= leaves it out; repeatable")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "the first row is a contact, not a header; name columns by position (#1, #2 ...)")
	return cmd
}

func (a *App) printImportPreview(p importPreview, noHeader bool) error {
	rows := "rows"
	if p.Rows == 1 {
		rows = "row"
	}
	fmt.Fprintf(a.Stdout, "Separated by %ss, with %d contact %s.\n\n", p.Separator, p.Rows, rows)
	tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "COLUMN\tFIELD\tFIRST VALUES")
	clean := func(v string) string { return strings.Join(strings.Fields(v), " ") }
	for _, c := range p.Columns {
		field := "not imported"
		if c.Field != nil {
			field = *c.Field
		}
		samples := make([]string, len(c.Samples))
		for i, v := range c.Samples {
			if samples[i] = clean(v); samples[i] == "" {
				samples[i] = "-"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", clean(c.Name), field, strings.Join(samples, ", "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	switch {
	case !noHeader && !p.HasHeaderGuess:
		fmt.Fprintln(a.Stdout, "\nThe first row looks like a contact, not a header. If it is, add --no-header.")
	case noHeader && p.HasHeaderGuess:
		fmt.Fprintln(a.Stdout, "\nThe first row looks like a header. If it is, leave out --no-header.")
	}
	if p.Problem != nil {
		fmt.Fprintf(a.Stdout, "\nThis file can't be imported as it is: %s\n", p.Problem.Message)
		if p.Problem.Code == "no_email_column" || p.Problem.Code == "invalid_mapping" {
			fmt.Fprintln(a.Stdout, "Name the columns with --map, as in --map email=\"Email Address\".")
		}
		return nil
	}
	fmt.Fprintln(a.Stdout, "\nTo import it this way, run \"mailgoose import\" with the same file and flags.")
	return nil
}

// importFields are what --map can name, with the API's names.
var importFields = map[string]string{
	"email": "email", "first_name": "first_name", "first-name": "first_name", "last_name": "last_name", "last-name": "last_name",
	"status": "status", "tags": "tags",
}

func (a *App) importCmd() *cobra.Command {
	var maps, tags []string
	var listSource, listSourceDetail string
	var permission, wait, noHeader bool
	cmd := callsAll([]string{"createImport", "getImport", "listImportSkippedRows"}, &cobra.Command{
		Use:   "import <file.csv>",
		Short: "Import contacts from a CSV file",
		Long: `Imports contacts from a CSV file. The first row is the header, unless
you pass --no-header (see below). The email, first name and last name columns
are found from common headers ("Email", "Email Address", "First Name",
"Surname" ...); name any others with --map,
by header or by position (--map first_name=#3), and leave a field out with
--map status= (nothing after =). Commas, semicolons and tabs all work. Pass -
to read the file from standard input. A file from "mailgoose export" imports
as the same contacts and tags.

Contacts pasted from a spreadsheet often have no header: pass --no-header,
and the first row is imported as a contact. Columns are then named by
position (--map first_name=#1), and the email column is the one with an
address in the first row. Contacts read from standard input have no file
name, and are listed as pasted.

Run "mailgoose import preview" first to see which column becomes which field,
with each column's first values. An agent importing for someone should show
them that and let them confirm it before importing.

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
past the free plan's 1,000 contacts are skipped and listed by
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
  pbpaste | mailgoose import - --no-header --list-source customers --map first_name=#1 --map last_name=#3
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
			columns, err := importColumns(maps)
			if err != nil {
				return err
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
			if noHeader {
				body["has_header"] = false
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
	cmd.Flags().StringArrayVar(&maps, "map", nil, "field=header or field=#position, naming the column a field comes from (email, first_name, last_name, status, tags); field= leaves it out; repeatable")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "tag (name or id) to put on every added contact; repeatable; it must exist")
	cmd.Flags().BoolVar(&permission, "permission-confirmed", false, "you have these people's permission to email them: add them subscribed, not unconfirmed")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the import to finish and show the result")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "the first row is a contact, not a header; name columns by position (#1, #2 ...)")
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
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", j.ID, j.fileLabel(), j.Status, j.TotalRows, j.Imported, j.Skipped, j.Review,
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

	cancel := calls("cancelImport", &cobra.Command{
		Use:     "cancel <id>",
		Example: `  mailgoose import cancel 3f1c...`,
		Short:   "Cancel an import that's waiting for the paid plan",
		Long: "Cancels a Mailchimp import that's waiting for the paid plan. The contacts it already took off the list stay off; " +
			"the rest never come in. Only a waiting import can be cancelled.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var j importJob
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/imports/"+url.PathEscape(args[0])+"/cancel", nil, &j, true); err != nil {
				return err
			}
			return a.printImport(cmd.Context(), j)
		},
	})

	var skippedOutput string
	skipped := calls("exportImportSkippedRows", &cobra.Command{
		Use:   "skipped <id>",
		Short: "Download every row an import skipped, as CSV",
		Long: `Writes every row the import didn't add as CSV, in file order: row (the line
in the file, where the header is 1), source (for a Mailchimp import, the
export file and row), email, reason and why (the reason in a sentence). The
CSV goes to standard output, or to the file --output names.`,
		Example: `  mailgoose import skipped 3f1c... --output skipped.csv`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/v1/imports/" + url.PathEscape(args[0]) + "/skipped-rows/export"
			if skippedOutput == "" {
				return a.download(cmd.Context(), path, a.Stdout)
			}
			n, err := a.saveCSV(cmd.Context(), path, skippedOutput)
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"file": skippedOutput, "rows": n})
				return nil
			}
			noun := "rows"
			if n == 1 {
				noun = "row"
			}
			fmt.Fprintf(a.Stdout, "Wrote %d skipped %s to %s.\n", n, noun, skippedOutput)
			return nil
		},
	})
	skipped.Flags().StringVarP(&skippedOutput, "output", "o", "", "write the CSV to this file instead of standard output")

	cmd.AddCommand(a.importPreviewCmd(), status, skipped, list, cancel, a.importMailchimpCmd())
	return cmd
}
