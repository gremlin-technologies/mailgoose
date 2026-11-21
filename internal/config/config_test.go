package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Port != 8080 || cfg.AppEnv != "local" || cfg.AWSRegion != "us-east-2" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if !cfg.MigrateOnStart {
		t.Error("MigrateOnStart should default to true")
	}
	if cfg.Database.Configured() {
		t.Error("database should not be configured")
	}
}

// The names below are the ones infra/modules/stack sets on the ECS tasks.
func TestLoadFromStackEnv(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"APP_ENV":               "staging",
		"AWS_REGION":            "us-east-2",
		"PORT":                  "8080",
		"DATABASE_HOST":         "db.example.internal",
		"DATABASE_PORT":         "5432",
		"DATABASE_NAME":         "mailgoose",
		"DATABASE_SECRET_ARN":   "arn:aws:secretsmanager:us-east-2:123:secret:rds!db-abc",
		"SEND_QUEUE_URL":        "https://sqs.us-east-2.amazonaws.com/123/mailgoose-staging-send",
		"SES_EVENTS_QUEUE_URL":  "https://sqs.us-east-2.amazonaws.com/123/mailgoose-staging-ses-events",
		"SES_CONFIGURATION_SET": "mailgoose-staging",
		"SYSTEM_SENDING_DOMAIN": "mail.staging.mailgoose.app",
		"SES_IDENTITY_TAG":      "Environment=staging",
		"PUBLIC_BASE_URL":       "https://api.staging.mailgoose.app",
		"APP_SECRETS_JSON":      `{"STRIPE_SECRET_KEY":"sk_test","N":3}`,
	}))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Database.UsesSecret() {
		t.Error("expected Secrets Manager credentials")
	}
	if cfg.Database.SSLMode != "require" {
		t.Errorf("SSLMode = %q, want require", cfg.Database.SSLMode)
	}
	if cfg.AppSecrets["STRIPE_SECRET_KEY"] != "sk_test" || cfg.AppSecrets["N"] != "3" {
		t.Errorf("AppSecrets = %v", cfg.AppSecrets)
	}
}

func TestLoadErrors(t *testing.T) {
	_, err := load(env(map[string]string{
		"PORT":             "eighty",
		"MIGRATE_ON_START": "maybe",
		"APP_SECRETS_JSON": "[]",
		"DATABASE_HOST":    "db",
	}))
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"PORT", "MIGRATE_ON_START", "APP_SECRETS_JSON", "DATABASE_SECRET_ARN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func TestRateLimitSetting(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.RateLimitPerMinute != 600 {
		t.Fatalf("default = %d, %v; want 600", cfg.RateLimitPerMinute, err)
	}
	cfg, err = load(env(map[string]string{"RATE_LIMIT_PER_MINUTE": "0"}))
	if err != nil || cfg.RateLimitPerMinute != 0 {
		t.Fatalf("0 should turn limiting off: %d, %v", cfg.RateLimitPerMinute, err)
	}
	for _, bad := range []string{"-1", "lots"} {
		if _, err := load(env(map[string]string{"RATE_LIMIT_PER_MINUTE": bad})); err == nil {
			t.Errorf("RATE_LIMIT_PER_MINUTE=%q should be an error", bad)
		}
	}
}

func TestMailSenderSetting(t *testing.T) {
	cases := []struct {
		env     map[string]string
		want    string
		wantErr bool
	}{
		{nil, "fake", false},
		{map[string]string{"APP_ENV": "staging"}, "ses", false},
		{map[string]string{"APP_ENV": "prod"}, "ses", false},
		{map[string]string{"APP_ENV": "staging", "MAIL_SENDER": "fake"}, "fake", false},
		{map[string]string{"APP_ENV": "prod", "MAIL_SENDER": "fake"}, "", true},
		{map[string]string{"MAIL_SENDER": "smtp"}, "", true},
	}
	for _, tc := range cases {
		cfg, err := load(env(tc.env))
		if (err != nil) != tc.wantErr {
			t.Errorf("%v: err = %v", tc.env, err)
			continue
		}
		if !tc.wantErr && cfg.MailSender != tc.want {
			t.Errorf("%v: MailSender = %q, want %q", tc.env, cfg.MailSender, tc.want)
		}
	}
}

