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

// The admin commands (MG-34: OPS-6) are for mailgoose staff. They need a
// person signed in with `mailgoose login` whose user is an admin; the API
// refuses everyone else, and refuses API keys outright.

// adminRates is the API's AdminRates.
type adminRates struct {
	Sent        int `json:"sent"`
	HardBounces int `json:"hard_bounces"`
	Complaints  int `json:"complaints"`
}

func (r adminRates) String() string {
	pct := func(n int) string {
		if r.Sent == 0 {
			return "-"
		}
		return strconv.FormatFloat(float64(n)*100/float64(r.Sent), 'f', 2, 64) + "%"
	}
	return fmt.Sprintf("%d sent, %d hard bounces (%s), %d complaints (%s)",
		r.Sent, r.HardBounces, pct(r.HardBounces), r.Complaints, pct(r.Complaints))
}

// adminAccountSummary is the API's AdminAccountSummary.
type adminAccountSummary struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	Status              string     `json:"status"`
	Plan                string     `json:"plan"`
	OwnerEmail          string     `json:"owner_email"`
	SendingPausedAt     *time.Time `json:"sending_paused_at"`
	SendingPausedReason *string    `json:"sending_paused_reason"`
	SuspendedAt         *time.Time `json:"suspended_at"`
	SuspendedReason     *string    `json:"suspended_reason"`
	SuspendedBy         *string    `json:"suspended_by"`
	CreatedAt           time.Time  `json:"created_at"`
}

// adminAccount is the API's AdminAccount.
type adminAccount struct {
	adminAccountSummary
	DailySendLimit *int      `json:"daily_send_limit"`
	SendLimit      sendLimit `json:"send_limit"`
	Throttles      []struct {
		ID         int64     `json:"id"`
		CampaignID *string   `json:"campaign_id"`
		Reason     string    `json:"reason"`
		CreatedAt  time.Time `json:"created_at"`
		Until      time.Time `json:"until"`
	} `json:"throttles"`
	Contacts      int64      `json:"contacts"`
	CreditBalance int64      `json:"credit_balance"`
	Last30Days    adminRates `json:"last_30_days"`
	HeldImports   []struct {
		ID         string    `json:"id"`
		TotalRows  int64     `json:"total_rows"`
		HoldReason string    `json:"hold_reason"`
		CreatedAt  time.Time `json:"created_at"`
	} `json:"held_imports"`
	LiveCampaigns []struct {
		ID     string     `json:"id"`
		Name   string     `json:"name"`
		Status string     `json:"status"`
		Rates  adminRates `json:"rates"`
	} `json:"live_campaigns"`
}

// adminAction is the API's AdminAction.
type adminAction struct {
	ID        int64          `json:"id"`
	Actor     string         `json:"actor"`
	Action    string         `json:"action"`
	AccountID *string        `json:"account_id"`
	TargetID  *string        `json:"target_id"`
	Reason    string         `json:"reason"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

func adminIDArg(arg, what string) (string, error) {
	if !domainIDPattern.MatchString(arg) {
		return "", usageError(fmt.Sprintf("%q isn't %s id.", arg, what), "`mailgoose admin find <address, name or domain>` finds an account and shows its id.")
	}
	return arg, nil
}

// needReason checks --reason, which every admin action takes and logs.
func needReason(reason string) error {
	if strings.TrimSpace(reason) == "" {
		return needFlag("reason", "Why (it's logged with your name, and a pause reason is shown to the account)")
	}
	return nil
}

func adminAccountPath(id string) string { return "/v1/admin/accounts/" + url.PathEscape(id) }

func (a *App) adminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "mailgoose staff tools (admins only)",
		Long: `Tools for mailgoose staff: find an account, pause or unpause its
sending, suspend or unsuspend it, stop a campaign in flight, approve a held
import and grant credits.
They need you signed in with ` + "`mailgoose login`" + ` as an admin; API keys
are refused. Every action takes --reason and is logged with who did it
(` + "`mailgoose admin log`" + `).`,
	}
	cmd.AddCommand(a.adminFindCmd(), a.adminAccountCmd(), a.adminPauseCmd(true), a.adminPauseCmd(false),
		a.adminSuspendCmd(true), a.adminSuspendCmd(false), a.adminStopCmd(), a.adminApproveImportCmd(), a.adminGrantCmd(), a.adminLogCmd())
	return cmd
}

func (a *App) adminFindCmd() *cobra.Command {
	var limit int
	find := calls("findAdminAccounts", &cobra.Command{
		Use:     "find <text>",
		Short:   "Find accounts by id, or part of an address, name or domain",
		Example: `  mailgoose admin find patsbakery.com`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{"q": {args[0]}}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			var page struct {
				Data       []adminAccountSummary `json:"data"`
				NextCursor *string               `json:"next_cursor"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, "/v1/admin/accounts?"+q.Encode(), nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintf(a.Stdout, "No account matches %q.\n", args[0])
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tOWNER\tSTATUS\tPLAN\tSENDING")
			for _, s := range page.Data {
				sending := "on"
				switch {
				case s.SuspendedAt != nil:
					sending = "suspended"
				case s.SendingPausedAt != nil:
					sending = "paused"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID, s.Name, s.OwnerEmail, s.Status, s.Plan, sending)
			}
			return tw.Flush()
		},
	})
	find.Flags().IntVar(&limit, "limit", 0, "most accounts to show (1 to 100, default 50)")
	return find
}

