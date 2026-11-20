// Package config loads the single iTop instance configuration. The file holds
// one instance and one credential reference; the secret itself never appears
// in it.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/geraldcsoftware/itop-cli/internal/credential"
)

const (
	defaultAuth       = "password"
	defaultAPIVersion = "1.3"
	defaultTimeout    = 30 * time.Second
)

// Config is the parsed configuration file with defaults applied.
type Config struct {
	// URL is the iTop base URL; always https.
	URL string
	// Username is a literal or a resolver URI.
	Username string
	// Credential is the resolver URI for the secret, never a literal.
	Credential string
	// Auth is "password" or "token" (Combodo personal token).
	Auth       string
	APIVersion string
	Timeout    time.Duration
	// BWSAccessToken is a resolver URI naming the source of the Bitwarden
	// Secrets Manager access token; empty falls back to BWS_ACCESS_TOKEN.
	BWSAccessToken string
}

// credentialMistakeKeys are the keys users reach for when they mean
// 'credential'. They are rejected with a pointer to the right key rather than
// ignored. Compared case-insensitively.
var credentialMistakeKeys = map[string]bool{
	"password": true, "passwd": true, "pwd": true, "pass": true,
}

// DefaultPath returns the config file location: $ITOP_CONFIG if set, else
// $XDG_CONFIG_HOME/itop-cli/config.toml, else ~/.config/itop-cli/config.toml.
func DefaultPath() string {
	if p := os.Getenv("ITOP_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "itop-cli", "config.toml")
}

// Load parses and validates the TOML config at path. Error messages name the
// offending key and never repeat a value.
func Load(path string) (Config, error) {
	var raw struct {
		URL        string `toml:"url"`
		Username   string `toml:"username"`
		Credential string `toml:"credential"`
		Auth       string `toml:"auth"`
		APIVersion string `toml:"api_version"`
		Timeout    string `toml:"timeout"`
		BWS        struct {
			AccessToken string `toml:"accessToken"`
		} `toml:"bws"`
	}
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return Config{}, fmt.Errorf("loading config %s: %w", path, err)
	}
	for _, key := range md.Undecoded() {
		name := key.String()
		if credentialMistakeKeys[strings.ToLower(name)] {
			return Config{}, fmt.Errorf(
				"config %s: unknown key %q: the secret source belongs under 'credential' as a resolver URI (e.g. credential = %q)",
				path, name, "keychain:<service>/<account>")
		}
		return Config{}, fmt.Errorf("config %s: unknown key %q", path, name)
	}

	cfg := Config{
		URL:            raw.URL,
		Username:       raw.Username,
		Credential:     raw.Credential,
		Auth:           raw.Auth,
		APIVersion:     raw.APIVersion,
		Timeout:        defaultTimeout,
		BWSAccessToken: raw.BWS.AccessToken,
	}
	if cfg.Auth == "" {
		cfg.Auth = defaultAuth
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = defaultAPIVersion
	}
	if raw.Timeout != "" {
		d, err := time.ParseDuration(raw.Timeout)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config %s: timeout must be a positive duration such as \"30s\"", path)
		}
		cfg.Timeout = d
	}

	if cfg.URL == "" {
		return Config{}, fmt.Errorf("config %s: url is required", path)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Config{}, fmt.Errorf("config %s: url must be an https:// URL", path)
	}
	if cfg.Username == "" {
		return Config{}, fmt.Errorf("config %s: username is required", path)
	}
	if cfg.Credential == "" {
		return Config{}, fmt.Errorf("config %s: credential is required", path)
	}
	if !credential.IsURI(cfg.Credential) {
		return Config{}, fmt.Errorf("config %s: credential must be a resolver URI (keychain:, env:, bw:, bws:), not a literal secret", path)
	}
	if cfg.BWSAccessToken != "" && !credential.IsURI(cfg.BWSAccessToken) {
		return Config{}, fmt.Errorf("config %s: bws.accessToken must be a resolver URI (keychain:, env:, bw:), not a literal token", path)
	}
	if cfg.Auth != "password" && cfg.Auth != "token" {
		return Config{}, fmt.Errorf("config %s: auth must be \"password\" or \"token\"", path)
	}
	return cfg, nil
}
