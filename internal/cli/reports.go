package cli

import (
	"fmt"
	"net/http"
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

func (a *App) campaignReportCmd() *cobra.Command {
	return calls("getCampaignReport", &cobra.Command{
		Use:   "report <campaign-id>",
		Short: "Show what happened to a campaign: deliveries, bounces, opens, clicks",
		Long: `Shows a campaign's copies sent, delivered, hard bounced, complaints,
unsubscribes, unique opens, unique and total clicks, credits used, and clicks
per link. Opens and clicks are shares of the copies delivered, the rest of the
copies sent, as in ` + "`mailgoose campaign list`" + `. Opens come from a tracking
image, which some mail apps load without anyone reading, so they can run high;
clicks are exact. The numbers are counted when you ask, from events that
arrive within a minute or two.`,
		Example: `  mailgoose campaign report 3f1c...
  mailgoose campaign report 3f1c... --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
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
