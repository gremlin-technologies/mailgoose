package sending

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/render"
)

// Launch confirmation (CAM-4).
const (
	// CodeTTL is how long a launch code works.
	CodeTTL = 15 * time.Minute
	// MaxCodeAttempts is how many wrong codes end a code.
	MaxCodeAttempts = 5
)

// Codes of the problems, besides Prepare's, that stop a launch.
const (
	Untested      = "untested"
	CheckFailed   = "check_failed"
	AccountPaused = "account_paused"
	// AccountSuspended is an account suspended (MG-65).
	AccountSuspended = "account_suspended"
	// SubscriptionPastDue is a pause while the paid plan's payment is past
	// due, lifted once it goes through.
	SubscriptionPastDue = "subscription_past_due"
	NoRecipients        = "no_recipients"
	CreditsShort        = "credits_short"
	SendAtPassed        = "send_at_passed"
)

// Summary is what a person confirms before a campaign launches (CAM-3).
type Summary struct {
	CampaignID      string
	Name            string
	Subject         string
	From            mail.Address
	ReplyTo         []mail.Address
	RecipientCount  int
	SendAt          *time.Time
	Credits         int
	Balance         int64
	CampaignVersion int
	EmailVersion    int
	// Warnings are the email check's warnings; they don't stop a launch.
	Warnings []emailcheck.Issue
}

// ConfirmationRequired is Launch's answer without a code: the summary to
// show the person, and the code that launches it once they say yes.
type ConfirmationRequired struct {
	Summary   *Summary
	Code      string
	ExpiresAt time.Time
}

func (e *ConfirmationRequired) Error() string { return "the launch needs confirmation" }

// Reasons a launch code is refused.
const (
	CodeMissing  = "no_code"
	CodeExpired  = "expired"
	CodeWrong    = "wrong"
	CodeUsedUp   = "too_many_attempts"
	CodeOutdated = "changed"
	CodeGrew     = "audience_grew"
)

// AllowedGrowth is how many more recipients than its summary counted a
// launch code still launches to: a tenth of the summary's count, rounded
// up. Past that the person agreed to a smaller send than the one that
// would go out, so the code is refused and a new summary is needed.
// Fewer recipients than summarized always launch. A reversible default
// (docs/decisions.md); the owner may want no allowance, or a larger one.
func AllowedGrowth(summarized int) int { return (summarized + 9) / 10 }

// CodeError is a launch code that can't be used. Each reason means: run
// the launch again without a code for a fresh summary.
type CodeError struct {
	Reason string
	// Summarized and Recipients are set for CodeGrew: the summary's count
	// and the count at confirm.
	Summarized, Recipients int
}

func (e *CodeError) Error() string {
	switch e.Reason {
	case CodeExpired:
		return "the confirmation code has expired"
	case CodeWrong:
		return "the confirmation code is wrong"
	case CodeUsedUp:
		return "the confirmation code had too many wrong tries"
	case CodeOutdated:
		return "the campaign or its email changed after the confirmation code was issued"
	case CodeGrew:
		return fmt.Sprintf("the campaign now reaches %d contacts, more than the %d in the summary", e.Recipients, e.Summarized)
	}
	return "there's no confirmation code for this campaign"
}

// Launched is a launched campaign: what Launch returns the first time, and
// every time after.
type Launched struct {
	Campaign        *campaigns.Campaign
	RecipientCount  int
	CreditsReserved int
}

// launchCopy is what a launched campaign keeps of its email: exactly what
// the send workers (MG-31) send.
type launchCopy struct {
	EmailID       string           `json:"email_id"`
	EmailVersion  int              `json:"email_version"`
	From          mail.Address     `json:"from"`
	ReplyTo       []mail.Address   `json:"reply_to"`
	PostalAddress string           `json:"postal_address"`
	Template      *render.Template `json:"template"`
	// Tracking adds the open pixel and tracked links to each copy (REP-3).
	Tracking bool `json:"tracking"`
}

// creditsRef is the ref a campaign's credits are reserved under.
func creditsRef(campaignID string) string { return "campaign:" + campaignID }

