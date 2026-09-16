// Package config resolves how dpx talks to Doppler.
//
// Three sources, in order. First, a pass entry named in dpx's own config
// (~/.config/dpx/config.yaml) — the token then lives only in the encrypted
// store, with no plaintext copy in .doppler.yaml or a shell profile. It wins
// over the environment on purpose: a shell that exports $DOPPLER_TOKEN
// globally would otherwise make the configured entry dead code.
//
// Failing that, the same places the official `doppler` CLI reads from —
// $DOPPLER_TOKEN, or the scoped token store in ~/.doppler/.doppler.yaml. A
// user already logged into the CLI can run dpx with no setup at all.
package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds everything needed to reach the Doppler API.
type Config struct {
	Token   string
	APIHost string
	// DashboardHost is where "open in browser" points. It is read from the
	// same places as APIHost rather than hardcoded, so a self-hosted or
	// non-default instance opens its own dashboard instead of the public one.
	DashboardHost string
	// Source records where the token came from, for the error message when a
	// request is rejected — "which token is it even using" is the first
	// question a 401 raises.
	Source string
}

const (
	defaultAPIHost       = "https://api.doppler.com"
	defaultDashboardHost = "https://dashboard.doppler.com"
)

// dopplerYAML mirrors the subset of ~/.doppler/.doppler.yaml that we read.
//
// The file keys scopes by directory path: the CLI walks from the working
// directory upward and takes the longest matching prefix, so a repo can pin a
// different token than the "/" fallback. We reproduce that lookup rather than
// blindly taking "/", or dpx would use the wrong workplace inside a repo that
// the CLI scopes elsewhere.
type dopplerYAML struct {
	Scoped map[string]scopeEntry `yaml:"scoped"`
}

// scopeEntry is one directory scope's settings.
type scopeEntry struct {
	Token         string `yaml:"token"`
	APIHost       string `yaml:"api-host"`
	DashboardHost string `yaml:"dashboard-host"`
}

// DefaultPath returns the conventional location of the Doppler CLI config.
func DefaultPath() string {
	if v := os.Getenv("DOPPLER_CONFIG_DIR"); v != "" {
		return filepath.Join(v, ".doppler.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".doppler/.doppler.yaml"
	}
	return filepath.Join(home, ".doppler", ".doppler.yaml")
}

// Load resolves a token for the given working directory. A pass entry named
// in dpx's own config wins; then $DOPPLER_TOKEN, matching the CLI's own
// precedence; otherwise the scoped store is consulted.
func Load(path, workdir string) (Config, error) {
	cfg := Config{APIHost: defaultAPIHost, DashboardHost: defaultDashboardHost}

	switch passCfg, err := loadFromPass(workdir); {
	case err == nil:
		return passCfg, nil
	case !errors.Is(err, errNoDpxConfig):
		return cfg, err
	}

	if v := strings.TrimSpace(os.Getenv("DOPPLER_TOKEN")); v != "" {
		cfg.Token = v
		cfg.Source = "$DOPPLER_TOKEN"
		if h := strings.TrimSpace(os.Getenv("DOPPLER_API_HOST")); h != "" {
			cfg.APIHost = h
		}
		if h := strings.TrimSpace(os.Getenv("DOPPLER_DASHBOARD_HOST")); h != "" {
			cfg.DashboardHost = h
		}
		return cfg, nil
	}

	if path == "" {
		path = DefaultPath()
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, fmt.Errorf("no Doppler token: name a pass entry in %s, set $DOPPLER_TOKEN, or run `doppler login`", DpxConfigPath())
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	var y dopplerYAML
	if err := yaml.Unmarshal(raw, &y); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}

	scope, entry, ok := longestScope(y, workdir)
	if !ok || strings.TrimSpace(entry.Token) == "" {
		return cfg, fmt.Errorf("no Doppler token in %s: run `doppler login`, or name a pass entry in %s", path, DpxConfigPath())
	}
	cfg.Token = strings.TrimSpace(entry.Token)
	cfg.Source = fmt.Sprintf("%s (scope %s)", path, scope)
	if h := strings.TrimSpace(entry.APIHost); h != "" {
		cfg.APIHost = h
	}
	if h := strings.TrimSpace(entry.DashboardHost); h != "" {
		cfg.DashboardHost = h
	}
	return cfg, nil
}

// longestScope picks the scope whose path is the longest prefix of workdir,
// which is how the Doppler CLI resolves a token for the current directory.
func longestScope(y dopplerYAML, workdir string) (string, scopeEntry, bool) {
	s, ok := longestMatch(keysOf(y.Scoped), workdir)
	if !ok {
		return "", scopeEntry{}, false
	}
	return s, y.Scoped[s], true
}

// longestMatch returns the scope that is the longest path prefix of workdir.
// It backs both .doppler.yaml's scopes and dpx's own, so a directory resolves
// to the same scope in either file.
func longestMatch(scopes []string, workdir string) (string, bool) {
	if len(scopes) == 0 {
		return "", false
	}
	// Longest first, so the most specific scope is tested before "/".
	sort.Slice(scopes, func(i, j int) bool { return len(scopes[i]) > len(scopes[j]) })

	workdir = filepath.Clean(workdir)
	for _, s := range scopes {
		if s == "/" || withinScope(workdir, s) {
			return s, true
		}
	}
	return "", false
}

// keysOf collects a map's keys. Generic so both scope maps can use it.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// withinScope reports whether dir is scope or lives under it. A string prefix
// alone would make /a/bcd match scope /a/bc, so the boundary is checked.
func withinScope(dir, scope string) bool {
	scope = filepath.Clean(scope)
	if dir == scope {
		return true
	}
	return strings.HasPrefix(dir, scope+string(filepath.Separator))
}

// CacheKey namespaces the cache by token identity. A service token sees a
// different slice of the workplace than a personal one, so a cache shared
// across tokens would show projects the current token cannot actually read.
// Only a short one-way digest reaches the filesystem; the token itself never
// leaves memory.
func (c Config) CacheKey() string {
	sum := sha256.Sum256([]byte(c.Token))
	host := strings.NewReplacer("https://", "", "http://", "", "/", "_", ":", "_").Replace(c.APIHost)
	return fmt.Sprintf("%s-%x", host, sum[:6])
}
