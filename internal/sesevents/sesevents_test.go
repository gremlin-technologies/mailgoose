package sesevents

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gremlin-technologies/mailgoose/internal/dbtest"
)

// The payloads in testdata follow the event records SES publishes for a
// configuration set (the examples in the SES developer guide), with the
// tags mailgoose puts on each message.

type harness struct {
	db       *pgxpool.Pool
	svc      *Service
	logs     *bytes.Buffer
	acc      string
	campaign string
	contact  string
	rcpt     int64
	now      time.Time
}

const (
	messageID = "0102019a-campaign-sam-0000"
	sam       = "sam@patsbakery.com"
)

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	db := dbtest.New(t)
	logs := &bytes.Buffer{}
	h := &harness{db: db, logs: logs, now: time.Date(2026, 11, 18, 12, 30, 0, 0, time.UTC)}
	h.svc = &Service{DB: db, Log: slog.New(slog.NewTextHandler(logs, nil)), Now: func() time.Time { return h.now }}
	if err := db.QueryRow(ctx, `
		INSERT INTO accounts (name, from_name, from_email, postal_address)
		VALUES ('Pat''s Bakery', 'Pat''s Bakery', 'hello@patsbakery.com', '12 Main St') RETURNING id`).Scan(&h.acc); err != nil {
		t.Fatal(err)
	}
	h.contact = h.addContact(t, sam, "subscribed")
	if err := db.QueryRow(ctx, `INSERT INTO campaigns (account_id, name, all_contacts, status) VALUES ($1, 'Bread week', true, 'sending') RETURNING id`,
		h.acc).Scan(&h.campaign); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, contact_id, status, sent_at, message_id)
		VALUES ($1, $2, $3, 'sent', now(), $4) RETURNING id`, h.acc, h.campaign, h.contact, messageID).Scan(&h.rcpt); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) addContact(t *testing.T, email, status string) string {
	t.Helper()
	var id string
	if err := h.db.QueryRow(context.Background(), `
		INSERT INTO contacts (account_id, email, status, source, status_changed_at) VALUES ($1, $2, $3, 'api', '2026-11-01')
		RETURNING id`, h.acc, email, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// payload is a testdata event for Sam's campaign copy, with fields replaced.
func (h *harness) payload(t *testing.T, name string, replace ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	r := strings.NewReplacer(append(replace,
		"{{MESSAGE_ID}}", messageID, "{{ADDRESS}}", sam, "{{KIND}}", "campaign",
		"{{ACCOUNT_ID}}", h.acc, "{{CAMPAIGN_ID}}", h.campaign,
		"{{TIMESTAMP}}", "2026-11-18T12:00:03.000Z")...)
	return []byte(r.Replace(string(b)))
}

func (h *harness) handle(t *testing.T, body []byte) Outcome {
	t.Helper()
	got, err := h.svc.handle(context.Background(), body)
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	return got
}

type contactState struct {
	status    string
	soft      int
	changedAt time.Time
}

func (h *harness) contactState(t *testing.T, id string) contactState {
	t.Helper()
	var c contactState
	if err := h.db.QueryRow(context.Background(), `SELECT status, soft_bounces, status_changed_at FROM contacts WHERE id = $1`, id).
		Scan(&c.status, &c.soft, &c.changedAt); err != nil {
		t.Fatal(err)
	}
	return c
}

type storedEvent struct {
	typ, kind, subtype string
	account, campaign  *string
	recipient          *int64
	occurredAt         time.Time
}

func (h *harness) events(t *testing.T) []storedEvent {
	t.Helper()
	rows, err := h.db.Query(context.Background(), `
		SELECT type, kind, coalesce(subtype, ''), account_id::text, campaign_id::text, recipient_id, occurred_at
		FROM send_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedEvent
	for rows.Next() {
		var e storedEvent
		if err := rows.Scan(&e.typ, &e.kind, &e.subtype, &e.account, &e.campaign, &e.recipient, &e.occurredAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestHardBounceSuppresses(t *testing.T) {
	h := newHarness(t)
	if got := h.handle(t, h.payload(t, "bounce-permanent")); got != Stored {
		t.Fatalf("outcome = %s", got)
	}
	ev := h.events(t)
	if len(ev) != 1 {
		t.Fatalf("events = %+v", ev)
	}
	e := ev[0]
	if e.typ != TypeBounce || e.subtype != "permanent/General" || e.kind != "campaign" ||
		e.account == nil || *e.account != h.acc || e.campaign == nil || *e.campaign != h.campaign ||
		e.recipient == nil || *e.recipient != h.rcpt {
		t.Errorf("event = %+v", e)
	}
	if want := time.Date(2026, 11, 18, 12, 0, 2, 0, time.UTC); !e.occurredAt.Equal(want) {
		t.Errorf("occurred_at = %s, want the bounce's own time %s", e.occurredAt, want)
	}
	c := h.contactState(t, h.contact)
	if c.status != "bounced" || !c.changedAt.Equal(h.now) {
		t.Errorf("contact = %+v, want bounced at %s", c, h.now)
	}
}

func TestComplaint(t *testing.T) {
	h := newHarness(t)
	if got := h.handle(t, h.payload(t, "complaint")); got != Stored {
		t.Fatalf("outcome = %s", got)
	}
	if c := h.contactState(t, h.contact); c.status != "complained" {
		t.Errorf("status = %s, want complained", c.status)
	}
	if ev := h.events(t); len(ev) != 1 || ev[0].typ != TypeComplaint || ev[0].subtype != "abuse" {
		t.Errorf("events = %+v", ev)
	}
	// A later bounce doesn't turn a complaint into a bounce.
	h.now = h.now.Add(time.Hour)
	h.handle(t, h.payload(t, "bounce-permanent"))
	if c := h.contactState(t, h.contact); c.status != "complained" || c.changedAt.Equal(h.now) {
		t.Errorf("contact = %+v, want still complained", c)
	}
}

func TestSoftBounces(t *testing.T) {
	h := newHarness(t)
	soft := func(n int) {
		t.Helper()
		at := time.Date(2026, 11, 18, 12, 0, n, 0, time.UTC).Format(time.RFC3339Nano)
		if got := h.handle(t, h.payload(t, "bounce-transient", "{{TIMESTAMP}}", at)); got != Stored {
			t.Fatalf("soft bounce %d: outcome = %s", n, got)
		}
	}
	soft(1)
	soft(2)
	if c := h.contactState(t, h.contact); c.status != "subscribed" || c.soft != 2 {
		t.Fatalf("after two soft bounces: %+v", c)
	}
	// A delivery in between starts the count again.
	h.handle(t, h.payload(t, "delivery"))
	if c := h.contactState(t, h.contact); c.soft != 0 {
		t.Fatalf("after a delivery: %+v", c)
	}
	soft(3)
	soft(4)
	if c := h.contactState(t, h.contact); c.status != "subscribed" || c.soft != 2 {
		t.Fatalf("two more: %+v", c)
	}
	soft(5)
	c := h.contactState(t, h.contact)
	if c.status != "bounced" || c.soft != SoftBounceLimit || !c.changedAt.Equal(h.now) {
		t.Errorf("third in a row: %+v, want bounced", c)
	}
	ev := h.events(t)
	if len(ev) != 6 || ev[0].subtype != "transient/MailboxFull" || ev[2].typ != TypeDelivery {
		t.Errorf("events = %+v", ev)
	}
}

func TestRedeliveredEventStoredOnce(t *testing.T) {
	h := newHarness(t)
	at := "2026-11-18T12:00:09.000Z"
	body := h.payload(t, "bounce-transient", "{{TIMESTAMP}}", at)
	if got := h.handle(t, body); got != Stored {
		t.Fatalf("first: %s", got)
	}
	if got := h.handle(t, body); got != Duplicate {
		t.Fatalf("again: %s, want duplicate", got)
	}
	if ev := h.events(t); len(ev) != 1 {
		t.Errorf("events = %d, want 1", len(ev))
	}
	if c := h.contactState(t, h.contact); c.soft != 1 {
		t.Errorf("soft bounces = %d, want 1: a redelivery counts once", c.soft)
	}
}

func TestOtherStoredTypes(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"delivery", "reject", "delivery-delay"} {
		if got := h.handle(t, h.payload(t, name)); got != Stored {
			t.Errorf("%s: outcome = %s", name, got)
		}
	}
	ev := h.events(t)
	if len(ev) != 3 || ev[0].typ != TypeDelivery || ev[1].typ != TypeReject || ev[1].subtype != "Bad content" ||
		ev[2].typ != TypeDeliveryDelay || ev[2].subtype != "TransientCommunicationFailure" {
		t.Errorf("events = %+v", ev)
	}
	// The reject has no time of its own: it's the mail's.
	if want := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC); !ev[1].occurredAt.Equal(want) {
		t.Errorf("reject occurred_at = %s, want %s", ev[1].occurredAt, want)
	}
	if c := h.contactState(t, h.contact); c.status != "subscribed" {
		t.Errorf("status = %s; deliveries, rejects and delays change nothing", c.status)
	}
}

