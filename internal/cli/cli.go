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
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/gremlin-technologies/mailgoose/internal/version"
)

// App holds the CLI's dependencies, so tests can swap them.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	HTTP   *http.Client
	// IsTerminal reports whether stdin is a terminal. Nothing prompts today;
	// a future prompt must check it and fall back to needFlag without one.
	IsTerminal func() bool

	jsonOut bool
	apiURL  string
	// extra commands, for tests.
	extra []*cobra.Command
}

// NewApp returns an App wired to the real process.
func NewApp() *App {
	return &App{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Getenv:     os.Getenv,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		IsTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }, //nolint:gosec // a file descriptor fits in an int
	}
}

// Run executes the CLI with args and returns the process exit code. Output
// goes to stdout; errors go to stderr, as JSON with --json.
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
	e := asError(err)
	// A flag parsing error comes before --json is parsed, so look for it too.
	if a.jsonOut || slices.Contains(args, "--json") {
		enc := json.NewEncoder(a.Stderr)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"error": map[string]string{"code": e.Code, "message": e.Message, "hint": e.Hint}})
	} else {
		fmt.Fprintln(a.Stderr, "Error:", e.Message)
		if e.Hint != "" {
			fmt.Fprintln(a.Stderr, "Hint:", e.Hint)
		}
	}
	return e.Exit
}

// Commands lists every runnable command and the API operation it calls.
func (a *App) Commands() []CommandInfo { return Registry(a.rootCmd()) }

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

	root.AddCommand(a.versionCmd(), a.statusCmd(), a.configCmd(), a.commandsCmd(), a.keysCmd(), a.creditsCmd(), a.accountCmd(), &cobra.Command{
		Use:   "exit-codes",
		Short: "What each exit code means",
		Long:  exitCodesHelp,
	})
	root.AddCommand(a.extra...)
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
		return Settings{}, usageError(err.Error(), "Pass --api-url https://... or set "+EnvAPIURL+".")
	}
	return s, nil
}

func (a *App) printJSON(v any) {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (a *App) versionCmd() *cobra.Command {
	return local(&cobra.Command{
		Use:   "version",
		Short: "Print the CLI version, commit and build date",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			v := version.Get()
			if a.jsonOut {
				a.printJSON(v)
				return nil
			}
			fmt.Fprintf(a.Stdout, "mailgoose %s\ncommit  %s\nbuilt   %s\n", v.Version, orUnknown(v.Commit), orUnknown(v.BuildDate))
			return nil
		},
	})
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func (a *App) commandsCmd() *cobra.Command {
	return local(&cobra.Command{
		Use:    "commands",
		Short:  "List every command and the API operation it calls",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg := Registry(cmd.Root())
			if a.jsonOut {
				a.printJSON(reg)
				return nil
			}
			for _, c := range reg {
				fmt.Fprintf(a.Stdout, "%-24s %s\n", c.Command, orNone(c.Operation))
			}
			return nil
		},
	})
}

func (a *App) statusCmd() *cobra.Command {
	return calls("getHealth", &cobra.Command{
		Use:   "status",
		Short: "Check that the API is reachable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.settings()
			if err != nil {
				return err
			}
			var body struct {
				Status  string `json:"status"`
				Version string `json:"version"`
				Commit  string `json:"commit"`
			}
			if err := a.call(cmd.Context(), http.MethodGet, "/healthz", nil, &body, false); err != nil {
				return err
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
	})
}

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or change CLI settings",
	}

	show := local(&cobra.Command{
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
	})

	set := local(&cobra.Command{
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
					return usageError(err.Error(), "")
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
					return usageError("API key is empty.", "Pass the key as an argument, or pipe it in with - as the value.")
				}
				fc.APIKey = value
			default:
				return usageError(fmt.Sprintf("Unknown setting %q.", key), "Use api-url or api-key.")
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
	})

	unset := local(&cobra.Command{
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
				return usageError(fmt.Sprintf("Unknown setting %q.", args[0]), "Use api-url or api-key.")
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
	})

	cmd.AddCommand(show, set, unset)
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
