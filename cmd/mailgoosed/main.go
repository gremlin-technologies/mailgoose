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
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/gremlin-technologies/mailgoose/internal/api"
	"github.com/gremlin-technologies/mailgoose/internal/auth"
	"github.com/gremlin-technologies/mailgoose/internal/config"
	"github.com/gremlin-technologies/mailgoose/internal/contacts"
	"github.com/gremlin-technologies/mailgoose/internal/credits"
	"github.com/gremlin-technologies/mailgoose/internal/db"
	"github.com/gremlin-technologies/mailgoose/internal/domains"
	"github.com/gremlin-technologies/mailgoose/internal/emailcheck"
	"github.com/gremlin-technologies/mailgoose/internal/emails"
	"github.com/gremlin-technologies/mailgoose/internal/mail"
	"github.com/gremlin-technologies/mailgoose/internal/version"
	"github.com/gremlin-technologies/mailgoose/internal/web"
	"github.com/gremlin-technologies/mailgoose/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
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
		WebURL: cfg.PublicBaseURL,
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
		srv.Contacts = &contacts.Service{DB: pool}
		srv.Emails = &emails.Service{DB: pool}
		if srv.Domains, err = newDomains(ctx, cfg, pool, log); err != nil {
			return err
		}

		if cfg.SystemSendingDomain == "" {
			log.Warn("SYSTEM_SENDING_DOMAIN is not set; sign-up and sign-in will answer 503")
		} else {
			sender, err := mail.New(ctx, cfg)
			if err != nil {
				return err
			}
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

func runWorker(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log = log.With("env", cfg.AppEnv)
	log.Info("starting worker", "version", version.Get().Version)

	queues := []worker.Queue{
		{Name: "send", URL: cfg.SendQueueURL, Handler: worker.NotImplemented()},
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
		log.Warn("no database configured; CSV imports won't run")
		return w.Run(ctx)
	}
	pool, err := openDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	imports := &contacts.Service{DB: pool}
	done := make(chan struct{})
	go func() {
		defer close(done)
		imports.RunImports(ctx, log.With("job", "import"), 2*time.Second)
	}()
	err = w.Run(ctx)
	<-done
	return err
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
