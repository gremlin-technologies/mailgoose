package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type campaignReport struct {
	CampaignID   string     `json:"campaign_id"`
	Status       string     `json:"status"`
	LaunchedAt   *time.Time `json:"launched_at"`
	Tracking     bool       `json:"tracking"`
	Recipients   int        `json:"recipients"`
	Delivered    int        `json:"delivered"`
	Bounced      int        `json:"bounced"`
	Complaints   int        `json:"complaints"`
	Unsubscribes int        `json:"unsubscribes"`
	Opens        *struct {
		Unique int `json:"unique"`
	} `json:"opens"`
	Clicks *struct {
		Unique int `json:"unique"`
		Total  int `json:"total"`
	} `json:"clicks"`
	CreditsUsed int `json:"credits_used"`
	Links       []struct {
		Position     int    `json:"position"`
		URL          string `json:"url"`
		Varies       bool   `json:"varies"`
		UniqueClicks int    `json:"unique_clicks"`
		TotalClicks  int    `json:"total_clicks"`
	} `json:"links"`
	Archived bool      `json:"archived"`
	AsOf     time.Time `json:"as_of"`
}

// share is n as a share of of (the copies sent, or for opens and clicks
// the copies delivered), when there were any.
func share(n, of int) string {
	if of == 0 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%d (%.1f%%)", n, 100*float64(n)/float64(of))
}

// reportActivities are the recipient lists --who can show (MG-112).
var reportActivities = []string{"opened", "not_opened", "clicked", "not_clicked", "bounced", "complained", "unsubscribed"}

type reportRecipient struct {
	ContactID string     `json:"contact_id"`
	Email     string     `json:"email"`
	FirstName *string    `json:"first_name"`
	LastName  *string    `json:"last_name"`
	At        *time.Time `json:"at"`
}

type reportRecipientPage struct {
	Data       []reportRecipient `json:"data"`
	NextCursor *string           `json:"next_cursor"`
}

func (a *App) campaignReportCmd() *cobra.Command {
	var who, output string
	var link int
	var asCSV bool
	cmd := callsAll([]string{"getCampaignReport", "listCampaignReportRecipients", "exportCampaignReportRecipients"}, &cobra.Command{
		Use:   "report <campaign-id>",
		Short: "Show what happened to a campaign: deliveries, bounces, opens, clicks",
		Long: `Shows a campaign's copies sent, delivered, hard bounced, complaints,
unsubscribes, unique opens, unique and total clicks, credits used, and clicks
per link. Opens and clicks are shares of the copies delivered, the rest of the
copies sent, as in ` + "`mailgoose campaign list`" + `. Opens come from a tracking
image, which some mail apps load without anyone reading, so they can run high;
clicks are exact. The numbers are counted when you ask, from events that
arrive within a minute or two.

--who lists the people behind a number instead: opened, clicked (--link N for
one link, by its # under "Clicks per link"), bounced (hard bounces),
complained or unsubscribed, or not_opened and not_clicked (their copy was
delivered and they haven't). Each row has the contact's address, name and
when it first happened; --json gives every row, and --csv the list as CSV
(to standard output, or to the file --output names). A contact deleted since
the send isn't listed. Raw events are kept 12 months after launch, so older
campaigns have their numbers but no lists.`,
		Example: `  mailgoose campaign report 3f1c...
  mailgoose campaign report 3f1c... --json
  mailgoose campaign report 3f1c... --who clicked --link 2
  mailgoose campaign report 3f1c... --who not_opened --csv --output not-opened.csv`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			if who != "" || link != 0 || asCSV || output != "" {
				return a.reportRecipients(cmd, id, who, link, asCSV, output)
			}
			var rep campaignReport
			if err := a.call(cmd.Context(), http.MethodGet, campaignPath(id)+"/report", nil, &rep, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(rep)
				return nil
			}
			a.accountZone(cmd.Context())
			a.printReport(rep)
			return nil
		},
	})
	cmd.Flags().StringVar(&who, "who", "", "list who: "+strings.Join(reportActivities, ", "))
	cmd.Flags().IntVar(&link, "link", 0, "with --who clicked, only who clicked this link (its # in the report)")
	cmd.Flags().BoolVar(&asCSV, "csv", false, "with --who, the list as CSV")
	cmd.Flags().StringVarP(&output, "output", "o", "", "with --csv, write the CSV to this file instead of standard output")
	return cmd
}

