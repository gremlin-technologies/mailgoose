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
	Review              *string    `json:"review"`
	ReviewFlags         []string   `json:"review_flags"`
	ReviewAt            *time.Time `json:"review_at"`
	// Test mode (MG-80).
	SendingMode            string `json:"sending_mode"`
	TestAddressesConfirmed int    `json:"test_addresses_confirmed"`
	TestAddressesPending   int    `json:"test_addresses_pending"`
	// TesterUntil is when a tester account's period ends or ended (MG-86).
	TesterUntil *time.Time `json:"tester_until"`
	CreatedAt   time.Time  `json:"created_at"`
}

// testerEnded says whether s is a tester account whose period has ended
// by now, so it sends nothing (MG-107).
func (s adminAccountSummary) testerEnded(now time.Time) bool {
	return s.TesterUntil != nil && !now.Before(*s.TesterUntil)
}

// adminAccount is the API's AdminAccount.
type adminAccount struct {
	adminAccountSummary
	DailySendLimit *int      `json:"daily_send_limit"`
	SendLimit      sendLimit `json:"send_limit"`
	// TermsVersion is the version of the terms the account last accepted.
	TermsVersion    *string    `json:"terms_version"`
	TermsAcceptedAt *time.Time `json:"terms_accepted_at"`
	Throttles       []struct {
		ID         int64     `json:"id"`
		CampaignID *string   `json:"campaign_id"`
		Reason     string    `json:"reason"`
		CreatedAt  time.Time `json:"created_at"`
		Until      time.Time `json:"until"`
	} `json:"throttles"`
	Contacts      int64 `json:"contacts"`
	CreditBalance int64 `json:"credit_balance"`
	// Tester is its tester state (MG-86); nil when it isn't one.
	Tester      *testerStatus `json:"tester"`
	Last30Days  adminRates    `json:"last_30_days"`
	HeldImports []struct {
		ID               string  `json:"id"`
		TotalRows        int64   `json:"total_rows"`
		HoldReason       string  `json:"hold_reason"`
		ListSource       *string `json:"list_source"`
		ListSourceDetail *string `json:"list_source_detail"`
		Checks           struct {
			InvalidRows         int     `json:"invalid_rows"`
			RoleAddresses       int     `json:"role_addresses"`
			DisposableAddresses int     `json:"disposable_addresses"`
			SharedAddresses     int     `json:"shared_addresses"`
			SharedAccountID     *string `json:"shared_account_id"`
		} `json:"checks"`
		CreatedAt time.Time `json:"created_at"`
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
sending, suspend or unsuspend it, review new sign-ups, switch it between
test mode and full sending, make it a tester account, see each account's
bounce and complaint rates and the database's table sizes, stop a campaign
in flight, approve a held import and grant credits.
They need you signed in with ` + "`mailgoose login`" + ` as an admin; API keys
are refused. Every action takes --reason and is logged with who did it
(` + "`mailgoose admin log`" + `).

Account states and how they interact: docs/account-states.md`,
	}
	cmd.AddCommand(a.adminFindCmd(), a.adminAccountCmd(), a.adminPauseCmd(true), a.adminPauseCmd(false),
		a.adminSuspendCmd(true), a.adminSuspendCmd(false), a.adminSignupsCmd(), a.adminReviewCmd(true), a.adminReviewCmd(false),
		a.adminTestModeCmd(), a.adminSendingModeCmd(true), a.adminSendingModeCmd(false), a.adminTesterCmd(), a.adminDeliverabilityCmd(),
		a.adminStorageCmd(), a.adminStopCmd(), a.adminApproveImportCmd(), a.adminGrantCmd(), a.adminLogCmd())
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
				case s.testerEnded(time.Now()):
					sending = "off: tester ended " + s.TesterUntil.Local().Format("2006-01-02")
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
	if acc.Review != nil {
		review := *acc.Review
		if len(acc.ReviewFlags) > 0 {
			review += ", flagged " + strings.Join(acc.ReviewFlags, ", ")
		}
		fmt.Fprintf(w, "Sign-up:      %s\n", review)
	}
	fmt.Fprintf(w, "Mode:         %s\n", sendingModeLine(acc.SendingMode, acc.TestAddressesConfirmed, acc.TestAddressesPending))
	switch {
	case acc.SendingPausedAt != nil:
		fmt.Fprintf(w, "Sending:      PAUSED since %s: %s\n", acc.SendingPausedAt.Local().Format("2006-01-02 15:04"),
			ptrString(acc.SendingPausedReason))
	case acc.Tester != nil && acc.Tester.Ended:
		// An ended tester's copies wait, queued or not (MG-107).
		fmt.Fprintf(w, "Sending:      OFF: the tester period ended %s; `mailgoose admin tester grant` extends it\n",
			acc.Tester.Until.Local().Format("2006-01-02 15:04"))
	default:
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
	if acc.Tester != nil {
		fmt.Fprintf(w, "Tester:       %s\n", acc.Tester.adminLine())
	}
	fmt.Fprintf(w, "Contacts:     %d\n", acc.Contacts)
	fmt.Fprintf(w, "Credits:      %d\n", acc.CreditBalance)
	fmt.Fprintf(w, "Terms:        %s\n", termsLine(acc.TermsVersion, acc.TermsAcceptedAt))
	fmt.Fprintf(w, "Last 30 days: %s\n", acc.Last30Days)
	for _, th := range acc.Throttles {
		fmt.Fprintf(w, "Throttled:    %s to %s: %s\n", th.CreatedAt.Local().Format("2006-01-02 15:04"),
			th.Until.Local().Format("2006-01-02 15:04"), th.Reason)
	}
	for _, i := range acc.HeldImports {
		fmt.Fprintf(w, "Held import:  %s, %d rows (%s), from %s\n", i.ID, i.TotalRows, i.HoldReason, i.CreatedAt.Local().Format("2006-01-02"))
		src := "none given"
		if i.ListSource != nil {
			src = *i.ListSource
			if i.ListSourceDetail != nil {
				src += ": " + *i.ListSourceDetail
			}
		}
		c := i.Checks
		fmt.Fprintf(w, "              list source %s; %d invalid, %d role, %d disposable", src, c.InvalidRows, c.RoleAddresses, c.DisposableAddresses)
		if c.SharedAddresses > 0 && c.SharedAccountID != nil {
			fmt.Fprintf(w, "; %d in other accounts' imports, most in account %s", c.SharedAddresses, *c.SharedAccountID)
		}
		fmt.Fprintln(w)
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
stopped, and a pause stays until `+"`mailgoose admin unpause`"+`. A rejected
sign-up stays rejected, so the account can't launch campaigns or take form
signups until `+"`mailgoose admin approve-signup`"+`. --reason is logged.`
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
				if ptrString(acc.Review) == "rejected" {
					fmt.Fprintln(a.Stdout, "Its sign-up review is still rejected, so it can't launch campaigns or take form signups: "+
						"`mailgoose admin approve-signup` lifts that.")
				}
			}
			return nil
		},
	})
	c.Flags().StringVar(&reason, "reason", "", "why (logged; a suspension reason is shown to the account)")
	return c
}

