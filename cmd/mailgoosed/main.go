// Command mailgoosed is the mailgoose server: the HTTP API, the queue
// worker and database migrations, in one binary and one container image.
//
// ECS starts the image with ["serve"] for the API service and ["worker"] for
// the worker service (see infra/modules/stack).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/gremlin-technologies/mailgoose/internal/admin"
	"github.com/gremlin-technologies/mailgoose/internal/api"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/billing"
	"github.com/gremlin-technologies/mailgoose/internal/campaigns"
	"github.com/gremlin-technologies/mailgoose/internal/config"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/db"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/forms"
	"github.com/gremlin-technologies/mailgoose/internal/images"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/reports"
	"github.com/gremlin-technologies/mailgoose/internal/sending"
	"github.com/gremlin-technologies/mailgoose/internal/sesevents"
	"github.com/gremlin-technologies/mailgoose/internal/signups"
	"github.com/gremlin-technologies/mailgoose/internal/tracking"
	"github.com/gremlin-technologies/mailgoose/internal/unsubscribe"
	"github.com/gremlin-technologies/mailgoose/internal/version"
	"github.com/gremlin-technologies/mailgoose/internal/web"
	"github.com/gremlin-technologies/mailgoose/internal/worker"
)

func main() {
	// JSON lines on stdout, which ECS ships to CloudWatch Logs. A line logged
	// with a request's or a queue message's context carries its request_id,
	// or its queue and message_id, so one request or job can be followed
	// across components (OPS-4). Lines at level ERROR feed the error alarm
	// (infra/modules/monitoring).
	log := slog.New(worker.LogWithJobs(api.LogWithRequestIDs(
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRoot(log).ExecuteContext(ctx); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func newRoot(log *slog.Logger) *cobra.Command {
	root := &cobra.Command{
		Use:           "mailgoosed",
		Short:         "The mailgoose server: API, worker and migrations",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		&cobra.Command{
			Use:   "serve",
			Short: "Run the HTTP API",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return serve(cmd.Context(), log) },
		},
		&cobra.Command{
			Use:   "worker",
			Short: "Drain the send and SES events queues, and run CSV imports",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, _ []string) error { return runWorker(cmd.Context(), log) },
		},
		newMigrateCmd(log),
		newAdminCmd(log),
		&cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			Run: func(cmd *cobra.Command, _ []string) {
				v := version.Get()
				fmt.Fprintln(cmd.OutOrStdout(), v.Version, v.Commit)
			},
		},
	)
	return root
}

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log = log.With("env", cfg.AppEnv)
	log.Info("starting api", "version", version.Get().Version)

	srv := &api.Server{Log: log, RateLimitPerMinute: cfg.RateLimitPerMinute, TrustedOrigins: cfg.WebOrigins,
		// The API serves the web app (MG-41), so its public URL is the web app's.
		WebURL: cfg.PublicBaseURL, AbuseEmail: cfg.AbuseEmail,
		// The email check fetches links, never private addresses.
		Links: &emailcheck.HTTPLinks{}}
	if cfg.WebDir != "" {
		app, err := web.New(os.DirFS(cfg.WebDir))
		if err != nil {
			return err
		}
		srv.Web = app
		log.Info("serving the web app", "dir", cfg.WebDir)
	}
	if cfg.Database.Configured() {
		pool, err := openDB(ctx, cfg)
		if err != nil {
			return err
		}
		defer pool.Close()
		srv.DB = pool
		srv.Idempotency = api.PGIdempotencyStore{DB: pool}
		srv.Credits = &credits.Ledger{DB: pool}
		srv.Contacts = &contacts.Service{DB: pool, Checks: contacts.ImportChecks(cfg.ImportHold)}
		srv.Emails = &emails.Service{DB: pool}
		srv.Forms = &forms.Service{DB: pool}
		srv.Campaigns = &campaigns.Service{DB: pool}
		srv.Unsubscribe = &unsubscribe.Service{DB: pool}
		srv.Tracking = &tracking.Service{DB: pool, Unsubscribe: srv.Unsubscribe}
		srv.Reports = &reports.Service{DB: pool}
		if srv.Billing, err = newBilling(cfg, pool, log); err != nil {
			return err
		}
		if srv.Domains, err = newDomains(ctx, cfg, pool, log); err != nil {
			return err
		}
		if srv.Images, srv.ImageFiles, err = newImages(ctx, cfg, pool, log); err != nil {
			return err
		}

		sender, err := mail.New(ctx, cfg)
		if err != nil {
			return err
		}
		srv.Sending = &sending.Service{DB: pool, Campaigns: srv.Campaigns, Emails: srv.Emails,
			Settings: &auth.Service{DB: pool}, Domains: srv.Domains, Unsubscribe: srv.Unsubscribe, Mail: sender,
			Limits: sendLimits(cfg), Slicing: slicing(cfg)}
		srv.Signups = &signups.Service{DB: pool, Forms: srv.Forms, Settings: &auth.Service{DB: pool},
			Domains: srv.Domains, Mail: sender}
		// A suspension emails the owner, with ABUSE_EMAIL for appeals, and
		// OPS_ALERT_EMAIL.
		srv.Admin = &admin.Service{DB: pool, Sending: srv.Sending, Limits: sendLimits(cfg), Log: log.With("component", "admin"),
			Alert: &sending.OpsAlert{Mail: sender, From: mail.Address{Name: "mailgoose", Email: "alerts@" + cfg.SystemSendingDomain},
				To: cfg.OpsAlertEmail, Abuse: cfg.AbuseEmail, AppURL: cfg.PublicBaseURL, Log: log.With("component", "admin")}}
		// Payments someone has to refund or cancel by hand go to OPS_ALERT_EMAIL.
		srv.Billing.Alert = (&sending.OpsAlert{Mail: sender, From: mail.Address{Name: "mailgoose", Email: "alerts@" + cfg.SystemSendingDomain},
			To: cfg.OpsAlertEmail, Log: log.With("component", "billing")}).Notify
		// With a send queue, the API puts batches on it, since only the
		// API's role may send to it. Without one, the worker does it all.
		if cfg.SendQueueURL != "" {
			awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
			if err != nil {
				return fmt.Errorf("load AWS config: %w", err)
			}
			p := newPipeline(cfg, pool, sender, log)
			go p.RunDispatcher(ctx, &sending.SQSQueue{SQS: sqs.NewFromConfig(awsCfg), URL: cfg.SendQueueURL}, dispatchEvery)
		}
		if cfg.SystemSendingDomain == "" {
			log.Warn("SYSTEM_SENDING_DOMAIN is not set; sign-up and sign-in will answer 503")
		} else {
			srv.Auth = &auth.Service{
				DB:   pool,
				Mail: sender,
				From: mail.Address{Name: "mailgoose", Email: "signin@" + cfg.SystemSendingDomain},
				// Production keys start mg_live_; staging and local ones mg_test_.
				LiveKeys: cfg.AppEnv == "prod",
				Log:      log,
			}
		}

		if cfg.MigrateOnStart {
			if err := migrateUp(ctx, log, pool); err != nil {
				return err
			}
		}
		// Pending domains are re-checked here rather than in the worker,
		// because only the API's role may read SES identities.
		go srv.Domains.RecheckLoop(ctx, time.Minute)
	} else {
		log.Warn("no database configured; /readyz will report unavailable and idempotency keys stay in memory")
	}

	return api.ListenAndServe(ctx, log, ":"+strconv.Itoa(cfg.Port), srv.Handler())
}

