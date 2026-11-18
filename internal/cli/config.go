package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DefaultAPIURL is where the CLI points unless told otherwise.
const DefaultAPIURL = "https://api.mailgoose.app"

// Environment variables that override the config file. Agents running
// headless should set MAILGOOSE_API_KEY rather than writing a file.
const (
	EnvAPIURL = "MAILGOOSE_API_URL"
	EnvAPIKey = "MAILGOOSE_API_KEY" //nolint:gosec // an environment variable name, not a credential
	EnvConfig = "MAILGOOSE_CONFIG"
)

// FileConfig is what the config file holds.
type FileConfig struct {
	APIURL string `json:"api_url,omitempty"`
	APIKey string `json:"api_key,omitempty"`
	// Logins holds one `mailgoose login` per API URL.
	Logins map[string]Login `json:"logins,omitempty"`
}

// Login is a stored CLI token for one API URL. The token itself is in the OS
// keychain when Store is "keychain", and in Token when Store is "file".
type Login struct {
	Store   string `json:"store"`
	Token   string `json:"token,omitempty"`
	KeyID   string `json:"key_id,omitempty"`
	Account string `json:"account,omitempty"`
	Email   string `json:"email,omitempty"`
}

// Where a login's token is kept.
const (
	StoreKeychain = "keychain"
	StoreFile     = "file"
)

// Settings are the resolved values and where each came from.
type Settings struct {
	APIURL       string `json:"api_url"`
	APIURLSource string `json:"api_url_source"`
	APIKey       string `json:"-"`
	APIKeySource string `json:"api_key_source"`
	ConfigPath   string `json:"config_path"`
}

// ConfigPath returns the config file location: $MAILGOOSE_CONFIG, or
// mailgoose/config.json under the user's config directory.
func ConfigPath(getenv func(string) string) (string, error) {
	if p := getenv(EnvConfig); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config directory: %w (set %s)", err, EnvConfig)
	}
	return filepath.Join(dir, "mailgoose", "config.json"), nil
}

// LoadFile reads the config file. A missing file is an empty config.
func LoadFile(path string) (FileConfig, error) {
	var fc FileConfig
	b, err := os.ReadFile(path) //nolint:gosec // the path is the user's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return fc, nil
	}
	if err != nil {
		return fc, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(b, &fc); err != nil {
		return fc, fmt.Errorf("config file %s is not valid JSON: %w", path, err)
	}
	return fc, nil
}

// SaveFile writes the config file readable only by its owner, since it may
// hold an API key.
func SaveFile(path string, fc FileConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	b, err := json.MarshalIndent(fc, "", "  ") //nolint:gosec // storing the key in this 0600 file is the point
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return os.Rename(tmp, path)
}

// Resolve merges flags, environment and file, in that order of precedence.
// For the credential: MAILGOOSE_API_KEY, then a stored login for the API URL,
// then the config file's api_key. A login kept in the keychain resolves with
// source "login-keychain" and no key; App.settings reads it from the keychain.
func Resolve(flagURL string, getenv func(string) string, path string, fc FileConfig) Settings {
	s := Settings{ConfigPath: path}
	switch {
	case flagURL != "":
		s.APIURL, s.APIURLSource = flagURL, "flag"
	case getenv(EnvAPIURL) != "":
		s.APIURL, s.APIURLSource = getenv(EnvAPIURL), "env"
	case fc.APIURL != "":
		s.APIURL, s.APIURLSource = fc.APIURL, "file"
	default:
		s.APIURL, s.APIURLSource = DefaultAPIURL, "default"
	}
	s.APIURL = strings.TrimRight(s.APIURL, "/")

	login, hasLogin := fc.Logins[s.APIURL]
	switch {
	case getenv(EnvAPIKey) != "":
		s.APIKey, s.APIKeySource = getenv(EnvAPIKey), "env"
	case hasLogin && login.Store == StoreKeychain:
		s.APIKeySource = "login-keychain"
	case hasLogin && login.Token != "":
		s.APIKey, s.APIKeySource = login.Token, "login-file"
	case fc.APIKey != "":
		s.APIKey, s.APIKeySource = fc.APIKey, "file"
	default:
		s.APIKeySource = "none"
	}
	return s
}

// ValidateAPIURL checks that u is an absolute http(s) URL. Plain http is
// allowed only for localhost, so an API key never crosses the internet in clear.
func ValidateAPIURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return fmt.Errorf("%q is not an absolute URL", u)
	}
	switch p.Scheme {
	case "https":
		return nil
	case "http":
		h := p.Hostname()
		if h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return nil
		}
		return fmt.Errorf("%q uses http; use https except for localhost", u)
	default:
		return fmt.Errorf("%q must start with https://", u)
	}
}

// MaskKey shows only the last four characters.
func MaskKey(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 4 {
		return "****"
	}
	return "****" + t[len(t)-4:]
}
