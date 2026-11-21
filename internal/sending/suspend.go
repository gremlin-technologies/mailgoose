package sending

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// Account suspension (MG-65), the step past a pause.
//
// Suspending an account revokes its API keys and CLI logins, ends its web
// sessions, stops its scheduled, sending and paused campaigns (what they
// hadn't sent is given back, as with any stop) and stops its forms taking
// signups. Its owner can still sign in, to a read-only page that explains
// the suspension and offers the data export for SuspensionExport. Credits
// are left as they are. An admin suspends with `mailgoose admin suspend`;
// the system suspends on a second automatic sending pause in 30 days, and
// other automatic triggers call Suspend from their own code.
const (
	// SuspensionExport is how long a suspended account's owner can still
	// export its contacts and campaigns.
	SuspensionExport = 30 * 24 * time.Hour
	// SuspendAfterPauses is how many automatic sending pauses within
	// HealthWindow suspend the account. The past-due pause isn't one
	// (MG-104); see suspendOnRepeat.
	SuspendAfterPauses = 2
)

// Admin actions a suspension logs.
const (
	ActionSuspend     = "suspend"
	ActionAutoSuspend = "auto_suspend"
	ActionUnsuspend   = "unsuspend"
)

// Suspender is who suspends an account.
type Suspender struct {
	// UserID is the admin's user id; empty for the system and mailgoosed.
	UserID string
	// Email is the admin's address, "mailgoosed" or "automatic".
	Email string
}

// Automatic is the system suspending an account on its own.
var Automatic = Suspender{Email: "automatic"}

// Errors from Suspend.
var (
	ErrAlreadySuspended = errors.New("the account is suspended already")
	ErrNoAccount        = errors.New("no such account")
)

// Suspension is an account just suspended.
type Suspension struct {
	AccountID   string
	AccountName string
	// OwnerEmail is where the notice goes; empty when the account has no
	// user.
	OwnerEmail string
	Reason     string
	// By is the Suspender's Email.
	By string
	At time.Time
	// ActionID is its admin_actions row.
	ActionID int64
	// What it stopped.
	KeysRevoked      int64
	SessionsEnded    int64
	CampaignsStopped []string
}

// ExportUntil is when the owner's data export stops working.
func (s Suspension) ExportUntil() time.Time { return s.At.Add(SuspensionExport) }