// newImages builds the image store (DES-5): the S3 bucket behind the CDN
// (or, without IMAGES_BASE_URL, served by the API through presigned
// redirects), or a local directory the API serves itself at /images/.
func newImages(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*images.Service, http.Handler, error) {
	svc := &images.Service{DB: pool, BaseURL: cfg.Images.BaseURL}
	switch {
	case cfg.Images.Bucket != "":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			return nil, nil, fmt.Errorf("load AWS config: %w", err)
		}
		bucket := &images.S3{Client: s3.NewFromConfig(awsCfg), Bucket: cfg.Images.Bucket, Log: log}
		svc.Store = bucket
		if cfg.Images.ServedByAPI {
			// No CDN (staging while AWS withholds CloudFront).
			log.Info("serving images from the bucket through presigned redirects", "url", cfg.Images.BaseURL)
		}
		// The API serves /images/ by presigned redirects with or without a
		// CDN: images uploaded before the CDN was turned on keep their API
		// URLs, which sent emails already point at.
		return svc, bucket.Handler(), nil
	case cfg.Images.Dir != "":
		dir := &images.Dir{Path: cfg.Images.Dir}
		svc.Store = dir
		log.Info("storing images in a local directory", "dir", cfg.Images.Dir, "url", cfg.Images.BaseURL)
		return svc, dir.Handler(), nil
	default:
		log.Warn("IMAGES_BUCKET is not set; image uploads will answer 503")
		return nil, nil, nil
	}
}

