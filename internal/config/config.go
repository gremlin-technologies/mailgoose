// Package config reads the server's settings from environment variables.
//
// The names match what infra/modules/stack passes to the ECS tasks, so the
// same binary runs locally (with DATABASE_URL) and on Fargate (with
// DATABASE_HOST, DATABASE_SECRET_ARN and friends).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config is everything the API and worker read at start.
type Config struct {
	// AppEnv is "local", "staging" or "prod".
	AppEnv string
	// AWSRegion is the AWS Region for SQS, SES and Secrets Manager.
	AWSRegion string
	// Port is the port the API listens on.
	Port int
	// PublicBaseURL is the URL people and the CLI reach the API at.
	PublicBaseURL string
	// WebOrigins are origins, besides the API's own, allowed to send
	// cookie-authenticated requests that change something (the web app's
	// origin when it's served elsewhere). Comma-separated in WEB_ORIGINS.
	WebOrigins []string
	// WebDir is the built web app (web/dist) the API serves at /. Empty
	// means the API serves no web app.
	WebDir string

	Database Database

	// SendQueueURL is the SQS queue the API enqueues send jobs on.
	SendQueueURL string
	// SESEventsQueueURL is the SQS queue SES events arrive on through SNS.
	SESEventsQueueURL string

	// SendRatePerSecond is the most campaign emails sent a second, across
	// every worker together, under the SES account's sending rate (CAM-7).
	SendRatePerSecond int
	// AccountDailySendLimit is how many campaign recipients an account may
	// be sent to in 24 hours once it's past its first week (CMP-7).
	AccountDailySendLimit int
	// NewAccountDailySendLimit is that limit in an account's first week.
	NewAccountDailySendLimit int
	// SESConfigurationSet must be passed on every send so events are recorded.
	SESConfigurationSet string
	// SystemSendingDomain is mailgoose's own sending domain (sign-in codes, double opt-in).
	SystemSendingDomain string
	// SESIdentityTag is "Environment=<env>"; every SES identity the app creates must carry it.
	SESIdentityTag string

	// MailSender picks how mail goes out: "ses", or "fake", which records
	// messages (and writes them to MailCaptureDir when set) and never calls
	// AWS. It defaults to "fake" when AppEnv is "local" and "ses" otherwise;
	// prod refuses "fake".
	MailSender string
	// MailCaptureDir is where the fake sender writes .eml files. Optional.
	MailCaptureDir string

	// MigrateOnStart runs pending database migrations when the API starts.
	MigrateOnStart bool

	// RateLimitPerMinute is how many API requests one key (or one IP, for
	// requests without credentials) may make a minute, with bursts up to the
	// same number. Zero turns rate limiting off.
	RateLimitPerMinute int

	// AppSecrets holds the JSON object injected from the <env>/app secret.
	AppSecrets map[string]string
}

// Database says how to reach Postgres. Either URL is set (local), or Host,
// Port, Name and SecretARN are (AWS, where RDS rotates the password in
// Secrets Manager).
type Database struct {
	URL       string
	Host      string
	Port      int
	Name      string
	SecretARN string
	// SSLMode for the AWS connection. RDS forces TLS, so the default is "require".
	SSLMode string
}

// UsesSecret reports whether credentials come from Secrets Manager.
func (d Database) UsesSecret() bool { return d.URL == "" && d.SecretARN != "" }

// Configured reports whether any database settings are present.
func (d Database) Configured() bool { return d.URL != "" || d.Host != "" }