func TestSystemSendingDomainDefault(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.SystemSendingDomain != "mail.localhost" {
		t.Fatalf("local default = %q, %v", cfg.SystemSendingDomain, err)
	}
	cfg, err = load(env(map[string]string{"APP_ENV": "staging"}))
	if err != nil || cfg.SystemSendingDomain != "" {
		t.Fatalf("staging without SYSTEM_SENDING_DOMAIN = %q, %v; want empty", cfg.SystemSendingDomain, err)
	}
}

func TestSharedSendingDomain(t *testing.T) {
	// Off unless set, locally too (MG-85).
	cfg, err := load(env(nil))
	if err != nil || cfg.SharedSendingDomain != "" {
		t.Fatalf("default = %q, %v; want off", cfg.SharedSendingDomain, err)
	}
	cfg, err = load(env(map[string]string{"SHARED_SENDING_DOMAIN": " Shared.Localhost. "}))
	if err != nil || cfg.SharedSendingDomain != "shared.localhost" {
		t.Fatalf("set = %q, %v", cfg.SharedSendingDomain, err)
	}
	for _, bad := range []string{"mail.localhost", "MAIL.localhost", "localhost", "pat@shared.localhost"} {
		if _, err := load(env(map[string]string{"SHARED_SENDING_DOMAIN": bad})); err == nil {
			t.Errorf("SHARED_SENDING_DOMAIN=%q: no error", bad)
		}
	}
}

func TestAbuseEmail(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.AbuseEmail != "abuse@mail.localhost" {
		t.Fatalf("default = %q, %v", cfg.AbuseEmail, err)
	}
	cfg, err = load(env(map[string]string{"ABUSE_EMAIL": " appeals@example.com "}))
	if err != nil || cfg.AbuseEmail != "appeals@example.com" {
		t.Fatalf("set = %q, %v", cfg.AbuseEmail, err)
	}
	if _, err := load(env(map[string]string{"ABUSE_EMAIL": "appeals"})); err == nil {
		t.Fatal("ABUSE_EMAIL without @ loaded")
	}
}

func TestSignupReview(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.SignupReview != "all" {
		t.Fatalf("default = %q, %v", cfg.SignupReview, err)
	}
	for _, v := range []string{"all", "flagged", "off"} {
		cfg, err := load(env(map[string]string{"SIGNUP_REVIEW": v}))
		if err != nil || cfg.SignupReview != v {
			t.Errorf("%s = %q, %v", v, cfg.SignupReview, err)
		}
	}
	if _, err := load(env(map[string]string{"SIGNUP_REVIEW": "some"})); err == nil {
		t.Fatal("SIGNUP_REVIEW=some loaded")
	}
}

func TestNewAccountsTestMode(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || !cfg.NewAccountsTestMode {
		t.Fatalf("default = %v, %v; want on", cfg.NewAccountsTestMode, err)
	}
	for v, want := range map[string]bool{"on": true, "off": false, "OFF": false} {
		cfg, err := load(env(map[string]string{"NEW_ACCOUNTS_TEST_MODE": v}))
		if err != nil || cfg.NewAccountsTestMode != want {
			t.Errorf("%s = %v, %v", v, cfg.NewAccountsTestMode, err)
		}
	}
	if _, err := load(env(map[string]string{"NEW_ACCOUNTS_TEST_MODE": "yes"})); err == nil {
		t.Fatal("NEW_ACCOUNTS_TEST_MODE=yes loaded")
	}
}

func TestWebDirSetting(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.WebDir != "" {
		t.Fatalf("default WebDir = %q, %v; want empty (no web app)", cfg.WebDir, err)
	}
	cfg, err = load(env(map[string]string{"WEB_DIR": "/srv/web"}))
	if err != nil || cfg.WebDir != "/srv/web" {
		t.Fatalf("WebDir = %q, %v", cfg.WebDir, err)
	}
}

