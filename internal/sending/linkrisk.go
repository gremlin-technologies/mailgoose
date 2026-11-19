package sending

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
)

// UnsafeLink is a launch or test problem: a link on Google Web Risk's
// phishing, malware or unwanted software lists (MG-64). Its details carry
// the link as "url".
const UnsafeLink = emailcheck.UnsafeLink

// unsafeLinkHint is what to do about an unsafe link.
const unsafeLinkHint = "Remove the link, or replace it with one to a site that isn't on those lists, then check the email again."

// unsafeProblem is the problem for an UnsafeLink check error.
func unsafeProblem(issue emailcheck.Issue) Problem {
	return Problem{Code: UnsafeLink, Message: issue.Message, Hint: unsafeLinkHint,
		Details: map[string]any{"url": issue.URL}}
}

// unsafeLinks are the links of the UnsafeLink problems among problems.
func unsafeLinks(problems []Problem) []string {
	var out []string
	for _, p := range problems {
		if p.Code != UnsafeLink {
			continue
		}
		if u, ok := p.Details["url"].(string); ok {
			out = append(out, u)
		}
	}
	return out
}

// pauseForUnsafeLinks pauses an account whose launch found links on the
// lists (MG-64), the way bounces and complaints pause it (CMP-6): the
// pause and its reason, an automatic entry in the admin log, and an alert
// to the ops address. An account already paused keeps its pause and
// reason, and nothing more is logged or sent.
func (s *Service) pauseForUnsafeLinks(ctx context.Context, accountID, campaignID string, links []string) {
	log := s.log().With("account_id", accountID, "campaign_id", campaignID)
	var accountName, campaignName, reason string
	paused := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT name FROM campaigns WHERE account_id = $1 AND id = $2`, accountID, campaignID).
			Scan(&campaignName); err != nil {
			return err
		}
		reason = unsafeLinkReason(campaignName, links)
		now := s.now()
		err := tx.QueryRow(ctx, `
			UPDATE accounts SET sending_paused_at = $2, sending_paused_reason = $3, sending_paused_code = NULL, updated_at = $2
			WHERE id = $1 AND sending_paused_at IS NULL RETURNING name`, accountID, now, reason).Scan(&accountName)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		paused = true
		_, err = tx.Exec(ctx, `
			INSERT INTO admin_actions (actor, action, account_id, target_id, reason, details, created_at)
			VALUES ('automatic', 'auto_pause', $1, $2, $3, $4, $5)`,
			accountID, campaignID, reason, map[string]any{"unsafe_links": links}, now)
		return err
	})
	if err != nil {
		log.ErrorContext(ctx, "pause an account for an unsafe link", "err", err)
		return
	}
	if !paused {
		log.WarnContext(ctx, "unsafe link at launch of a paused account", "links", links)
		return
	}
	log.ErrorContext(ctx, "account paused automatically for an unsafe link", "links", links)
	if s.Alert == nil {
		return
	}
	s.Alert(ctx, accountName+" paused automatically for an unsafe link", fmt.Sprintf(`Sending is paused for %s (account %s).

Launching campaign %q (%s) found links on Google Web Risk's phishing, malware or unwanted software lists:
  %s

%s

Look into it with:
  mailgoose admin account %s
Lift the pause with:
  mailgoose admin unpause %s --reason "..."
`, accountName, accountID, campaignName, campaignID, strings.Join(links, "\n  "), reason, accountID, accountID))
}

// unsafeLinkReason is what the account's owner is told.
func unsafeLinkReason(campaign string, links []string) string {
	what := "a link"
	if len(links) > 1 {
		what = "links"
	}
	return fmt.Sprintf("It was paused automatically: %q has %s on Google's lists of phishing or malware sites (%s).",
		campaign, what, strings.Join(clipAll(links), ", "))
}

func clipAll(links []string) []string {
	out := make([]string, len(links))
	for i, l := range links {
		if r := []rune(l); len(r) > 100 {
			l = string(r[:100]) + "…"
		}
		out[i] = l
	}
	return out
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// AlertFunc tells mailgoose's operators about something: OpsAlert.Notify.
type AlertFunc func(ctx context.Context, subject, text string)
