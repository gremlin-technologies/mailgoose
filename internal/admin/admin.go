// Package admin is mailgoose's staff tools (MG-34: OPS-6, CMP-8): find an
// account, pause and unpause its sending, stop a campaign in flight,
// approve a held import and grant credits. There's no admin web UI; the
// CLI's `mailgoose admin` commands call the /v1/admin API.
//
// Only users marked admin may use them, and every action needs a reason:
// it's written to admin_actions with who did it, in the same transaction
// as the change.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/billing"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
	"github.com/gremlin-technologies/mailgoose/internal/tester"
)

// Actions, as logged.
const (
	ActionGrantAdmin    = "grant_admin"
	ActionRevokeAdmin   = "revoke_admin"
	ActionPause         = "pause"
	ActionUnpause       = "unpause"
	ActionAutoPause     = "auto_pause"
	ActionStopCampaign  = "stop_campaign"
	ActionApproveImport = "approve_import"
	ActionGrantCredits  = "grant_credits"
	ActionThrottle      = "throttle"
	ActionSuspend       = sending.ActionSuspend
	ActionUnsuspend     = sending.ActionUnsuspend
	ActionAutoSuspend   = sending.ActionAutoSuspend
	ActionApproveSignup = "approve_signup"
	ActionRejectSignup  = "reject_signup"
	// Test mode (MG-80).
	ActionAllowFullSending = "allow_full_sending"
	ActionSetTestMode      = "set_test_mode"
	// Tester accounts (MG-86).
	ActionGrantTester  = "grant_tester"
	ActionRevokeTester = "revoke_tester"
)

// Limits on what an admin sends.
const (
	MaxReason      = 500
	MaxGrant       = 1_000_000
	MaxFindResults = 50
)

// Errors.
var (
	ErrNotAdmin = errors.New("not an admin")
	ErrNotFound = errors.New("not found")
	// ErrNotHeld is an import that isn't waiting for approval.
	ErrNotHeld = errors.New("the import isn't held")
	// ErrNoUser is an address with no mailgoose user.
	ErrNoUser = errors.New("no user with that address")
	// ErrSuspended is an account that's suspended already.
	ErrSuspended = errors.New("the account is suspended already")
	// ErrNotSuspended is unsuspending an account that isn't suspended.
	ErrNotSuspended = errors.New("the account isn't suspended")
	// ErrNotWaiting is approving or rejecting an account that isn't waiting
	// for its sign-up review (MG-66).
	ErrNotWaiting = errors.New("the account isn't waiting for review")
	// ErrSendingModeUnchanged is allowing full sending to an account that
	// has it, or putting one in test mode that's in it already (MG-80).
	ErrSendingModeUnchanged = errors.New("the account is in that sending mode already")
	// ErrNotApproved is making an account a tester while its sign-up
	// review waits or was rejected (MG-86).
	ErrNotApproved = errors.New("the account's sign-up isn't approved")
	// ErrNotTester is revoking an account that isn't a tester (MG-86).
	ErrNotTester = tester.ErrNotTester
)

// InputError is a reason or amount that can't be used.
type InputError struct{ Field, Message string }

func (e *InputError) Error() string { return e.Field + ": " + e.Message }

// Actor is who acts: an admin user, or the server's own command line.
type Actor struct {
	UserID string // empty for mailgoosed
	Email  string
}

// Server is the actor for `mailgoosed admin`, run with database access.
var Server = Actor{Email: "mailgoosed"}