func TestSendLimitSettings(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.SendRatePerSecond != 10 || cfg.AccountDailySendLimit != 10000 || cfg.NewAccountDailySendLimit != 1000 {
		t.Fatalf("defaults = %d, %d, %d, %v", cfg.SendRatePerSecond, cfg.AccountDailySendLimit, cfg.NewAccountDailySendLimit, err)
	}
	cfg, err = load(env(map[string]string{"SEND_RATE_PER_SECOND": "14", "ACCOUNT_DAILY_SEND_LIMIT": "50000"}))
	if err != nil || cfg.SendRatePerSecond != 14 || cfg.AccountDailySendLimit != 50000 {
		t.Fatalf("set = %d, %d, %v", cfg.SendRatePerSecond, cfg.AccountDailySendLimit, err)
	}
	for _, name := range []string{"SEND_RATE_PER_SECOND", "ACCOUNT_DAILY_SEND_LIMIT", "NEW_ACCOUNT_DAILY_SEND_LIMIT"} {
		for _, bad := range []string{"0", "-3", "fast"} {
			if _, err := load(env(map[string]string{name: bad})); err == nil {
				t.Errorf("%s=%q should be an error", name, bad)
			}
		}
	}
}

func TestPlatformDailySendLimit(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.PlatformDailySendLimit != 45000 {
		t.Fatalf("default = %d, %v; want 45000", cfg.PlatformDailySendLimit, err)
	}
	for v, want := range map[string]int{"0": 0, "120000": 120000} {
		cfg, err = load(env(map[string]string{"PLATFORM_DAILY_SEND_LIMIT": v}))
		if err != nil || cfg.PlatformDailySendLimit != want {
			t.Errorf("%q = %d, %v; want %d", v, cfg.PlatformDailySendLimit, err, want)
		}
	}
	for _, bad := range []string{"-1", "lots"} {
		if _, err := load(env(map[string]string{"PLATFORM_DAILY_SEND_LIMIT": bad})); err == nil {
			t.Errorf("PLATFORM_DAILY_SEND_LIMIT=%q should be an error", bad)
		}
	}
}

func TestFormDailySignupLimit(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.FormDailySignupLimit != 1000 {
		t.Fatalf("default = %d, %v; want 1000", cfg.FormDailySignupLimit, err)
	}
	for v, want := range map[string]int{"0": 0, "5000": 5000} {
		cfg, err = load(env(map[string]string{"FORM_DAILY_SIGNUP_LIMIT": v}))
		if err != nil || cfg.FormDailySignupLimit != want {
			t.Errorf("%q = %d, %v; want %d", v, cfg.FormDailySignupLimit, err, want)
		}
	}
	for _, bad := range []string{"-1", "lots"} {
		if _, err := load(env(map[string]string{"FORM_DAILY_SIGNUP_LIMIT": bad})); err == nil {
			t.Errorf("FORM_DAILY_SIGNUP_LIMIT=%q should be an error", bad)
		}
	}
}

func TestEarlyWarningSettings(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.EarlyWarningComplaintsPer10k != 5 || cfg.EarlyWarningHardBouncesPer10k != 200 {
		t.Fatalf("defaults = %d, %d, %v", cfg.EarlyWarningComplaintsPer10k, cfg.EarlyWarningHardBouncesPer10k, err)
	}
	cfg, err = load(env(map[string]string{"EARLY_WARNING_COMPLAINT_PERCENT": "0.08", "EARLY_WARNING_HARD_BOUNCE_PERCENT": "2.5%"}))
	if err != nil || cfg.EarlyWarningComplaintsPer10k != 8 || cfg.EarlyWarningHardBouncesPer10k != 250 {
		t.Fatalf("set = %d, %d, %v", cfg.EarlyWarningComplaintsPer10k, cfg.EarlyWarningHardBouncesPer10k, err)
	}
	// Each must be over 0 and under the threshold that pauses.
	for name, bads := range map[string][]string{
		"EARLY_WARNING_COMPLAINT_PERCENT":   {"0", "-1", "0.1", "0.3", "1", "lots"},
		"EARLY_WARNING_HARD_BOUNCE_PERCENT": {"0", "3", "5", "12"},
	} {
		for _, bad := range bads {
			if _, err := load(env(map[string]string{name: bad})); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q: %v", name, bad, err)
			}
		}
	}
}

