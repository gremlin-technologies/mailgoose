package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// maxMailchimpFile matches the API's limit on an uploaded Mailchimp export.
const maxMailchimpFile = 100 << 20

// mailchimpUploadTimeout is how long an upload may take.
const mailchimpUploadTimeout = 10 * time.Minute

// mailchimpNote is the API's MailchimpEmailNote.
type mailchimpNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    *int   `json:"line"`
}

// mailchimpEmail is the API's MailchimpEmail.
type mailchimpEmail struct {
	From     string          `json:"from"`
	File     string          `json:"file"`
	Name     string          `json:"name"`
	Subject  string          `json:"subject"`
	Images   int             `json:"images"`
	Errors   []mailchimpNote `json:"errors"`
	Warnings []mailchimpNote `json:"warnings"`
	EmailID  *string         `json:"email_id"`
}

// mailchimpProgress is the API's MailchimpEmailProgress.
type mailchimpProgress struct {
	Status string           `json:"status"`
	Total  int              `json:"total"`
	Made   []mailchimpEmail `json:"made"`
	Error  *string          `json:"error"`
}

// mailchimpBundle is the API's MailchimpBundle, as far as the CLI shows it.
type mailchimpBundle struct {
	ID          string     `json:"id"`
	FileName    *string    `json:"file_name"`
	Bytes       int        `json:"bytes"`
	Status      string     `json:"status"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ConfirmedAt *time.Time `json:"confirmed_at"`
	ImportID    *string    `json:"import_id"`
	Campaigns   int        `json:"campaigns"`
	Preview     *struct {
		Source    string `json:"source"`
		Audiences []struct {
			Name     string         `json:"name"`
			Members  int            `json:"members"`
			ByStatus map[string]int `json:"by_status"`
		} `json:"audiences"`
		Contacts *struct {
			New                map[string]int `json:"new"`
			ExistingTagged     int            `json:"existing_tagged"`
			ExistingSuppressed int            `json:"existing_suppressed"`
			Skipped            map[string]int `json:"skipped"`
			TagsToCreate       []string       `json:"tags_to_create"`
			RenamedTags        []struct {
				From string `json:"from"`
				To   string `json:"to"`
			} `json:"renamed_tags"`
			MergeSuppressed int      `json:"merge_suppressed"`
			DroppedFields   []string `json:"dropped_fields"`
			Holds           []string `json:"holds"`
			Waits           bool     `json:"waits"`
			Plan            struct {
				Plan       string `json:"plan"`
				Upgrade    bool   `json:"upgrade"`
				Contacts   int    `json:"contacts"`
				Limit      int    `json:"limit"`
				MonthCents int    `json:"month_cents"`
				YearCents  int    `json:"year_cents"`
				Tester     bool   `json:"tester"`
			} `json:"plan"`
		} `json:"contacts"`
		Emails             []mailchimpEmail `json:"emails"`
		CampaignsAvailable int              `json:"campaigns_available"`
		IgnoredFiles       []struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		} `json:"ignored_files"`
	} `json:"preview"`
	Emails mailchimpProgress `json:"emails"`
}

// mailchimpHolds says what each hold a Mailchimp import may get means.
var mailchimpHolds = map[string]string{
	"over_contact_limit": "it would take the account past its contact limit",
	"first_large_import": "it's the account's first import of more than 2,000 contacts",
	"invalid_rows":       "too many rows aren't valid addresses",
	"role_addresses":     "too many are role addresses like info@",
	"disposable":         "too many are at disposable-address domains",
	"shared_list":        "many of its addresses are on lists other accounts imported",
}

// callMailchimp sends a request and decodes the bundle in its response, also
// keeping the response as it came for --json.
func (a *App) callMailchimp(ctx context.Context, method, path string, body any) (mailchimpBundle, json.RawMessage, error) {
	var raw json.RawMessage
	var b mailchimpBundle
	if err := a.call(ctx, method, path, body, &raw, true); err != nil {
		return b, nil, err
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, nil, fmt.Errorf("the API sent a response the CLI can't read: %w", err)
	}
	return b, raw, nil
}

// uploadMailchimp sends the export at path as a multipart upload.
func (a *App) uploadMailchimp(ctx context.Context, path, audience string, groups []string) (mailchimpBundle, json.RawMessage, error) {
	var b mailchimpBundle
	st, err := a.settings()
	if err != nil {
		return b, nil, err
	}
	if st.APIKey == "" {
		return b, nil, authError("Not signed in.", "Run `mailgoose login`, or set "+EnvAPIKey+" to an API key (agents and scripts).")
	}
	f, err := os.Open(path) //nolint:gosec // the file the user named
	if err != nil {
		return b, nil, usageError("Can't read "+path+": "+err.Error(), "Pass the path of Mailchimp's export: the .zip it emails you, or one CSV from it.")
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxMailchimpFile {
		return b, nil, usageError(fmt.Sprintf("%s is over %d MB.", path, maxMailchimpFile>>20),
			"Export each audience on its own from Mailchimp and upload them one at a time.")
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if audience != "" {
		_ = mw.WriteField("audience_name", audience)
	}
	for _, g := range groups {
		_ = mw.WriteField("group_columns", g)
	}
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return b, nil, err
	}
	if _, err := io.Copy(part, io.LimitReader(f, maxMailchimpFile+1)); err != nil {
		return b, nil, usageError("Can't read "+path+": "+err.Error(), "Pass the path of Mailchimp's export.")
	}
	if err := mw.Close(); err != nil {
		return b, nil, err
	}

	client := &http.Client{Timeout: mailchimpUploadTimeout}
	if a.HTTP != nil {
		c := *a.HTTP
		c.Timeout = mailchimpUploadTimeout
		client = &c
	}
	var raw json.RawMessage
	if _, err := a.sendBody(ctx, client, st.APIURL, http.MethodPost, "/v1/imports/mailchimp/bundles", mw.FormDataContentType(),
		&body, &raw, st.APIKey); err != nil {
		return b, nil, err
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, nil, fmt.Errorf("the API sent a response the CLI can't read: %w", err)
	}
	return b, raw, nil
}

// printMailchimpBundle shows an upload: its preview before it's confirmed,
// then how its import and emails are going.
func (a *App) printMailchimpBundle(b mailchimpBundle, raw json.RawMessage) {
	if a.jsonOut {
		a.printJSON(raw)
		return
	}
	w := a.Stdout
	name := ""
	if b.FileName != nil {
		name = " (" + *b.FileName + ")"
	}
	if b.Status == "confirmed" {
		fmt.Fprintf(w, "Mailchimp upload %s%s is confirmed.\n", b.ID, name)
		if b.ImportID != nil {
			fmt.Fprintf(w, "Contacts: import %s. Check it with: mailgoose import status %s\n", *b.ImportID, *b.ImportID)
		} else {
			fmt.Fprintln(w, "Contacts: the export has none.")
		}
		a.printMailchimpEmails(b.Emails)
		return
	}
	fmt.Fprintf(w, "Mailchimp upload %s%s, %s, kept until %s. Nothing is imported yet.\n",
		b.ID, name, sizeText(b.Bytes), b.ExpiresAt.Local().Format("2006-01-02 15:04"))
	p := b.Preview
	if p == nil {
		return
	}
	statuses := []string{"subscribed", "unsubscribed", "cleaned", "pending", "transactional", "archived"}
	for _, au := range p.Audiences {
		var parts []string
		for _, st := range statuses {
			if n := au.ByStatus[st]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, st))
			}
		}
		fmt.Fprintf(w, "Audience %q: %d members (%s).\n", au.Name, au.Members, strings.Join(parts, ", "))
	}
	if c := p.Contacts; c != nil {
		fmt.Fprintf(w, "\nNew contacts: %d subscribed, %d unconfirmed, %d unsubscribed, %d bounced.\n",
			c.New["subscribed"], c.New["unconfirmed"], c.New["unsubscribed"], c.New["bounced"])
		if c.ExistingTagged+c.ExistingSuppressed > 0 {
			fmt.Fprintf(w, "Existing contacts: %d gain tags; %d come off the list as Mailchimp has them unsubscribed or cleaned.\n",
				c.ExistingTagged, c.ExistingSuppressed)
		}
		var skipped []string
		for _, r := range skipReasons {
			if n := c.Skipped[r.code]; n > 0 {
				skipped = append(skipped, fmt.Sprintf("%d %s", n, r.text))
			}
		}
		if len(skipped) > 0 {
			fmt.Fprintf(w, "Skipped: %s.\n", strings.Join(skipped, "; "))
		}
		if c.MergeSuppressed > 0 {
			fmt.Fprintf(w, "%d addresses are in more than one audience and unsubscribed or cleaned in one, so they come in that way.\n", c.MergeSuppressed)
		}
		if len(c.TagsToCreate) > 0 {
			fmt.Fprintf(w, "Tags to create: %s.\n", strings.Join(c.TagsToCreate, ", "))
		}
		for _, t := range c.RenamedTags {
			fmt.Fprintf(w, "Tag %q becomes %q.\n", t.From, t.To)
		}
		if len(c.DroppedFields) > 0 {
			fmt.Fprintf(w, "Fields mailgoose doesn't keep: %s.\n", strings.Join(c.DroppedFields, ", "))
		}
		if c.Plan.Upgrade {
			fmt.Fprintf(w, "Plan: %d contacts needs the paid plan, $%.2f a month or $%.2f a year.\n",
				c.Plan.Contacts, float64(c.Plan.MonthCents)/100, float64(c.Plan.YearCents)/100)
		}
		if c.Waits {
			fmt.Fprintln(w, "The subscribed contacts would take the account past its limit, so they wait for the paid plan; unsubscribed and bounced ones come in now.")
		}
		for _, h := range c.Holds {
			text := mailchimpHolds[h]
			if text == "" {
				text = h
			}
			fmt.Fprintf(w, "Held for review: %s. Held contacts don't get campaigns until mailgoose approves the import.\n", text)
		}
	}
	if len(p.Emails) > 0 {
		templates, campaigns := 0, 0
		for _, e := range p.Emails {
			if e.From == "template" {
				templates++
			} else {
				campaigns++
			}
		}
		fmt.Fprintf(w, "\nEmails: %d templates and the newest %d of %d campaigns, as HTML emails.\n", templates, campaigns, p.CampaignsAvailable)
		a.printMailchimpEmailTable(p.Emails)
	}
	leftOut := 0
	for _, f := range p.IgnoredFiles {
		if f.Reason == "too_many_templates" {
			leftOut++
		}
	}
	if leftOut > 0 {
		fmt.Fprintf(w, "\nOnly the first 100 templates come over as emails; %d more are left out (see --json).\n", leftOut)
	}
	if n := len(p.IgnoredFiles) - leftOut; n > 0 {
		fmt.Fprintf(w, "\n%d files in the export aren't imported (see --json).\n", n)
	}
	fmt.Fprintf(w, "\nTo import it: mailgoose import mailchimp confirm %s --permission-confirmed\n", b.ID)
	if p.CampaignsAvailable > campaignsShown(p.Emails) {
		fmt.Fprintln(w, "Add --campaigns N to bring more of the newest campaigns, up to 100.")
	}
}

func campaignsShown(es []mailchimpEmail) int {
	n := 0
	for _, e := range es {
		if e.From == "campaign" {
			n++
		}
	}
	return n
}

func (a *App) printMailchimpEmailTable(es []mailchimpEmail) {
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FROM\tNAME\tIMAGES\tERRORS\tWARNINGS\tEMAIL")
	for _, e := range es {
		id := "-"
		if e.EmailID != nil {
			id = *e.EmailID
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", e.From, strings.ReplaceAll(e.Name, "\t", " "), e.Images, len(e.Errors), len(e.Warnings), id)
	}
	_ = tw.Flush()
}

func (a *App) printMailchimpEmails(p mailchimpProgress) {
	w := a.Stdout
	switch p.Status {
	case "none":
		fmt.Fprintln(w, "Emails: the export has none.")
		return
	case "queued", "running":
		fmt.Fprintf(w, "Emails: %d of %d made so far.\n", len(p.Made), p.Total)
	case "failed":
		fmt.Fprintf(w, "Emails: stopped after %d of %d.\n", len(p.Made), p.Total)
		if p.Error != nil {
			fmt.Fprintln(w, *p.Error)
		}
	default:
		fmt.Fprintf(w, "Emails: all %d made, as drafts.\n", p.Total)
	}
	if len(p.Made) > 0 {
		a.printMailchimpEmailTable(p.Made)
		fmt.Fprintln(w, "Open one with: mailgoose email get <id>")
	}
}

func (a *App) importMailchimpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mailchimp",
		Short: "Bring contacts, tags and emails over from a Mailchimp export",
		Long: `Imports what Mailchimp's account export holds: each audience's contacts
with their tags and groups, and the templates and newest campaigns as HTML
emails. In Mailchimp, export your account data (Account & billing > Settings >
Manage my data) or an audience, then:

  1. mailgoose import mailchimp upload <export.zip>   shows what it would do
  2. mailgoose import mailchimp confirm <id> --permission-confirmed   does it

Nothing is imported until you confirm. An upload is kept for 7 days, and an
account keeps at most 3 unconfirmed uploads; delete one you don't need.`,
	}

	var audience string
	var groups []string
	upload := calls("createMailchimpBundle", &cobra.Command{
		Use:   "upload <export.zip|audience.csv>",
		Short: "Upload a Mailchimp export and show what importing it would do",
		Long: `Uploads Mailchimp's export, the .zip of the account or an audience, or one
audience CSV from it, up to 25 MB (export a larger account one audience at a
time). Nothing is imported: it shows the
contacts by status, the tags to create, the emails it would make and the plan
the contacts need, and gives the upload's id to confirm.

A single CSV doesn't name its audience: --audience-name does, for the tag
when there's more than one. Group categories are found from the export; name
any others with --group-column.`,
		Example: `  mailgoose import mailchimp upload mailchimp-export.zip
  mailgoose import mailchimp upload subscribed_members.csv --audience-name Newsletter --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, raw, err := a.uploadMailchimp(cmd.Context(), args[0], audience, groups)
			if err != nil {
				return err
			}
			a.printMailchimpBundle(b, raw)
			return nil
		},
	})
	upload.Flags().StringVar(&audience, "audience-name", "", "the audience's name, for a CSV that doesn't say")
	upload.Flags().StringArrayVar(&groups, "group-column", nil, "a column that is a group category; repeatable")

	preview := calls("getMailchimpBundle", &cobra.Command{
		Use:     "preview <id>",
		Short:   "Show an upload: what importing it would do, or how its import is going",
		Example: `  mailgoose import mailchimp preview 3f1c... --json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, raw, err := a.callMailchimp(cmd.Context(), http.MethodGet, "/v1/imports/mailchimp/bundles/"+url.PathEscape(args[0]), nil)
			if err != nil {
				return err
			}
			a.printMailchimpBundle(b, raw)
			return nil
		},
	})

	var permission, wait bool
	var campaigns int
	var key string
	confirm := callsAll([]string{"confirmMailchimpBundle", "getMailchimpBundle"}, &cobra.Command{
		Use:   "confirm <id>",
		Short: "Import an uploaded Mailchimp export",
		Long: `Imports an upload: its contacts and tags, and its templates and newest
campaigns as draft HTML emails, with their Mailchimp-hosted images copied.

--permission-confirmed is required. Mailchimp's subscribed contacts come in
subscribed, with no confirmation email, so it says you have these people's
permission to email them. Pending contacts come in unconfirmed, unsubscribed
ones unsubscribed and cleaned ones bounced.

--campaigns sets how many of the newest campaigns to bring (default 10, up
to 100). The import runs in the background; --wait waits for its emails.
Confirming again shows the same import.`,
		Example: `  mailgoose import mailchimp confirm 3f1c... --permission-confirmed
  mailgoose import mailchimp confirm 3f1c... --permission-confirmed --campaigns 25 --wait --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !permission {
				return usageError("--permission-confirmed is required: it says you have these people's permission to email them.",
					"Mailchimp's subscribed contacts come in subscribed, with no confirmation email. Pass --permission-confirmed once you're sure.")
			}
			body := map[string]any{"permission_confirmed": true}
			if cmd.Flags().Changed("campaigns") {
				body["campaigns"] = campaigns
			}
			if key != "" {
				body["idempotency_key"] = key
			}
			ctx := cmd.Context()
			path := "/v1/imports/mailchimp/bundles/" + url.PathEscape(args[0])
			b, raw, err := a.callMailchimp(ctx, http.MethodPost, path+"/confirm", body)
			if err != nil {
				return err
			}
			for wait && (b.Emails.Status == "queued" || b.Emails.Status == "running") {
				if err := a.Sleep(ctx, 2*time.Second); err != nil {
					return err
				}
				if b, raw, err = a.callMailchimp(ctx, http.MethodGet, path, nil); err != nil {
					return err
				}
			}
			a.printMailchimpBundle(b, raw)
			return nil
		},
	})
	confirm.Flags().BoolVar(&permission, "permission-confirmed", false, "required: you have these people's permission to email them")
	confirm.Flags().IntVar(&campaigns, "campaigns", 10, "how many of the newest campaigns to bring, 1 to 100")
	confirm.Flags().StringVar(&key, "idempotency-key", "", "a key that makes retrying return the same import (default: the export's own)")
	confirm.Flags().BoolVar(&wait, "wait", false, "wait until the emails are made")

	var yes bool
	del := calls("deleteMailchimpBundle", &cobra.Command{
		Use:     "delete <id>",
		Short:   "Delete an upload that hasn't been confirmed",
		Long:    "Deletes an unconfirmed upload with its file. A confirmed upload stays with its import.",
		Example: `  mailgoose import mailchimp delete 3f1c... --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return needFlag("yes", "Confirmation (the upload's file is deleted)")
			}
			if err := a.call(cmd.Context(), http.MethodDelete, "/v1/imports/mailchimp/bundles/"+url.PathEscape(args[0]), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"deleted": args[0]})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted upload %s.\n", args[0])
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	cmd.AddCommand(upload, preview, confirm, del)
	return cmd
}
