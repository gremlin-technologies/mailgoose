// Package cli is the customer-facing mailgoose command line.
//
// It is built for people and for AI agents alike: every command takes --json
// for machine-readable output, nothing prompts, and an API key can come from
// MAILGOOSE_API_KEY so a headless agent never needs a config file.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/gremlin-technologies/mailgoose/internal/version"
)

// Exit codes, documented so agents can branch on them. These follow the MVP
// CLI requirements; an unreachable or unhealthy API is a plain error (1).
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
	// ExitAuth means the API key is missing, invalid or lacks permission.
	ExitAuth = 3
	// ExitNotFound means the named resource doesn't exist.
	ExitNotFound = 4
	// ExitNeedsConfirmation means the command needs an explicit confirmation,
	// such as launching a campaign without --confirm.
	ExitNeedsConfirmation = 5
)

// exitError carries an exit code with an error.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// Code returns the exit code for err.
func Code(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return ExitError
}

// App holds the CLI's dependencies, so tests can swap them.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	HTTP   *http.Client

	jsonOut bool
	apiURL  string
}

// NewApp returns an App wired to the real process.
func NewApp() *App {
	return &App{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
		HTTP:   &http.Client{Timeout: 30 * time.Second},
	}
}

// Run executes the CLI with args and returns the process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	code := Code(err)
	if code == ExitError && isUsageError(err) {
		code = ExitUsage
	}
	if a.jsonOut {
		a.printJSON(map[string]any{"error": map[string]any{"message": err.Error(), "exit_code": code}})
	} else {
		fmt.Fprintln(a.Stderr, "Error:", err)
	}
	return code
}

func isUsageError(err error) bool {
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand", "accepts ", "requires ", "invalid argument", "flag needs"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mailgoose",
		Short: "Design and send emails, and collect subscribers, from the command line",
		Long: `mailgoose designs and sends emails and runs signup forms.

Every command works for people and for AI agents: add --json for
machine-readable output. Set MAILGOOSE_API_KEY to run headless.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().BoolVar(&a.jsonOut, "json", false, "print machine-readable JSON")
	root.PersistentFlags().StringVar(&a.apiURL, "api-url", "", "API base URL (overrides "+EnvAPIURL+" and the config file)")

	root.AddCommand(a.versionCmd(), a.statusCmd(), a.configCmd())
	return root
}

func (a *App) settings() (Settings, error) {
	path, err := ConfigPath(a.Getenv)
	if err != nil {
		return Settings{}, err
	}
	fc, err := LoadFile(path)
	if err != nil {
		return Settings{}, err
	}
	s := Resolve(a.apiURL, a.Getenv, path, fc)
	if err := ValidateAPIURL(s.APIURL); err != nil {
		return Settings{}, &exitError{code: ExitUsage, err: err}
	}
	return s, nil
}

func (a *App) printJSON(v any) {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			v := version.Get()
			if a.jsonOut {
				a.printJSON(v)
				return nil
			}
			if v.Commit != "" {
				fmt.Fprintf(a.Stdout, "mailgoose %s (%s)\n", v.Version, v.Commit)
			} else {
				fmt.Fprintf(a.Stdout, "mailgoose %s\n", v.Version)
			}
			return nil
		},
	}
}

func (a *App) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check that the API is reachable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.settings()
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, s.APIURL+"/healthz", nil)
			if err != nil {
				return err
			}
			req.Header.Set("User-Agent", "mailgoose-cli/"+version.Get().Version)
			resp, err := a.HTTP.Do(req)
			if err != nil {
				return fmt.Errorf("cannot reach %s: %w", s.APIURL, err)
			}
			defer func() { _ = resp.Body.Close() }()

			var body struct {
				Status  string `json:"status"`
				Version string `json:"version"`
				Commit  string `json:"commit"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s answered %s", s.APIURL, resp.Status)
			}
			if a.jsonOut {
				a.printJSON(map[string]any{
					"api_url":        s.APIURL,
					"status":         body.Status,
					"server_version": body.Version,
					"server_commit":  body.Commit,
				})
				return nil
			}
			fmt.Fprintf(a.Stdout, "%s is %s (server %s)\n", s.APIURL, body.Status, body.Version)
			return nil
		},
	}
}

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change CLI settings",
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Show the settings in effect and where each comes from",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			s, err := a.settings()
			if err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{
					"api_url":        s.APIURL,
					"api_url_source": s.APIURLSource,
					"api_key":        MaskKey(s.APIKey),
					"api_key_source": s.APIKeySource,
					"config_path":    s.ConfigPath,
				})
				return nil
			}
			fmt.Fprintf(a.Stdout, "api_url  %s (%s)\n", s.APIURL, s.APIURLSource)
			fmt.Fprintf(a.Stdout, "api_key  %s (%s)\n", orNone(MaskKey(s.APIKey)), s.APIKeySource)
			fmt.Fprintf(a.Stdout, "file     %s\n", s.ConfigPath)
			return nil
		},
	}

	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Save a setting: api-url, or api-key (pass - to read the key from stdin)",
		Example: `  mailgoose config set api-url https://api.staging.mailgoose.app
  printf '%s' "$KEY" | mailgoose config set api-key -`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			path, err := ConfigPath(a.Getenv)
			if err != nil {
				return err
			}
			fc, err := LoadFile(path)
			if err != nil {
				return err
			}
			switch key {
			case "api-url":
				value = strings.TrimRight(value, "/")
				if err := ValidateAPIURL(value); err != nil {
					return &exitError{code: ExitUsage, err: err}
				}
				fc.APIURL = value
			case "api-key":
				if value == "-" {
					line, err := bufio.NewReader(a.Stdin).ReadString('\n')
					if err != nil && !errors.Is(err, io.EOF) {
						return fmt.Errorf("read API key from stdin: %w", err)
					}
					value = strings.TrimSpace(line)
				}
				if value == "" {
					return &exitError{code: ExitUsage, err: errors.New("API key is empty")}
				}
				fc.APIKey = value
			default:
				return &exitError{code: ExitUsage, err: fmt.Errorf("unknown setting %q; use api-url or api-key", key)}
			}
			if err := SaveFile(path, fc); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"saved": key, "config_path": path})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Saved %s to %s\n", key, path)
			return nil
		},
	}

	unset := &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a saved setting: api-url or api-key",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := ConfigPath(a.Getenv)
			if err != nil {
				return err
			}
			fc, err := LoadFile(path)
			if err != nil {
				return err
			}
			switch args[0] {
			case "api-url":
				fc.APIURL = ""
			case "api-key":
				fc.APIKey = ""
			default:
				return &exitError{code: ExitUsage, err: fmt.Errorf("unknown setting %q; use api-url or api-key", args[0])}
			}
			if err := SaveFile(path, fc); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{"removed": args[0], "config_path": path})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Removed %s from %s\n", args[0], path)
			return nil
		},
	}

	cmd.AddCommand(show, set, unset)
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
