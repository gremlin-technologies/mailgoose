package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
)

// Keyring keeps login tokens in the OS keychain (the macOS Keychain, the
// Secret Service on Linux, the Windows Credential Manager).
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, secret string) error
	Delete(service, user string) error
}

// keyringService is the keychain entry's service name; the user name is the
// API URL, so staging and production logins live side by side.
const keyringService = "mailgoose"

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (osKeyring) Set(service, user, secret string) error   { return keyring.Set(service, user, secret) }
func (osKeyring) Delete(service, user string) error        { return keyring.Delete(service, user) }

var errNoKeychain = errors.New("no keychain available")

func (a *App) keychainGet(apiURL string) (string, error) {
	if a.Keyring == nil {
		return "", errNoKeychain
	}
	return a.Keyring.Get(keyringService, apiURL)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// accountSummary and userSummary are the API's Account and User.
type accountSummary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

type userSummary struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// maxClientName matches CreateDeviceLoginRequest.client_name.
const maxClientName = 100

func (a *App) loginCmd() *cobra.Command {
	return callsAll([]string{"createDeviceLogin", "pollDeviceLogin"}, &cobra.Command{
		Use:   "login",
		Short: "Sign in through your browser and keep a token for this API",
		Long: `Starts a sign-in that you approve in the browser: the CLI prints a short code
and a link, you open the link while signed in to the web app and confirm the
code, and the CLI stores a full-scope token for the API URL in use.

The token goes in the OS keychain, or in the config file (readable only by
you) when there is no keychain. MAILGOOSE_API_KEY, when set, wins over it.

login needs a person at a terminal. Agents and scripts should set
MAILGOOSE_API_KEY to an API key instead (mailgoose keys create).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.IsTerminal() {
				return usageError("mailgoose login needs a person at a terminal to approve it in a browser.",
					"Agents and scripts: set "+EnvAPIKey+" to an API key instead. Make one with `mailgoose keys create` or in the web app.")
			}
			s, _, err := a.loadSettings()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			var start struct {
				DeviceCode              string `json:"device_code"`
				UserCode                string `json:"user_code"`
				VerificationURL         string `json:"verification_url"`
				VerificationURLComplete string `json:"verification_url_complete"`
				ExpiresIn               int    `json:"expires_in"`
				Interval                int    `json:"interval"`
			}
			body := map[string]string{"client_name": a.clientName()}
			if _, err := a.send(ctx, s.APIURL, http.MethodPost, "/v1/device-logins", body, &start, ""); err != nil {
				return err
			}

			// With --json, stdout carries only the result; the instructions go to stderr.
			msg := a.Stdout
			if a.jsonOut {
				msg = a.Stderr
			}
			fmt.Fprintf(msg, "To sign in, open this link in a browser where you're signed in to mailgoose:\n\n  %s\n\n", start.VerificationURLComplete)
			fmt.Fprintf(msg, "and check that it shows the code %s.\n", start.UserCode)
			fmt.Fprintf(msg, "Waiting for approval (the code expires in %d minutes)...\n", (start.ExpiresIn+59)/60)

			tok, err := a.pollLogin(ctx, s.APIURL, start.DeviceCode, seconds(start.Interval), seconds(start.ExpiresIn))
			if err != nil {
				return err
			}

			where, path, err := a.storeLogin(ctx, s.APIURL, tok.Token, Login{KeyID: tok.KeyID, Account: tok.Account.Name, Email: tok.User.Email})
			if err != nil {
				return fmt.Errorf("signed in, but couldn't store the token: %w", err)
			}
			envWins := a.Getenv(EnvAPIKey) != ""
			if a.jsonOut {
				a.printJSON(map[string]any{
					"api_url":     s.APIURL,
					"account":     tok.Account,
					"user":        tok.User,
					"key_id":      tok.KeyID,
					"stored_in":   where,
					"config_path": path,
					"env_api_key": envWins,
				})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Signed in to %s as %s.\n", tok.Account.Name, tok.User.Email)
			if where == StoreKeychain {
				fmt.Fprintln(a.Stdout, "The token is in the OS keychain.")
			} else {
				fmt.Fprintf(a.Stdout, "There's no keychain here, so the token is in %s, readable only by you.\n", path)
			}
			if envWins {
				fmt.Fprintf(a.Stdout, "Note: %s is set, and it wins over this login until you unset it.\n", EnvAPIKey)
			}
			return nil
		},
	})
}

func (a *App) clientName() string {
	name := ""
	if a.Hostname != nil {
		if h, err := a.Hostname(); err == nil {
			name = strings.TrimSpace(h)
		}
	}
	if name == "" {
		name = "mailgoose CLI"
	}
	if r := []rune(name); len(r) > maxClientName {
		name = string(r[:maxClientName])
	}
	return name
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

type loginToken struct {
	Token   string         `json:"token"`
	KeyID   string         `json:"key_id"`
	Account accountSummary `json:"account"`
	User    userSummary    `json:"user"`
}

// pollLogin polls until the person approves or denies the request, or it
// expires. The server decides expiry; the local deadline only stops a loop
// against a server that never answers.
func (a *App) pollLogin(ctx context.Context, apiURL, deviceCode string, interval, expiresIn time.Duration) (loginToken, error) {
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(expiresIn + time.Minute)
	body := map[string]string{"device_code": deviceCode}
	for time.Now().Before(deadline) {
		if err := a.Sleep(ctx, interval); err != nil {
			return loginToken{}, err
		}
		var out struct {
			loginToken
			Status   string `json:"status"`
			Interval int    `json:"interval"`
		}
		status, err := a.send(ctx, apiURL, http.MethodPost, "/v1/device-logins/token", body, &out, "")
		var e *Error
		switch {
		case err == nil && status == http.StatusAccepted:
			if out.Interval > 0 {
				interval = seconds(out.Interval)
			}
			continue
		case err == nil:
			return out.loginToken, nil
		case errors.As(err, &e) && (e.Code == "slow_down" || e.Code == "rate_limited"):
			interval += 5 * time.Second
			continue
		case errors.As(err, &e) && e.Code == "device_login_denied":
			return loginToken{}, &Error{Exit: ExitAuth, Code: e.Code, Message: "The sign-in was denied in the browser.",
				Hint: "Run `mailgoose login` again if that was a mistake."}
		case errors.As(err, &e) && e.Code == "device_login_expired":
			return loginToken{}, &Error{Exit: ExitAuth, Code: e.Code, Message: "The code expired before anyone approved it.",
				Hint: "Run `mailgoose login` again and approve it within 10 minutes."}
		default:
			return loginToken{}, err
		}
	}
	return loginToken{}, &Error{Exit: ExitAuth, Code: "device_login_expired", Message: "The code expired before anyone approved it.",
		Hint: "Run `mailgoose login` again and approve it within 10 minutes."}
}

// storeLogin keeps token for apiURL, in the keychain when there is one and
// in the config file otherwise, and returns where. A login it replaces is
// revoked first, best effort, so old tokens don't pile up.
func (a *App) storeLogin(ctx context.Context, apiURL, token string, login Login) (where, path string, err error) {
	path, err = ConfigPath(a.Getenv)
	if err != nil {
		return "", "", err
	}
	fc, err := LoadFile(path)
	if err != nil {
		return "", path, err
	}
	if old, ok := fc.Logins[apiURL]; ok {
		if oldToken := a.loginToken(apiURL, old); oldToken != "" && oldToken != token {
			_, _ = a.send(ctx, apiURL, http.MethodDelete, "/v1/cli-tokens/current", nil, nil, oldToken)
		}
	}

	login.Store, login.Token = StoreFile, token
	if a.Keyring != nil && a.Keyring.Set(keyringService, apiURL, token) == nil {
		login.Store, login.Token = StoreKeychain, ""
	}
	if fc.Logins == nil {
		fc.Logins = map[string]Login{}
	}
	fc.Logins[apiURL] = login
	if err := SaveFile(path, fc); err != nil {
		return "", path, err
	}
	return login.Store, path, nil
}

// loginToken returns a stored login's token, or "" if it can't be read.
func (a *App) loginToken(apiURL string, l Login) string {
	if l.Store == StoreKeychain {
		t, err := a.keychainGet(apiURL)
		if err != nil {
			return ""
		}
		return t
	}
	return l.Token
}

func (a *App) logoutCmd() *cobra.Command {
	return calls("revokeCurrentCliToken", &cobra.Command{
		Use:   "logout",
		Short: "Revoke this API's login token and remove it from this computer",
		Long: `Revokes the token that mailgoose login stored for the API URL in use, and
removes it from the keychain or config file. API keys, including one in
MAILGOOSE_API_KEY or saved with config set api-key, are not touched; revoke
those with mailgoose keys revoke.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, fc, err := a.loadSettings()
			if err != nil {
				return err
			}
			login, ok := fc.Logins[s.APIURL]
			if !ok {
				if a.jsonOut {
					a.printJSON(map[string]any{"api_url": s.APIURL, "logged_out": false, "revoked": false})
					return nil
				}
				fmt.Fprintf(a.Stdout, "Not signed in to %s with mailgoose login; nothing to do.\n", s.APIURL)
				return nil
			}

			revoked := false
			if token := a.loginToken(s.APIURL, login); token != "" {
				_, err := a.send(cmd.Context(), s.APIURL, http.MethodDelete, "/v1/cli-tokens/current", nil, nil, token)
				var e *Error
				switch {
				case err == nil:
					revoked = true
				case errors.As(err, &e) && e.Exit == ExitAuth:
					// Already revoked or expired: nothing left to revoke.
				default:
					return &Error{Exit: Code(err), Code: asError(err).Code,
						Message: "Couldn't revoke the login token: " + asError(err).Message,
						Hint:    "The token is still stored. Run `mailgoose logout` again once the API can be reached."}
				}
			}

			if login.Store == StoreKeychain && a.Keyring != nil {
				if err := a.Keyring.Delete(keyringService, s.APIURL); err != nil && !errors.Is(err, keyring.ErrNotFound) {
					return fmt.Errorf("remove the token from the keychain: %w", err)
				}
			}
			path, err := ConfigPath(a.Getenv)
			if err != nil {
				return err
			}
			delete(fc.Logins, s.APIURL)
			if err := SaveFile(path, fc); err != nil {
				return err
			}

			if a.jsonOut {
				a.printJSON(map[string]any{"api_url": s.APIURL, "logged_out": true, "revoked": revoked})
				return nil
			}
			fmt.Fprintf(a.Stdout, "Signed out of %s, and removed the token from this computer.\n", s.APIURL)
			if !revoked {
				fmt.Fprintln(a.Stdout, "The token was already revoked or had expired.")
			}
			if a.Getenv(EnvAPIKey) != "" {
				fmt.Fprintf(a.Stdout, "%s is still set; commands keep using it.\n", EnvAPIKey)
			}
			return nil
		},
	})
}