func TestDroppedEvents(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name string
		body []byte
		want Outcome
	}{
		{"open", h.payload(t, "open"), Ignored},
		{"click", h.payload(t, "click"), Ignored},
		{"rendering failure", h.payload(t, "rendering-failure"), Ignored},
		{"unknown type", h.payload(t, "unknown-type"), Malformed},
		{"no message id", h.payload(t, "no-message-id"), Malformed},
		{"not JSON", []byte("not json at all"), Malformed},
		{"bounce without its details", []byte(`{"eventType":"Bounce","mail":{"messageId":"x"}}`), Malformed},
		{"bounce of an unknown type", []byte(`{"eventType":"Bounce","bounce":{"bounceType":"Odd"},"mail":{"messageId":"x"}}`), Malformed},
		{"SNS subscription confirmation", []byte(`{"Type":"SubscriptionConfirmation","Message":"confirm"}`), Ignored},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := h.svc.handle(context.Background(), c.body)
			if err != nil || got != c.want {
				t.Errorf("handle = %s, %v; want %s, acknowledged", got, err, c.want)
			}
			// The worker's handler acknowledges them all.
			if err := h.svc.Handle(context.Background(), c.body); err != nil {
				t.Errorf("Handle = %v, want nil", err)
			}
		})
	}
	if ev := h.events(t); len(ev) != 0 {
		t.Errorf("events = %+v, want none stored", ev)
	}
	if c := h.contactState(t, h.contact); c.status != "subscribed" {
		t.Errorf("status = %s", c.status)
	}
	if n := strings.Count(h.logs.String(), "level=WARN"); n < 5 {
		t.Errorf("%d warnings logged, want one for each malformed event:\n%s", n, h.logs)
	}
}