func TestSliceSettings(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.SlicePercent != 10 || cfg.SliceMin != 200 || cfg.SliceWait != time.Hour {
		t.Fatalf("defaults = %d, %d, %s, %v", cfg.SlicePercent, cfg.SliceMin, cfg.SliceWait, err)
	}
	cfg, err = load(env(map[string]string{"SLICE_PERCENT": "20", "SLICE_MIN": "50", "SLICE_WAIT": "90m"}))
	if err != nil || cfg.SlicePercent != 20 || cfg.SliceMin != 50 || cfg.SliceWait != 90*time.Minute {
		t.Fatalf("set = %d, %d, %s, %v", cfg.SlicePercent, cfg.SliceMin, cfg.SliceWait, err)
	}
	for name, bads := range map[string][]string{
		"SLICE_PERCENT": {"0", "101", "ten"},
		"SLICE_MIN":     {"0", "-5"},
		"SLICE_WAIT":    {"30s", "soon", "1"},
	} {
		for _, bad := range bads {
			if _, err := load(env(map[string]string{name: bad})); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%q: %v", name, bad, err)
			}
		}
	}
}

func TestAbuseDigestHour(t *testing.T) {
	cfg, err := load(env(nil))
	if err != nil || cfg.AbuseDigestHour != 13 {
		t.Fatalf("default = %d, %v", cfg.AbuseDigestHour, err)
	}
	for v, want := range map[string]int{"0": 0, "8": 8, "23": 23} {
		cfg, err := load(env(map[string]string{"ABUSE_DIGEST_HOUR": v}))
		if err != nil || cfg.AbuseDigestHour != want {
			t.Errorf("ABUSE_DIGEST_HOUR=%s: %d, %v", v, cfg.AbuseDigestHour, err)
		}
	}
	for _, bad := range []string{"24", "-1", "eight", "8am"} {
		if _, err := load(env(map[string]string{"ABUSE_DIGEST_HOUR": bad})); err == nil || !strings.Contains(err.Error(), "ABUSE_DIGEST_HOUR") {
			t.Errorf("ABUSE_DIGEST_HOUR=%q: %v", bad, err)
		}
	}
}