// adminSignupsCmd is `mailgoose admin signups` (MG-66).
func (a *App) adminSignupsCmd() *cobra.Command {
	var limit int
	c := calls("listSignupReviews", &cobra.Command{
		Use:   "signups",
		Short: "List new accounts waiting for their sign-up review",
		Long: `Lists the accounts waiting for their sign-up review, oldest first, with
what flagged them: disposable_domain (a throwaway address), ip_burst (more
than 3 accounts from its IP in 24 hours), suspended_ip or suspended_domain
(an IP or company domain shared with a suspended account). The server's
SIGNUP_REVIEW setting says who waits: every new account, only flagged ones,
or none. Approve with ` + "`mailgoose admin approve-signup`" + `, or reject (and
suspend) with ` + "`mailgoose admin reject-signup`" + `.`,
		Example: `  mailgoose admin signups`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/admin/signups"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}
			var page struct {
				Data       []adminAccountSummary `json:"data"`
				NextCursor *string               `json:"next_cursor"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No sign-ups are waiting for review.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tOWNER\tWAITING SINCE\tFLAGS")
			for _, s := range page.Data {
				flags := "-"
				if len(s.ReviewFlags) > 0 {
					flags = strings.Join(s.ReviewFlags, ", ")
				}
				since := ""
				if s.ReviewAt != nil {
					since = s.ReviewAt.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Name, s.OwnerEmail, since, flags)
			}
			return tw.Flush()
		},
	})
	c.Flags().IntVar(&limit, "limit", 0, "most accounts to show (1 to 100, default 50)")
	return c
}

// adminReviewCmd is `mailgoose admin approve-signup` and `reject-signup`
// (MG-66).
func (a *App) adminReviewCmd(approve bool) *cobra.Command {
	var reason string
	op, use, short, long, def := "approveSignup", "approve-signup", "Approve a sign-up that waits for review or was rejected",
		`Approves a new account waiting for its sign-up review: it can launch
campaigns and take form signups from now on. A rejected sign-up can be
approved too; that doesn't lift its suspension, which
`+"`mailgoose admin unsuspend`"+` does. --reason is logged.`, "Sign-up approved."
	if !approve {
		op, use, short, long, def = "rejectSignup", "reject-signup", "Reject a new account and suspend it",
			`Rejects a new account waiting for its sign-up review, and suspends it as
`+"`mailgoose admin suspend`"+` does: the owner gets an email saying why and how
to appeal. --reason is logged and shown to the account as why.`, ""
	}
	c := calls(op, &cobra.Command{
		Use:     use + " <account-id>",
		Short:   short,
		Long:    long,
		Example: `  mailgoose admin ` + use + ` 3f1c... --reason "Throwaway address, no website"`,
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
			switch {
			case approve && acc.SuspendedAt != nil:
				fmt.Fprintf(a.Stdout, "Approved %s (%s). It's still suspended: `mailgoose admin unsuspend` lifts that.\n", acc.Name, acc.ID)
			case approve:
				fmt.Fprintf(a.Stdout, "Approved %s (%s). It can launch campaigns now.\n", acc.Name, acc.ID)
			default:
				fmt.Fprintf(a.Stdout, "Rejected %s (%s) and suspended it. The owner has been emailed.\n", acc.Name, acc.ID)
			}
			return nil
		},
	})
	help := "why (logged; a rejection reason is shown to the account)"
	if approve {
		help = "why (logged)"
	}
	c.Flags().StringVar(&reason, "reason", def, help)
	return c
}

// sendingModeLine describes an account's sending mode (MG-80) for people.
func sendingModeLine(mode string, confirmed, pending int) string {
	if mode != "test" {
		return "full sending"
	}
	return fmt.Sprintf("TEST MODE (sends only to its test addresses: %d confirmed, %d pending)", confirmed, pending)
}

// adminTestModeCmd is `mailgoose admin test-mode` (MG-80).
func (a *App) adminTestModeCmd() *cobra.Command {
	var limit int
	c := calls("listTestModeAccounts", &cobra.Command{
		Use:   "test-mode",
		Short: "List accounts in test mode",
		Long: `Lists the accounts in test mode, oldest first, with how many test addresses
each has confirmed and pending. An account in test mode sends only to its
confirmed test addresses and its owner's own address. New accounts start in
test mode while the server's NEW_ACCOUNTS_TEST_MODE setting is on. Allow one
full sending with ` + "`mailgoose admin allow-full-sending`" + `.`,
		Example: `  mailgoose admin test-mode`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/admin/test-mode"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}
			var page struct {
				Data       []adminAccountSummary `json:"data"`
				NextCursor *string               `json:"next_cursor"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &page, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(page)
				return nil
			}
			if len(page.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No accounts are in test mode.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tOWNER\tCREATED\tTEST ADDRESSES")
			for _, s := range page.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d confirmed, %d pending\n", s.ID, s.Name, s.OwnerEmail,
					s.CreatedAt.Local().Format("2006-01-02"), s.TestAddressesConfirmed, s.TestAddressesPending)
			}
			return tw.Flush()
		},
	})
	c.Flags().IntVar(&limit, "limit", 0, "most accounts to show (1 to 100, default 50)")
	return c
}