func TestSNSEnvelope(t *testing.T) {
	h := newHarness(t)
	inner := h.payload(t, "complaint")
	env, err := json.Marshal(map[string]string{"Type": "Notification", "MessageId": "sns-1", "Message": string(inner)})
	if err != nil {
		t.Fatal(err)
	}
	if got := h.handle(t, env); got != Stored {
		t.Fatalf("outcome = %s", got)
	}
	if c := h.contactState(t, h.contact); c.status != "complained" {
		t.Errorf("status = %s", c.status)
	}
}

func TestConfirmationBounce(t *testing.T) {
	h := newHarness(t)
	jo := h.addContact(t, "jo@example.com", "unconfirmed")
	body := h.payload(t, "bounce-permanent",
		"{{MESSAGE_ID}}", "0102019a-confirm-jo", "{{ADDRESS}}", "Jo@Example.com", "{{KIND}}", "confirm", "{{CAMPAIGN_ID}}", "")
	if got := h.handle(t, body); got != Stored {
		t.Fatalf("outcome = %s", got)
	}
	if c := h.contactState(t, jo); c.status != "bounced" {
		t.Errorf("Jo = %s, want bounced", c.status)
	}
	if c := h.contactState(t, h.contact); c.status != "subscribed" {
		t.Errorf("Sam = %s, want untouched", c.status)
	}
	ev := h.events(t)
	if len(ev) != 1 || ev[0].kind != "confirm" || ev[0].account == nil || *ev[0].account != h.acc || ev[0].recipient != nil {
		t.Errorf("events = %+v", ev)
	}
}

func TestTestAndSystemMail(t *testing.T) {
	h := newHarness(t)
	// A test send that bounces changes no contact, even one with the address.
	body := h.payload(t, "bounce-permanent", "{{MESSAGE_ID}}", "0102019a-test", "{{KIND}}", "test", "{{CAMPAIGN_ID}}", "")
	if got := h.handle(t, body); got != Stored {
		t.Fatalf("test send: %s", got)
	}
	// mailgoose's own mail names no account.
	body = h.payload(t, "complaint", "{{MESSAGE_ID}}", "0102019a-system", "{{KIND}}", "system", "{{ACCOUNT_ID}}", "", "{{CAMPAIGN_ID}}", "")
	if got := h.handle(t, body); got != Stored {
		t.Fatalf("system mail: %s", got)
	}
	if c := h.contactState(t, h.contact); c.status != "subscribed" {
		t.Errorf("Sam = %s, want untouched", c.status)
	}
	ev := h.events(t)
	if len(ev) != 2 || ev[0].kind != "test" || ev[0].account == nil || ev[1].kind != "system" || ev[1].account != nil {
		t.Errorf("events = %+v", ev)
	}
}

func TestUnknownAccountTag(t *testing.T) {
	h := newHarness(t)
	body := h.payload(t, "delivery", "{{MESSAGE_ID}}", "0102019a-other", "{{KIND}}", "test",
		"{{ACCOUNT_ID}}", "00000000-0000-0000-0000-000000000000")
	if got := h.handle(t, body); got != Stored {
		t.Fatalf("outcome = %s", got)
	}
	if ev := h.events(t); len(ev) != 1 || ev[0].account != nil {
		t.Errorf("events = %+v, want stored without an account", ev)
	}
}

func TestDatabaseErrorIsRetried(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.svc.Handle(ctx, h.payload(t, "bounce-permanent")); err == nil {
		t.Fatal("Handle = nil, want an error so SQS delivers it again")
	}
	if c := h.contactState(t, h.contact); c.status != "subscribed" {
		t.Errorf("status = %s", c.status)
	}
}

func TestSQSHandler(t *testing.T) {
	h := newHarness(t)
	msg := types.Message{MessageId: aws.String("sqs-1"), Body: aws.String(string(h.payload(t, "bounce-permanent")))}
	if err := h.svc.SQSHandler()(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if c := h.contactState(t, h.contact); c.status != "bounced" {
		t.Errorf("status = %s", c.status)
	}
}
