package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// form is the API's Form.
type form struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Fields []formField `json:"fields"`
	Tags   []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"tags"`
	ButtonText     string    `json:"button_text"`
	SuccessMessage string    `json:"success_message"`
	RedirectURL    *string   `json:"redirect_url"`
	DoubleOptIn    bool      `json:"double_opt_in"`
	ConfirmSubject string    `json:"confirm_subject"`
	ConfirmText    string    `json:"confirm_text"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type formField struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// Required is nil when a --file leaves it out, so the API's default
	// (email required, names optional) applies.
	Required *bool `json:"required,omitempty"`
}

type formPage struct {
	Data       []form  `json:"data"`
	NextCursor *string `json:"next_cursor"`
}

type formEmbed struct {
	ActionURL string `json:"action_url"`
	HTML      string `json:"html"`
	Script    string `json:"script"`
}

func formIDArg(arg string) (string, error) {
	if !domainIDPattern.MatchString(arg) {
		return "", usageError(fmt.Sprintf("%q isn't a form id.", arg), "`mailgoose form list` shows the account's forms and their ids.")
	}
	return arg, nil
}

func formPath(id string) string { return "/v1/forms/" + url.PathEscape(id) }

// formFlags are the parts of a form create and update both take.
type formFlags struct {
	file, name, firstName, lastName, button, success, redirect, confirmSubject, confirmText string
	tags                                                                                    []string
	noTags, doubleOptIn                                                                     bool
}

func (f *formFlags) register(cmd *cobra.Command, update bool) {
	fl := cmd.Flags()
	fl.StringVar(&f.file, "file", "", "a JSON form to start from (- reads standard input); flags override it")
	nameHelp := "the form's name, for you (required)"
	if update {
		nameHelp = "a new name"
	}
	fl.StringVar(&f.name, "name", "", nameHelp)
	fl.StringVar(&f.firstName, "first-name", "", "ask for a first name: off, optional or required")
	fl.StringVar(&f.lastName, "last-name", "", "ask for a last name: off, optional or required")
	fl.StringArrayVar(&f.tags, "tag", nil, "a tag (name or id) for everyone who signs up; repeat for more")
	fl.StringVar(&f.button, "button", "", `the button's text (default "Subscribe")`)
	fl.StringVar(&f.success, "success-message", "", "what people see after signing up")
	fl.StringVar(&f.redirect, "redirect-url", "", `an https:// page to send people to instead ("" for none)`)
	fl.BoolVar(&f.doubleOptIn, "double-opt-in", true, "email people a link to confirm before they're subscribed")
	fl.StringVar(&f.confirmSubject, "confirm-subject", "", "the confirmation email's subject")
	fl.StringVar(&f.confirmText, "confirm-text", "", "the confirmation email's text, above its link")
	if update {
		fl.BoolVar(&f.noTags, "no-tags", false, "take every tag off the form")
	}
}

// formFileHint says what --file takes.
const formFileHint = "Pass a form as JSON, such as the output of `mailgoose form get <id> --json`."

// body builds the request body from --file and the flags, and reports
// whether it holds anything. fields is the form's current list, which
// --first-name and --last-name change; nil for a new form.
func (a *App) formBody(ctx context.Context, cmd *cobra.Command, f *formFlags, fields []formField) (map[string]any, error) {
	body := map[string]any{}
	if f.file != "" {
		raw, err := a.readJSONFile(f.file, formFileHint)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, usageError(f.file+" isn't a JSON object.", formFileHint)
		}
		// A form as `form get --json` prints it: its tags become tag_ids,
		// and the parts the API sets are left out.
		if tags, ok := body["tags"].([]any); ok {
			if _, given := body["tag_ids"]; !given {
				ids := []any{}
				for _, t := range tags {
					if m, ok := t.(map[string]any); ok {
						ids = append(ids, m["id"])
					}
				}
				body["tag_ids"] = ids
			}
		}
		for _, k := range []string{"id", "tags", "created_at", "updated_at"} {
			delete(body, k)
		}
		if raw, ok := body["fields"]; ok && fields != nil {
			// The file's fields are the list --first-name and --last-name change.
			b, _ := json.Marshal(raw)
			fields = nil
			if err := json.Unmarshal(b, &fields); err != nil {
				return nil, usageError(f.file+": fields must be a list of fields.", formFileHint)
			}
		}
	}
	flags := cmd.Flags()
	for flag, key := range map[string]string{"name": "name", "button": "button_text", "success-message": "success_message",
		"redirect-url": "redirect_url", "confirm-subject": "confirm_subject", "confirm-text": "confirm_text"} {
		if flags.Changed(flag) {
			v, _ := flags.GetString(flag)
			body[key] = v
		}
	}
	if flags.Changed("double-opt-in") {
		body["double_opt_in"] = f.doubleOptIn
	}
	if flags.Changed("first-name") || flags.Changed("last-name") {
		if fields == nil {
			if raw, ok := body["fields"]; ok {
				b, _ := json.Marshal(raw)
				_ = json.Unmarshal(b, &fields)
			} else {
				fields = []formField{{Name: "email"}}
			}
		}
		for flag, name := range map[string]string{"first-name": "first_name", "last-name": "last_name"} {
			if !flags.Changed(flag) {
				continue
			}
			v, _ := flags.GetString(flag)
			var err error
			if fields, err = setFormField(fields, name, v, flag); err != nil {
				return nil, err
			}
		}
		body["fields"] = fields
	}
	switch {
	case f.noTags && len(f.tags) > 0:
		return nil, usageError("Pass --tag or --no-tags, not both.", "")
	case f.noTags:
		body["tag_ids"] = []string{}
	case len(f.tags) > 0:
		ids := make([]string, 0, len(f.tags))
		for _, t := range f.tags {
			id, err := a.tagID(ctx, t)
			if err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		body["tag_ids"] = ids
	}
	return body, nil
}