// deliverabilityRates is the API's AdminDeliverabilityRates.
type deliverabilityRates struct {
	Sent              int      `json:"sent"`
	HardBounces       int      `json:"hard_bounces"`
	Complaints        int      `json:"complaints"`
	HardBouncePercent *float64 `json:"hard_bounce_percent"`
	ComplaintPercent  *float64 `json:"complaint_percent"`
}

// rate is n with its percentage: "0.3% (3)", or "-" when nothing was sent.
func (r deliverabilityRates) rate(n int, pct *float64) string {
	if pct == nil {
		if n == 0 {
			return "-"
		}
		return fmt.Sprintf("- (%s)", thousands(n))
	}
	return fmt.Sprintf("%s%% (%s)", strconv.FormatFloat(*pct, 'f', -1, 64), thousands(n))
}

// accountDeliverability is the API's AdminAccountDeliverability.
type accountDeliverability struct {
	AccountID       string              `json:"account_id"`
	AccountName     string              `json:"account_name"`
	OwnerEmail      *string             `json:"owner_email"`
	SendingMode     string              `json:"sending_mode"`
	TesterUntil     *time.Time          `json:"tester_until"`
	SendingPausedAt *time.Time          `json:"sending_paused_at"`
	SuspendedAt     *time.Time          `json:"suspended_at"`
	ThrottledUntil  *time.Time          `json:"throttled_until"`
	Day             deliverabilityRates `json:"last_24_hours"`
	Week            deliverabilityRates `json:"last_7_days"`
	OverWarning     bool                `json:"over_warning"`
}