func TestStripeSettings(t *testing.T) {
	cfg, err := load(env(map[string]string{
		"APP_ENV":          "staging",
		"APP_SECRETS_JSON": `{"STRIPE_SECRET_KEY":"sk_test_abc","STRIPE_WEBHOOK_SECRET":"whsec_1"}`,
		"CREDIT_PACKS":     "10k=10000:2000",
		"PLAN_PRICES":      "month:600,year:6000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Stripe != (Stripe{SecretKey: "sk_test_abc", WebhookSecret: "whsec_1", Packs: "10k=10000:2000", PlanPrices: "month:600,year:6000"}) || !cfg.Stripe.Enabled() {
		t.Errorf("stripe %+v", cfg.Stripe)
	}
	for name, e := range map[string]map[string]string{
		"live key outside prod": {"APP_ENV": "staging", "STRIPE_SECRET_KEY": "sk_live_x", "STRIPE_WEBHOOK_SECRET": "whsec_1"},
		"test key in prod":      {"APP_ENV": "prod", "STRIPE_SECRET_KEY": "sk_test_x", "STRIPE_WEBHOOK_SECRET": "whsec_1"},
		"no webhook secret":     {"STRIPE_SECRET_KEY": "sk_test_x"},
	} {
		cfg, err := load(env(e))
		if err != nil || cfg.Stripe.Enabled() || !strings.Contains(cfg.Stripe.Problem, "STRIPE") {
			t.Errorf("%s: %v %+v", name, err, cfg.Stripe)
		}
	}
	if cfg, err := load(env(map[string]string{"APP_ENV": "prod", "STRIPE_SECRET_KEY": "sk_live_x", "STRIPE_WEBHOOK_SECRET": "whsec_1"})); err != nil || !cfg.Stripe.Enabled() {
		t.Errorf("live key in prod: %v %+v", err, cfg.Stripe)
	}
	if cfg, _ := load(env(nil)); cfg.Stripe.Enabled() || cfg.Stripe.Problem != "" {
		t.Errorf("no keys: %+v", cfg.Stripe)
	}
}

func TestImagesSettings(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Images
	}{
		{"local default", nil, Images{Dir: ".local/images", BaseURL: "http://localhost:8080/images", ServedByAPI: true}},
		{"local dir", map[string]string{"IMAGES_DIR": "/tmp/img", "PUBLIC_BASE_URL": "http://localhost:9000/"},
			Images{Dir: "/tmp/img", BaseURL: "http://localhost:9000/images", ServedByAPI: true}},
		{"dir with base url", map[string]string{"IMAGES_DIR": "/tmp/img", "IMAGES_BASE_URL": "https://img.example"},
			Images{Dir: "/tmp/img", BaseURL: "https://img.example"}},
		{"bucket", map[string]string{"APP_ENV": "staging", "IMAGES_BUCKET": "mailgoose-staging-images", "IMAGES_BASE_URL": "https://img.staging.mailgoose.app/"},
			Images{Bucket: "mailgoose-staging-images", BaseURL: "https://img.staging.mailgoose.app"}},
		{"bucket without a CDN", map[string]string{"APP_ENV": "staging", "IMAGES_BUCKET": "mailgoose-staging-images", "PUBLIC_BASE_URL": "https://api.staging.mailgoose.app"},
			Images{Bucket: "mailgoose-staging-images", BaseURL: "https://api.staging.mailgoose.app/images", ServedByAPI: true}},
		{"off outside local", map[string]string{"APP_ENV": "staging"}, Images{}},
	}
	for _, c := range cases {
		cfg, err := load(env(c.env))
		if err != nil || cfg.Images != c.want || cfg.Images.Enabled() != (c.want != Images{}) {
			t.Errorf("%s: %v %+v", c.name, err, cfg.Images)
		}
	}
	for name, e := range map[string]map[string]string{
		"both":         {"IMAGES_BUCKET": "b", "IMAGES_DIR": "d", "IMAGES_BASE_URL": "https://x"},
		"dir in prod":  {"APP_ENV": "prod", "IMAGES_DIR": "d", "MAIL_SENDER": "ses"},
		"bad base url": {"IMAGES_BUCKET": "b", "IMAGES_BASE_URL": "img.example"},
	} {
		if _, err := load(env(e)); err == nil || !strings.Contains(err.Error(), "IMAGES_") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestImportHoldSettings(t *testing.T) {
	cfg, err := load(env(nil))
	want := ImportHold{RiskyPercent: 10, InvalidPercent: 5, SharedPercent: 50, SharedMin: 200}
	if err != nil || cfg.ImportHold != want {
		t.Fatalf("defaults = %+v, %v", cfg.ImportHold, err)
	}
	cfg, err = load(env(map[string]string{"IMPORT_HOLD_RISKY_PERCENT": "20", "IMPORT_HOLD_SHARED_MIN": "50"}))
	if err != nil || cfg.ImportHold.RiskyPercent != 20 || cfg.ImportHold.SharedMin != 50 {
		t.Fatalf("set = %+v, %v", cfg.ImportHold, err)
	}
	for _, name := range []string{"IMPORT_HOLD_RISKY_PERCENT", "IMPORT_HOLD_INVALID_PERCENT", "IMPORT_HOLD_SHARED_PERCENT", "IMPORT_HOLD_SHARED_MIN"} {
		for _, bad := range []string{"0", "-3", "lots"} {
			if _, err := load(env(map[string]string{name: bad})); err == nil {
				t.Errorf("%s=%q should be an error", name, bad)
			}
		}
	}
	if _, err := load(env(map[string]string{"IMPORT_HOLD_RISKY_PERCENT": "101"})); err == nil {
		t.Error("a percentage over 100 should be an error")
	}
}

func TestWebRiskKey(t *testing.T) {
	cfg, err := load(env(map[string]string{"APP_SECRETS_JSON": `{"WEB_RISK_API_KEY":" AIza-secret "}`, "WEB_RISK_API_KEY": "from-env"}))
	if err != nil || cfg.WebRiskAPIKey != "AIza-secret" {
		t.Errorf("from the secret: %q %v", cfg.WebRiskAPIKey, err)
	}
	if cfg, _ := load(env(map[string]string{"WEB_RISK_API_KEY": "from-env"})); cfg.WebRiskAPIKey != "from-env" {
		t.Errorf("from the environment: %q", cfg.WebRiskAPIKey)
	}
	if cfg, _ := load(env(nil)); cfg.WebRiskAPIKey != "" {
		t.Errorf("unset: %q", cfg.WebRiskAPIKey)
	}
}