// reportRecipients is `campaign report --who`.
func (a *App) reportRecipients(cmd *cobra.Command, id, who string, link int, asCSV bool, output string) error {
	ctx := cmd.Context()
	if who == "" {
		return usageError("--link, --csv and --output go with --who.", "Pass --who with one of: "+strings.Join(reportActivities, ", ")+".")
	}
	if !slices.Contains(reportActivities, who) {
		return usageError("--who must be one of: "+strings.Join(reportActivities, ", ")+".", "")
	}
	if link != 0 && (who != "clicked" || link < 1) {
		return usageError("--link goes with --who clicked, and is a link's # in the report, from 1.", "")
	}
	if output != "" && !asCSV {
		return usageError("--output goes with --csv.", "Add --csv to write the list as CSV.")
	}
	q := url.Values{"activity": {who}}
	if link > 0 {
		q.Set("link", strconv.Itoa(link))
	}
	if asCSV {
		path := campaignPath(id) + "/report/recipients/export?" + q.Encode()
		if output == "" {
			return a.download(ctx, path, a.Stdout)
		}
		n, err := a.saveCSV(ctx, path, output)
		if err != nil {
			return err
		}
		if a.jsonOut {
			a.printJSON(map[string]any{"file": output, "recipients": n})
			return nil
		}
		fmt.Fprintf(a.Stdout, "Wrote %d %s to %s.\n", n, plural(n, "recipient", "recipients"), output)
		return nil
	}
	q.Set("limit", "100")
	rows := []reportRecipient{}
	for {
		var page reportRecipientPage
		if err := a.call(ctx, http.MethodGet, campaignPath(id)+"/report/recipients?"+q.Encode(), nil, &page, true); err != nil {
			return err
		}
		rows = append(rows, page.Data...)
		if page.NextCursor == nil {
			break
		}
		q.Set("cursor", *page.NextCursor)
	}
	if a.jsonOut {
		a.printJSON(map[string]any{"data": rows})
		return nil
	}
	label := map[string]string{
		"opened": "opened", "not_opened": "got it and didn't open", "clicked": "clicked",
		"not_clicked": "got it and didn't click", "bounced": "hard bounced", "complained": "marked it as spam",
		"unsubscribed": "unsubscribed",
	}[who]
	if link > 0 {
		label += fmt.Sprintf(" link %d", link)
	}
	if len(rows) == 0 {
		fmt.Fprintf(a.Stdout, "Nobody %s.\n", label)
		return nil
	}
	a.accountZone(ctx)
	timed := who != "not_opened" && who != "not_clicked"
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if timed {
		fmt.Fprintln(tw, "EMAIL\tNAME\tWHEN")
	} else {
		fmt.Fprintln(tw, "EMAIL\tNAME")
	}
	for _, r := range rows {
		name := contact{FirstName: r.FirstName, LastName: r.LastName}.name()
		if timed && r.At != nil {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Email, name, r.At.In(a.loc()).Format(timeLayout))
		} else {
			fmt.Fprintf(tw, "%s\t%s\n", r.Email, name)
		}
	}
	_ = tw.Flush()
	fmt.Fprintf(a.Stdout, "\n%d %s %s.\n", len(rows), plural(len(rows), "person", "people"), label)
	return nil
}

func (a *App) printReport(rep campaignReport) {
	if rep.LaunchedAt == nil {
		fmt.Fprintf(a.Stdout, "Campaign %s hasn't been launched (%s): nothing sent yet.\n", rep.CampaignID, rep.Status)
		return
	}
	fmt.Fprintf(a.Stdout, "Campaign %s, %s, launched %s.\n", rep.CampaignID, rep.Status, rep.LaunchedAt.In(a.loc()).Format(timeLayout))
	n := rep.Recipients
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "Sent:\t%d\n", n)
	fmt.Fprintf(tw, "Delivered:\t%s\n", share(rep.Delivered, n))
	fmt.Fprintf(tw, "Bounced:\t%s\n", share(rep.Bounced, n))
	fmt.Fprintf(tw, "Complaints:\t%s\n", share(rep.Complaints, n))
	fmt.Fprintf(tw, "Unsubscribes:\t%s\n", share(rep.Unsubscribes, n))
	if rep.Opens != nil && rep.Clicks != nil {
		fmt.Fprintf(tw, "Opens:\t%s\n", share(rep.Opens.Unique, rep.Delivered))
		fmt.Fprintf(tw, "Clicks:\t%s unique, %d total\n", share(rep.Clicks.Unique, rep.Delivered), rep.Clicks.Total)
	} else {
		fmt.Fprintf(tw, "Opens and clicks:\tnot tracked\n")
	}
	fmt.Fprintf(tw, "Credits used:\t%d\n", rep.CreditsUsed)
	_ = tw.Flush()
	if len(rep.Links) > 0 {
		fmt.Fprintln(a.Stdout, "\nClicks per link:")
		tw = tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "  #\tUNIQUE\tTOTAL\tLINK")
		for _, l := range rep.Links {
			u := l.URL
			if l.Varies {
				u += " (differs per contact)"
			}
			fmt.Fprintf(tw, "  %d\t%d\t%d\t%s\n", l.Position, l.UniqueClicks, l.TotalClicks, u)
		}
		_ = tw.Flush()
	}
	note := "As of " + rep.AsOf.In(a.loc()).Format(timeLayout) + "."
	if rep.Archived {
		note = "Saved " + rep.AsOf.In(a.loc()).Format(timeLayout) + ", when its raw events were deleted, 12 months after launch."
	}
	fmt.Fprintln(a.Stdout, "\n"+note)
}
