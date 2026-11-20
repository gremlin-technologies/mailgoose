package sesevents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
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
		{"SES validation notice", []byte("Successfully validated SNS topic for Amazon SES event publishing" + "\n"), Ignored},
		{"SES validation notice in an SNS notification", []byte(`{"Type":"Notification","Message":"` + "Successfully validated SNS topic for Amazon SES event publishing" + `"}`), Ignored},
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

// A campaign copy whose message id was never recorded is matched by its
// campaign and address, so its bounce or complaint still reaches the
// contact (from the MG-33 review).
func TestUnrecordedCopy(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"bounce-permanent", "complaint"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.db.Exec(ctx, `UPDATE campaign_recipients SET message_id = NULL, status = 'failed' WHERE id = $1`, h.rcpt); err != nil {
				t.Fatal(err)
			}
			if got := h.handle(t, h.payload(t, name, "{{ADDRESS}}", "Sam@PatsBakery.com")); got != Stored {
				t.Fatalf("outcome = %s", got)
			}
			if c := h.contactState(t, h.contact); c.status == "subscribed" {
				t.Errorf("Sam still subscribed")
			}
			ev := h.events(t)
			if len(ev) != 1 || ev[0].recipient == nil || *ev[0].recipient != h.rcpt ||
				ev[0].campaign == nil || *ev[0].campaign != h.campaign {
				t.Errorf("events = %+v", ev)
			}
		})
	}
	t.Run("not a recipient of that campaign", func(t *testing.T) {
		h := newHarness(t)
		jo := h.addContact(t, "jo@example.com", "subscribed")
		if _, err := h.db.Exec(ctx, `UPDATE campaign_recipients SET message_id = NULL WHERE id = $1`, h.rcpt); err != nil {
			t.Fatal(err)
		}
		h.handle(t, h.payload(t, "bounce-permanent", "{{ADDRESS}}", "jo@example.com"))
		if c := h.contactState(t, jo); c.status != "subscribed" {
			t.Errorf("Jo = %s, want untouched", c.status)
		}
		if ev := h.events(t); len(ev) != 1 || ev[0].recipient != nil {
			t.Errorf("events = %+v", ev)
		}
	})
	t.Run("another account's campaign", func(t *testing.T) {
		h := newHarness(t)
		other := newHarness(t)
		if _, err := h.db.Exec(ctx, `UPDATE campaign_recipients SET message_id = NULL WHERE id = $1`, h.rcpt); err != nil {
			t.Fatal(err)
		}
		h.handle(t, h.payload(t, "bounce-permanent", "{{CAMPAIGN_ID}}", other.campaign))
		if c := h.contactState(t, h.contact); c.status != "subscribed" {
			t.Errorf("Sam = %s, want untouched", c.status)
		}
	})
}

