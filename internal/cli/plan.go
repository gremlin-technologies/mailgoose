package cli

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

// plan is the API's Plan.
type plan struct {
	Plan              string     `json:"plan"`
	Status            *string    `json:"status"`
	Interval          *string    `json:"interval"`
	Units             int        `json:"units"`
	CurrentPeriodEnd  *time.Time `json:"current_period_end"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	Contacts          int        `json:"contacts"`
	UnitsNeeded       int        `json:"units_needed"`
	Prices            struct {
		MonthCents int64 `json:"month_cents"`
		YearCents  int64 `json:"year_cents"`
	} `json:"prices"`
}

// planCheckout is the API's PlanCheckout.
type planCheckout struct {
	URL            string    `json:"url"`
	Interval       string    `json:"interval"`
	Units          int       `json:"units"`
	UnitPriceCents int64     `json:"unit_price_cents"`
	Currency       string    `json:"currency"`
	ExpiresAt      time.Time `json:"expires_at"`
}

const planHelp = `The paid plan is a card subscription: $5 a month, or $50 a year. It
covers up to 2,500 contacts; each further 10,000 contacts, or part of them,
adds the same again. Paid accounts buy credits in packs (` + "`mailgoose credits buy`" + `)
instead of getting 1,000 free credits a month. Contacts count while they're
subscribed or waiting to confirm. Sales tax, if any, is added at checkout.`

func (a *App) planCmd() *cobra.Command {
	cmd := calls("getPlan", &cobra.Command{
		Use: "plan",
		Example: `  mailgoose plan
  mailgoose plan --json`,
		Short: "Show the account's plan",
		Long:  planHelp,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var p plan
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/plan", nil, &p, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(p)
				return nil
			}
			if p.Plan != "paid" {
				fmt.Fprintf(a.Stdout, "Free plan, %s counted contacts.\n", groupThousands(int64(p.Contacts)))
				fmt.Fprintf(a.Stdout, "The paid plan would be %s a month or %s a year for them: `mailgoose plan upgrade`.\n",
					dollars(p.Prices.MonthCents*int64(p.UnitsNeeded)), dollars(p.Prices.YearCents*int64(p.UnitsNeeded)))
				return nil
			}
			interval, status := "month", ""
			if p.Interval != nil {
				interval = *p.Interval
			}
			if p.Status != nil {
				status = *p.Status
			}
			each := p.Prices.MonthCents
			if interval == "year" {
				each = p.Prices.YearCents
			}
			fmt.Fprintf(a.Stdout, "Paid plan, %s a %s (%d × %s), %s counted contacts.\n",
				dollars(each*int64(p.Units)), interval, p.Units, dollars(each), groupThousands(int64(p.Contacts)))
			if p.CurrentPeriodEnd != nil {
				when := p.CurrentPeriodEnd.Local().Format("2006-01-02")
				if p.CancelAtPeriodEnd {
					fmt.Fprintf(a.Stdout, "Cancelled: it ends on %s, then the account is on the free plan.\n", when)
				} else {
					fmt.Fprintf(a.Stdout, "Renews on %s.\n", when)
				}
			}
			if p.UnitsNeeded != p.Units && !p.CancelAtPeriodEnd {
				fmt.Fprintf(a.Stdout, "The contacts now make it %s a %s from the next renewal.\n",
					dollars(each*int64(p.UnitsNeeded)), interval)
			}
			if status == "past_due" || status == "unpaid" {
				fmt.Fprintln(a.Stdout, "The last payment didn't go through, so sending is paused. Update the card: `mailgoose plan manage`.")
			}
			return nil
		},
	})

	var yearly bool
	upgrade := calls("createPlanCheckout", &cobra.Command{
		Use:   "upgrade",
		Short: "Get a Stripe Checkout link to start the paid plan",
		Long: `Makes a Stripe Checkout page for the paid plan and prints its link. Open
the link in a browser to pay; the account is on the paid plan as soon as
Stripe confirms it. Monthly unless --yearly. Needs a full-scope key.

` + planHelp,
		Example: "  mailgoose plan upgrade\n  mailgoose plan upgrade --yearly",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			interval := "month"
			if yearly {
				interval = "year"
			}
			var out planCheckout
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/plan/checkout", map[string]string{"interval": interval}, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "The paid plan: %s a %s, plus any sales tax.\n", dollars(out.UnitPriceCents*int64(out.Units)), out.Interval)
			fmt.Fprintf(a.Stdout, "Pay here (the link works until %s):\n%s\n", out.ExpiresAt.Local().Format("2006-01-02 15:04"), out.URL)
			fmt.Fprintln(a.Stdout, "The plan starts once Stripe confirms the payment; check with `mailgoose plan`.")
			return nil
		},
	})
	upgrade.Flags().BoolVar(&yearly, "yearly", false, "pay yearly ($50 a year instead of $5 a month)")

	manage := calls("createPlanPortal", &cobra.Command{
		Use:     "manage",
		Example: `  mailgoose plan manage`,
		Short:   "Get a Stripe link to change the card, see invoices or cancel",
		Long: `Prints a link to Stripe's billing page for the subscription, where the
card can be changed, invoices downloaded and the plan cancelled. A
cancelled plan runs to the end of the period it's paid for; bought credits
stay. Needs a full-scope key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				URL string `json:"url"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/plan/portal", nil, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Manage the plan here:\n%s\n", out.URL)
			return nil
		},
	})

	cmd.AddCommand(upgrade, manage)
	return cmd
}