// Service runs the admin tools.
type Service struct {
	DB *pgxpool.Pool
	// Sending stops campaigns.
	Sending *sending.Service
	// Limits are the send limits, for showing an account's.
	Limits sending.Limits
	// Warning is the early warning the deliverability report judges
	// accounts by (MG-82); unset is sending.DefaultWarning.
	Warning sending.Warning
	// Alert tells a suspended account's owner and the operators (MG-65);
	// nil tells no one.
	Alert *sending.OpsAlert
	Log   *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// Admin returns the user as an Actor if they're an admin, else ErrNotAdmin.
func (s *Service) Admin(ctx context.Context, userID string) (*Actor, error) {
	if userID == "" {
		return nil, ErrNotAdmin
	}
	var a Actor
	var admin bool
	err := s.DB.QueryRow(ctx, `SELECT id, email, admin FROM users WHERE id = $1`, userID).Scan(&a.UserID, &a.Email, &admin)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !admin {
		return nil, ErrNotAdmin
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func cleanReason(reason string) (string, error) {
	reason = strings.Join(strings.Fields(reason), " ")
	switch {
	case reason == "":
		return "", &InputError{Field: "reason", Message: "say why; every admin action is logged with its reason"}
	case utf8.RuneCountInString(reason) > MaxReason:
		return "", &InputError{Field: "reason", Message: fmt.Sprintf("keep it to %d characters", MaxReason)}
	}
	return reason, nil
}

// record logs an action inside tx and returns its id.
func (s *Service) record(ctx context.Context, tx pgx.Tx, a Actor, action string, accountID, targetID *string, reason string,
	details map[string]any, now time.Time) (int64, error) {
	if details == nil {
		details = map[string]any{}
	}
	var userID *string
	if a.UserID != "" {
		userID = &a.UserID
	}
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO admin_actions (actor_user_id, actor, action, account_id, target_id, reason, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		userID, a.Email, action, accountID, targetID, reason, details, now).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("log admin action: %w", err)
	}
	s.log().InfoContext(ctx, "admin action", "action", action, "actor", a.Email, "account_id", deref(accountID),
		"target_id", deref(targetID), "reason", reason, "admin_action_id", id)
	return id, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s *Service) inTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetAdmin makes the user with email an admin, or stops them being one.
func (s *Service) SetAdmin(ctx context.Context, actor Actor, email string, admin bool, reason string) error {
	reason, err := cleanReason(reason)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var userID, accountID string
		err := tx.QueryRow(ctx, `UPDATE users SET admin = $2, updated_at = $3 WHERE lower(email) = lower($1) RETURNING id, account_id`,
			strings.TrimSpace(email), admin, s.now()).Scan(&userID, &accountID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoUser
		}
		if err != nil {
			return err
		}
		action := ActionGrantAdmin
		if !admin {
			action = ActionRevokeAdmin
		}
		_, err = s.record(ctx, tx, actor, action, &accountID, &userID, reason, nil, s.now())
		return err
	})
}

// AccountSummary is a search result.
type AccountSummary struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Plan        string     `json:"plan"`
	OwnerEmail  *string    `json:"owner_email"`
	PausedAt    *time.Time `json:"sending_paused_at"`
	PauseReason *string    `json:"sending_paused_reason"`
	// Suspension (MG-65): when, why and by whom; all nil when it isn't.
	SuspendedAt     *time.Time `json:"suspended_at"`
	SuspendedReason *string    `json:"suspended_reason"`
	SuspendedBy     *string    `json:"suspended_by"`
	// Sign-up review (MG-66): waiting, approved, rejected or nil; what
	// flagged it; and since when it waited.
	Review      *string    `json:"review"`
	ReviewFlags []string   `json:"review_flags"`
	ReviewAt    *time.Time `json:"review_at"`
	// Test mode (MG-80): test or full, and how many test addresses are
	// confirmed and how many wait for their link to be used.
	SendingMode            string    `json:"sending_mode"`
	TestAddressesConfirmed int       `json:"test_addresses_confirmed"`
	TestAddressesPending   int       `json:"test_addresses_pending"`
	CreatedAt              time.Time `json:"created_at"`
}

const summaryColumns = `a.id, a.name, a.status, a.plan, u.email, a.sending_paused_at, a.sending_paused_reason,
	a.suspended_at, a.suspended_reason, a.suspended_by, a.review, a.review_flags, a.review_at, a.sending_mode,
	(SELECT count(*) FILTER (WHERE t.confirmed_at IS NOT NULL) FROM test_addresses t WHERE t.account_id = a.id),
	(SELECT count(*) FILTER (WHERE t.confirmed_at IS NULL) FROM test_addresses t WHERE t.account_id = a.id),
	a.created_at`

func scanSummary(row pgx.Row, a *AccountSummary) error {
	if err := row.Scan(&a.ID, &a.Name, &a.Status, &a.Plan, &a.OwnerEmail, &a.PausedAt, &a.PauseReason,
		&a.SuspendedAt, &a.SuspendedReason, &a.SuspendedBy, &a.Review, &a.ReviewFlags, &a.ReviewAt,
		&a.SendingMode, &a.TestAddressesConfirmed, &a.TestAddressesPending, &a.CreatedAt); err != nil {
		return err
	}
	if a.ReviewFlags == nil {
		a.ReviewFlags = []string{}
	}
	return nil
}

