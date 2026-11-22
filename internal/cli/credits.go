package cli

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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
		Use: "credits",
		Example: `  mailgoose credits
  mailgoose credits --json`,
		Short: "Show the credit balance",
		Long: `One credit sends one email to one recipient. Every account, on the free plan
or the paid one, gets free credits each month, on the anniversary of signing
up, and unused free credits expire when the next ones arrive. Accounts on the
paid plan (` + "`mailgoose plan`" + `) can also buy credits in packs, which last 24 months. ` + "`mailgoose credits history`" + ` lists every change.`,
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
				fmt.Fprintf(a.Stdout, "Next free credits: %s\n", sum.NextGrantAt.Local().Format("2006-01-02 15:04"))
			}
			return nil
		},
	})

	var limit int
	var cursor string
	history := calls("listCreditTransactions", &cobra.Command{
		Use: "history",
		Example: `  mailgoose credits history --limit 20
  mailgoose credits history --json`,
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

	buy := calls("createCreditCheckout", &cobra.Command{
		Use:   "buy <pack>",
		Short: "Get a Stripe Checkout link to buy a credit pack",
		Long: `Makes a Stripe Checkout page for a credit pack and prints its link. Open
the link in a browser to pay; the credits arrive as soon as Stripe confirms
the payment, and last 24 months. Packs are for the paid plan: start it
first with ` + "`mailgoose plan upgrade`" + `.

The packs are 10k ($20), 50k ($75), 200k ($240) and 1m ($900). Sales tax, if
any, is added at checkout. Needs a full-scope key.`,
		Example: "  mailgoose credits buy 10k",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out creditCheckout
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/credits/checkout", map[string]string{"pack": args[0]}, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "%s credits for %s, plus any sales tax.\n", groupThousands(out.Credits), dollars(out.PriceCents))
			fmt.Fprintf(a.Stdout, "Pay here (the link works until %s):\n%s\n", out.ExpiresAt.Local().Format("2006-01-02 15:04"), out.URL)
			fmt.Fprintln(a.Stdout, "The credits arrive once Stripe confirms the payment; check with `mailgoose credits`.")
			return nil
		},
	})

	cmd.AddCommand(history, buy)
	return cmd
}

// creditCheckout is the API's CreditCheckout.
type creditCheckout struct {
	URL        string    `json:"url"`
	Pack       string    `json:"pack"`
	Credits    int64     `json:"credits"`
	PriceCents int64     `json:"price_cents"`
	Currency   string    `json:"currency"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// dollars writes 7500 cents as "$75" and 1999 as "$19.99".
func dollars(cents int64) string {
	if cents%100 == 0 {
		return "$" + groupThousands(cents/100)
	}
	return fmt.Sprintf("$%s.%02d", groupThousands(cents/100), cents%100)
}

// groupThousands writes 10000 as "10,000".
func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