func (a *App) printAdminAccount(acc adminAccount) {
	if a.jsonOut {
		a.printJSON(acc)
		return
	}
	w := a.Stdout
	fmt.Fprintf(w, "%s (%s)\n", acc.Name, acc.ID)
	fmt.Fprintf(w, "Owner:        %s\n", acc.OwnerEmail)
	fmt.Fprintf(w, "Status:       %s, %s plan, since %s\n", acc.Status, acc.Plan, acc.CreatedAt.Local().Format("2006-01-02"))
	if acc.SuspendedAt != nil {
		fmt.Fprintf(w, "Suspended:    since %s by %s: %s\n", acc.SuspendedAt.Local().Format("2006-01-02 15:04"),
			ptrString(acc.SuspendedBy), ptrString(acc.SuspendedReason))
	}
	if acc.SendingPausedAt != nil {
		reason := ""
		if acc.SendingPausedReason != nil {
			reason = *acc.SendingPausedReason
		}
		fmt.Fprintf(w, "Sending:      PAUSED since %s: %s\n", acc.SendingPausedAt.Local().Format("2006-01-02 15:04"), reason)
	} else {
		fmt.Fprintln(w, "Sending:      on")
	}
	limit := fmt.Sprint(acc.SendLimit.Daily)
	switch {
	case acc.SendLimit.LoweredUntil != nil:
		limit += " (THROTTLED until " + acc.SendLimit.LoweredUntil.Local().Format("2006-01-02 15:04") + ")"
	case acc.DailySendLimit != nil:
		limit += " (set for this account)"
	}
	fmt.Fprintf(w, "Daily limit:  %s\n", limit)
	fmt.Fprintf(w, "Contacts:     %d\n", acc.Contacts)
	fmt.Fprintf(w, "Credits:      %d\n", acc.CreditBalance)
	fmt.Fprintf(w, "Last 30 days: %s\n", acc.Last30Days)
	for _, th := range acc.Throttles {
		fmt.Fprintf(w, "Throttled:    %s to %s: %s\n", th.CreatedAt.Local().Format("2006-01-02 15:04"),
			th.Until.Local().Format("2006-01-02 15:04"), th.Reason)
	}
	for _, i := range acc.HeldImports {
		fmt.Fprintf(w, "Held import:  %s, %d rows (%s), from %s\n", i.ID, i.TotalRows, i.HoldReason, i.CreatedAt.Local().Format("2006-01-02"))
	}
	for _, c := range acc.LiveCampaigns {
		fmt.Fprintf(w, "Campaign:     %s %q, %s: %s\n", c.ID, c.Name, c.Status, c.Rates)
	}
}

func (a *App) adminAccountCmd() *cobra.Command {
	return calls("getAdminAccount", &cobra.Command{
		Use:     "account <account-id>",
		Example: `  mailgoose admin account 3f1c...`,
		Short:   "Show an account's state, limits, health and held imports",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an account")
			if err != nil {
				return err
			}
			var acc adminAccount
			if err := a.call(cmd.Context(), http.MethodGet, adminAccountPath(id), nil, &acc, true); err != nil {
				return err
			}
			a.printAdminAccount(acc)
			return nil
		},
	})
}

