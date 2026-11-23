package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func planHarness(t *testing.T, planJSON string) (*harness, *[]string) {
	t.Helper()
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, r.Method+" "+r.URL.Path+" "+string(b))
		switch r.URL.Path {
		case "/v1/plan":
			_, _ = w.Write([]byte(planJSON))
		case "/v1/plan/checkout":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"url":"https://checkout.stripe.com/c/pay/cs_sub","interval":"year","units":2,"unit_price_cents":5000,"currency":"usd","expires_at":"2026-11-19T12:00:00Z"}`))
		case "/v1/plan/portal":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"url":"https://billing.stripe.com/p/session/bps_1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t, "")
	h.env[EnvAPIURL] = srv.URL
	h.env[EnvAPIKey] = "mg_test_fullkey"
	return h, &bodies
}

const prices = `"prices":{"month_cents":500,"year_cents":5000,"currency":"usd"}`

// planTerms are the plan's terms as the API gives them (MG-126).
func planTerms(needsDomain string) string {
	return `,"included_contacts":2500,"contacts_per_unit":10000,` +
		`"free_plan":{"contacts":1000,"monthly_credits":2500,"daily_sends":1000},` +
		`"paid_sending":{"starting_daily":1000,"most_daily":10000,"needs_domain":` + needsDomain + `},` +
		`"credit_packs":{"cheapest":{"name":"10k","credits":10000,"price_cents":2000},"months":24}`
}

// The free plan's output says what it has and what the paid plan adds, from
// the plan's terms, so a change of price or limit shows without a new CLI.
func TestPlanFreeTerms(t *testing.T) {
	h, _ := planHarness(t, `{"plan":"free","status":null,"interval":null,"units":0,"current_period_end":null,
		"cancel_at_period_end":false,"contacts":300,"units_needed":1,`+prices+planTerms("false")+`}`)
	if code := h.run("plan"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{
		`The free plan sends to up to 1,000 contacts, with 2,500 credits a month and at most 1,000 emails a day, and every email carries a "Sent by mailgoose" line.`,
		`The paid plan has no "Sent by mailgoose" line, keeps the 2,500 monthly credits, buys credit packs (from $20 for 10,000 credits, which last 24 months), and starts at 1,000 emails a day and grows each week of clean sending, up to 10,000.`,
		"It's $5 a month or $50 a year for up to 2,500 contacts, and each further 10,000 contacts adds the same again.",
		"$5 a month or $50 a year for them",
	} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}

	changed := strings.NewReplacer(`"month_cents":500`, `"month_cents":700`, `"included_contacts":2500`, `"included_contacts":3000`,
		`"needs_domain":false`, `"needs_domain":true`).Replace(`{"plan":"free","contacts":300,"units_needed":1,` + prices + planTerms("false") + `}`)
	h2, _ := planHarness(t, changed)
	if code := h2.run("plan"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h2.errOut)
	}
	for _, want := range []string{"It's $7 a month or $50 a year for up to 3,000 contacts", "stays at 1,000 emails a day until you add your own domain"} {
		if !strings.Contains(h2.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h2.out)
		}
	}
}

func TestPlanFree(t *testing.T) {
	h, _ := planHarness(t, `{"plan":"free","status":null,"interval":null,"units":0,"current_period_end":null,
		"cancel_at_period_end":false,"contacts":3000,"units_needed":2,`+prices+`}`)
	if code := h.run("plan"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"Free plan, 3,000 counted contacts", "$10 a month or $100 a year", "mailgoose plan upgrade"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	if code := h.run("plan", "--json"); code != ExitOK || !strings.Contains(h.out.String(), `"units_needed": 2`) {
		t.Errorf("--json: exit %d, %q", code, h.out)
	}
}

func TestPlanPaid(t *testing.T) {
	h, _ := planHarness(t, `{"plan":"paid","status":"past_due","interval":"year","units":1,
		"current_period_end":"2027-11-18T12:00:00Z","cancel_at_period_end":false,"contacts":2600,"units_needed":2,`+prices+`}`)
	if code := h.run("plan"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"Paid plan, $50 a year (1 × $50), 2,600 counted contacts", "Renews on 2027-11-1",
		"$100 a year from the next renewal", "sending is paused", "mailgoose plan manage"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	h2, _ := planHarness(t, `{"plan":"paid","status":"active","interval":"month","units":1,
		"current_period_end":"2026-12-18T12:00:00Z","cancel_at_period_end":true,"contacts":10,"units_needed":1,`+prices+`}`)
	if code := h2.run("plan"); code != ExitOK || !strings.Contains(h2.out.String(), "Cancelled: it ends on 2026-12-1") {
		t.Errorf("cancelled: exit %d, %q", code, h2.out)
	}
}

func TestPlanUpgradeAndManage(t *testing.T) {
	h, bodies := planHarness(t, `{}`)
	if code := h.run("plan", "upgrade", "--yearly"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.errOut)
	}
	for _, want := range []string{"$100 a year", "https://checkout.stripe.com/c/pay/cs_sub"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, h.out)
		}
	}
	if code := h.run("plan", "upgrade"); code != ExitOK {
		t.Fatalf("monthly: exit %d", code)
	}
	if code := h.run("plan", "manage"); code != ExitOK || !strings.Contains(h.out.String(), "https://billing.stripe.com/p/session/bps_1") {
		t.Errorf("manage: exit %d, %q", code, h.out)
	}
	got := strings.Join(*bodies, "\n")
	for _, want := range []string{`POST /v1/plan/checkout {"interval":"year"}`, `POST /v1/plan/checkout {"interval":"month"}`, "POST /v1/plan/portal"} {
		if !strings.Contains(got, want) {
			t.Errorf("requests lack %q:\n%s", want, got)
		}
	}
	if code := h.run("plan", "upgrade", "extra"); code == ExitOK {
		t.Error("extra argument: exit 0")
	}
}