// Suspend suspends an account inside tx, logs it in admin_actions with
// details added, and returns what it did. The caller tells the owner and
// the operators after the commit (OpsAlert.Suspended). A suspended or
// closed account is ErrAlreadySuspended or ErrNoAccount, and nothing
// changes.
//
// Campaigns are locked before the account, in the order a launch takes
// them; the account's own row last.
func Suspend(ctx context.Context, tx pgx.Tx, accountID string, by Suspender, reason string, details map[string]any, now time.Time) (*Suspension, error) {
	s := &Suspension{AccountID: accountID, Reason: reason, By: by.Email, At: now}
	rows, err := tx.Query(ctx, `
		SELECT id FROM campaigns WHERE account_id = $1 AND status IN ('scheduled', 'sending', 'paused') ORDER BY id`, accountID)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		stopped, err := stopForSuspension(ctx, tx, accountID, id, now)
		if err != nil {
			return nil, err
		}
		if stopped {
			s.CampaignsStopped = append(s.CampaignsStopped, id)
		}
	}

	err = tx.QueryRow(ctx, `
		UPDATE accounts SET suspended_at = $2, suspended_reason = $3, suspended_by = $4, updated_at = $2
		WHERE id = $1 AND status <> 'closed' AND suspended_at IS NULL
		RETURNING name`, accountID, now, reason, by.Email).Scan(&s.AccountName)
	if errors.Is(err, pgx.ErrNoRows) {
		var suspended bool
		err := tx.QueryRow(ctx, `SELECT suspended_at IS NOT NULL FROM accounts WHERE id = $1 AND status <> 'closed'`,
			accountID).Scan(&suspended)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, ErrNoAccount
		case err != nil:
			return nil, err
		case suspended:
			return nil, ErrAlreadySuspended
		}
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = $2 WHERE account_id = $1 AND revoked_at IS NULL`, accountID, now)
	if err != nil {
		return nil, err
	}
	s.KeysRevoked = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = $2
		WHERE user_id IN (SELECT id FROM users WHERE account_id = $1) AND revoked_at IS NULL AND expires_at > $2`, accountID, now)
	if err != nil {
		return nil, err
	}
	s.SessionsEnded = tag.RowsAffected()
	// A CLI login approved but not yet collected gets no token.
	if _, err := tx.Exec(ctx, `
		UPDATE device_logins SET status = 'denied', decided_at = $2 WHERE account_id = $1 AND status = 'approved'`,
		accountID, now); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT email FROM users WHERE account_id = $1 LIMIT 1), '')`,
		accountID).Scan(&s.OwnerEmail); err != nil {
		return nil, err
	}

	d := map[string]any{
		"keys_revoked":      s.KeysRevoked,
		"sessions_ended":    s.SessionsEnded,
		"campaigns_stopped": s.CampaignsStopped,
	}
	if s.CampaignsStopped == nil {
		d["campaigns_stopped"] = []string{}
	}
	for k, v := range details {
		d[k] = v
	}
	action := ActionSuspend
	var userID *string
	if by == Automatic {
		action = ActionAutoSuspend
	} else if by.UserID != "" {
		userID = &by.UserID
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO admin_actions (actor_user_id, actor, action, account_id, reason, details, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		userID, by.Email, action, accountID, reason, d, now).Scan(&s.ActionID); err != nil {
		return nil, fmt.Errorf("log the suspension: %w", err)
	}
	return s, nil
}

// stopForSuspension cancels one live campaign, locking its queue row and
// then the campaign as Stop does. One that ended meanwhile is left alone.
func stopForSuspension(ctx context.Context, tx pgx.Tx, accountID, campaignID string, now time.Time) (bool, error) {
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM send_queue WHERE account_id = $1 AND campaign_id = $2 FOR UPDATE`,
		accountID, campaignID).Scan(&one); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM campaigns WHERE account_id = $1 AND id = $2 FOR UPDATE`,
		accountID, campaignID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch status {
	case campaigns.StatusScheduled, campaigns.StatusSending, campaigns.StatusPaused:
	default:
		return false, nil
	}
	return true, cancelLaunched(ctx, tx, accountID, campaignID, "stopped: the account was suspended", now)
}

// suspendOnRepeat suspends the account when this is its
// SuspendAfterPauses-th automatic sending pause within HealthWindow
// (MG-65). It runs in CheckCampaign's transaction, after the pause is
// logged.
//
// Every automatic pause is logged as 'auto_pause', but only those about
// the account's sending count: complaints, bounces, an account-level pause
// and an unsafe link. The pause billing makes while the paid plan's payment
// is past due is logged the same way, with details code
// SubscriptionPastDue; a failed card says nothing about what the account
// sends, so it's left out (MG-104). The digest and the admin log still list
// it.
func suspendOnRepeat(ctx context.Context, tx pgx.Tx, ap *AutoPause, now time.Time) (*Suspension, error) {
	var pauses int
	var suspended bool
	if err := tx.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM admin_actions WHERE account_id = $1 AND action = 'auto_pause' AND created_at > $2
				AND details->>'code' IS DISTINCT FROM $3),
			(SELECT suspended_at IS NOT NULL FROM accounts WHERE id = $1)`,
		ap.AccountID, now.Add(-HealthWindow), SubscriptionPastDue).Scan(&pauses, &suspended); err != nil {
		return nil, err
	}
	if pauses < SuspendAfterPauses || suspended {
		return nil, nil
	}
	reason := fmt.Sprintf("It was suspended automatically: sending was paused automatically %d times in 30 days. The latest: %s",
		pauses, strings.TrimPrefix(ap.Reason, "It was paused automatically: "))
	s, err := Suspend(ctx, tx, ap.AccountID, Automatic, reason,
		map[string]any{"campaign_id": ap.CampaignID, "auto_pauses": pauses}, now)
	if errors.Is(err, ErrAlreadySuspended) || errors.Is(err, ErrNoAccount) {
		return nil, nil
	}
	return s, err
}