// newDomains builds the sending-domains service: real SES and DNS, or with
// MAIL_SENDER=fake an in-memory SES and a DNS that answers every record, so
// local domains verify without touching AWS.
func newDomains(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*domains.Service, error) {
	tags, err := domains.ParseTags(cfg.SESIdentityTag)
	if err != nil {
		return nil, err
	}
	svc := &domains.Service{
		DB:               pool,
		Region:           cfg.AWSRegion,
		IdentityTags:     tags,
		ConfigurationSet: cfg.SESConfigurationSet,
		Reserved:         []string{"mailgoose.app"},
		Log:              log,
	}
	if cfg.SystemSendingDomain != "" {
		svc.Reserved = append(svc.Reserved, cfg.SystemSendingDomain)
	}
	if cfg.MailSender == "fake" {
		svc.SES = &domains.FakeSES{}
		svc.DNS = domains.LocalDNS{Region: cfg.AWSRegion}
		return svc, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	svc.SES = sesv2.NewFromConfig(awsCfg)
	svc.DNS = net.DefaultResolver
	return svc, nil
}

// sendQueueVisibility is the send queue's visibility timeout in
// infra/modules/stack.
const sendQueueVisibility = 120 * time.Second

func runWorker(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log = log.With("env", cfg.AppEnv)
	log.Info("starting worker", "version", version.Get().Version)

	queues := []worker.Queue{
		// A batch shares the global send rate with every other worker, so
		// it can take longer than the queue's 120-second visibility timeout
		// (infra/modules/stack); the worker keeps extending it.
		{Name: "send", URL: cfg.SendQueueURL, Handler: worker.NotImplemented(), MaxMessages: 1, Visibility: sendQueueVisibility},
		{Name: "ses-events", URL: cfg.SESEventsQueueURL, Handler: worker.NotImplemented()},
	}

	w := &worker.Worker{Queues: queues, Log: log}
	if cfg.SendQueueURL != "" || cfg.SESEventsQueueURL != "" {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			return fmt.Errorf("load AWS config: %w", err)
		}
		w.SQS = sqs.NewFromConfig(awsCfg)
	}

	// CSV imports wait in Postgres, not SQS, so they need no queue.
	if !cfg.Database.Configured() {
		log.Warn("no database configured; CSV imports and sending won't run")
		return w.Run(ctx)
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	sender, err := mail.New(ctx, cfg)
	if err != nil {
		return err
	}
	// Deliveries, bounces and complaints from SES.
	alert, notices := healthAlerts(cfg, sender, log)
	events := &sesevents.Service{DB: pool, Log: log.With("queue", "ses-events"), Alert: alert.AutoPaused,
		Throttled: notices.Throttled, Warning: warning(cfg)}
	queues[1].Handler = worker.HandlerFunc(events.SQSHandler())
	if cfg.MailSender == "fake" && cfg.SESEventsQueueURL == "" {
		// No SES (local): mail to SES's mailbox simulator gets its events
		// all the same, so bounces and complaints can be tried end to end.
		sender = &mail.Simulator{Sender: sender, Events: func(ctx context.Context, body []byte) {
			if err := events.Handle(ctx, body); err != nil {
				log.ErrorContext(ctx, "simulated SES event failed", "err", err)
			}
		}}
	}
	p := newPipeline(cfg, pool, sender, log)
	// One handler takes one batch at a time, at the configured rate.
	queues[0].Handler = worker.HandlerFunc(p.SQSHandler())

	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }
	imports := &contacts.Service{DB: pool}
	run(func() { imports.RunImports(ctx, log.With("job", "import"), 2*time.Second) })
	// Scheduled campaigns start within 15 seconds of their time.
	run(func() { sending.RunScheduler(ctx, pool, 15*time.Second, log.With("job", "schedule")) })
	// Raw events are kept 12 months (REP-5); checked hourly.
	pruner := &reports.Service{DB: pool}
	run(func() { pruner.RunPruner(ctx, time.Hour, log.With("job", "prune")) })
	// Plan units follow contact counts, and expired credits are written
	// off (MG-40), hourly.
	jobs, err := newBilling(cfg, pool, log.With("job", "billing"))
	if err != nil {
		return err
	}
	run(func() { jobs.RunJobs(ctx, time.Hour, log.With("job", "billing")) })
	if cfg.SendQueueURL == "" {
		// No SQS (local): the worker dispatches too, into a queue in memory.
		log.Info("no send queue configured; sending through an in-process queue")
		local := &sending.LocalQueue{Pipeline: p, Log: log}
		run(func() { local.Run(ctx) })
		run(func() { p.RunDispatcher(ctx, local, dispatchEvery) })
	}
	err = w.Run(ctx)
	wg.Wait()
	return err
}

