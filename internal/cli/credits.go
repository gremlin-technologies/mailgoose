package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// creditSummary is the API's CreditSummary.
type creditSummary struct {
	Balance     int64      `json:"balance"`
	Plan        string     `json:"plan"`
	NextGrantAt *time.Time `json:"next_grant_at"`
}

// creditTransaction is the API's CreditTransaction.
type creditTransaction struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Amount    int64      `json:"amount"`
	ExpiresAt *time.Time `json:"expires_at"`
	Ref       string     `json:"ref"`
	CreatedAt time.Time  `json:"created_at"`
}

func (a *App) creditsCmd() *cobra.Command {
	cmd := calls("getCredits", &cobra.Command{
		Use:   "credits",
		Short: "Show the credit balance",
		Long: `One credit sends one email to one recipient. Free accounts get 1,000
credits a month, on the anniversary of signing up, and unused free credits
expire when the next ones arrive. ` + "`mailgoose credits history`" + ` lists every change.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var sum creditSummary
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/credits", nil, &sum, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(sum)
				return nil
			}
			fmt.Fprintf(a.Stdout, "%s credits (%s plan)\n", strconv.FormatInt(sum.Balance, 10), sum.Plan)
			if sum.NextGrantAt != nil {
				fmt.Fprintf(a.Stdout, "Next 1,000 free credits: %s\n", sum.NextGrantAt.Local().Format("2006-01-02 15:04"))
			}
			return nil
		},
	})

	var limit int
	var cursor string
	history := calls("listCreditTransactions", &cobra.Command{
		Use:   "history",
		Short: "List credit transactions, newest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/credits/transactions"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page struct {
				Data       []creditTransaction `json:"data"`
				NextCursor *string             `json:"next_cursor"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No credit transactions yet.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tTYPE\tAMOUNT\tEXPIRES\tFOR")
			for _, t := range page.Data {
				expires := ""
				if t.ExpiresAt != nil {
					expires = t.ExpiresAt.Local().Format("2006-01-02")
				}
				fmt.Fprintf(tw, "%s\t%s\t%+d\t%s\t%s\n", t.CreatedAt.Local().Format("2006-01-02 15:04"), t.Type, t.Amount, expires, t.Ref)
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore: mailgoose credits history --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	history.Flags().IntVar(&limit, "limit", 0, "transactions per page (1 to 100, default 50)")
	history.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")

	cmd.AddCommand(history)
	return cmd
}