// state is how the account stands: "OVER WARNING, paused, test mode", or
// "ok".
func (d accountDeliverability) state() string {
	var s []string
	if d.OverWarning {
		s = append(s, "OVER WARNING")
	}
	if d.SuspendedAt != nil {
		s = append(s, "suspended")
	}
	if d.SendingPausedAt != nil {
		s = append(s, "paused")
	}
	if d.ThrottledUntil != nil {
		s = append(s, "throttled until "+d.ThrottledUntil.Local().Format("2006-01-02 15:04"))
	}
	if d.SendingMode == "test" {
		s = append(s, "test mode")
	}
	if d.TesterUntil != nil {
		s = append(s, "tester until "+d.TesterUntil.UTC().Format("2006-01-02"))
	}
	if len(s) == 0 {
		return "ok"
	}
	return strings.Join(s, ", ")
}

// sendThroughput is the API's AdminSendThroughput (MG-91).
type sendThroughput struct {
	Since                   time.Time `json:"since"`
	Batches                 int       `json:"batches"`
	Copies                  int       `json:"copies"`
	BusySeconds             float64   `json:"busy_seconds"`
	CopiesPerSecond         *float64  `json:"copies_per_second"`
	BatchSeconds            float64   `json:"batch_seconds"`
	PerBatchCopiesPerSecond *float64  `json:"per_batch_copies_per_second"`
	SendRatePerSecond       int       `json:"send_rate_per_second"`
}

// lines says how fast copies went out, against the configured rate.
func (t sendThroughput) lines() []string {
	limit := fmt.Sprintf("SEND_RATE_PER_SECOND is %d", t.SendRatePerSecond)
	if t.Batches == 0 || t.CopiesPerSecond == nil {
		return []string{"Throughput, last 24 hours: no batch sent a copy (" + limit + ")."}
	}
	batches := "batches"
	if t.Batches == 1 {
		batches = "batch"
	}
	out := []string{fmt.Sprintf("Throughput, last 24 hours: %s copies in %s %s over %s seconds of sending: %s a second (%s).",
		thousands(t.Copies), thousands(t.Batches), batches, strconv.FormatFloat(t.BusySeconds, 'f', 1, 64),
		strconv.FormatFloat(*t.CopiesPerSecond, 'f', 2, 64), limit)}
	if t.PerBatchCopiesPerSecond != nil && t.BatchSeconds > t.BusySeconds*1.05 {
		out = append(out, fmt.Sprintf("Batches overlapped and shared the rate: one batch sent %s a second (over %s batch-seconds).",
			strconv.FormatFloat(*t.PerBatchCopiesPerSecond, 'f', 2, 64), strconv.FormatFloat(t.BatchSeconds, 'f', 1, 64)))
	}
	return out
}

// earlyWarning is the API's early-warning thresholds (MG-82).
type earlyWarning struct {
	ComplaintPercent  float64 `json:"complaint_percent"`
	HardBouncePercent float64 `json:"hard_bounce_percent"`
	MinComplaints     int     `json:"min_complaints"`
	MinCopies         int     `json:"min_copies"`
}