// Load reads Config from the environment.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	var errs []error

	port, err := intEnv(getenv, "PORT", 8080)
	errs = append(errs, err)
	dbPort, err := intEnv(getenv, "DATABASE_PORT", 5432)
	errs = append(errs, err)
	migrate, err := boolEnv(getenv, "MIGRATE_ON_START", true)
	errs = append(errs, err)
	rateLimit, err := intEnv(getenv, "RATE_LIMIT_PER_MINUTE", 600)
	errs = append(errs, err)
	if rateLimit < 0 {
		errs = append(errs, errors.New("RATE_LIMIT_PER_MINUTE must be 0 (off) or more"))
	}

	sendRate, err := intEnv(getenv, "SEND_RATE_PER_SECOND", 10)
	errs = append(errs, err)
	dailyLimit, err := intEnv(getenv, "ACCOUNT_DAILY_SEND_LIMIT", 10000)
	errs = append(errs, err)
	newDailyLimit, err := intEnv(getenv, "NEW_ACCOUNT_DAILY_SEND_LIMIT", 1000)
	errs = append(errs, err)
	for name, v := range map[string]int{"SEND_RATE_PER_SECOND": sendRate, "ACCOUNT_DAILY_SEND_LIMIT": dailyLimit,
		"NEW_ACCOUNT_DAILY_SEND_LIMIT": newDailyLimit} {
		if v < 1 {
			errs = append(errs, fmt.Errorf("%s must be 1 or more", name))
		}
	}

	secrets, err := parseAppSecrets(getenv("APP_SECRETS_JSON"))
	errs = append(errs, err)

	cfg := Config{
		AppEnv:        orDefault(getenv("APP_ENV"), "local"),
		AWSRegion:     orDefault(getenv("AWS_REGION"), "us-east-2"),
		Port:          port,
		PublicBaseURL: orDefault(getenv("PUBLIC_BASE_URL"), fmt.Sprintf("http://localhost:%d", port)),
		Database: Database{
			URL:       getenv("DATABASE_URL"),
			Host:      getenv("DATABASE_HOST"),
			Port:      dbPort,
			Name:      orDefault(getenv("DATABASE_NAME"), "mailgoose"),
			SecretARN: getenv("DATABASE_SECRET_ARN"),
			SSLMode:   orDefault(getenv("DATABASE_SSLMODE"), "require"),
		},
		SendQueueURL:             getenv("SEND_QUEUE_URL"),
		SESEventsQueueURL:        getenv("SES_EVENTS_QUEUE_URL"),
		SendRatePerSecond:        sendRate,
		AccountDailySendLimit:    dailyLimit,
		NewAccountDailySendLimit: newDailyLimit,
		SESConfigurationSet:      getenv("SES_CONFIGURATION_SET"),
		SystemSendingDomain:      getenv("SYSTEM_SENDING_DOMAIN"),
		SESIdentityTag:           getenv("SES_IDENTITY_TAG"),
		WebDir:                   getenv("WEB_DIR"),
		MigrateOnStart:           migrate,
		RateLimitPerMinute:       rateLimit,
		AppSecrets:               secrets,
	}

	for _, o := range strings.Split(getenv("WEB_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.WebOrigins = append(cfg.WebOrigins, o)
		}
	}

	cfg.MailSender = getenv("MAIL_SENDER")
	if cfg.MailSender == "" {
		cfg.MailSender = "ses"
		if cfg.AppEnv == "local" {
			cfg.MailSender = "fake"
		}
	}
	// Mail captured locally needs no real domain.
	if cfg.SystemSendingDomain == "" && cfg.MailSender == "fake" {
		cfg.SystemSendingDomain = "mail.localhost"
	}
	cfg.MailCaptureDir = getenv("MAIL_CAPTURE_DIR")
	switch {
	case cfg.MailSender != "ses" && cfg.MailSender != "fake":
		errs = append(errs, fmt.Errorf("MAIL_SENDER must be ses or fake, got %q", cfg.MailSender))
	case cfg.MailSender == "fake" && cfg.AppEnv == "prod":
		errs = append(errs, errors.New("MAIL_SENDER=fake is not allowed in prod; it would drop every email"))
	}

	if cfg.Database.URL == "" && cfg.Database.Host != "" && cfg.Database.SecretARN == "" {
		errs = append(errs, errors.New("DATABASE_HOST is set without DATABASE_SECRET_ARN; set DATABASE_SECRET_ARN, or use DATABASE_URL instead"))
	}

	return cfg, errors.Join(errs...)
}

// parseAppSecrets reads the <env>/app secret. Values may be strings or other
// JSON scalars; non-strings are kept as their JSON text.
func parseAppSecrets(raw string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, errors.New("APP_SECRETS_JSON is not a JSON object")
	}
	for k, v := range m {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intEnv(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def, fmt.Errorf("%s must be a number, got %q", key, v)
	}
	return n, nil
}

func boolEnv(getenv func(string) string, key string, def bool) (bool, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def, fmt.Errorf("%s must be true or false, got %q", key, v)
	}
	return b, nil
}
