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
	Log    *slog.Logger
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
	CreatedAt   time.Time  `json:"created_at"`
}

const summaryColumns = `a.id, a.name, a.status, a.plan, u.email, a.sending_paused_at, a.sending_paused_reason, a.created_at`

func scanSummary(row pgx.Row, a *AccountSummary) error {
	return row.Scan(&a.ID, &a.Name, &a.Status, &a.Plan, &a.OwnerEmail, &a.PausedAt, &a.PauseReason, &a.CreatedAt)
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

// HeldImport is an import waiting for approval (CON-6).
type HeldImport struct {
	ID         string    `json:"id"`
	FileName   *string   `json:"file_name"`
	TotalRows  int       `json:"total_rows"`
	HoldReason string    `json:"hold_reason"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
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
		SELECT a.daily_send_limit,
			(SELECT count(*) FROM contacts WHERE account_id = a.id),
			(SELECT count(*) FROM campaign_recipients WHERE account_id = a.id AND sent_at > $2),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE account_id = a.id AND occurred_at > $2 AND kind = 'campaign' AND type = 'bounce' AND subtype LIKE 'permanent/%'),
			(SELECT count(DISTINCT recipient_id) FROM send_events
				WHERE account_id = a.id AND occurred_at > $2 AND kind = 'campaign' AND type = 'complaint')
		FROM accounts a WHERE a.id = $1`, id, since).Scan(&a.DailySendLimit, &a.Contacts,
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
		SELECT id, file_name, total_rows, coalesce(hold_reason, ''), status, created_at FROM imports
		WHERE account_id = $1 AND review = 'held' ORDER BY created_at`, id)
	if err != nil {
		return nil, err
	}
	if a.HeldImports, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (HeldImport, error) {
		var h HeldImport
		err := row.Scan(&h.ID, &h.FileName, &h.TotalRows, &h.HoldReason, &h.Status, &h.CreatedAt)
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
// replaces the reason.
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
