package auth

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/emaillists"
)

// Risky sign-up review (MG-66): which new accounts wait for a person to
// approve them before they can launch campaigns or take form signups.
const (
	// ReviewAll makes every new account wait (the beta and launch month).
	ReviewAll = "all"
	// ReviewFlagged makes only flagged accounts wait.
	ReviewFlagged = "flagged"
	// ReviewOff lets every account start on the normal ramp.
	ReviewOff = "off"
)

// Why a sign-up was flagged.
const (
	FlagDisposableDomain = "disposable_domain"
	FlagIPBurst          = "ip_burst"
	FlagSuspendedIP      = "suspended_ip"
	FlagSuspendedDomain  = "suspended_domain"
)

const (
	// MaxSignupsPerIP is how many accounts one IP may make in SignupIPWindow
	// before the next is flagged.
	MaxSignupsPerIP = 3
	// SignupIPWindow is the window MaxSignupsPerIP counts over.
	SignupIPWindow = 24 * time.Hour
	// SignupIPRetention is how long a sign-up's IP is kept.
	SignupIPRetention = 90 * 24 * time.Hour
)

// Waiting is an account that has just started waiting for review: it was
// activated with its review pending.
type Waiting struct {
	AccountID   string
	AccountName string
	Email       string
	Flags       []string
}

func (s *Service) reviewSetting() string {
	switch s.SignupReview {
	case ReviewFlagged, ReviewOff:
		return s.SignupReview
	}
	return ReviewAll
}

// signupFlags says why a new sign-up from email and ip looks risky, if it
// does. It runs before the account's own IP is stored.
func signupFlags(ctx context.Context, tx pgx.Tx, email, ip string, now time.Time) ([]string, error) {
	domain := strings.ToLower(email[strings.LastIndex(email, "@")+1:])
	flags := []string{}
	if emaillists.Disposable(domain) {
		flags = append(flags, FlagDisposableDomain)
	}
	if ip != "" {
		var recent int
		var suspended bool
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE s.created_at > $2),
			       coalesce(bool_or(a.suspended_at IS NOT NULL), false)
			FROM signup_ips s JOIN accounts a ON a.id = s.account_id WHERE s.ip = $1`,
			ip, now.Add(-SignupIPWindow)).Scan(&recent, &suspended); err != nil {
			return nil, fmt.Errorf("check sign-up IP: %w", err)
		}
		if recent >= MaxSignupsPerIP {
			flags = append(flags, FlagIPBurst)
		}
		if suspended {
			flags = append(flags, FlagSuspendedIP)
		}
	}
	// Many unrelated people share a free-mail domain, so only a company's
	// own domain counts against it.
	if !emaillists.FreeMail(domain) {
		var suspended bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM users u JOIN accounts a ON a.id = u.account_id
				WHERE a.suspended_at IS NOT NULL AND lower(split_part(u.email, '@', 2)) = $1)`,
			domain).Scan(&suspended); err != nil {
			return nil, fmt.Errorf("check sign-up domain: %w", err)
		}
		if suspended {
			flags = append(flags, FlagSuspendedDomain)
		}
	}
	return flags, nil
}

// startReview records a new account's flags and IP, and whether it waits.
func (s *Service) startReview(ctx context.Context, tx pgx.Tx, accountID, email, ip string, now time.Time) error {
	flags, err := signupFlags(ctx, tx, email, ip, now)
	if err != nil {
		return err
	}
	wait := false
	switch s.reviewSetting() {
	case ReviewAll:
		wait = true
	case ReviewFlagged:
		wait = len(flags) > 0
	}
	if wait {
		_, err = tx.Exec(ctx, `UPDATE accounts SET review = 'waiting', review_at = $2, review_flags = $3 WHERE id = $1`,
			accountID, now, flags)
	} else {
		_, err = tx.Exec(ctx, `UPDATE accounts SET review_flags = $2 WHERE id = $1`, accountID, flags)
	}
	if err != nil {
		return fmt.Errorf("record review: %w", err)
	}
	if ip != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO signup_ips (account_id, ip, created_at) VALUES ($1, $2, $3)`,
			accountID, ip, now); err != nil {
			return fmt.Errorf("record sign-up IP: %w", err)
		}
	}
	return nil
}

// PruneSignupIPs deletes sign-up IPs older than SignupIPRetention.
func PruneSignupIPs(ctx context.Context, db Beginner, now time.Time) (int64, error) {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM signup_ips WHERE created_at < $1`, now.Add(-SignupIPRetention))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// RunSignupIPPruner deletes old sign-up IPs every interval until ctx ends.
func RunSignupIPPruner(ctx context.Context, db Beginner, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		n, err := PruneSignupIPs(ctx, db, time.Now())
		switch {
		case err != nil && ctx.Err() == nil:
			log.ErrorContext(ctx, "prune sign-up IPs", "err", err)
		case n > 0:
			log.InfoContext(ctx, "sign-up IPs pruned", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
