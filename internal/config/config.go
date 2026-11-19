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
	"math"
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
	// EarlyWarningComplaintsPer10k and EarlyWarningHardBouncesPer10k are
	// the early-warning thresholds that throttle an account (MG-61), in
	// hundredths of a percent, from EARLY_WARNING_COMPLAINT_PERCENT
	// (default 0.1) and EARLY_WARNING_HARD_BOUNCE_PERCENT (default 2).
	EarlyWarningComplaintsPer10k  int
	EarlyWarningHardBouncesPer10k int
	// OpsAlertEmail gets an email when an account is paused automatically
	// (CMP-6). Empty: the pause is only logged, as an error.
	OpsAlertEmail string
	// AbuseEmail is where a suspended account's owner appeals (MG-65): the
	// suspension email's Reply-To. Default: abuse@ the system sending
	// domain.
	AbuseEmail string
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

	// Stripe is how credits are sold (BIL-3).
	Stripe Stripe

	// Images is where uploaded pictures for image blocks go (DES-5).
	Images Images
}

// Images says where uploaded images are stored and served from. With
// neither a bucket nor a directory, uploads answer 503.
type Images struct {
	// Bucket is the S3 bucket (IMAGES_BUCKET), in AWSRegion.
	Bucket string
	// Dir is a local directory the API stores images in and serves at
	// /images/ (IMAGES_DIR); it defaults to .local/images when APP_ENV is
	// local and there's no bucket. Prod refuses it.
	Dir string
	// BaseURL is where the stored files are served from (IMAGES_BASE_URL),
	// such as the CDN in front of the bucket. Without one, the API serves
	// them itself at PUBLIC_BASE_URL/images.
	BaseURL string
	// ServedByAPI is true when there's no IMAGES_BASE_URL, so new images get
	// API URLs at /images/: served straight from the directory, or from the
	// bucket by redirecting to presigned URLs (staging, while it has no CDN).
	// The API serves /images/ from a bucket with a CDN too, for images
	// uploaded before it was turned on.
	ServedByAPI bool
}

// Enabled reports whether images can be uploaded.
func (i Images) Enabled() bool { return i.Bucket != "" || i.Dir != "" }

// Stripe holds the Stripe keys, from the <env>/app secret (or the
// environment, locally), and the credit packs' prices.
type Stripe struct {
	// SecretKey is empty when buying credits isn't set up. Only prod may
	// use a live key (sk_live_), and prod may use only a live one.
	SecretKey string
	// WebhookSecret checks the webhook's signatures (whsec_...).
	WebhookSecret string
	// Packs is CREDIT_PACKS: "name=credits:cents,..."; empty means the
	// decided packs.
	Packs string
	// PlanPrices is PLAN_PRICES: one plan unit's price in cents,
	// "month:500,year:5000"; empty means the decided prices.
	PlanPrices string
	// Problem says why the keys can't be used; buying credits is then off
	// (and the server says so in its log) rather than the server refusing
	// to start.
	Problem string
}