// Launch launches a draft campaign in two steps (CAM-3, CAM-4). Without a
// code it checks everything a launch needs and returns a
// *ConfirmationRequired with the summary and a new code, or a
// *NotReadyError listing what's wrong. With the code, it checks again and,
// in one transaction, copies the compiled email into the campaign, fixes
// its recipients, reserves a credit for each (CAM-5) and queues the send.
// links fetches the email's links for the summary's check; nil skips that.
//
// A campaign that's already launched returns its launch again, whatever
// the code, so retries are safe (CAM-8).
func (s *Service) Launch(ctx context.Context, accountID, campaignID, code string, links emailcheck.LinkChecker) (*Launched, error) {
	c, err := s.Campaigns.Get(ctx, accountID, campaignID)
	if err != nil {
		return nil, err
	}
	if c.LaunchedAt != nil {
		return launched(c), nil
	}
	if !campaigns.Editable(c.Status) {
		return nil, &campaigns.StatusError{Status: c.Status}
	}
	if strings.TrimSpace(code) == "" {
		return nil, s.summarize(ctx, accountID, c, links)
	}
	return s.confirm(ctx, accountID, campaignID, code)
}

func launched(c *campaigns.Campaign) *Launched {
	l := &Launched{Campaign: c}
	if c.RecipientTotal != nil {
		l.RecipientCount = *c.RecipientTotal
	}
	if c.CreditsReserved != nil {
		l.CreditsReserved = *c.CreditsReserved
	}
	return l
}

// summarize checks the campaign and, when it can launch, issues a code.
func (s *Service) summarize(ctx context.Context, accountID string, c *campaigns.Campaign, links emailcheck.LinkChecker) error {
	p, problems, err := s.launchProblems(ctx, accountID, c, links)
	if err != nil {
		return err
	}
	var sum *Summary
	if p != nil {
		n, err := (&contacts.Service{DB: s.DB}).CountRecipients(ctx, accountID, c.Audience())
		if err != nil {
			return err
		}
		var bal int64
		err = s.inTx(ctx, func(tx pgx.Tx) error {
			var err error
			bal, err = credits.Balance(ctx, tx, accountID, s.now())
			return err
		})
		if err != nil {
			return err
		}
		problems = append(problems, audienceProblems(c, n, bal)...)
		sum = &Summary{
			CampaignID: c.ID, Name: c.Name, Subject: p.Email.Document.Settings.Subject,
			From: p.From, ReplyTo: p.ReplyTo, RecipientCount: n, SendAt: c.SendAt,
			Credits: n, Balance: bal, CampaignVersion: c.Version, EmailVersion: p.Email.Version,
			Warnings: p.warnings,
		}
	}
	if len(problems) > 0 {
		return &NotReadyError{Problems: problems}
	}

	code, err := newCode()
	if err != nil {
		return err
	}
	expires := s.now().Add(CodeTTL)
	if _, err := s.DB.Exec(ctx, `
		INSERT INTO campaign_launch_codes (campaign_id, account_id, code_hash, campaign_version, email_version, recipient_count, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (campaign_id) DO UPDATE SET code_hash = $3, campaign_version = $4, email_version = $5,
			recipient_count = $6, expires_at = $7, attempts = 0, created_at = $8`,
		c.ID, accountID, hashCode(c.ID, code), sum.CampaignVersion, sum.EmailVersion, sum.RecipientCount, expires, s.now()); err != nil {
		return err
	}
	return &ConfirmationRequired{Summary: sum, Code: code, ExpiresAt: expires}
}

// checked is a prepared campaign with its email check's warnings.
type checked struct {
	*Prepared
	warnings []emailcheck.Issue
}