// dispatchEvery is how often due campaigns are batched.
const dispatchEvery = 2 * time.Second

func newPipeline(cfg config.Config, pool *pgxpool.Pool, sender mail.Sender, log *slog.Logger) *sending.Pipeline {
	// A new account's slice that goes over the limits is reported like a
	// campaign's bounces and complaints (MG-62).
	alert, notices := healthAlerts(cfg, sender, log)
	return &sending.Pipeline{
		DB:          pool,
		Mail:        sender,
		Unsubscribe: &unsubscribe.Service{DB: pool},
		BaseURL:     cfg.PublicBaseURL,
		Limits:      sendLimits(cfg),
		Slicing:     slicing(cfg),
		Warning:     warning(cfg),
		Health: func(ctx context.Context, h sending.Health) {
			if h.Pause != nil {
				alert.AutoPaused(ctx, *h.Pause)
			}
			if h.Throttle != nil {
				notices.Throttled(ctx, *h.Throttle)
			}
		},
		Log: log.With("job", "send"),
	}
}

// healthAlerts tell the ops address about automatic pauses and throttles
// (CMP-6, MG-61), and owners about throttles, from notices@.
func healthAlerts(cfg config.Config, sender mail.Sender, log *slog.Logger) (*sending.OpsAlert, *sending.Notices) {
	alert := &sending.OpsAlert{Mail: sender, From: mail.Address{Name: "mailgoose", Email: "alerts@" + cfg.SystemSendingDomain},
		To: cfg.OpsAlertEmail, Abuse: cfg.AbuseEmail, AppURL: cfg.PublicBaseURL, Log: log.With("job", "health")}
	notices := &sending.Notices{Mail: sender, From: mail.Address{Name: "mailgoose", Email: "notices@" + cfg.SystemSendingDomain},
		NewDaily: cfg.NewAccountDailySendLimit, Ops: alert, Log: log.With("job", "health")}
	return alert, notices
}

func warning(cfg config.Config) sending.Warning {
	return sending.Warning{ComplaintsPer10k: cfg.EarlyWarningComplaintsPer10k, HardBouncesPer10k: cfg.EarlyWarningHardBouncesPer10k}
}

func slicing(cfg config.Config) sending.Slicing {
	return sending.Slicing{Percent: cfg.SlicePercent, Min: cfg.SliceMin, Wait: cfg.SliceWait}
}

func sendLimits(cfg config.Config) sending.Limits {
	return sending.Limits{
		PerSecond: cfg.SendRatePerSecond,
		Daily:     cfg.AccountDailySendLimit,
		NewDaily:  cfg.NewAccountDailySendLimit,
	}
}