// FindAccounts finds accounts by id, the owner's address, the account's
// name or a sending domain. An address or domain matches in part; a name
// matches any part of it.
func (s *Service) FindAccounts(ctx context.Context, q string, limit int) ([]AccountSummary, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, &InputError{Field: "q", Message: "give an account id, an address, a name or a domain"}
	}
	limit = min(max(limit, 1), MaxFindResults)
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(q)) + "%"
	rows, err := s.DB.Query(ctx, `
		SELECT `+summaryColumns+`
		FROM accounts a LEFT JOIN users u ON u.account_id = a.id
		WHERE a.id::text = $1
			OR lower(u.email) LIKE $2
			OR lower(a.name) LIKE $2
			OR EXISTS (SELECT 1 FROM domains d WHERE d.account_id = a.id AND lower(d.name) LIKE $2)
		ORDER BY (a.id::text = $1) DESC, a.created_at DESC
		LIMIT $3`, q, like, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountSummary, error) {
		var a AccountSummary
		err := scanSummary(row, &a)
		return a, err
	})
}

// HeldImport is an import waiting for approval (CON-6), with what its list
// checks found (MG-63). It shows counts and another account's id, never
// another account's addresses.
type HeldImport struct {
	ID               string      `json:"id"`
	FileName         *string     `json:"file_name"`
	TotalRows        int         `json:"total_rows"`
	HoldReason       string      `json:"hold_reason"`
	Status           string      `json:"status"`
	ListSource       *string     `json:"list_source"`
	ListSourceDetail *string     `json:"list_source_detail"`
	Checks           ImportCheck `json:"checks"`
	CreatedAt        time.Time   `json:"created_at"`
}

// ImportCheck is what a held import's checks found.
type ImportCheck struct {
	InvalidRows         int     `json:"invalid_rows"`
	RoleAddresses       int     `json:"role_addresses"`
	DisposableAddresses int     `json:"disposable_addresses"`
	SharedAddresses     int     `json:"shared_addresses"`
	SharedAccountID     *string `json:"shared_account_id"`
}

// LiveCampaign is a campaign that's scheduled, sending or paused.
type LiveCampaign struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	SendAt     *time.Time    `json:"send_at"`
	Recipients int           `json:"recipients"`
	Rates      sending.Rates `json:"rates"`
}

