// Package sesevents handles Amazon SES events (MG-33: CMP-4, CMP-5):
// deliveries, bounces, complaints, rejects and delivery delays, which the
// configuration set publishes to SNS and SNS delivers raw to the
// ses-events queue.
//
// Each event is stored once against its send record, and changes the
// contact it's about: a hard bounce or a complaint suppresses the address at
// once, three soft bounces in a row suppress it, and a delivery clears the
// soft bounce count. Open and click events are dropped: opens and clicks
// come only from mailgoose's own tracking (MG-37). An event that can't be
// read is logged and dropped, so it isn't retried forever.
package sesevents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// SoftBounceLimit is how many soft bounces in a row suppress an address.
const SoftBounceLimit = 3

// Event types as stored.
const (
	TypeDelivery      = "delivery"
	TypeBounce        = "bounce"
	TypeComplaint     = "complaint"
	TypeReject        = "reject"
	TypeDeliveryDelay = "delivery_delay"
)

// Outcome is what Handle did with an event, for logs and tests.
type Outcome string

// Outcomes.
const (
	Stored    Outcome = "stored"
	Duplicate Outcome = "duplicate"
	Ignored   Outcome = "ignored"   // an event type mailgoose doesn't use
	Malformed Outcome = "malformed" // unreadable, or missing what it needs
)

