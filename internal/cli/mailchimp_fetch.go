package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// mailchimpFetch is the API's MailchimpFetch, as far as the CLI shows it.
type mailchimpFetch struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Step         string  `json:"step"`
	Audience     *string `json:"audience"`
	MembersRead  int     `json:"members_read"`
	MembersTotal int     `json:"members_total"`
	Error        *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	BundleID *string         `json:"bundle_id"`
	Bundle   json.RawMessage `json:"bundle"`
}

// mailchimpFetchPoll is how often fetch checks on its fetch.
const mailchimpFetchPoll = 2 * time.Second

func (a *App) getMailchimpFetch(ctx context.Context, id string) (mailchimpFetch, json.RawMessage, error) {
	var raw json.RawMessage
	var f mailchimpFetch
	if err := a.call(ctx, http.MethodGet, "/v1/imports/mailchimp/fetch/"+url.PathEscape(id), nil, &raw, true); err != nil {
		return f, nil, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, nil, fmt.Errorf("the API sent a response the CLI can't read: %w", err)
	}
	return f, raw, nil
}

// printMailchimpFetch shows a fetch: how it's going, why it failed, or the
// bundle it made.
func (a *App) printMailchimpFetch(f mailchimpFetch, raw json.RawMessage) error {
	if a.jsonOut {
		a.printJSON(raw)
	}
	w := a.Stdout
	switch {
	case f.Status == "failed":
		msg := "it stopped"
		if f.Error != nil {
			msg = f.Error.Message
		}
		return &Error{Exit: ExitError, Code: errorCode(f), Message: "The Mailchimp fetch failed: " + msg}
	case a.jsonOut:
		return nil
	}
	switch f.Status {
	case "queued":
		fmt.Fprintf(w, "Mailchimp fetch %s is waiting to start. Check it with: mailgoose import mailchimp fetch-status %s\n", f.ID, f.ID)
	case "running":
		fmt.Fprintf(w, "Mailchimp fetch %s: %s. Check it with: mailgoose import mailchimp fetch-status %s\n", f.ID, fetchStepText(f), f.ID)
	default:
		if len(f.Bundle) == 0 || string(f.Bundle) == "null" {
			fmt.Fprintf(w, "Mailchimp fetch %s is done; its bundle is gone (deleted or expired).\n", f.ID)
			return nil
		}
		var b mailchimpBundle
		if err := json.Unmarshal(f.Bundle, &b); err != nil {
			return fmt.Errorf("the API sent a response the CLI can't read: %w", err)
		}
		fmt.Fprintf(w, "Fetched %d members from Mailchimp.\n", f.MembersRead)
		a.printMailchimpBundle(b, f.Bundle)
		a.printFetchNotes(f.Bundle)
	}
	return nil
}

func errorCode(f mailchimpFetch) string {
	if f.Error != nil && f.Error.Code != "" {
		return f.Error.Code
	}
	return "fetch_failed"
}

// printFetchNotes adds what only a fetched bundle's preview says.
func (a *App) printFetchNotes(bundle json.RawMessage) {
	var b struct {
		Preview *struct {
			SegmentTags []struct {
				Audience string `json:"audience"`
				Tag      string `json:"tag"`
				Members  int    `json:"members"`
			} `json:"segment_tags"`
			TemplatesNotFetched int `json:"templates_not_fetched"`
		} `json:"preview"`
	}
	if json.Unmarshal(bundle, &b) != nil || b.Preview == nil {
		return
	}
	for _, t := range b.Preview.SegmentTags {
		fmt.Fprintf(a.Stdout, "Tag %q holds the %d members the segment has in %q now; it won't update as the segment changes.\n",
			t.Tag, t.Members, t.Audience)
	}
	if n := b.Preview.TemplatesNotFetched; n > 0 {
		fmt.Fprintf(a.Stdout, "Mailchimp's API doesn't give templates' HTML, so your %d templates aren't included; upload the account export to bring them.\n", n)
	}
}

func fetchStepText(f mailchimpFetch) string {
	switch f.Step {
	case "members":
		aud := ""
		if f.Audience != nil {
			aud = " of " + *f.Audience
		}
		return fmt.Sprintf("reading members%s, %d of %d", aud, f.MembersRead, f.MembersTotal)
	case "segments":
		return "reading saved segments"
	case "campaigns":
		return "reading campaigns"
	}
	return "reading audiences"
}