// Throttle is an early-warning throttle (MG-61).
type Throttle struct {
	ID         int64     `json:"id"`
	CampaignID *string   `json:"campaign_id"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
	Until      time.Time `json:"until"`
}

// Account is everything an admin looks at.
type Account struct {
	AccountSummary
	// DailySendLimit is the account's own limit; nil uses the schedule.
	DailySendLimit *int `json:"daily_send_limit"`
	// SendLimit is its limit now, and why (MG-61).
	SendLimit sending.SendLimit `json:"send_limit"`
	// Throttles are its throttles in the last 30 days, newest first.
	Throttles     []Throttle     `json:"throttles"`
	Contacts      int            `json:"contacts"`
	Balance       int64          `json:"credit_balance"`
	Last30Days    sending.Rates  `json:"last_30_days"`
	HeldImports   []HeldImport   `json:"held_imports"`
	LiveCampaigns []LiveCampaign `json:"live_campaigns"`
	// TermsVersion is the latest version of the terms the account accepted,
	// at TermsAcceptedAt (MG-68).
	TermsVersion    *string    `json:"terms_version"`
	TermsAcceptedAt *time.Time `json:"terms_accepted_at"`
	// Tester is its tester state (MG-86); nil when it isn't one.
	Tester *tester.Status `json:"tester"`
}

// GetAccount is one account in full.
func (s *Service) GetAccount(ctx context.Context, id string) (*Account, error) {
	var a Account
	err := scanSummary(s.DB.QueryRow(ctx, `
		SELECT `+summaryColumns+` FROM accounts a LEFT JOIN users u ON u.account_id = a.id WHERE a.id = $1`, id), &a.AccountSummary)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	since := s.now().Add(-sending.HealthWindow)
	if err := s.DB.QueryRow(ctx, `
		SELECT a.daily_send_limit, a.terms_version, a.terms_accepted_at,
			(SELECT count(*) FROM contacts WHERE account_id = a.id),
			(SELECT count(*) FROM campaign_recipients WHERE account_id = a.id AND sent_at > $2 AND NOT simulated),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE account_id = a.id AND occurred_at > $2 AND kind = 'campaign' AND NOT simulated
					AND type = 'bounce' AND subtype LIKE 'permanent/%'),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE account_id = a.id AND occurred_at > $2 AND kind = 'campaign' AND NOT simulated AND type = 'complaint')
		FROM accounts a WHERE a.id = $1`, id, since).Scan(&a.DailySendLimit, &a.TermsVersion, &a.TermsAcceptedAt, &a.Contacts,
		&a.Last30Days.Sent, &a.Last30Days.HardBounces, &a.Last30Days.Complaints); err != nil {
		return nil, err
	}
	ledger := &credits.Ledger{DB: s.DB, Now: s.Now}
	sum, err := ledger.Summary(ctx, id)
	if err != nil {
		return nil, err
	}
	a.Balance = sum.Balance
	if a.SendLimit, err = s.Limits.Account(ctx, s.DB, id, s.now()); err != nil {
		return nil, err
	}
	if a.Tester, err = tester.Load(ctx, s.DB, id, s.now(), false); err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `
		SELECT id, campaign_id, reason, created_at, until FROM account_throttles
		WHERE account_id = $1 AND created_at > $2 ORDER BY created_at DESC, id DESC`, id, s.now().Add(-sending.ThrottleWindow))
	if err != nil {
		return nil, err
	}
	if a.Throttles, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Throttle, error) {
		var t Throttle
		err := row.Scan(&t.ID, &t.CampaignID, &t.Reason, &t.CreatedAt, &t.Until)
		return t, err
	}); err != nil {
		return nil, err
	}

	rows, err = s.DB.Query(ctx, `
		SELECT id, file_name, total_rows, coalesce(hold_reason, ''), status, list_source, list_source_detail,
			invalid_rows, role_addresses, disposable_addresses, shared_addresses, shared_account_id::text, created_at FROM imports
		WHERE account_id = $1 AND review = 'held' ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	if a.HeldImports, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (HeldImport, error) {
		var h HeldImport
		c := &h.Checks
		err := row.Scan(&h.ID, &h.FileName, &h.TotalRows, &h.HoldReason, &h.Status, &h.ListSource, &h.ListSourceDetail,
			&c.InvalidRows, &c.RoleAddresses, &c.DisposableAddresses, &c.SharedAddresses, &c.SharedAccountID, &h.CreatedAt)
		return h, err
	}); err != nil {
		return nil, err
	}

	rows, err = s.DB.Query(ctx, `
		SELECT id, name, status, send_at, coalesce(recipient_total, 0) FROM campaigns
		WHERE account_id = $1 AND status IN ('scheduled', 'sending', 'paused') ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	if a.LiveCampaigns, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (LiveCampaign, error) {
		var c LiveCampaign
		err := row.Scan(&c.ID, &c.Name, &c.Status, &c.SendAt, &c.Recipients)
		return c, err
	}); err != nil {
		return nil, err
	}
	for i := range a.LiveCampaigns {
		if a.LiveCampaigns[i].Rates, err = sending.CampaignRates(ctx, s.DB, a.LiveCampaigns[i].ID); err != nil {
			return nil, err
		}
	}
	return &a, nil
}

// Pause stops an account's sending at once (CMP-8): nothing more is
// batched, and a batch on its way stops before its next copy. Its
// campaigns stay as they are and carry on when it's unpaused. reason is
// logged, and shown to the account as why. Pausing a paused account
// replaces the reason, and takes over a past-due pause (its code goes), so
// the plan's payment going through doesn't lift a person's pause (MG-105).
func (s *Service) Pause(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		tag, err := tx.Exec(ctx, `
			UPDATE accounts SET sending_paused_at = coalesce(sending_paused_at, $2), sending_paused_reason = $3, sending_paused_code = NULL, updated_at = $2
			WHERE id = $1`, accountID, now, reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = s.record(ctx, tx, actor, ActionPause, &accountID, nil, reason, nil, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// Unpause lets an account send again; its sending campaigns carry on from
// where they stopped.
func (s *Service) Unpause(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		var was, code, subStatus *string
		err := tx.QueryRow(ctx, `
			UPDATE accounts a SET sending_paused_at = NULL, sending_paused_reason = NULL, sending_paused_code = NULL, updated_at = $2
			FROM accounts old WHERE a.id = $1 AND old.id = a.id
			RETURNING old.sending_paused_reason, old.sending_paused_code, a.subscription_status`, accountID, now).Scan(&was, &code, &subStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := s.record(ctx, tx, actor, ActionUnpause, &accountID, nil, reason, map[string]any{"paused_reason": was}, now); err != nil {
			return err
		}
		// A plan payment that went past due while sending was paused for
		// something else is still owed: its own pause takes over. Lifting
		// the past-due pause itself is staff's call and stands.
		if billing.Overdue(deref(subStatus)) && deref(code) != billing.SubscriptionPastDue {
			return billing.PauseOverdue(ctx, tx, accountID, deref(subStatus), now)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// Suspend suspends an account (MG-65): see sending.Suspend. Its keys and
// CLI logins are revoked, its web sessions end, its live campaigns stop,
// and its owner and the operators get an email. Its credits stay as they
// are. reason is logged and shown to the account as why.
func (s *Service) Suspend(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	var sus *sending.Suspension
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		sus, err = sending.Suspend(ctx, tx, accountID, sending.Suspender{UserID: actor.UserID, Email: actor.Email}, reason, nil, s.now())
		switch {
		case errors.Is(err, sending.ErrNoAccount):
			return ErrNotFound
		case errors.Is(err, sending.ErrAlreadySuspended):
			return ErrSuspended
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	s.log().InfoContext(ctx, "admin action", "action", ActionSuspend, "actor", actor.Email, "account_id", accountID,
		"reason", reason, "admin_action_id", sus.ActionID)
	if s.Alert != nil {
		s.Alert.Suspended(ctx, *sus)
	}
	return s.GetAccount(ctx, accountID)
}

// Unsuspend lets a suspended account sign in and send again. Its API keys
// and CLI logins stay revoked: the owner makes new ones. A pause stays
// until it's lifted on its own (Unpause).
func (s *Service) Unsuspend(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		var was, by *string
		err := tx.QueryRow(ctx, `
			UPDATE accounts a SET suspended_at = NULL, suspended_reason = NULL, suspended_by = NULL, updated_at = $2
			FROM accounts old WHERE a.id = $1 AND old.id = a.id
			RETURNING old.suspended_reason, old.suspended_by`, accountID, now).Scan(&was, &by)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if was == nil {
			return ErrNotSuspended
		}
		_, err = s.record(ctx, tx, actor, ActionUnsuspend, &accountID, nil, reason,
			map[string]any{"suspended_reason": was, "suspended_by": by}, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// ListSignups is the accounts waiting for their sign-up review (MG-66),
// oldest first.
func (s *Service) ListSignups(ctx context.Context, limit int) ([]AccountSummary, error) {
	limit = min(max(limit, 1), MaxFindResults)
	rows, err := s.DB.Query(ctx, `
		SELECT `+summaryColumns+`
		FROM accounts a LEFT JOIN users u ON u.account_id = a.id
		WHERE a.review = 'waiting'
		ORDER BY a.review_at, a.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountSummary, error) {
		var a AccountSummary
		err := scanSummary(row, &a)
		return a, err
	})
}

