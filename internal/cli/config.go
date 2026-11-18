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
// headless should set MAILGOOSE_TOKEN rather than writing a file.
const (
	EnvAPIURL = "MAILGOOSE_API_URL"
	EnvToken  = "MAILGOOSE_TOKEN"
	EnvConfig = "MAILGOOSE_CONFIG"
)

// FileConfig is what the config file holds.
type FileConfig struct {
	APIURL string `json:"api_url,omitempty"`
	Token  string `json:"token,omitempty"`
}

// Settings are the resolved values and where each came from.
type Settings struct {
	APIURL       string `json:"api_url"`
	APIURLSource string `json:"api_url_source"`
	Token        string `json:"-"`
	TokenSource  string `json:"token_source"`
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
	b, err := json.MarshalIndent(fc, "", "  ")
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

	switch {
	case getenv(EnvToken) != "":
		s.Token, s.TokenSource = getenv(EnvToken), "env"
	case fc.Token != "":
		s.Token, s.TokenSource = fc.Token, "file"
	default:
		s.TokenSource = "none"
	}
	return s
}

// ValidateAPIURL checks that u is an absolute http(s) URL. Plain http is
// allowed only for localhost, so a token never crosses the internet in clear.
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

// MaskToken shows only the last four characters.
func MaskToken(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 4 {
		return "****"
	}
	return "****" + t[len(t)-4:]
}