// Suspended tells the account's owner about a suspension, and how to
// appeal, and the operators too. It never fails: a notice that can't be
// sent is logged.
func (a *OpsAlert) Suspended(ctx context.Context, s Suspension) {
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	log.ErrorContext(ctx, "account suspended", "account_id", s.AccountID, "by", s.By,
		"keys_revoked", s.KeysRevoked, "sessions_ended", s.SessionsEnded, "campaigns_stopped", len(s.CampaignsStopped))
	if a.Mail == nil {
		return
	}
	if s.OwnerEmail != "" {
		text := SuspensionNotice(s, a.AppURL, a.abuse())
		_, err := a.Mail.Send(context.WithoutCancel(ctx), mail.Message{
			From:    a.From,
			To:      mail.Address{Email: s.OwnerEmail},
			ReplyTo: []mail.Address{{Email: a.abuse()}},
			Subject: "Your mailgoose account is suspended",
			Text:    text,
			HTML:    "<!doctype html><html><body><pre style=\"white-space: pre-wrap\">" + html.EscapeString(text) + "</pre></body></html>",
			Kind:    mail.KindSystem,
		})
		if err != nil {
			log.ErrorContext(ctx, "send the suspension notice", "err", err, "account_id", s.AccountID)
		}
	}
	a.Notify(ctx, s.AccountName+" suspended", fmt.Sprintf(`%s (account %s) was suspended by %s.

%s

Revoked %d API keys and CLI logins, ended %d web sessions, stopped %d campaigns.
The owner was told how to appeal (by replying to %s).

Look into it with:
  mailgoose admin account %s
Lift it with:
  mailgoose admin unsuspend %s --reason "..."
`, s.AccountName, s.AccountID, s.By, s.Reason, s.KeysRevoked, s.SessionsEnded, len(s.CampaignsStopped),
		a.abuse(), s.AccountID, s.AccountID))
}

// abuse is where appeals go: Abuse, or abuse@ the From address's domain.
func (a *OpsAlert) abuse() string {
	if a.Abuse != "" {
		return a.Abuse
	}
	_, domain, _ := strings.Cut(a.From.Email, "@")
	return "abuse@" + domain
}

// SuspensionNotice is the email that tells an account's owner it's
// suspended. appURL is where they sign in; abuse is where appeals go.
func SuspensionNotice(s Suspension, appURL, abuse string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your mailgoose account %q was suspended on %s.\n\n", s.AccountName, s.At.UTC().Format("January 2, 2006 at 15:04 UTC"))
	fmt.Fprintf(&b, "Why: %s\n\n", s.Reason)
	b.WriteString("While it's suspended, nothing is sent from it. Its scheduled and sending campaigns were stopped, " +
		"its signup forms don't take new signups, and its API keys and CLI logins no longer work. " +
		"Your credits are unchanged.\n\n")
	where := "the mailgoose web app"
	if appURL != "" {
		where = strings.TrimRight(appURL, "/")
	}
	fmt.Fprintf(&b, "You can still sign in at %s to download your contacts and campaigns, until %s.\n\n",
		where, s.ExportUntil().UTC().Format("January 2, 2006"))
	fmt.Fprintf(&b, "To appeal, reply to this email (it goes to %s) and tell us what happened.\n", abuse)
	return b.String()
}
