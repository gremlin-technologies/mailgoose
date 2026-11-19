package mail

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SimulatorDomain is the domain of Amazon SES's mailbox simulator. Mail to
// success@, bounce@, complaint@, ooto@ and suppressionlist@ there gets the
// matching SES event, and SES never delivers it to anyone.
const SimulatorDomain = "simulator.amazonses.com"

// Simulator plays SES's mailbox simulator for the fake sender. It sends
// through Sender as usual; after a send to a simulator address, it builds
// the event SES would publish to the ses-events queue and hands it to
// Events after Delay, so local runs and end-to-end tests see deliveries,
// bounces and complaints come back as they do in staging. Mail to any other
// address gets no event.
type Simulator struct {
	Sender
	// Events gets each event's JSON, as an SQS message body from SES.
	Events func(ctx context.Context, body []byte)
	// Delay is how long after the send the event arrives, so the sender's
	// caller has stored the message ID first. Zero means one second.
	Delay time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Send implements Sender.
func (s *Simulator) Send(ctx context.Context, m Message) (string, error) {
	id, err := s.Sender.Send(ctx, m)
	if err != nil || s.Events == nil {
		return id, err
	}
	body := s.event(m, id)
	if body == nil {
		return id, nil
	}
	delay := s.Delay
	if delay <= 0 {
		delay = time.Second
	}
	ctx = context.WithoutCancel(ctx)
	time.AfterFunc(delay, func() { s.Events(ctx, body) })
	return id, nil
}

// SimulatedEvent is the SES event type a simulator address gets, or "" for
// an address that isn't one. A label after + is allowed, as SES allows it:
// bounce+anything@ bounces.
func SimulatedEvent(address string) string {
	local, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(address)), "@")
	if !ok || domain != SimulatorDomain {
		return ""
	}
	local, _, _ = strings.Cut(local, "+")
	switch local {
	case "success", "ooto":
		return "Delivery"
	case "bounce", "suppressionlist":
		return "Bounce"
	case "complaint":
		return "Complaint"
	}
	return ""
}

// event builds the SES event for a send to m.To, or nil.
func (s *Simulator) event(m Message, messageID string) []byte {
	kind := SimulatedEvent(m.To.Email)
	if kind == "" {
		return nil
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	at := now().UTC().Format(time.RFC3339Nano)
	tags := map[string][]string{TagKind: {tagValue(string(m.Kind))}}
	if m.AccountID != "" {
		tags[TagAccount] = []string{tagValue(m.AccountID)}
	}
	if m.CampaignID != "" {
		tags[TagCampaign] = []string{tagValue(m.CampaignID)}
	}
	e := map[string]any{
		"eventType": kind,
		"mail": map[string]any{
			"timestamp":   at,
			"messageId":   messageID,
			"source":      m.From.String(),
			"destination": []string{m.To.Email},
			"tags":        tags,
		},
	}
	to := []map[string]string{{"emailAddress": m.To.Email}}
	switch kind {
	case "Delivery":
		e["delivery"] = map[string]any{"timestamp": at, "recipients": []string{m.To.Email}}
	case "Bounce":
		sub := "General"
		if strings.HasPrefix(strings.ToLower(m.To.Email), "suppressionlist") {
			sub = "OnAccountSuppressionList"
		}
		e["bounce"] = map[string]any{"bounceType": "Permanent", "bounceSubType": sub, "bouncedRecipients": to, "timestamp": at}
	case "Complaint":
		e["complaint"] = map[string]any{"complainedRecipients": to, "complaintFeedbackType": "abuse", "timestamp": at}
	}
	body, _ := json.Marshal(e)
	return body
}