// launchProblems runs every check that doesn't depend on the audience:
// Prepare's, the email check, the test since the last edit, the send time
// and whether the account may send. The prepared campaign is nil when
// Prepare found problems.
func (s *Service) launchProblems(ctx context.Context, accountID string, c *campaigns.Campaign, links emailcheck.LinkChecker) (*checked, []Problem, error) {
	var problems []Problem
	var out *checked
	p, err := s.Prepare(ctx, accountID, c.ID)
	var nr *NotReadyError
	switch {
	case errors.As(err, &nr):
		problems = append(problems, nr.Problems...)
	case err != nil:
		return nil, nil, err
	default:
		res := emailcheck.Check(ctx, emailcheck.Input{Doc: p.Email.Document, PostalAddress: p.PostalAddress,
			FromEmail: p.From.Email, FromVerified: true}, links)
		for _, issue := range res.Errors {
			problems = append(problems, Problem{Code: CheckFailed, Message: issue.Message,
				Hint: "Run `mailgoose email check " + p.Email.ID + "` for the details, and fix it."})
		}
		if !c.TestedAsIs(p.Email.Version) {
			msg := "No test has gone out since the campaign or its email last changed."
			if c.LastTest == nil {
				msg = "The campaign hasn't been tested yet."
			}
			problems = append(problems, Problem{Code: Untested, Message: msg,
				Hint: "Send one with `mailgoose campaign test " + c.ID + "` and check it looks right."})
		}
		out = &checked{Prepared: p, warnings: res.Warnings}
	}
	if c.SendAt != nil && !c.SendAt.After(s.now()) {
		problems = append(problems, Problem{Code: SendAtPassed, Message: "The campaign's send time has passed.",
			Hint: "Set a new one with `mailgoose campaign update " + c.ID + " --send-at <time>`, or `--send-at now`."})
	}
	var pausedAt, suspendedAt *time.Time
	var reason, code, suspendedReason *string
	if err := s.DB.QueryRow(ctx, `
		SELECT sending_paused_at, sending_paused_reason, sending_paused_code, suspended_at, suspended_reason
		FROM accounts WHERE id = $1`,
		accountID).Scan(&pausedAt, &reason, &code, &suspendedAt, &suspendedReason); err != nil {
		return nil, nil, err
	}
	switch {
	case suspendedAt != nil:
		problems = append(problems, Problem{Code: AccountSuspended, Message: "This account is suspended. " + trimmed(suspendedReason),
			Hint: "Reply to the email about the suspension to appeal."})
	case pausedAt != nil && trimmed(code) == SubscriptionPastDue:
		problems = append(problems, Problem{Code: SubscriptionPastDue, Message: "Sending is paused. " + trimmed(reason),
			Hint: "Update the card with `mailgoose plan manage`; sending resumes as soon as the payment goes through."})
	case pausedAt != nil:
		msg := "Sending is paused for this account."
		if r := trimmed(reason); r != "" {
			msg += " " + r
		}
		problems = append(problems, Problem{Code: AccountPaused, Message: msg,
			Hint: "Reply to the email about the pause, or write to support, to get it lifted."})
	}
	return out, problems, nil
}

// audienceProblems are the launch's problems with who it goes to and what
// it costs: n recipients against a balance of bal.
func audienceProblems(c *campaigns.Campaign, n int, bal int64) []Problem {
	if n == 0 {
		return []Problem{{Code: NoRecipients, Message: "The campaign reaches no subscribed contacts.",
			Hint: "Pick recipients with `mailgoose campaign update " + c.ID + " --all` or `--tag`, and see who they reach with `mailgoose campaign count`."}}
	}
	if int64(n) > bal {
		return []Problem{{Code: CreditsShort,
			Message: fmt.Sprintf("The campaign needs %d credits and the account has %d: %d short.", n, bal, int64(n)-bal),
			Hint:    "Buy credits with `mailgoose credits buy`, or send to fewer contacts.",
			Details: map[string]any{"needed": n, "balance": bal, "short": int64(n) - bal}}}
	}
	return nil
}