func (a *App) mailchimpAudiencesCmd() *cobra.Command {
	return calls("listMailchimpAudiences", &cobra.Command{
		Use:   "audiences",
		Short: "List the connected Mailchimp account's audiences",
		Long: `Lists the audiences of the Mailchimp account connected with
` + "`mailgoose import mailchimp connect`" + `, with Mailchimp's own member counts and
whether each asks for double opt-in. Fetch one with
` + "`mailgoose import mailchimp fetch --audience <id>`" + `.`,
		Example: `  mailgoose import mailchimp audiences
  mailgoose import mailchimp audiences --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var raw json.RawMessage
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/imports/mailchimp/audiences", nil, &raw, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(raw)
				return nil
			}
			var out struct {
				Audiences []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					DoubleOptin bool   `json:"double_optin"`
					Members     struct {
						Subscribed   int `json:"subscribed"`
						Unsubscribed int `json:"unsubscribed"`
						Cleaned      int `json:"cleaned"`
						Total        int `json:"total"`
					} `json:"members"`
				} `json:"audiences"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				return fmt.Errorf("the API sent a response the CLI can't read: %w", err)
			}
			if len(out.Audiences) == 0 {
				fmt.Fprintln(a.Stdout, "The connected Mailchimp account has no audiences.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tSUBSCRIBED\tUNSUBSCRIBED\tCLEANED\tDOUBLE OPT-IN")
			for _, au := range out.Audiences {
				optin := "no"
				if au.DoubleOptin {
					optin = "yes"
				}
				fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n", au.ID, strings.ReplaceAll(au.Name, "\t", " "),
					au.Members.Subscribed, au.Members.Unsubscribed, au.Members.Cleaned, optin)
			}
			_ = tw.Flush()
			fmt.Fprintln(a.Stdout, "\nFetch one with: mailgoose import mailchimp fetch --audience <id>")
			return nil
		},
	})
}

func (a *App) mailchimpFetchCmd() *cobra.Command {
	var audiences, segments []string
	var campaigns int
	var noWait bool
	cmd := callsAll([]string{"createMailchimpFetch", "getMailchimpFetch"}, &cobra.Command{
		Use:   "fetch --audience <id>",
		Short: "Read audiences and campaigns from the connected Mailchimp account",
		Long: `Reads the chosen audiences from the connected Mailchimp account: every
member whatever their status, with merge fields, groups, tags, opt-in and
signup times and IPs and GDPR permissions, and the content of the newest sent
campaigns. Each --segment (a saved segment's id) becomes a tag
"segment: <name>" on the members it has now; the tag doesn't update later.

It waits for the fetch and shows what importing it would do, like an upload;
--no-wait returns at once (check it with fetch-status). Nothing is imported
until you confirm it:

  mailgoose import mailchimp confirm <id> --permission-confirmed

Confirming forgets the Mailchimp connection. Mailchimp's API doesn't give
templates' HTML, so upload the account export for those.`,
		Example: `  mailgoose import mailchimp fetch --audience a1b2c3d4e5
  mailgoose import mailchimp fetch --audience a1b2c3d4e5 --segment 40213 --campaigns 25 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(audiences) == 0 {
				return needFlag("audience", "An audience id (from `mailgoose import mailchimp audiences`)")
			}
			body := map[string]any{"audience_ids": audiences}
			if len(segments) > 0 {
				body["segment_ids"] = segments
			}
			if cmd.Flags().Changed("campaigns") {
				body["campaigns"] = campaigns
			}
			ctx := cmd.Context()
			var raw json.RawMessage
			if err := a.call(ctx, http.MethodPost, "/v1/imports/mailchimp/fetch", body, &raw, true); err != nil {
				return err
			}
			var f mailchimpFetch
			if err := json.Unmarshal(raw, &f); err != nil {
				return fmt.Errorf("the API sent a response the CLI can't read: %w", err)
			}
			shown := ""
			for !noWait && (f.Status == "queued" || f.Status == "running") {
				if !a.jsonOut && f.Status == "running" {
					if line := fetchStepText(f); line != shown {
						fmt.Fprintln(a.Stderr, "Mailchimp: "+line)
						shown = line
					}
				}
				if err := a.Sleep(ctx, mailchimpFetchPoll); err != nil {
					return err
				}
				var err error
				if f, raw, err = a.getMailchimpFetch(ctx, f.ID); err != nil {
					return err
				}
			}
			return a.printMailchimpFetch(f, raw)
		},
	})
	cmd.Flags().StringArrayVar(&audiences, "audience", nil, "a Mailchimp audience id; repeatable, up to 20")
	cmd.Flags().StringArrayVar(&segments, "segment", nil, "a saved segment id of one of those audiences; repeatable, up to 20")
	cmd.Flags().IntVar(&campaigns, "campaigns", 10, "how many of the newest sent campaigns to read, 0 to 100")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the fetch is queued")
	return cmd
}

func (a *App) mailchimpFetchStatusCmd() *cobra.Command {
	return calls("getMailchimpFetch", &cobra.Command{
		Use:     "fetch-status <id>",
		Short:   "Show how a Mailchimp fetch is going, or what it fetched",
		Example: `  mailgoose import mailchimp fetch-status 7d2e... --json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, raw, err := a.getMailchimpFetch(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printMailchimpFetch(f, raw)
		},
	})
}