func (a *App) whoamiCmd() *cobra.Command {
	return calls("getWhoami", &cobra.Command{
		Use:   "whoami",
		Short: "Show the account, user and sign-in method in use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.settings()
			if err != nil {
				return err
			}
			var who struct {
				Account    accountSummary `json:"account"`
				User       *userSummary   `json:"user"`
				AuthMethod string         `json:"auth_method"`
				Scope      string         `json:"scope"`
				KeyID      *string        `json:"key_id"`
			}
			if s.APIKey == "" {
				return authError("Not signed in.", "Run `mailgoose login`, or set "+EnvAPIKey+" to an API key (agents and scripts).")
			}
			if _, err := a.send(cmd.Context(), s.APIURL, http.MethodGet, "/v1/whoami", nil, &who, s.APIKey); err != nil {
				return err
			}
			if a.jsonOut {
				a.printJSON(map[string]any{
					"api_url":           s.APIURL,
					"credential_source": s.APIKeySource,
					"account":           who.Account,
					"user":              who.User,
					"auth_method":       who.AuthMethod,
					"scope":             who.Scope,
					"key_id":            who.KeyID,
				})
				return nil
			}
			user := "(none: the key's maker was removed)"
			if who.User != nil {
				user = who.User.Email
			}
			fmt.Fprintf(a.Stdout, "account  %s (%s plan)\n", who.Account.Name, who.Account.Plan)
			fmt.Fprintf(a.Stdout, "user     %s\n", user)
			fmt.Fprintf(a.Stdout, "auth     %s, %s scope (from %s)\n", who.AuthMethod, who.Scope, s.APIKeySource)
			fmt.Fprintf(a.Stdout, "api_url  %s\n", s.APIURL)
			return nil
		},
	})
}