// confirm launches with a code.
func (s *Service) confirm(ctx context.Context, accountID, campaignID, code string) (*Launched, error) {
	var result *Launched
	var codeErr *CodeError
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT 1 FROM campaigns WHERE account_id = $1 AND id = $2 FOR UPDATE`, accountID, campaignID)
		if err := row.Scan(new(int)); errors.Is(err, pgx.ErrNoRows) {
			return campaigns.ErrNotFound
		} else if err != nil {
			return err
		}
		c, err := s.Campaigns.Get(ctx, accountID, campaignID)
		if err != nil {
			return err
		}
		if c.LaunchedAt != nil {
			result = launched(c)
			return nil
		}
		if !campaigns.Editable(c.Status) {
			return &campaigns.StatusError{Status: c.Status}
		}

		var hash []byte
		var campaignVersion, emailVersion, summarized, attempts int
		var expires time.Time
		err = tx.QueryRow(ctx, `
			SELECT code_hash, campaign_version, email_version, recipient_count, expires_at, attempts
			FROM campaign_launch_codes WHERE campaign_id = $1 FOR UPDATE`, campaignID).
			Scan(&hash, &campaignVersion, &emailVersion, &summarized, &expires, &attempts)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return &CodeError{Reason: CodeMissing}
		case err != nil:
			return err
		case !expires.After(s.now()):
			return &CodeError{Reason: CodeExpired}
		case attempts >= MaxCodeAttempts:
			return &CodeError{Reason: CodeUsedUp}
		case subtle.ConstantTimeCompare(hash, hashCode(campaignID, code)) != 1:
			// The wrong try counts, so it's kept: commit, then refuse.
			if _, err := tx.Exec(ctx, `UPDATE campaign_launch_codes SET attempts = attempts + 1 WHERE campaign_id = $1`, campaignID); err != nil {
				return err
			}
			codeErr = &CodeError{Reason: CodeWrong}
			return nil
		case campaignVersion != c.Version:
			return &CodeError{Reason: CodeOutdated}
		}

		p, problems, err := s.launchProblems(ctx, accountID, c, nil)
		if err != nil {
			return err
		}
		if p != nil && p.Email.Version != emailVersion {
			return &CodeError{Reason: CodeOutdated}
		}
		if len(problems) > 0 {
			return &NotReadyError{Problems: problems}
		}

		now := s.now()
		n, err := contacts.SnapshotRecipients(ctx, tx, accountID, campaignID, c.Audience())
		if err != nil {
			return err
		}
		// The snapshot is rolled back with the rest of the transaction.
		if n > summarized+AllowedGrowth(summarized) {
			return &CodeError{Reason: CodeGrew, Summarized: summarized, Recipients: n}
		}
		bal, err := credits.Balance(ctx, tx, accountID, now)
		if err != nil {
			return err
		}
		if problems := audienceProblems(c, n, bal); len(problems) > 0 {
			return &NotReadyError{Problems: problems}
		}
		if _, err := credits.Debit(ctx, tx, accountID, credits.TypeReserve, int64(n), creditsRef(campaignID), now); err != nil {
			return err
		}
		if _, err := s.sliceLaunch(ctx, tx, accountID, campaignID, n, now); err != nil {
			return err
		}

		cp, err := json.Marshal(launchCopy{EmailID: p.Email.ID, EmailVersion: p.Email.Version, From: p.From,
			ReplyTo: p.ReplyTo, PostalAddress: p.PostalAddress, Template: p.Template, Tracking: c.Tracking})
		if err != nil {
			return err
		}
		status, runAt := campaigns.StatusSending, now
		if c.SendAt != nil {
			status, runAt = campaigns.StatusScheduled, *c.SendAt
		}
		if !campaigns.CanMove(c.Status, status) {
			return &campaigns.StatusError{Status: c.Status, To: status}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE campaigns SET status = $3, status_changed_at = $4, updated_at = $4,
				launched_at = $4, launch = $5, recipient_total = $6, credits_reserved = $6
			WHERE account_id = $1 AND id = $2`, accountID, campaignID, status, now, cp, n); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO send_queue (account_id, campaign_id, run_at, created_at) VALUES ($1, $2, $3, $4)`,
			accountID, campaignID, runAt, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM campaign_launch_codes WHERE campaign_id = $1`, campaignID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if codeErr != nil {
		return nil, codeErr
	}
	if result != nil {
		return result, nil
	}
	c, err := s.Campaigns.Get(ctx, accountID, campaignID)
	if err != nil {
		return nil, err
	}
	return launched(c), nil
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

// codeAlphabet leaves out letters and digits people mix up (0 O 1 I L U).
const codeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// newCode is a launch code: 8 characters in two groups, like 7KQ2-M9XD.
// Bytes past the last whole multiple of the alphabet are thrown away, so
// every character is equally likely.
func newCode() (string, error) {
	const limit = 256 / len(codeAlphabet) * len(codeAlphabet)
	out := make([]byte, 0, 9)
	b := make([]byte, 16)
	for len(out) < 9 {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		for _, v := range b {
			if len(out) == 9 {
				break
			}
			if int(v) >= limit {
				continue
			}
			if len(out) == 4 {
				out = append(out, '-')
			}
			out = append(out, codeAlphabet[int(v)%len(codeAlphabet)])
		}
	}
	return string(out), nil
}

// NormalizeCode reads a code as typed: any case, with or without the dash
// or spaces.
func NormalizeCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if r != '-' && r != ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hashCode(campaignID, code string) []byte {
	sum := sha256.Sum256([]byte("mailgoose-launch\x00" + campaignID + "\x00" + NormalizeCode(code)))
	return sum[:]
}