// Enabled reports whether credits can be bought.
func (s Stripe) Enabled() bool { return s.SecretKey != "" && s.Problem == "" }

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

	// The early-warning thresholds must sit under the 0.3% and 5% that
	// pause an account, or they'd never fire first.
	warnComplaints, err := percentEnv(getenv, "EARLY_WARNING_COMPLAINT_PERCENT", 10, 30)
	errs = append(errs, err)
	warnBounces, err := percentEnv(getenv, "EARLY_WARNING_HARD_BOUNCE_PERCENT", 200, 500)
	errs = append(errs, err)

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
		OpsAlertEmail:            strings.TrimSpace(getenv("OPS_ALERT_EMAIL")),

		EarlyWarningComplaintsPer10k:  warnComplaints,
		EarlyWarningHardBouncesPer10k: warnBounces,
		SESConfigurationSet:           getenv("SES_CONFIGURATION_SET"),
		SystemSendingDomain:           getenv("SYSTEM_SENDING_DOMAIN"),
		SESIdentityTag:                getenv("SES_IDENTITY_TAG"),
		WebDir:                        getenv("WEB_DIR"),
		MigrateOnStart:                migrate,
		RateLimitPerMinute:            rateLimit,
		AppSecrets:                    secrets,
	}

	secret := func(name string) string {
		if v := strings.TrimSpace(cfg.AppSecrets[name]); v != "" {
			return v
		}
		return strings.TrimSpace(getenv(name))
	}
	cfg.Stripe = Stripe{
		SecretKey:     secret("STRIPE_SECRET_KEY"),
		WebhookSecret: secret("STRIPE_WEBHOOK_SECRET"),
		Packs:         getenv("CREDIT_PACKS"),
		PlanPrices:    getenv("PLAN_PRICES"),
	}
	live := strings.HasPrefix(cfg.Stripe.SecretKey, "sk_live_") || strings.HasPrefix(cfg.Stripe.SecretKey, "rk_live_")
	switch {
	case cfg.Stripe.SecretKey == "":
	case live && cfg.AppEnv != "prod":
		cfg.Stripe.Problem = fmt.Sprintf("STRIPE_SECRET_KEY is a live key, which only prod may use (APP_ENV is %q)", cfg.AppEnv)
	case !live && cfg.AppEnv == "prod":
		cfg.Stripe.Problem = "STRIPE_SECRET_KEY isn't a live key; prod would sell credits for test payments"
	case cfg.Stripe.WebhookSecret == "":
		cfg.Stripe.Problem = "STRIPE_SECRET_KEY is set without STRIPE_WEBHOOK_SECRET; purchases would never be credited"
	}

	cfg.Images = Images{Bucket: strings.TrimSpace(getenv("IMAGES_BUCKET")), Dir: strings.TrimSpace(getenv("IMAGES_DIR")),
		BaseURL: strings.TrimRight(strings.TrimSpace(getenv("IMAGES_BASE_URL")), "/")}
	switch {
	case cfg.Images.Bucket != "" && cfg.Images.Dir != "":
		errs = append(errs, errors.New("set IMAGES_BUCKET or IMAGES_DIR, not both"))
	case cfg.Images.Dir != "" && cfg.AppEnv == "prod":
		errs = append(errs, errors.New("IMAGES_DIR is not allowed in prod; images would vanish with the container"))
	case cfg.Images.Bucket == "" && cfg.Images.Dir == "" && cfg.AppEnv == "local":
		cfg.Images.Dir = ".local/images"
	}
	if cfg.Images.Enabled() && cfg.Images.BaseURL == "" {
		cfg.Images.BaseURL = strings.TrimRight(cfg.PublicBaseURL, "/") + "/images"
		cfg.Images.ServedByAPI = true
	}
	if u := cfg.Images.BaseURL; u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		errs = append(errs, fmt.Errorf("IMAGES_BASE_URL must start with https://, got %q", u))
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
	cfg.AbuseEmail = strings.TrimSpace(getenv("ABUSE_EMAIL"))
	switch {
	case cfg.AbuseEmail != "" && !strings.Contains(cfg.AbuseEmail, "@"):
		errs = append(errs, errors.New("ABUSE_EMAIL must be an email address"))
	case cfg.AbuseEmail == "" && cfg.SystemSendingDomain != "":
		cfg.AbuseEmail = "abuse@" + cfg.SystemSendingDomain
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

// percentEnv reads a percentage such as "0.1" as hundredths of a percent
// (10). It must be over 0 and under below, also in hundredths.
func percentEnv(getenv func(string) string, key string, def, below int) (int, error) {
	v := strings.TrimSuffix(strings.TrimSpace(getenv(key)), "%")
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	n := int(math.Round(f * 100))
	if err != nil || n < 1 || n >= below {
		return def, fmt.Errorf("%s must be a percentage from 0.01 to under %s, got %q", key,
			strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(float64(below)/100, 'f', 2, 64), "0"), "."), getenv(key))
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