// Soft bounces leave an unsubscribed contact as it is, so a form can still
// bring it back; a hard bounce marks it bounced.
func TestUnsubscribedBounces(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	if _, err := h.db.Exec(ctx, `UPDATE contacts SET status = 'unsubscribed' WHERE id = $1`, h.contact); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= SoftBounceLimit+1; n++ {
		at := time.Date(2026, 11, 18, 12, 0, n, 0, time.UTC).Format(time.RFC3339Nano)
		h.handle(t, h.payload(t, "bounce-transient", "{{TIMESTAMP}}", at))
	}
	if c := h.contactState(t, h.contact); c.status != "unsubscribed" || c.soft != 0 {
		t.Errorf("after soft bounces: %+v, want unsubscribed", c)
	}
	h.handle(t, h.payload(t, "bounce-permanent"))
	if c := h.contactState(t, h.contact); c.status != "bounced" {
		t.Errorf("after a hard bounce: %+v, want bounced", c)
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

func TestComplaintsPauseAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// 3,000 copies of the campaign went out.
	if _, err := h.db.Exec(ctx, `
		INSERT INTO campaign_recipients (account_id, campaign_id, status, sent_at, message_id)
		SELECT $1, $2, 'sent', now(), 'copy-' || i FROM generate_series(1, 2999) i`, h.acc, h.campaign); err != nil {
		t.Fatal(err)
	}
	var alerts []sending.AutoPause
	h.svc.Alert = func(_ context.Context, p sending.AutoPause) { alerts = append(alerts, p) }
	var throttles []sending.Throttle
	h.svc.Throttled = func(_ context.Context, t sending.Throttle) { throttles = append(throttles, t) }
	complain := func(i int) {
		t.Helper()
		id := fmt.Sprintf("copy-%d", i)
		if got := h.handle(t, h.payload(t, "complaint", "{{MESSAGE_ID}}", id)); got != Stored {
			t.Fatalf("complaint %d: %s", i, got)
		}
	}
	complain(1)
	if len(throttles) != 0 {
		t.Fatalf("throttled on one complaint: %+v", throttles)
	}
	// Two complaints, past the 0.05% early warning: throttled (MG-61), once.
	// A third is 0.1%, the pause limit but not over it.
	for i := 2; i <= 3; i++ {
		complain(i)
	}
	if len(alerts) != 0 {
		t.Fatalf("paused at 0.1%%: %+v", alerts)
	}
	if len(throttles) != 1 || throttles[0].AccountID != h.acc || throttles[0].Rates.Complaints != 2 {
		t.Fatalf("throttles = %+v", throttles)
	}
	complain(4)
	if len(alerts) != 1 || alerts[0].AccountID != h.acc || alerts[0].Rates.Complaints != 4 || alerts[0].Rates.Sent != 3000 {
		t.Fatalf("alerts = %+v", alerts)
	}
	var paused bool
	if err := h.db.QueryRow(ctx, `SELECT sending_paused_at IS NOT NULL FROM accounts WHERE id = $1`, h.acc).Scan(&paused); err != nil || !paused {
		t.Errorf("account paused = %v, %v", paused, err)
	}
	complain(5)
	if len(alerts) != 1 {
		t.Errorf("alerted again for the same campaign: %d", len(alerts))
	}
}

// TestSimulatorEvents checks the events the local fake sender makes up for
// SES's mailbox simulator are read like SES's own, so the end-to-end tests
// see real bounce and complaint handling.
func TestSimulatorEvents(t *testing.T) {
	for _, tc := range []struct{ to, typ, status string }{
		{"bounce@simulator.amazonses.com", TypeBounce, "bounced"},
		{"complaint@simulator.amazonses.com", TypeComplaint, "complained"},
		{"success@simulator.amazonses.com", TypeDelivery, "subscribed"},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			h := newHarness(t)
			got := make(chan []byte, 1)
			sim := &mail.Simulator{Sender: &mail.Fake{}, Delay: time.Millisecond,
				Events: func(_ context.Context, body []byte) { got <- body }}
			id, err := sim.Send(context.Background(), mail.Message{
				From: mail.Address{Email: "hello@patsbakery.com"}, To: mail.Address{Email: tc.to},
				Subject: "Fresh bread", Text: "Hi", Kind: mail.KindCampaign, AccountID: h.acc, CampaignID: h.campaign,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Exec(context.Background(), `UPDATE campaign_recipients SET message_id = $1 WHERE id = $2`, id, h.rcpt); err != nil {
				t.Fatal(err)
			}
			var body []byte
			select {
			case body = <-got:
			case <-time.After(5 * time.Second):
				t.Fatal("no simulated event")
			}
			if out := h.handle(t, body); out != Stored {
				t.Fatalf("outcome = %s", out)
			}
			ev := h.events(t)
			if len(ev) != 1 || ev[0].typ != tc.typ || ev[0].recipient == nil || *ev[0].recipient != h.rcpt {
				t.Errorf("events = %+v", ev)
			}
			if c := h.contactState(t, h.contact); c.status != tc.status {
				t.Errorf("contact %s, want %s", c.status, tc.status)
			}
		})
	}
}

// SES sends a plain-text notice, not an event, when an event destination is
// set up. The first staging deploy's stub refused it until it reached the
// dead-letter queue (Nov 19); it's acknowledged at info level, with no
// database work and no warning.
func TestValidationNotice(t *testing.T) {
	var buf bytes.Buffer
	svc := &Service{Log: slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	for _, body := range []string{
		"Successfully validated SNS topic for Amazon SES event publishing.",
		`{"Type":"Notification","Message":"Successfully validated SNS topic for Amazon SES event publishing."}`,
	} {
		buf.Reset()
		got, err := svc.handle(context.Background(), []byte(body))
		if err != nil || got != Ignored {
			t.Fatalf("handle(%q) = %s, %v; want ignored, acknowledged", body, got, err)
		}
		var line struct{ Level, Msg string }
		if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
			t.Fatalf("log = %q: %v", buf.String(), err)
		}
		if line.Level != "INFO" || !strings.Contains(line.Msg, "validation notice") {
			t.Errorf("log = %s %q, want one INFO line about the validation notice", line.Level, line.Msg)
		}
	}
	// Other text still counts as unreadable.
	buf.Reset()
	if got, _ := svc.handle(context.Background(), []byte("Successfully did something else")); got != Malformed {
		t.Errorf("other text = %s, want malformed", got)
	}
	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("log = %q, want a warning", buf.String())
	}
}
