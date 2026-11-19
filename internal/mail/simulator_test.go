package mail

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestSimulatedEvent(t *testing.T) {
	for addr, want := range map[string]string{
		"success@simulator.amazonses.com":         "Delivery",
		"ooto@simulator.amazonses.com":            "Delivery",
		"Bounce@Simulator.AmazonSES.com":          "Bounce",
		"bounce+e2e-42@simulator.amazonses.com":   "Bounce",
		"suppressionlist@simulator.amazonses.com": "Bounce",
		"complaint@simulator.amazonses.com":       "Complaint",
		"someone@simulator.amazonses.com":         "",
		"bounce@example.com":                      "",
		"not an address":                          "",
	} {
		if got := SimulatedEvent(addr); got != want {
			t.Errorf("SimulatedEvent(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestSimulatorSendsTheEvent(t *testing.T) {
	got := make(chan []byte, 2)
	fake := &Fake{}
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	s := &Simulator{Sender: fake, Delay: time.Millisecond, Now: func() time.Time { return at },
		Events: func(_ context.Context, body []byte) { got <- body }}

	m := Message{
		From: Address{Name: "Pat", Email: "news@patsbakery.example"}, Subject: "Hi", Text: "Hi",
		Kind: KindCampaign, AccountID: "acct-1", CampaignID: "camp-1",
	}
	m.To = Address{Email: "pat@example.com"}
	if _, err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m.To = Address{Email: "bounce@simulator.amazonses.com"}
	id, err := s.Send(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(fake.Sent()); n != 2 {
		t.Fatalf("the fake recorded %d messages, want 2", n)
	}

	var e struct {
		EventType string `json:"eventType"`
		Mail      struct {
			MessageID string              `json:"messageId"`
			Tags      map[string][]string `json:"tags"`
		} `json:"mail"`
		Bounce struct {
			BounceType        string `json:"bounceType"`
			BouncedRecipients []struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"bouncedRecipients"`
			Timestamp string `json:"timestamp"`
		} `json:"bounce"`
	}
	select {
	case body := <-got:
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	if e.EventType != "Bounce" || e.Mail.MessageID != id || e.Bounce.BounceType != "Permanent" ||
		len(e.Bounce.BouncedRecipients) != 1 || e.Bounce.BouncedRecipients[0].EmailAddress != "bounce@simulator.amazonses.com" ||
		e.Bounce.Timestamp != "2026-11-19T09:00:00Z" {
		t.Errorf("event %+v", e)
	}
	if e.Mail.Tags[TagKind][0] != "campaign" || e.Mail.Tags[TagAccount][0] != "acct-1" || e.Mail.Tags[TagCampaign][0] != "camp-1" {
		t.Errorf("tags %v", e.Mail.Tags)
	}
	select {
	case body := <-got:
		t.Errorf("a second event for an ordinary address: %s", body)
	case <-time.After(50 * time.Millisecond):
	}
}
