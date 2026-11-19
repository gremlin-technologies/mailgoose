package cli

import (
	"net/http"
	"strings"
	"testing"
)

func creditsHarness(t *testing.T) *harness {
	srv := apiStub(t, map[string]func(http.ResponseWriter){
		"/v1/credits": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"balance":740,"plan":"free","next_grant_at":"2026-12-18T09:00:00Z"}`))
		},
		"/v1/credits/transactions": func(w http.ResponseWriter) {
			_, _ = w.Write([]byte(`{"data":[{"id":"2","type":"spend","amount":-260,"expires_at":null,"ref":"campaign-1","created_at":"2026-11-18T10:00:00Z"},` +
				`{"id":"1","type":"grant","amount":1000,"expires_at":"2026-12-18T09:00:00Z","ref":"","created_at":"2026-11-18T09:00:00Z"}],"next_cursor":"abc"}`))
		},
	})
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_draftkey"
	return h
}

func TestCredits(t *testing.T) {
	h := creditsHarness(t)
	if code := h.run("credits"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	if !strings.Contains(h.out.String(), "740 credits (free plan)") || !strings.Contains(h.out.String(), "Next 1,000 free credits") {
		t.Errorf("output %q", h.out)
	}
	if code := h.run("credits", "--json"); code != ExitOK || !strings.Contains(h.out.String(), `"balance": 740`) {
		t.Errorf("--json: exit %d, %q", code, h.out)
	}
}

func TestCreditsHistory(t *testing.T) {
	h := creditsHarness(t)
	if code := h.run("credits", "history", "--limit", "2"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"spend", "-260", "campaign-1", "grant", "+1000", "--cursor abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCreditsBuy(t *testing.T) {
	srv := apiStub(t, map[string]func(http.ResponseWriter){
		"/v1/credits/checkout": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"url":"https://checkout.stripe.com/c/pay/cs_1","pack":"50k","credits":50000,"price_cents":7500,"currency":"usd","expires_at":"2026-11-19T12:00:00Z"}`))
		},
	})
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	if code := h.run("credits", "buy", "50k"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	out := h.out.String()
	for _, want := range []string{"50,000 credits for $75", "https://checkout.stripe.com/c/pay/cs_1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if code := h.run("credits", "buy", "50k", "--json"); code != ExitOK || !strings.Contains(h.out.String(), `"price_cents": 7500`) {
		t.Errorf("--json: exit %d, %q", code, h.out)
	}
	if code := h.run("credits", "buy"); code == ExitOK {
		t.Error("no pack: exit 0")
	}
}

func TestDollars(t *testing.T) {
	for cents, want := range map[int64]string{2000: "$20", 7500: "$75", 90000: "$900", 1999: "$19.99", 120000: "$1,200", 5: "$0.05"} {
		if got := dollars(cents); got != want {
			t.Errorf("%d: %q", cents, got)
		}
	}
}