// line says what OVER WARNING means and how many of the accounts shown are
// over it, so a report with none doesn't read as an alert.
func (w earlyWarning) line(over int) string {
	rule := fmt.Sprintf("the last 7 days are over %s%% complaints (and at least %d) or %s%% hard bounces, on %s or more copies",
		strconv.FormatFloat(w.ComplaintPercent, 'f', -1, 64), w.MinComplaints,
		strconv.FormatFloat(w.HardBouncePercent, 'f', -1, 64), thousands(w.MinCopies))
	switch over {
	case 0:
		return "No account shown is over the early warning. OVER WARNING in STATE means " + rule + "."
	case 1:
		return "1 account shown is OVER WARNING: " + rule + "."
	}
	return fmt.Sprintf("%d accounts shown are OVER WARNING: %s.", over, rule)
}

// adminDeliverabilityCmd is `mailgoose admin deliverability` (MG-82).
func (a *App) adminDeliverabilityCmd() *cobra.Command {
	var limit int
	c := calls("getDeliverabilityReport", &cobra.Command{
		Use:   "deliverability",
		Short: "List accounts' bounce and complaint rates, worst first",
		Long: `Lists every account that sent campaign email in the last 7 days (or had a
hard bounce or complaint reported in them), with its copies sent, hard
bounce rate and complaint rate over the last 24 hours and the last 7 days.
Worst first: accounts over the early warning over 7 days (OVER WARNING) come
first, then by how far their rates are over it. STATE says whether each is
suspended, paused, throttled or in test mode. The daily abuse digest lists
the accounts over the early warning.

It ends with the last 24 hours' send throughput across every account:
copies sent, over the seconds when a batch was sending, against the
configured SEND_RATE_PER_SECOND. SES mailbox simulator copies count there
(they're real SES calls), though they're left out of the rates above.`,
		Example: `  mailgoose admin deliverability
  mailgoose admin deliverability --limit 10 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/admin/deliverability"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}
			var rep struct {
				Data           []accountDeliverability `json:"data"`
				NextCursor     *string                 `json:"next_cursor"`
				Total          int                     `json:"total"`
				EarlyWarning   earlyWarning            `json:"early_warning"`
				SendThroughput sendThroughput          `json:"send_throughput"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &rep, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(rep)
				return nil
			}
			if len(rep.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No account sent campaign email in the last 7 days.")
				fmt.Fprintln(a.Stdout)
				for _, l := range rep.SendThroughput.lines() {
					fmt.Fprintln(a.Stdout, l)
				}
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\t24H SENT\t24H HARD BOUNCES\t24H COMPLAINTS\t7D SENT\t7D HARD BOUNCES\t7D COMPLAINTS\tSTATE")
			for _, d := range rep.Data {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.AccountID, d.AccountName,
					thousands(d.Day.Sent), d.Day.rate(d.Day.HardBounces, d.Day.HardBouncePercent),
					d.Day.rate(d.Day.Complaints, d.Day.ComplaintPercent),
					thousands(d.Week.Sent), d.Week.rate(d.Week.HardBounces, d.Week.HardBouncePercent),
					d.Week.rate(d.Week.Complaints, d.Week.ComplaintPercent), d.state())
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			over := 0
			for _, d := range rep.Data {
				if d.OverWarning {
					over++
				}
			}
			fmt.Fprintln(a.Stdout)
			fmt.Fprintln(a.Stdout, rep.EarlyWarning.line(over))
			if rep.Total > len(rep.Data) {
				fmt.Fprintf(a.Stdout, "Showing the worst %d of %d accounts; --limit shows up to 100.\n", len(rep.Data), rep.Total)
			}
			for _, l := range rep.SendThroughput.lines() {
				fmt.Fprintln(a.Stdout, l)
			}
			return nil
		},
	})
	c.Flags().IntVar(&limit, "limit", 0, "most accounts to show (1 to 100, default 50)")
	return c
}

// tableSize is the API's AdminTableSize.
type tableSize struct {
	Table              string `json:"table"`
	RowEstimate        *int64 `json:"row_estimate"`
	TotalBytes         int64  `json:"total_bytes"`
	TotalBytes7DaysAgo *int64 `json:"total_bytes_7_days_ago"`
	Change7DaysBytes   *int64 `json:"change_7_days_bytes"`
}

