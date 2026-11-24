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
	// The plan's terms, from the server's settings (MG-126).
	IncludedContacts int64 `json:"included_contacts"`
	ContactsPerUnit  int64 `json:"contacts_per_unit"`
	FreePlan         struct {
		Contacts       int64 `json:"contacts"`
		MonthlyCredits int64 `json:"monthly_credits"`
		DailySends     int64 `json:"daily_sends"`
	} `json:"free_plan"`
	PaidSending struct {
		StartingDaily int64 `json:"starting_daily"`
		MostDaily     int64 `json:"most_daily"`
		NeedsDomain   bool  `json:"needs_domain"`
	} `json:"paid_sending"`
	CreditPacks struct {
		Cheapest struct {
			Name       string `json:"name"`
			Credits    int64  `json:"credits"`
			PriceCents int64  `json:"price_cents"`
		} `json:"cheapest"`
		Months int `json:"months"`
	} `json:"credit_packs"`
	// ContactFieldLimit is the most contact fields the plan allows (MG-130).
	ContactFieldLimit int `json:"contact_field_limit"`
}

// printFreeTerms says what the free plan allows and what the paid plan adds,
// from the plan's terms; an older server that sends none gets nothing.
func (a *App) printFreeTerms(p plan) {
	if p.IncludedContacts == 0 {
		return
	}
	n := groupThousands
	free := fmt.Sprintf("The free plan sends to up to %s contacts, with %s credits a month", n(p.FreePlan.Contacts), n(p.FreePlan.MonthlyCredits))
	if p.FreePlan.DailySends > 0 {
		free += fmt.Sprintf(" and at most %s emails a day", n(p.FreePlan.DailySends))
	}
	fmt.Fprintln(a.Stdout, free+`, and every email carries a "Sent by mailgoose" line.`)
	daily := fmt.Sprintf("starts at %s emails a day and grows each week of clean sending, up to %s",
		n(p.PaidSending.StartingDaily), n(p.PaidSending.MostDaily))
	if p.PaidSending.NeedsDomain {
		daily = fmt.Sprintf("stays at %s emails a day until you add your own domain, then grows each week of clean sending, up to %s",
			n(p.PaidSending.StartingDaily), n(p.PaidSending.MostDaily))
	}
	fmt.Fprintf(a.Stdout, "The paid plan has no \"Sent by mailgoose\" line, keeps the %s monthly credits, buys credit packs "+
		"(from %s for %s credits, which last %d months), and %s.\n",
		n(p.FreePlan.MonthlyCredits), dollars(p.CreditPacks.Cheapest.PriceCents), n(p.CreditPacks.Cheapest.Credits),
		p.CreditPacks.Months, daily)
	a.printContactPricing(p)
}

// printContactPricing says what the paid plan costs by contacts.
func (a *App) printContactPricing(p plan) {
	if p.IncludedContacts == 0 {
		return
	}
	fmt.Fprintf(a.Stdout, "It's %s a month or %s a year for up to %s contacts, and each further %s contacts adds the same again.\n",
		dollars(p.Prices.MonthCents), dollars(p.Prices.YearCents), groupThousands(p.IncludedContacts), groupThousands(p.ContactsPerUnit))
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
adds the same again. Paid accounts keep the free monthly credits and buy more in
packs (` + "`mailgoose credits buy`" + `). Contacts count while they're
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
				a.printFreeTerms(p)
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
			a.printContactPricing(p)
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
