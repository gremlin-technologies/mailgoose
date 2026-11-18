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