// newAdminCmd makes and unmakes mailgoose admins (OPS-6). It needs the
// database, so only someone who can run the server (the ECS task, or the
// owner's machine) can grant the role; there is no API for it.
func newAdminCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Grant or revoke the admin role",
	}
	var reason string
	set := func(grant bool) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			pool, err := openDB(ctx, cfg)
			if err != nil {
				return err
			}
			defer pool.Close()
			svc := &admin.Service{DB: pool, Log: log.With("component", "admin")}
			if err := svc.SetAdmin(ctx, admin.Server, args[0], grant, reason); err != nil {
				return err
			}
			if grant {
				fmt.Fprintf(cmd.OutOrStdout(), "%s is an admin now.\n", args[0])
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%s isn't an admin any more.\n", args[0])
			}
			return nil
		}
	}
	grant := &cobra.Command{
		Use:   "grant <email> --reason <why>",
		Short: "Make the person with this address an admin",
		Args:  cobra.ExactArgs(1),
		RunE:  set(true),
	}
	revoke := &cobra.Command{
		Use:   "revoke <email> --reason <why>",
		Short: "Stop the person with this address being an admin",
		Args:  cobra.ExactArgs(1),
		RunE:  set(false),
	}
	for _, c := range []*cobra.Command{grant, revoke} {
		c.Flags().StringVar(&reason, "reason", "", "why (logged)")
	}
	cmd.AddCommand(grant, revoke)
	return cmd
}

func newMigrateCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply or inspect database migrations",
	}
	withMigrator := func(fn func(context.Context, *db.Migrator) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			pool, err := openDB(ctx, cfg)
			if err != nil {
				return err
			}
			defer pool.Close()
			m, err := db.NewMigrator(pool)
			if err != nil {
				return err
			}
			defer func() { _ = m.Close() }()
			return fn(ctx, m)
		}
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply every pending migration",
			Args:  cobra.NoArgs,
			RunE: withMigrator(func(ctx context.Context, m *db.Migrator) error {
				applied, err := m.Up(ctx)
				log.Info("migrations applied", "versions", applied)
				return err
			}),
		},
		&cobra.Command{
			Use:   "down",
			Short: "Roll back the most recent migration",
			Args:  cobra.NoArgs,
			RunE: withMigrator(func(ctx context.Context, m *db.Migrator) error {
				undone, err := m.Down(ctx)
				log.Info("migration rolled back", "versions", undone)
				return err
			}),
		},
		&cobra.Command{
			Use:   "status",
			Short: "List migrations and whether each is applied",
			Args:  cobra.NoArgs,
			RunE: withMigrator(func(ctx context.Context, m *db.Migrator) error {
				status, err := m.Status(ctx)
				if err != nil {
					return err
				}
				for _, s := range status {
					fmt.Printf("%05d  %-8s  %s\n", s.Source.Version, s.State, s.Source.Path)
				}
				return nil
			}),
		},
	)
	return cmd
}

func openDB(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	var src db.CredentialSource
	if cfg.Database.UsesSecret() {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			return nil, fmt.Errorf("load AWS config: %w", err)
		}
		src = db.SecretsManagerSource{Client: secretsmanager.NewFromConfig(awsCfg), SecretID: cfg.Database.SecretARN}
	}
	return db.Open(ctx, cfg.Database, src)
}

func migrateUp(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool) error {
	m, err := db.NewMigrator(pool)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	applied, err := m.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "versions", applied)
	}
	return nil
}

// newBilling sells the paid plan and credit packs (BIL-3, BIL-4). Without
// Stripe keys, or with keys this environment mustn't use, billing answers
// 503 and the log says why; the rest of the API runs as usual.
func newBilling(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*billing.Service, error) {
	packs, err := billing.ParsePacks(cfg.Stripe.Packs)
	if err != nil {
		return nil, err
	}
	prices, err := billing.ParsePrices(cfg.Stripe.PlanPrices)
	if err != nil {
		return nil, err
	}
	b := &billing.Service{DB: pool, Packs: packs, Prices: prices, BaseURL: cfg.PublicBaseURL, Log: log}
	switch {
	case cfg.Stripe.Enabled():
		b.Stripe = &billing.Client{SecretKey: cfg.Stripe.SecretKey}
		b.WebhookSecret = cfg.Stripe.WebhookSecret
	case cfg.Stripe.Problem != "":
		log.Warn("billing is off", "reason", cfg.Stripe.Problem)
	default:
		log.Info("billing is off: no STRIPE_SECRET_KEY")
	}
	return b, nil
}