// adminStorageCmd is `mailgoose admin storage` (MG-95).
func (a *App) adminStorageCmd() *cobra.Command {
	var limit int
	c := calls("getStorageReport", &cobra.Command{
		Use:   "storage",
		Short: "List the database's largest tables and how much they grew",
		Long: `Lists the database's table sizes: campaign_recipients and send_events
first (every copy sent adds a row to the first and one or two to the
second, kept 12 months), then the largest other tables. SIZE includes
indexes and TOAST. ROWS (EST.) is the planner's estimate from the last
ANALYZE, not an exact count ("not analysed" when there's none yet).
7-DAY CHANGE is against the sizes the daily digest job recorded 7 days
ago, "-" where it has none. The daily abuse digest gives the first two.`,
		Example: `  mailgoose admin storage
  mailgoose admin storage --limit 20 --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/v1/admin/storage"
			if limit > 0 {
				path += "?limit=" + strconv.Itoa(limit)
			}
			var rep struct {
				Data            []tableSize `json:"data"`
				NextCursor      *string     `json:"next_cursor"`
				Total           int         `json:"total"`
				TotalBytes      int64       `json:"total_bytes"`
				ComparedWithDay string      `json:"compared_with_day"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, path, nil, &rep, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(rep)
				return nil
			}
			if len(rep.Data) == 0 {
				fmt.Fprintln(a.Stdout, "No tables found.")
				return nil
			}
			tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "TABLE\tROWS (EST.)\tSIZE\t7-DAY CHANGE")
			for _, t := range rep.Data {
				rows := "not analysed"
				if t.RowEstimate != nil {
					rows = thousands(int(*t.RowEstimate))
				}
				change := "-"
				if c := t.Change7DaysBytes; c != nil {
					switch {
					case *c == 0:
						change = "no change"
					case *c > 0:
						change = "+" + bytesText(*c)
					default:
						change = bytesText(*c)
					}
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Table, rows, bytesText(t.TotalBytes), change)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(a.Stdout, "\nRow counts are estimates. 7-day change is against %s (UTC).\n", rep.ComparedWithDay)
			if rep.Total > len(rep.Data) {
				fmt.Fprintf(a.Stdout, "Showing %d of %d tables, %s in all; --limit shows up to 50.\n", len(rep.Data), rep.Total, bytesText(rep.TotalBytes))
			} else {
				fmt.Fprintf(a.Stdout, "%d tables, %s in all.\n", rep.Total, bytesText(rep.TotalBytes))
			}
			return nil
		},
	})
	c.Flags().IntVar(&limit, "limit", 0, "most tables to show (1 to 50, default 10)")
	return c
}

// bytesText writes n bytes as Postgres's pg_size_pretty does, in powers of
// 1,024, with a decimal under 10: "512 bytes", "16 KB", "1.2 GB".
func bytesText(n int64) string {
	if n < 0 {
		return "-" + bytesText(-n)
	}
	if n < 1024 {
		if n == 1 {
			return "1 byte"
		}
		return fmt.Sprintf("%d bytes", n)
	}
	v := float64(n)
	unit := ""
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB"} {
		v /= 1024
		unit = u
		if v < 1024 {
			break
		}
	}
	if v < 10 {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0") + " " + unit
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}

// adminSendingModeCmd is `mailgoose admin allow-full-sending` and
// `set-test-mode` (MG-80).
func (a *App) adminSendingModeCmd(full bool) *cobra.Command {
	var reason string
	op, use, short, long, def := "allowFullSending", "allow-full-sending", "Take an account out of test mode",
		`Allows an account full sending: it can send to its whole audience, not
just its test addresses. It doesn't approve a sign-up review; an account
still waiting for one needs `+"`mailgoose admin approve-signup`"+` too.
--reason is logged.`, "Full sending allowed."
	if !full {
		op, use, short, long, def = "setTestMode", "set-test-mode", "Put an account in test mode",
			`Puts an account in test mode: from now on it sends only to its confirmed
test addresses and its owner's own address. Copies of a campaign already
sending to anyone else are skipped, with their credits given back.
--reason is logged.`, "Put in test mode."
	}
	c := calls(op, &cobra.Command{
		Use:     use + " <account-id>",
		Short:   short,
		Long:    long,
		Example: `  mailgoose admin ` + use + ` 3f1c... --reason "Helping us test sending"`,
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
			if full {
				fmt.Fprintf(a.Stdout, "Allowed %s (%s) full sending. It can send to its whole audience now.\n", acc.Name, acc.ID)
			} else {
				fmt.Fprintf(a.Stdout, "Put %s (%s) in test mode. It sends only to its test addresses now.\n", acc.Name, acc.ID)
			}
			return nil
		},
	})
	c.Flags().StringVar(&reason, "reason", def, "why (logged)")
	return c
}