func (a *App) adminPauseCmd(pause bool) *cobra.Command {
	var reason string
	op, use, short, long := "pauseAccount", "pause", "Pause an account's sending",
		`Pauses an account's sending. Campaigns in flight stop before their next
email and carry on where they stopped when it's unpaused; new launches are
refused. --reason is logged and shown to the account as why.`
	if !pause {
		op, use, short, long = "unpauseAccount", "unpause", "Let a paused account send again",
			`Lifts a pause, whether an admin's or an automatic one. Campaigns paused
mid-send carry on where they stopped. --reason is logged.`
	}
	c := calls(op, &cobra.Command{
		Use:     use + " <account-id> --reason <why>",
		Short:   short,
		Long:    long,
		Example: `  mailgoose admin ` + use + ` 3f1c... --reason "Phishing reports from SES"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an account")
			if err != nil {
				return err
			}
			if err := needReason(reason); err != nil {
				return err
			}
			var acc adminAccount
			if err := a.call(cmd.Context(), http.MethodPost, adminAccountPath(id)+"/"+use, map[string]any{"reason": reason}, &acc, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(acc)
				return nil
			}
			if pause {
				fmt.Fprintf(a.Stdout, "Paused sending for %s (%s).\n", acc.Name, acc.ID)
			} else {
				fmt.Fprintf(a.Stdout, "%s (%s) can send again.\n", acc.Name, acc.ID)
			}
			return nil
		},
	})
	c.Flags().StringVar(&reason, "reason", "", "why (logged; a pause reason is shown to the account)")
	return c
}

// adminSuspendCmd is `mailgoose admin suspend` and `unsuspend` (MG-65).
func (a *App) adminSuspendCmd(suspend bool) *cobra.Command {
	var reason string
	op, use, short, long := "suspendAccount", "suspend", "Suspend an account for abuse",
		`Suspends an account: every API key and CLI login is revoked, web
sessions end, scheduled campaigns are cancelled and sending ones stop before
their next batch, and its signup forms stop taking signups. The owner can
still sign in to a read-only page that says why and exports their contacts
and campaigns for 30 days, and gets an email saying why and how to appeal.
Credits stay as they are. --reason is logged and shown to the account as
why.`
	if !suspend {
		op, use, short, long = "unsuspendAccount", "unsuspend", "Lift an account's suspension",
			`Lets a suspended account sign in and send again. Its API keys and CLI
logins stay revoked, so the owner makes new ones; stopped campaigns stay
stopped, and a pause stays until `+"`mailgoose admin unpause`"+`. --reason is
logged.`
	}
	c := calls(op, &cobra.Command{
		Use:     use + " <account-id> --reason <why>",
		Short:   short,
		Long:    long,
		Example: `  mailgoose admin ` + use + ` 3f1c... --reason "Sent to a bought list after a warning"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an account")
			if err != nil {
				return err
			}
			if err := needReason(reason); err != nil {
				return err
			}
			var acc adminAccount
			if err := a.call(cmd.Context(), http.MethodPost, adminAccountPath(id)+"/"+use, map[string]any{"reason": reason}, &acc, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(acc)
				return nil
			}
			if suspend {
				fmt.Fprintf(a.Stdout, "Suspended %s (%s). The owner has been emailed.\n", acc.Name, acc.ID)
			} else {
				fmt.Fprintf(a.Stdout, "%s (%s) is no longer suspended. Its old API keys stay revoked.\n", acc.Name, acc.ID)
				if acc.SendingPausedAt != nil {
					fmt.Fprintln(a.Stdout, "Its sending is still paused: `mailgoose admin unpause` lifts that.")
				}
			}
			return nil
		},
	})
	c.Flags().StringVar(&reason, "reason", "", "why (logged; a suspension reason is shown to the account)")
	return c
}

func (a *App) adminStopCmd() *cobra.Command {
	var reason string
	var yes bool
	stop := calls("stopCampaign", &cobra.Command{
		Use:   "stop-campaign <campaign-id> --reason <why> --yes",
		Short: "Stop a campaign in flight (the kill switch)",
		Long: `Stops a scheduled, sending or paused campaign where it stands: nobody
else gets it, and the credits reserved for the rest come back. Copies
already sent stay sent. It ends cancelled, and it can't be undone.`,
		Example: `  mailgoose admin stop-campaign 3f1c... --reason "Phishing content" --yes`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := campaignIDArg(args[0])
			if err != nil {
				return err
			}
			if err := needReason(reason); err != nil {
				return err
			}
			if !yes {
				return needFlag("yes", "Confirmation (stopping a campaign can't be undone)")
			}
			var c campaign
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/admin/campaigns/"+url.PathEscape(id)+"/stop",
				map[string]any{"reason": reason}, &c, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(c)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Stopped campaign %q (%s). Nobody else gets it, and its unused credits are back.\n", c.Name, c.ID)
			return nil
		},
	})
	stop.Flags().StringVar(&reason, "reason", "", "why (logged)")
	stop.Flags().BoolVar(&yes, "yes", false, "confirm the stop")
	return stop
}