// setFormField turns a field off, or makes it optional or required. A field
// already on the form keeps its label and place; a new one goes last.
func setFormField(fields []formField, name, mode, flag string) ([]formField, error) {
	if mode != "off" && mode != "optional" && mode != "required" {
		return nil, usageError(fmt.Sprintf("--%s takes off, optional or required, not %q.", flag, mode), "")
	}
	out := make([]formField, 0, len(fields)+1)
	found := false
	for _, fl := range fields {
		if fl.Name != name {
			out = append(out, fl)
			continue
		}
		found = true
		if mode != "off" {
			req := mode == "required"
			fl.Required = &req
			out = append(out, fl)
		}
	}
	if !found && mode != "off" {
		req := mode == "required"
		out = append(out, formField{Name: name, Required: &req})
	}
	return out, nil
}

func (a *App) printForm(f form, verb string) {
	if a.jsonOut {
		a.printJSON(f)
		return
	}
	if verb != "" {
		fmt.Fprintf(a.Stdout, "%s form %q (%s).\n", verb, f.Name, f.ID)
		fmt.Fprintf(a.Stdout, "Get its embed code with `mailgoose form embed %s`.\n", f.ID)
		return
	}
	fields := make([]string, len(f.Fields))
	for i, fl := range f.Fields {
		req := "optional"
		if fl.Required != nil && *fl.Required {
			req = "required"
		}
		fields[i] = fmt.Sprintf("%s %q (%s)", fl.Name, fl.Label, req)
	}
	tags := make([]string, len(f.Tags))
	for i, t := range f.Tags {
		tags[i] = t.Name
	}
	doi := "off: people are subscribed when they sign up"
	if f.DoubleOptIn {
		doi = "on: people confirm by email first"
	}
	after := strconv.Quote(f.SuccessMessage)
	if f.RedirectURL != nil {
		after = "go to " + *f.RedirectURL
	}
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Name:\t%s\n", f.Name)
	fmt.Fprintf(tw, "ID:\t%s\n", f.ID)
	fmt.Fprintf(tw, "Fields:\t%s\n", strings.Join(fields, ", "))
	fmt.Fprintf(tw, "Tags:\t%s\n", orDash(strings.Join(tags, ", ")))
	fmt.Fprintf(tw, "Button:\t%q\n", f.ButtonText)
	fmt.Fprintf(tw, "After signup:\t%s\n", after)
	fmt.Fprintf(tw, "Double opt-in:\t%s\n", doi)
	if f.DoubleOptIn {
		fmt.Fprintf(tw, "Confirm subject:\t%q\n", f.ConfirmSubject)
	}
	_ = tw.Flush()
}

