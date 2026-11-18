package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// email is the API's Email. The document stays raw JSON, so `email get
// --json` prints it exactly as the API sent it.
type email struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Version   int             `json:"version"`
	Document  json.RawMessage `json:"document"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type emailSummary struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	Version    int       `json:"version"`
	Subject    string    `json:"subject"`
	BlockCount int       `json:"block_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type emailPage struct {
	Data       []emailSummary `json:"data"`
	NextCursor *string        `json:"next_cursor"`
}

// maxDocumentFile is the most --file reads: the API's request limit.
const maxDocumentFile = 1 << 20

func emailIDArg(arg string) (string, error) {
	if !domainIDPattern.MatchString(arg) {
		return "", usageError(fmt.Sprintf("%q isn't an email id.", arg), "`mailgoose email list` shows the account's emails and their ids.")
	}
	return arg, nil
}

func emailPath(id string) string { return "/v1/emails/" + url.PathEscape(id) }

// readDocumentFile reads --file: a path, or - for standard input.
func (a *App) readDocumentFile(name string) ([]byte, error) {
	var rd io.Reader
	if name == "-" {
		rd = a.Stdin
	} else {
		f, err := os.Open(name) //nolint:gosec // the file the user named
		if err != nil {
			return nil, usageError("Can't read "+name+": "+err.Error(), "Pass a JSON file, or - to read standard input.")
		}
		defer func() { _ = f.Close() }()
		rd = f
	}
	raw, err := io.ReadAll(io.LimitReader(rd, maxDocumentFile+1))
	if err != nil {
		return nil, usageError("Can't read "+name+": "+err.Error(), "")
	}
	if len(raw) > maxDocumentFile {
		return nil, usageError(name+" is over 1 MB.", "")
	}
	if !json.Valid(raw) {
		return nil, usageError(name+" isn't valid JSON.", "Pass a document, or the output of `mailgoose email get <id> --json`.")
	}
	return raw, nil
}

// currentVersion reads an email's version, for commands not given one.
func (a *App) currentVersion(ctx context.Context, id string) (int, error) {
	var e email
	if err := a.call(ctx, http.MethodGet, emailPath(id), nil, &e, true); err != nil {
		return 0, err
	}
	return e.Version, nil
}