func (a *App) adminApproveImportCmd() *cobra.Command {
	var reason string
	approve := calls("approveImport", &cobra.Command{
		Use:   "approve-import <import-id> --reason <why>",
		Short: "Let a held import's contacts be mailed",
		Long: `Approves an import held for review (` + "`mailgoose admin account`" + ` lists
them): its contacts can be mailed from then on, and one still running
carries on unheld.`,
		Example: `  mailgoose admin approve-import 3f1c... --reason "Checked the source: a signup sheet"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an import")
			if err != nil {
				return err
			}
			if err := needReason(reason); err != nil {
				return err
			}
			var out struct {
				ID        string `json:"id"`
				AccountID string `json:"account_id"`
				Released  int64  `json:"contacts_released"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, "/v1/admin/imports/"+url.PathEscape(id)+"/approve",
				map[string]any{"reason": reason}, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Approved import %s: %d contacts can be mailed now.\n", out.ID, out.Released)
			return nil
		},
	})
	approve.Flags().StringVar(&reason, "reason", "", "why (logged)")
	return approve
}

func (a *App) adminGrantCmd() *cobra.Command {
	var reason string
	var amount int64
	grant := calls("grantCredits", &cobra.Command{
		Use:   "grant-credits <account-id> --amount <n> --reason <why>",
		Short: "Give an account credits",
		Long: `Adds credits to an account, as a goodwill or support grant. They last
24 months, like bought credits. At most 1,000,000 at a time.`,
		Example: `  mailgoose admin grant-credits 3f1c... --amount 5000 --reason "Refund for the outage"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an account")
			if err != nil {
				return err
			}
			if amount <= 0 {
				return needFlag("amount", "How many credits (a whole number above 0)")
			}
			if err := needReason(reason); err != nil {
				return err
			}
			var out struct {
				AccountID string    `json:"account_id"`
				Amount    int64     `json:"amount"`
				ExpiresAt time.Time `json:"expires_at"`
				Balance   int64     `json:"credit_balance"`
			}
			if err := a.call(cmd.Context(), http.MethodPost, adminAccountPath(id)+"/credits",
				map[string]any{"amount": amount, "reason": reason}, &out, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(out)
				return nil
			}
			fmt.Fprintf(a.Stdout, "Granted %d credits, lasting until %s. The balance is now %d.\n",
				out.Amount, out.ExpiresAt.Local().Format("2006-01-02"), out.Balance)
			return nil
		},
	})
	grant.Flags().Int64Var(&amount, "amount", 0, "credits to grant")
	grant.Flags().StringVar(&reason, "reason", "", "why (logged)")
	return grant
}

func (a *App) adminLogCmd() *cobra.Command {
	var account, cursor string
	var limit int
	log := calls("listAdminActions", &cobra.Command{
		Use: "log",
		Example: `  mailgoose admin log --limit 20
  mailgoose admin log --account 3f1c... --json`,
		Short: "List admin actions, newest first",
		Long: `Lists what admins (and automatic pauses) did, with who, why and when,
newest first. --account narrows it to one account.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if account != "" {
				id, err := adminIDArg(account, "an account")
				if err != nil {
					return err
				}
				q.Set("account_id", id)
			}
			if limit > 0 {
				q.Set("limit", strconv.Itoa(limit))
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			path := "/v1/admin/actions"
			if len(q) > 0 {
				path += "?" + q.Encode()
			}
			var page struct {
				Data       []adminAction `json:"data"`
				NextCursor *string       `json:"next_cursor"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No admin actions yet.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tWHO\tACTION\tACCOUNT\tON\tWHY")
			for _, x := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", x.CreatedAt.Local().Format("2006-01-02 15:04"), x.Actor, x.Action,
					orDash(ptrString(x.AccountID)), orDash(ptrString(x.TargetID)), x.Reason)
			}
			_ = tw.Flush()
			if page.NextCursor != nil {
				fmt.Fprintf(a.Stdout, "\nMore: mailgoose admin log --cursor %s\n", *page.NextCursor)
			}
			return nil
		},
	})
	log.Flags().StringVar(&account, "account", "", "only this account's actions")
	log.Flags().IntVar(&limit, "limit", 0, "actions per page (1 to 100, default 50)")
	log.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	return log
}

func ptrString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