func (a *App) formCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "form",
		Aliases: []string{"forms"},
		Short:   "Make signup forms and get their embed code",
		Long: `A signup form collects subscribers on your own site. It asks for an email and,
if you like, a first and last name; everyone who signs up gets the form's
tags. With double opt-in (on by default) people get an email with a link to
confirm before they're subscribed.

"mailgoose form embed <id>" prints the code to paste into a page: a plain
HTML form that works without JavaScript, and an optional script that shows
the result in the page.`,
	}

	var limit int
	var cursor string
	list := calls("listForms", &cobra.Command{
		Use:   "list",
		Short: "List signup forms, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/forms"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page formPage
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No signup forms. Make one with `mailgoose form create --name <name>`.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tFIELDS\tDOUBLE OPT-IN\tUPDATED")
			for _, f := range page.Data {
				names := make([]string, len(f.Fields))
				for i, fl := range f.Fields {
					names[i] = fl.Name
				}
				doi := "off"
				if f.DoubleOptIn {
					doi = "on"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.ID, f.Name, strings.Join(names, ","), doi, f.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore forms: mailgoose form list --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	list.Flags().IntVar(&limit, "limit", 0, "forms per page (1 to 100, default 50)")
	list.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	var cf formFlags
	create := calls("createForm", &cobra.Command{
		Use:   "create",
		Short: "Make a signup form",
		Long: `Makes a signup form. It asks for an email; --first-name and --last-name add
those fields. Parts not given take their defaults: a "Subscribe" button,
double opt-in on, and a success message and confirmation email to match.
--file takes the whole form as JSON, for labels and field order.`,
		Example: `  mailgoose form create --name "Footer"
  mailgoose form create --name "Footer" --first-name optional --tag Newsletter
  mailgoose form create --name "Event" --double-opt-in=false --redirect-url https://bakery.example/thanks`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := a.formBody(cmd.Context(), cmd, &cf, nil)
			if err != nil {
				return err
			}
			if n, _ := body["name"].(string); n == "" {
				return needFlag("name", "A name")
			}
			var f form
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/forms", body, &f, true); err != nil {
				return err
			}
			a.printForm(f, "Created")
			return nil
		},
	})
	cf.register(create, false)

	get := calls("getForm", &cobra.Command{
		Use:     "get <form-id>",
		Short:   "Show a signup form",
		Example: `  mailgoose form get 3f1c... --json > footer.json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := formIDArg(args[0])
			if err != nil {
				return err
			}
			var f form
			if err := a.call(cmd.Context(), http.MethodGet, formPath(id), nil, &f, true); err != nil {
				return err
			}
			a.printForm(f, "")
			return nil
		},
	})

	var uf formFlags
	update := callsAll([]string{"updateForm", "getForm"}, &cobra.Command{
		Use:   "update <form-id>",
		Short: "Change a signup form",
		Long: `Changes the parts given and leaves the rest. --tag replaces the form's tags;
--no-tags takes them all off. Pass "" to --redirect-url to show the success
message again. Embed code already on a site keeps working, but shows the
fields it was made with until you paste the new code.`,
		Example: `  mailgoose form update 3f1c... --button "Join" --last-name off
  mailgoose form update 3f1c... --file footer.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := formIDArg(args[0])
			if err != nil {
				return err
			}
			var fields []formField
			if cmd.Flags().Changed("first-name") || cmd.Flags().Changed("last-name") {
				var cur form
				if err := a.call(cmd.Context(), http.MethodGet, formPath(id), nil, &cur, true); err != nil {
					return err
				}
				fields = cur.Fields
			}
			body, err := a.formBody(cmd.Context(), cmd, &uf, fields)
			if err != nil {
				return err
			}
			if len(body) == 0 {
				return usageError("Nothing to change.", "Pass a part to change, such as --button or --file; `mailgoose form update --help` lists them.")
			}
			var f form
			if err := a.call(cmd.Context(), http.MethodPatch, formPath(id), body, &f, true); err != nil {
				return err
			}
			a.printForm(f, "Updated")
			return nil
		},
	})
	uf.register(update, true)

	var yes bool
	del := calls("deleteForm", &cobra.Command{
		Use:   "delete <form-id>",
		Short: "Delete a signup form",
		Long: `Deletes the form. Contacts who signed up with it stay; its embed code stops
taking signups.`,
		Example: `  mailgoose form delete 3f1c... --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := formIDArg(args[0])
			if err != nil {
				return err
			}
			if !yes {
				return needFlag("yes", "Confirmation (deleting can't be undone)")
			}
			if err := a.call(cmd.Context(), http.MethodDelete, formPath(id), nil, nil, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"deleted": id})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Deleted form %s.\n", id)
			return nil
		},
	})
	del.Flags().BoolVar(&yes, "yes", false, "confirm the delete")

	var noScript bool
	embed := calls("getFormEmbed", &cobra.Command{
		Use:   "embed <form-id>",
		Short: "Print a form's embed code",
		Long: `Prints the code to paste into a page where the form should appear. The form
is plain HTML and works without JavaScript; the script after it is optional
and shows the result in the page instead of leaving it. --no-script leaves
the script out.`,
		Example: `  mailgoose form embed 3f1c... > signup.html`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := formIDArg(args[0])
			if err != nil {
				return err
			}
			var e formEmbed
			if err := a.call(cmd.Context(), http.MethodGet, formPath(id)+"/embed", nil, &e, true); err != nil {
				return err
			}
			if noScript {
				e.Script = ""
			}
			if a.jsonOut {
				a.printJSON(e)
				return nil
			}
			fmt.Fprintln(a.Stdout, "<!-- mailgoose signup form -->")
			fmt.Fprint(a.Stdout, e.HTML)
			if e.Script != "" {
				fmt.Fprintln(a.Stdout, "<!-- Optional: shows the result in the page instead of leaving it -->")
				fmt.Fprint(a.Stdout, e.Script)
			}
			return nil
		},
	})
	embed.Flags().BoolVar(&noScript, "no-script", false, "print only the form, without the optional script")

	cmd.AddCommand(list, create, get, update, del, embed)
	return cmd
}