func (a *App) printEmail(e email, verb string) {
	if a.jsonOut {
		a.printJSON(e)
		return
	}
	if verb != "" {
		fmt.Fprintf(a.Stdout, "%s %q (%s), version %d.\n", verb, e.Name, e.ID, e.Version)
		return
	}
	var doc struct {
		Settings struct {
			Subject   string  `json:"subject"`
			FromName  string  `json:"from_name"`
			FromEmail *string `json:"from_email"`
		} `json:"settings"`
		Blocks []map[string]any `json:"blocks"`
		HTML   *string          `json:"html"`
	}
	_ = json.Unmarshal(e.Document, &doc)
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Name:\t%s\n", e.Name)
	fmt.Fprintf(tw, "ID:\t%s\n", e.ID)
	fmt.Fprintf(tw, "Kind:\t%s\n", e.Kind)
	fmt.Fprintf(tw, "Version:\t%d\n", e.Version)
	fmt.Fprintf(tw, "Subject:\t%s\n", orDash(doc.Settings.Subject))
	from := doc.Settings.FromName
	if doc.Settings.FromEmail != nil {
		from = strings.TrimSpace(from + " <" + *doc.Settings.FromEmail + ">")
	}
	fmt.Fprintf(tw, "From:\t%s\n", orDash(from))
	_ = tw.Flush()
	if doc.HTML != nil {
		fmt.Fprintf(a.Stdout, "\nHTML: %d bytes. `mailgoose email get %s --json` shows it.\n", len(*doc.HTML), e.ID)
		return
	}
	fmt.Fprintln(a.Stdout, "\nBlocks:")
	for i, b := range doc.Blocks {
		a.printBlock(fmt.Sprintf("%d.", i+1), "  ", b)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printBlock writes one line for a block, and indented lines for what's in
// its columns.
func (a *App) printBlock(num, indent string, b map[string]any) {
	typ, _ := b["type"].(string)
	id, _ := b["id"].(string)
	gist := ""
	for _, k := range []string{"text", "src"} {
		if v, ok := b[k].(string); ok && v != "" {
			gist = strings.ReplaceAll(v, "\n", " ")
			if r := []rune(gist); len(r) > 50 {
				gist = string(r[:49]) + "…"
			}
			gist = strconv.Quote(gist)
			break
		}
	}
	fmt.Fprintf(a.Stdout, "%s%s %s %s %s\n", indent, num, typ, id, gist)
	if cols, ok := b["columns"].([]any); ok {
		for c, col := range cols {
			blocks, _ := col.(map[string]any)["blocks"].([]any)
			fmt.Fprintf(a.Stdout, "%s   column %d:\n", indent, c+1)
			for j, inner := range blocks {
				if m, ok := inner.(map[string]any); ok {
					a.printBlock(fmt.Sprintf("%d.", j+1), indent+"     ", m)
				}
			}
		}
	}
}

func (a *App) emailCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "email",
		Aliases: []string{"emails"},
		Short:   "Create and edit emails",
		Long: `An email is a block document (kind blocks, the default) or hand-written HTML
(kind html), with settings both share: subject, preview text, From, reply-to,
background color and content width.

A block document is JSON: {"schema_version": 1, "settings": {...}, "blocks":
[...]}. Block types are heading, text, image, button, divider, spacer,
columns, social and footer; the API reference lists every field. Each block
has an id that stays the same through edits and moves. Every blocks email
ends with its footer, which holds the postal address and unsubscribe link.

Saves name the version they read, so two edits can't overwrite each other
unseen: a stale one fails with version_conflict.`,
	}

	var limit int
	var cursor string
	list := calls("listEmails", &cobra.Command{
		Use:   "list",
		Short: "List emails, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/emails"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page emailPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No emails. Make one with `mailgoose email create --name <name>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tKIND\tSUBJECT\tUPDATED")
			for _, e := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.ID, e.Name, e.Kind, orDash(e.Subject), e.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore emails: mailgoose email list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "emails per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	var name, kind, file string
	create := calls("createEmail", &cobra.Command{
		Use:   "create",
		Short: "Make an email",
		Long: `Makes an email. Without --file, a blocks email starts with only its footer
and an html email with empty HTML. --file takes a document (- reads standard
input); block ids and fields with defaults can be left out.`,
		Example: `  mailgoose email create --name "Spring menu"
  mailgoose email create --name "Spring menu" --file spring.json
  mailgoose email create --name "Plain" --kind html`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return needFlag("name", "A name")
			}
			body := map[string]any{"name": name, "kind": kind}
			if file != "" {
				raw, err := a.readDocumentFile(file)
				if err != nil {
					return err
				}
				body["document"] = json.RawMessage(raw)
			}
			var e email
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/emails", body, &e, true); err != nil {
				return err
			}
			a.printEmail(e, "Created")
			return nil
		},
	})
	create.Flags().StringVar(&name, "name", "", "the email's name, for you (required)")
	create.Flags().StringVar(&kind, "kind", "blocks", "blocks or html")
	create.Flags().StringVar(&file, "file", "", "a JSON document to start from, or - for standard input")

	get := calls("getEmail", &cobra.Command{
		Use:   "get <email-id>",
		Short: "Show an email; --json prints it whole",
		Long: `Shows an email's settings and an outline of its blocks. With --json it prints
the whole email; "mailgoose email replace <id> --file" takes that output back
unchanged.`,
		Example: `  mailgoose email get 3f1c... --json > spring.json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			var e email
			if err := a.call(cmd.Context(), http.MethodGet, emailPath(id), nil, &e, true); err != nil {
				return err
			}
			a.printEmail(e, "")
			return nil
		},
	})

	var newName, subject, preview, fromName, fromEmail, replyTo, background string
	var width, updateVersion int
	update := callsAll([]string{"updateEmail", "getEmail"}, &cobra.Command{
		Use:   "update <email-id>",
		Short: "Rename an email or change its settings",
		Long: `Changes the name or settings given and leaves the rest. Pass "" to
--from-email or --reply-to to clear it. Without --version the change applies
to the email as it is now.`,
		Example: `  mailgoose email update 3f1c... --subject "Spring is here" --preview-text "New bakes"
  mailgoose email update 3f1c... --from-email news@bakery.example --version 4`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			flags := cmd.Flags()
			settings := map[string]any{}
			for flag, key := range map[string]string{"subject": "subject", "preview-text": "preview_text", "from-name": "from_name", "background-color": "background_color"} {
				if flags.Changed(flag) {
					v, _ := flags.GetString(flag)
					settings[key] = v
				}
			}
			for flag, v := range map[string]string{"from-email": fromEmail, "reply-to": replyTo} {
				if flags.Changed(flag) {
					if v == "" {
						settings[strings.ReplaceAll(flag, "-", "_")] = nil
					} else {
						settings[strings.ReplaceAll(flag, "-", "_")] = v
					}
				}
			}
			if flags.Changed("content-width") {
				settings["content_width"] = width
			}
			body := map[string]any{}
			if flags.Changed("name") {
				body["name"] = newName
			}
			if len(settings) > 0 {
				body["settings"] = settings
			}
			if len(body) == 0 {
				return usageError("Nothing to change.", "Pass --name or a setting such as --subject; `mailgoose email update --help` lists them.")
			}
			v := updateVersion
			if v == 0 {
				if v, err = a.currentVersion(cmd.Context(), id); err != nil {
					return err
				}
			}
			body["version"] = v
			var e email
			if err := a.call(cmd.Context(), http.MethodPatch, emailPath(id), body, &e, true); err != nil {
				return err
			}
			a.printEmail(e, "Updated")
			return nil
		},
	})
	uf := update.Flags()
	uf.StringVar(&newName, "name", "", "a new name")
	uf.StringVar(&subject, "subject", "", "the subject line")
	uf.StringVar(&preview, "preview-text", "", "the line inboxes show after the subject")
	uf.StringVar(&fromName, "from-name", "", "the sender's name")
	uf.StringVar(&fromEmail, "from-email", "", `the From address, at a verified domain ("" clears it)`)
	uf.StringVar(&replyTo, "reply-to", "", `where replies go ("" clears it)`)
	uf.StringVar(&background, "background-color", "", "the background, like #f4f4f5")
	uf.IntVar(&width, "content-width", 0, "content width in pixels, 480 to 800")
	uf.IntVar(&updateVersion, "version", 0, "the version you read; the update fails if the email changed since")

	var replaceFile string
	var replaceVersion int
	replace := callsAll([]string{"replaceEmailDocument", "getEmail"}, &cobra.Command{
		Use:   "replace <email-id> --file <doc.json>",
		Short: "Replace an email's whole document from a JSON file",
		Long: `Replaces the email's settings and content with the document in --file (- reads
standard input). The file can be a document, or the output of "mailgoose email
get <id> --json", whose version is then used. Blocks keep the ids they have;
blocks without one get a new id. Without a version in the file or --version,
the document replaces the email as it is now.`,
		Example: `  mailgoose email get 3f1c... --json > spring.json
  # edit spring.json
  mailgoose email replace 3f1c... --file spring.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			if replaceFile == "" {
				return needFlag("file", "A document file")
			}
			raw, err := a.readDocumentFile(replaceFile)
			if err != nil {
				return err
			}
			doc, v, err := documentFromFile(raw, id)
			if err != nil {
				return err
			}
			if replaceVersion != 0 {
				v = replaceVersion
			}
			if v == 0 {
				if v, err = a.currentVersion(cmd.Context(), id); err != nil {
					return err
				}
			}
			var e email
			body := map[string]any{"version": v, "document": doc}
			if err := a.call(cmd.Context(), http.MethodPut, emailPath(id)+"/document", body, &e, true); err != nil {
				return err
			}
			a.printEmail(e, "Replaced the document of")
			return nil
		},
	})
	replace.Flags().StringVar(&replaceFile, "file", "", "the JSON document, or - for standard input (required)")
	replace.Flags().IntVar(&replaceVersion, "version", 0, "the version you read; the replace fails if the email changed since")

	var dupName string
	duplicate := calls("duplicateEmail", &cobra.Command{
		Use:     "duplicate <email-id>",
		Short:   "Copy an email",
		Example: `  mailgoose email duplicate 3f1c... --name "Summer menu"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			var body any
			if dupName != "" {
				body = map[string]string{"name": dupName}
			}
			var e email
			if err := a.call(cmd.Context(), http.MethodPost, emailPath(id)+"/duplicate", body, &e, true); err != nil {
				return err
			}
			a.printEmail(e, "Created")
			return nil
		},
	})
	duplicate.Flags().StringVar(&dupName, "name", "", `the copy's name (default "Copy of" the original's)`)

	var yes bool
	del := calls("deleteEmail", &cobra.Command{
		Use:     "delete <email-id>",
		Short:   "Delete an email",
		Example: `  mailgoose email delete 3f1c... --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := emailIDArg(args[0])
			if err != nil {
				return err
			}
			if !yes {
				return needFlag("yes", "Confirmation (deleting can't be undone)")
			}
			if err := a.call(cmd.Context(), http.MethodDelete, emailPath(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"deleted": id})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted email %s.\n", id)
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	cmd.AddCommand(list, create, get, update, replace, duplicate, del, a.emailBlocksCmd())
	return cmd
}

// documentFromFile takes a replace file: a document, or a whole email as
// `email get --json` prints it, whose document and version it uses. An
// email file for another email is refused, so a mix-up can't overwrite the
// wrong one.
func documentFromFile(raw []byte, id string) (json.RawMessage, int, error) {
	var whole struct {
		ID       *string         `json:"id"`
		Version  int             `json:"version"`
		Document json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal(raw, &whole); err != nil || whole.Document == nil {
		return bytes.TrimSpace(raw), 0, nil //nolint:nilerr // not an email object: it's the document itself
	}
	if whole.ID != nil && *whole.ID != id {
		return nil, 0, usageError(fmt.Sprintf("The file holds email %s, not %s.", *whole.ID, id),
			"To copy its content into this email, put only its \"document\" in the file.")
	}
	return whole.Document, whole.Version, nil
}
