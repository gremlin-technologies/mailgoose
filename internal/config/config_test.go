package config

import (
	"strings"
	"testing"
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
