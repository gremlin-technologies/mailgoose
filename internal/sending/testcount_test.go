package sending

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gremlin-technologies/mailgoose/internal/mail"
)

// refusingSender sends through next, except that a copy to an address in
// refuse gets that error back and isn't sent.
type refusingSender struct {
	next   mail.Sender
	refuse map[string]error
}

func (r *refusingSender) Send(ctx context.Context, m mail.Message) (string, error) {
	if err, ok := r.refuse[m.To.Email]; ok {
		return "", err
	}
	return r.next.Send(ctx, m)
}

// TestTestCountsOnlyAccepted: a test copy counts toward the day's tests
// only once the mail provider accepted it (MG-101). One refused by the send
// guard, rejected by the provider or told to retry later gives its place
// back, and so do the copies after it, which aren't tried; one the provider
// didn't answer may have gone, so it still counts.
func TestTestCountsOnlyAccepted(t *testing.T) {
	guard := func(h *harness) mail.Sender {
		return &mail.Guard{Sender: h.fake, Verified: func(context.Context, string, string) error { return errors.New("not verified here") }}
	}
	refuse := func(err error) func(h *harness) mail.Sender {
		return func(h *harness) mail.Sender {
			return &refusingSender{next: h.fake, refuse: map[string]error{"a@patsbakery.com": err, "b@patsbakery.com": err}}
		}
	}
	for name, sender := range map[string]func(*harness) mail.Sender{
		"guard":       guard,
		"rejected":    refuse(fmt.Errorf("ses: send: %w", mail.ErrRejected)),
		"retry later": refuse(fmt.Errorf("ses: send: %w", mail.ErrRetryLater)),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.svc.Mail = sender(h)
			var se *SendError
			if _, err := h.test("a@patsbakery.com", "b@patsbakery.com"); !errors.As(err, &se) || se.Address != "a@patsbakery.com" || se.Counted {
				t.Fatalf("refused: %v %+v", err, se)
			}
			if n := h.testsCounted(t); n != 0 || len(h.fake.Sent()) != 0 {
				t.Fatalf("after a refusal: %d counted, %d sent", n, len(h.fake.Sent()))
			}
			// An accepted copy adds one.
			h.svc.Mail = h.fake
			res, err := h.test()
			if err != nil || res.SentToday != 1 || h.testsCounted(t) != 1 {
				t.Fatalf("accepted: %+v %v, %d counted", res, err, h.testsCounted(t))
			}
		})
	}

	t.Run("part way", func(t *testing.T) {
		h := newHarness(t)
		h.svc.Mail = &refusingSender{next: h.fake, refuse: map[string]error{"b@patsbakery.com": fmt.Errorf("ses: send: %w", mail.ErrRejected)}}
		var se *SendError
		if _, err := h.test("a@patsbakery.com", "b@patsbakery.com", "c@patsbakery.com"); !errors.As(err, &se) || se.Address != "b@patsbakery.com" || se.Counted {
			t.Fatalf("refused: %v %+v", err, se)
		}
		// a went and counts; b was refused and c wasn't tried.
		if n := h.testsCounted(t); n != 1 || len(h.fake.Sent()) != 1 {
			t.Errorf("%d counted, %d sent", n, len(h.fake.Sent()))
		}
	})

	t.Run("no answer", func(t *testing.T) {
		h := newHarness(t)
		h.svc.Mail = &refusingSender{next: h.fake, refuse: map[string]error{"b@patsbakery.com": errors.New("ses: send: i/o timeout")}}
		var se *SendError
		if _, err := h.test("a@patsbakery.com", "b@patsbakery.com", "c@patsbakery.com"); !errors.As(err, &se) || !se.Counted {
			t.Fatalf("no answer: %v %+v", err, se)
		}
		// a went, b may have; c wasn't tried.
		if n := h.testsCounted(t); n != 2 {
			t.Errorf("%d counted", n)
		}
	})

	t.Run("at the limit", func(t *testing.T) {
		h := newHarness(t)
		h.addTests(t, 49)
		h.svc.Mail = guard(h)
		var se *SendError
		if _, err := h.test(); !errors.As(err, &se) {
			t.Fatalf("refused: %v", err)
		}
		// The refused copy took no place, so the last one is still there.
		h.svc.Mail = h.fake
		if res, err := h.test(); err != nil || res.SentToday != 50 {
			t.Fatalf("last place: %+v %v", res, err)
		}
		var le *LimitError
		if _, err := h.test(); !errors.As(err, &le) || le.Sent != 50 {
			t.Errorf("over: %v", err)
		}
	})
}

// TestTestLimitConcurrent: tests at once never pass the day's limit, and
// places that refused copies give back are taken by later tests (MG-101).
func TestTestLimitConcurrent(t *testing.T) {
	run := func(h *harness, to []string) (ok, limited int) {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, addr := range to {
			wg.Go(func() {
				_, err := h.test(addr)
				var le *LimitError
				var se *SendError
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					ok++
				case errors.As(err, &le):
					limited++
				case errors.As(err, &se) && !se.Counted:
				default:
					t.Errorf("test to %s: %v", addr, err)
				}
			})
		}
		wg.Wait()
		return ok, limited
	}
	repeat := func(addr string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = addr
		}
		return out
	}

	t.Run("accepted", func(t *testing.T) {
		h := newHarness(t)
		h.addTests(t, 45)
		ok, limited := run(h, repeat("ok@patsbakery.com", 12))
		if ok != 5 || limited != 7 || h.testsCounted(t) != 50 || len(h.fake.Sent()) != 5 {
			t.Errorf("%d ok, %d limited, %d counted, %d sent", ok, limited, h.testsCounted(t), len(h.fake.Sent()))
		}
	})

	t.Run("with refusals", func(t *testing.T) {
		h := newHarness(t)
		h.addTests(t, 45)
		h.svc.Mail = &refusingSender{next: h.fake, refuse: map[string]error{"no@patsbakery.com": fmt.Errorf("ses: send: %w", mail.ErrRejected)}}
		to := append(repeat("ok@patsbakery.com", 10), repeat("no@patsbakery.com", 10)...)
		ok, _ := run(h, to)
		sent := len(h.fake.Sent())
		if ok != sent || sent > 5 || h.testsCounted(t) != 45+sent {
			t.Fatalf("%d ok, %d sent, %d counted", ok, sent, h.testsCounted(t))
		}
		// What the refusals held is free again: the rest of the day fills
		// to exactly the limit.
		h.svc.Mail = h.fake
		for range 5 - sent {
			if _, err := h.test(); err != nil {
				t.Fatalf("after: %v", err)
			}
		}
		var le *LimitError
		if _, err := h.test(); !errors.As(err, &le) || h.testsCounted(t) != 50 {
			t.Errorf("full: %v, %d counted", err, h.testsCounted(t))
		}
	})
}

// addTests records n tests in the last hour.
func (h *harness) addTests(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		if _, err := h.db.Exec(context.Background(), `INSERT INTO test_sends (account_id, sent_at) VALUES ($1, $2)`,
			h.acc, h.now.Add(-time.Hour+time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
}