// Service stores events and applies them.
type Service struct {
	DB  *pgxpool.Pool
	Log *slog.Logger
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

// event is the part of an SES event mailgoose reads. The configuration
// set's events say eventType; SES's older identity notifications say
// notificationType, with the same fields.
type event struct {
	EventType        string `json:"eventType"`
	NotificationType string `json:"notificationType"`
	Mail             struct {
		Timestamp string              `json:"timestamp"`
		MessageID string              `json:"messageId"`
		Tags      map[string][]string `json:"tags"`
	} `json:"mail"`
	Bounce *struct {
		BounceType        string      `json:"bounceType"`
		BounceSubType     string      `json:"bounceSubType"`
		BouncedRecipients []recipient `json:"bouncedRecipients"`
		Timestamp         string      `json:"timestamp"`
	} `json:"bounce"`
	Complaint *struct {
		ComplainedRecipients  []recipient `json:"complainedRecipients"`
		ComplaintFeedbackType string      `json:"complaintFeedbackType"`
		Timestamp             string      `json:"timestamp"`
	} `json:"complaint"`
	Delivery *struct {
		Timestamp  string   `json:"timestamp"`
		Recipients []string `json:"recipients"`
	} `json:"delivery"`
	DeliveryDelay *struct {
		DelayType          string      `json:"delayType"`
		DelayedRecipients  []recipient `json:"delayedRecipients"`
		Timestamp          string      `json:"timestamp"`
		ExpirationTime     string      `json:"expirationTime"`
		ReportingMTA       string      `json:"reportingMTA"`
		RemoteMtaIP        string      `json:"remoteMtaIp"`
		DiagnosticCodeText string      `json:"diagnosticCode"`
	} `json:"deliveryDelay"`
	Reject *struct {
		Reason string `json:"reason"`
	} `json:"reject"`
}

type recipient struct {
	EmailAddress string `json:"emailAddress"`
}

// snsEnvelope is an SNS notification, for a subscription without raw
// message delivery.
type snsEnvelope struct {
	Type    string `json:"Type"`
	Message string `json:"Message"`
}

func (e *event) kind() string {
	if e.EventType != "" {
		return e.EventType
	}
	return e.NotificationType
}

func (e *event) tag(name string) string {
	if v := e.Mail.Tags[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// parsed is an event ready to store.
type parsed struct {
	typ, subtype string
	at           time.Time
	// addresses the event is about (bounced or complained recipients).
	addresses []string
	hard      bool // a permanent bounce
}

// Handle reads one SQS message body and acts on it. It returns an error
// only when the database failed, so the message is tried again; anything
// else is logged and acknowledged.
func (s *Service) Handle(ctx context.Context, body []byte) error {
	_, err := s.handle(ctx, body)
	return err
}

func (s *Service) handle(ctx context.Context, body []byte) (Outcome, error) {
	var env snsEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Type != "" {
		if env.Type != "Notification" {
			s.log().WarnContext(ctx, "SNS message that isn't a notification dropped", "sns_type", env.Type)
			return Ignored, nil
		}
		body = []byte(env.Message)
	}
	var e event
	if err := json.Unmarshal(body, &e); err != nil {
		s.log().WarnContext(ctx, "unreadable SES event dropped", "err", err)
		return Malformed, nil
	}
	p, outcome := s.parse(&e)
	if outcome != "" {
		if outcome == Malformed {
			s.log().WarnContext(ctx, "SES event dropped", "event_type", e.kind(), "message_id", e.Mail.MessageID)
		}
		return outcome, nil
	}
	sum := sha256.Sum256(body)
	return s.store(ctx, &e, p, hex.EncodeToString(sum[:]))
}

// parse picks out what's stored, or says why the event isn't.
func (s *Service) parse(e *event) (*parsed, Outcome) {
	p := &parsed{}
	var at string
	switch e.kind() {
	case "Delivery":
		if e.Delivery == nil {
			return nil, Malformed
		}
		p.typ, at = TypeDelivery, e.Delivery.Timestamp
	case "Bounce":
		if e.Bounce == nil {
			return nil, Malformed
		}
		kind := strings.ToLower(e.Bounce.BounceType)
		switch kind {
		case "permanent":
			p.hard = true
		case "transient", "undetermined":
		default:
			return nil, Malformed
		}
		p.typ, at = TypeBounce, e.Bounce.Timestamp
		p.subtype = kind + "/" + e.Bounce.BounceSubType
		for _, r := range e.Bounce.BouncedRecipients {
			p.addresses = append(p.addresses, r.EmailAddress)
		}
	case "Complaint":
		if e.Complaint == nil {
			return nil, Malformed
		}
		p.typ, at, p.subtype = TypeComplaint, e.Complaint.Timestamp, e.Complaint.ComplaintFeedbackType
		for _, r := range e.Complaint.ComplainedRecipients {
			p.addresses = append(p.addresses, r.EmailAddress)
		}
	case "Reject":
		p.typ = TypeReject
		if e.Reject != nil {
			p.subtype = e.Reject.Reason
		}
	case "DeliveryDelay":
		if e.DeliveryDelay == nil {
			return nil, Malformed
		}
		p.typ, at, p.subtype = TypeDeliveryDelay, e.DeliveryDelay.Timestamp, e.DeliveryDelay.DelayType
	case "Open", "Click", "Send", "Subscription", "Rendering Failure", "AmazonSnsSubscriptionSucceeded":
		// Opens and clicks come from MG-37's own tracking; the rest
		// change nothing here.
		return nil, Ignored
	default:
		return nil, Malformed
	}
	if e.Mail.MessageID == "" {
		return nil, Malformed
	}
	p.at = s.now()
	for _, v := range []string{at, e.Mail.Timestamp} {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			p.at = t.UTC()
			break
		}
	}
	if len(p.subtype) > 200 {
		p.subtype = p.subtype[:200]
	}
	return p, ""
}

// store saves the event and applies it, once.
func (s *Service) store(ctx context.Context, e *event, p *parsed, key string) (Outcome, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The send record, for campaign mail.
	var recipientID *int64
	var accountID, campaignID, contactID *string
	err = tx.QueryRow(ctx, `
		SELECT id, account_id, campaign_id, contact_id FROM campaign_recipients WHERE message_id = $1 LIMIT 1`,
		e.Mail.MessageID).Scan(&recipientID, &accountID, &campaignID, &contactID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	kind := e.tag(mail.TagKind)
	if kind == "" {
		kind = "unknown"
	}
	if accountID == nil {
		// Other mail names its account in a tag; one that names no
		// account here is stored without.
		if a := e.tag(mail.TagAccount); uuidPattern.MatchString(a) {
			err := tx.QueryRow(ctx, `SELECT id FROM accounts WHERE id = $1`, a).Scan(&accountID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
		}
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO send_events (event_key, account_id, campaign_id, recipient_id, message_id, kind, type, subtype, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9)
		ON CONFLICT (event_key) DO NOTHING RETURNING id`,
		key, accountID, campaignID, recipientID, e.Mail.MessageID, kind, p.typ, p.subtype, p.at).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Duplicate, tx.Commit(ctx)
	}
	if err != nil {
		return "", err
	}

	contactIDs, err := s.contactsFor(ctx, tx, kind, accountID, contactID, p.addresses)
	if err != nil {
		return "", err
	}
	if err := s.apply(ctx, tx, p, contactIDs); err != nil {
		return "", err
	}
	return Stored, tx.Commit(ctx)
}

// contactsFor is who the event is about: the send record's contact, or for
// a double opt-in confirmation, the account's contact with the bounced or
// complained address. Test sends and mailgoose's own mail change no
// contact.
func (s *Service) contactsFor(ctx context.Context, tx pgx.Tx, kind string, accountID, contactID *string, addresses []string) ([]string, error) {
	if contactID != nil {
		return []string{*contactID}, nil
	}
	if kind != string(mail.KindConfirm) || accountID == nil || len(addresses) == 0 {
		return nil, nil
	}
	lower := make([]string, len(addresses))
	for i, a := range addresses {
		lower[i] = strings.ToLower(strings.TrimSpace(a))
	}
	rows, err := tx.Query(ctx, `SELECT id FROM contacts WHERE account_id = $1 AND email = ANY($2)`, *accountID, lower)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// apply changes the contacts: a hard bounce or a complaint suppresses at
// once, the third soft bounce in a row suppresses, a delivery clears the
// count. A complaint outranks a bounce: a complained contact stays so.
func (s *Service) apply(ctx context.Context, tx pgx.Tx, p *parsed, contactIDs []string) error {
	if len(contactIDs) == 0 {
		return nil
	}
	var sql string
	switch {
	case p.typ == TypeComplaint:
		sql = `UPDATE contacts SET status = 'complained', status_changed_at = $2, updated_at = $2, soft_bounces = 0
			WHERE id = ANY($1) AND status <> 'complained'`
	case p.typ == TypeBounce && p.hard:
		sql = `UPDATE contacts SET status = 'bounced', status_changed_at = $2, updated_at = $2, soft_bounces = 0
			WHERE id = ANY($1) AND status NOT IN ('bounced', 'complained')`
	case p.typ == TypeBounce:
		sql = `UPDATE contacts SET soft_bounces = soft_bounces + 1,
				status = CASE WHEN soft_bounces + 1 >= $3 THEN 'bounced' ELSE status END,
				status_changed_at = CASE WHEN soft_bounces + 1 >= $3 THEN $2 ELSE status_changed_at END,
				updated_at = $2
			WHERE id = ANY($1) AND status NOT IN ('bounced', 'complained')`
		_, err := tx.Exec(ctx, sql, contactIDs, s.now(), SoftBounceLimit)
		return err
	case p.typ == TypeDelivery:
		sql = `UPDATE contacts SET soft_bounces = 0, updated_at = $2 WHERE id = ANY($1) AND soft_bounces > 0`
	default:
		return nil
	}
	_, err := tx.Exec(ctx, sql, contactIDs, s.now())
	return err
}

// SQSHandler handles messages from the ses-events queue.
func (s *Service) SQSHandler() func(ctx context.Context, msg types.Message) error {
	return func(ctx context.Context, msg types.Message) error {
		return s.Handle(ctx, []byte(aws.ToString(msg.Body)))
	}
}