// adminTesterCmd is `mailgoose admin tester grant|revoke` (MG-86).
func (a *App) adminTesterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tester",
		Short: "Make an account a tester account, or end one",
		Long: `A tester account is one of the people invited to try mailgoose before
launch. It sends real campaigns, with full sending, within tight limits:
500 credits for its life (campaign copies and test sends; SES mailbox
simulator addresses use none), 100 copies a day, 500 contacts and a
stricter automatic pause, until its end date.`,
	}

	var until, reason string
	var credits int
	grant := calls("grantTester", &cobra.Command{
		Use:   "grant <account-id>",
		Short: "Make an account a tester account, or extend one",
		Long: `Makes the account a tester account, and gives it full sending. --until is
when it stops sending: a date (the end of that day, UTC) or an RFC 3339
time, at most 366 days away; 30 days from now by default. --credits is
its sends for its life (500 by default). Granting an account that's a
tester already sets its new end and credits; the credits it used still
count. Only an account whose sign-up was approved, or never held for
review, can be one. --reason is logged.`,
		Example: `  mailgoose admin tester grant 3f1c... --reason "Invited to try staging"
  mailgoose admin tester grant 3f1c... --until 2027-01-31 --reason "Extended for the beta"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an account")
			if err != nil {
				return err
			}
			if err := needReason(reason); err != nil {
				return err
			}
			body := map[string]any{"reason": reason}
			if until != "" {
				t, err := parseUntil(until)
				if err != nil {
					return err
				}
				body["until"] = t.UTC().Format(time.RFC3339)
			}
			if credits != 0 {
				body["credits"] = credits
			}
			var acc adminAccount
			if err := a.call(cmd.Context(), http.MethodPost, adminAccountPath(id)+"/grant-tester", body, &acc, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(acc)
				return nil
			}
			fmt.Fprintf(a.Stdout, "%s (%s) is a tester account.\n", acc.Name, acc.ID)
			if acc.Tester != nil {
				fmt.Fprintf(a.Stdout, "Tester: %s\n", acc.Tester.adminLine())
			}
			return nil
		},
	})
	grant.Flags().StringVar(&until, "until", "", "when it stops sending: a date or an RFC 3339 time (default 30 days from now)")
	grant.Flags().IntVar(&credits, "credits", 0, "its sends for its life, 1 to 10000 (default 500)")
	grant.Flags().StringVar(&reason, "reason", "Invited to test mailgoose.", "why (logged)")

	var revokeReason string
	revoke := calls("revokeTester", &cobra.Command{
		Use:   "revoke <account-id>",
		Short: "End a tester account",
		Long: `Ends the account's tester state and puts it in test mode: from now on it
sends only to its confirmed test addresses and its owner's own address,
and copies of a campaign already sending to anyone else are skipped. To
make it a customer afterwards, use ` + "`mailgoose admin allow-full-sending`" + `.
--reason is logged.`,
		Example: `  mailgoose admin tester revoke 3f1c... --reason "Testing finished"`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := adminIDArg(args[0], "an account")
			if err != nil {
				return err
			}
			if err := needReason(revokeReason); err != nil {
				return err
			}
			var acc adminAccount
			if err := a.call(cmd.Context(), http.MethodPost, adminAccountPath(id)+"/revoke-tester",
				map[string]any{"reason": revokeReason}, &acc, true); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(acc)
				return nil
			}
			fmt.Fprintf(a.Stdout, "%s (%s) is no longer a tester account. It's in test mode now.\n", acc.Name, acc.ID)
			return nil
		},
	})
	revoke.Flags().StringVar(&revokeReason, "reason", "Testing finished.", "why (logged)")
	cmd.AddCommand(grant, revoke)
	return cmd
}

// parseUntil reads --until: a date, meaning the end of that day in UTC, or
// an RFC 3339 time.
func parseUntil(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if d, err := time.Parse("2006-01-02", v); err == nil {
		return d.Add(24*time.Hour - time.Second), nil
	}
	return time.Time{}, usageError(fmt.Sprintf("--until %q isn't a date.", v),
		"Use a date like 2027-01-31, or a time like 2027-01-31T17:00:00Z.")
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