// endReview moves a waiting account's review to outcome and logs it as
// action, inside tx.
func (s *Service) endReview(ctx context.Context, tx pgx.Tx, actor Actor, accountID, outcome, action, reason string, now time.Time) error {
	var review *string
	var flags []string
	err := tx.QueryRow(ctx, `SELECT review, review_flags FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&review, &flags)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if deref(review) != "waiting" {
		return ErrNotWaiting
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET review = $2, updated_at = $3 WHERE id = $1`, accountID, outcome, now); err != nil {
		return err
	}
	_, err = s.record(ctx, tx, actor, action, &accountID, nil, reason, map[string]any{"review_flags": flags}, now)
	return err
}

// ApproveSignup ends a waiting account's review (MG-66): it can launch
// campaigns and take form signups.
func (s *Service) ApproveSignup(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	if err := s.inTx(ctx, func(tx pgx.Tx) error {
		return s.endReview(ctx, tx, actor, accountID, "approved", ActionApproveSignup, reason, s.now())
	}); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// RejectSignup rejects a waiting account (MG-66) and suspends it, as
// Suspend does, with reason as why. An account suspended already stays as
// it is, and is marked rejected.
func (s *Service) RejectSignup(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	var sus *sending.Suspension
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		if err := s.endReview(ctx, tx, actor, accountID, "rejected", ActionRejectSignup, reason, now); err != nil {
			return err
		}
		sus, err = sending.Suspend(ctx, tx, accountID, sending.Suspender{UserID: actor.UserID, Email: actor.Email}, reason,
			map[string]any{"signup_review": "rejected"}, now)
		if errors.Is(err, sending.ErrAlreadySuspended) {
			sus, err = nil, nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if sus != nil && s.Alert != nil {
		s.Alert.Suspended(ctx, *sus)
	}
	return s.GetAccount(ctx, accountID)
}

// ListTestMode is the accounts in test mode (MG-80), oldest first, closed
// ones left out.
func (s *Service) ListTestMode(ctx context.Context, limit int) ([]AccountSummary, error) {
	limit = min(max(limit, 1), MaxFindResults)
	rows, err := s.DB.Query(ctx, `
		SELECT `+summaryColumns+`
		FROM accounts a LEFT JOIN users u ON u.account_id = a.id
		WHERE a.sending_mode = 'test' AND a.status <> 'closed'
		ORDER BY a.created_at, a.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (AccountSummary, error) {
		var a AccountSummary
		err := scanSummary(row, &a)
		return a, err
	})
}

// MaxDeliverabilityRows is the most accounts the deliverability report
// lists at once.
const MaxDeliverabilityRows = 100

// Deliverability is the per-customer deliverability report (MG-82).
type Deliverability struct {
	// Accounts are the worst first, at most the limit asked for.
	Accounts []sending.AccountDeliverability
	// Total is every account in the report, over the limit or not.
	Total int
	// Warning is the early warning OverWarning was judged by.
	Warning sending.Warning
	// Throughput is how fast copies went out over the last 24 hours,
	// against the configured rate (MG-91).
	Throughput sending.Throughput
}

// Deliverability is every account that sent campaign email in the last 7
// days, or had a hard bounce or complaint reported in them, with its rates
// over 24 hours and 7 days, worst first: at most limit (1 to
// MaxDeliverabilityRows) of them (MG-82); and the last 24 hours' send
// throughput (MG-91).
func (s *Service) Deliverability(ctx context.Context, limit int) (*Deliverability, error) {
	limit = min(max(limit, 1), MaxDeliverabilityRows)
	w := s.Warning
	if w.ComplaintsPer10k <= 0 || w.HardBouncesPer10k <= 0 {
		w = sending.DefaultWarning
	}
	now := s.now()
	all, err := sending.DeliverabilityReport(ctx, s.DB, now, w)
	if err != nil {
		return nil, err
	}
	tp, err := sending.SendThroughput(ctx, s.DB, now, s.Limits.PerSecond)
	if err != nil {
		return nil, err
	}
	return &Deliverability{Accounts: all[:min(limit, len(all))], Total: len(all), Warning: w, Throughput: tp}, nil
}

// Storage is the database's table sizes (MG-95): campaign_recipients and
// send_events first, then the largest other tables, at most limit (1 to
// sending.MaxStorageTables; 0 is sending.DefaultStorageTables) of them,
// each with its size 7 days before where the digest job recorded one.
func (s *Service) Storage(ctx context.Context, limit int) (*sending.StorageReport, error) {
	if limit == 0 {
		limit = sending.DefaultStorageTables
	}
	rep, err := sending.StorageSizes(ctx, s.DB, s.now(), limit)
	if err != nil {
		return nil, err
	}
	return &rep, nil
}

// AllowFullSending takes an account out of test mode (MG-80): it can send
// to its whole audience. It doesn't approve a sign-up review; a waiting
// account still needs that to launch to anyone but its test addresses.
func (s *Service) AllowFullSending(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	return s.setSendingMode(ctx, actor, accountID, "full", ActionAllowFullSending, reason)
}

// SetTestMode puts an account in test mode (MG-80): from its next launch,
// test send or form signup, and its next copy in flight, it sends only to
// its confirmed test addresses and its owner's address.
func (s *Service) SetTestMode(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	return s.setSendingMode(ctx, actor, accountID, "test", ActionSetTestMode, reason)
}

func (s *Service) setSendingMode(ctx context.Context, actor Actor, accountID, mode, action, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	if err := s.inTx(ctx, func(tx pgx.Tx) error {
		var cur string
		err := tx.QueryRow(ctx, `SELECT sending_mode FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur == mode {
			return ErrSendingModeUnchanged
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE accounts SET sending_mode = $2, updated_at = $3 WHERE id = $1`, accountID, mode, now); err != nil {
			return err
		}
		_, err = s.record(ctx, tx, actor, action, &accountID, nil, reason, map[string]any{"from": cur, "to": mode}, now)
		return err
	}); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// GrantTester makes an account a tester account (MG-86), or changes the
// end and credits of one: until is when it stops sending (nil is
// tester.DefaultDays from now), and credits its sends for its life (0 is
// tester.Credits). Its credits are counted from when it first became a
// tester, so granting again extends it without a fresh start. It also
// gets full sending, as a tester sends to people who agreed, not only to
// test addresses. Only an account whose sign-up was approved, or never
// held for review (MG-66), and that isn't suspended can be one.
func (s *Service) GrantTester(ctx context.Context, actor Actor, accountID string, until *time.Time, credits int, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	now := s.now()
	end := now.AddDate(0, 0, tester.DefaultDays)
	if until != nil {
		end = until.UTC()
	}
	switch {
	case !end.After(now):
		return nil, &InputError{Field: "until", Message: "the end date has passed"}
	case end.After(now.AddDate(0, 0, tester.MaxDays)):
		return nil, &InputError{Field: "until", Message: fmt.Sprintf("a tester account lasts at most %d days", tester.MaxDays)}
	}
	if credits == 0 {
		credits = tester.Credits
	}
	if credits < 1 || credits > tester.MaxCredits {
		return nil, &InputError{Field: "credits", Message: fmt.Sprintf("credits must be 1 to %d", tester.MaxCredits)}
	}
	if err := s.inTx(ctx, func(tx pgx.Tx) error {
		var review, mode string
		var suspended bool
		var started *time.Time
		err := tx.QueryRow(ctx, `
			SELECT coalesce(review, ''), suspended_at IS NOT NULL, sending_mode, tester_started_at FROM accounts WHERE id = $1 FOR UPDATE`,
			accountID).Scan(&review, &suspended, &mode, &started)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrNotFound
		case err != nil:
			return err
		case review == "waiting" || review == "rejected":
			return ErrNotApproved
		case suspended:
			return ErrSuspended
		}
		if _, err := tx.Exec(ctx, `
			UPDATE accounts SET tester_started_at = coalesce(tester_started_at, $2), tester_until = $3, tester_credits = $4,
				sending_mode = 'full', updated_at = $2
			WHERE id = $1`, accountID, now, end, credits); err != nil {
			return err
		}
		_, err = s.record(ctx, tx, actor, ActionGrantTester, &accountID, nil, reason,
			map[string]any{"until": end, "credits": credits, "extended": started != nil, "sending_mode_was": mode}, now)
		return err
	}); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// RevokeTester ends an account's tester state (MG-86) and puts it in test
// mode (MG-80): from now on it sends only to its confirmed test addresses
// and its owner's address, and copies of a campaign already sending to
// anyone else are skipped. Making it a customer afterwards is
// AllowFullSending. Granting it again starts its credits afresh.
func (s *Service) RevokeTester(ctx context.Context, actor Actor, accountID, reason string) (*Account, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	if err := s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		st, err := tester.Load(ctx, tx, accountID, now, false)
		if err != nil {
			return err
		}
		if st == nil {
			return ErrNotTester
		}
		if _, err := tx.Exec(ctx, `
			UPDATE accounts SET tester_started_at = NULL, tester_until = NULL, tester_credits = NULL,
				sending_mode = 'test', updated_at = $2
			WHERE id = $1`, accountID, now); err != nil {
			return err
		}
		_, err = s.record(ctx, tx, actor, ActionRevokeTester, &accountID, nil, reason,
			map[string]any{"until": st.Until, "credits": st.Credits, "used": st.Used}, now)
		return err
	}); err != nil {
		return nil, err
	}
	return s.GetAccount(ctx, accountID)
}

// StopCampaign is the kill switch for one campaign (CMP-8): see
// sending.Service.Stop.
func (s *Service) StopCampaign(ctx context.Context, actor Actor, campaignID, reason string) (*campaigns.Campaign, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	var accountID string
	err = s.DB.QueryRow(ctx, `SELECT account_id FROM campaigns WHERE id = $1`, campaignID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// The stop and its log are one transaction: a stop is never left
	// unlogged, and stopping a cancelled campaign changes nothing and logs
	// nothing.
	return s.Sending.StopAnd(ctx, accountID, campaignID, func(tx pgx.Tx, before string) error {
		_, err := s.record(ctx, tx, actor, ActionStopCampaign, &accountID, &campaignID, reason,
			map[string]any{"status_before": before}, s.now())
		return err
	})
}

// ApprovedImport is an import let through.
type ApprovedImport struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	// Released is how many of its contacts can now be mailed.
	Released int64 `json:"contacts_released"`
}

// ApproveImport lets a held import's contacts be mailed (CON-6). One still
// running keeps going, unheld.
func (s *Service) ApproveImport(ctx context.Context, actor Actor, importID, reason string) (*ApprovedImport, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	out := &ApprovedImport{ID: importID}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var review string
		err := tx.QueryRow(ctx, `SELECT account_id, review FROM imports WHERE id = $1 FOR UPDATE`, importID).Scan(&out.AccountID, &review)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if review != "held" {
			return ErrNotHeld
		}
		now := s.now()
		if _, err := tx.Exec(ctx, `UPDATE imports SET review = 'approved' WHERE id = $1`, importID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE contacts SET held_import_id = NULL WHERE held_import_id = $1`, importID)
		if err != nil {
			return err
		}
		out.Released = tag.RowsAffected()
		_, err = s.record(ctx, tx, actor, ActionApproveImport, &out.AccountID, &importID, reason,
			map[string]any{"contacts_released": out.Released}, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Granted is credits given.
type Granted struct {
	AccountID string    `json:"account_id"`
	Amount    int64     `json:"amount"`
	ExpiresAt time.Time `json:"expires_at"`
	Balance   int64     `json:"credit_balance"`
}

// GrantCredits gives an account credits, which last as bought ones do.
func (s *Service) GrantCredits(ctx context.Context, actor Actor, accountID string, amount int64, reason string) (*Granted, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	if amount < 1 || amount > MaxGrant {
		return nil, &InputError{Field: "amount", Message: fmt.Sprintf("grant between 1 and %d credits", MaxGrant)}
	}
	out := &Granted{AccountID: accountID, Amount: amount}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		now := s.now()
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM accounts WHERE id = $1`, accountID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		id, err := s.record(ctx, tx, actor, ActionGrantCredits, &accountID, nil, reason, map[string]any{"amount": amount}, now)
		if err != nil {
			return err
		}
		t, err := credits.Grant(ctx, tx, accountID, amount, "admin:"+strconv.FormatInt(id, 10), now)
		if err != nil {
			return err
		}
		out.ExpiresAt = *t.ExpiresAt
		out.Balance, err = credits.Balance(ctx, tx, accountID, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Action is one logged action.
type Action struct {
	ID        int64           `json:"id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	AccountID *string         `json:"account_id"`
	TargetID  *string         `json:"target_id"`
	Reason    string          `json:"reason"`
	Details   json.RawMessage `json:"details"`
	CreatedAt time.Time       `json:"created_at"`
}

// ListActions is the log, newest first, for one account or all, before
// the action id before (0: from the newest).
func (s *Service) ListActions(ctx context.Context, accountID string, before int64, limit int) ([]Action, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, actor, action, account_id, target_id, reason, details, created_at FROM admin_actions
		WHERE ($1 = '' OR account_id = nullif($1, '')::uuid) AND ($2 = 0 OR id < $2)
		ORDER BY id DESC LIMIT $3`, accountID, before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Action, error) {
		var a Action
		err := row.Scan(&a.ID, &a.Actor, &a.Action, &a.AccountID, &a.TargetID, &a.Reason, &a.Details, &a.CreatedAt)
		return a, err
	})
}
