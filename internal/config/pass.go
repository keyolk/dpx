package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// dpxYAML is dpx's own config, read from ~/.config/dpx/config.yaml.
//
// It exists for one thing the Doppler CLI has no concept of: naming a
// password-store entry instead of storing the token itself. The token then
// lives only in the encrypted store, and neither .doppler.yaml nor a shell
// profile has to hold a plaintext copy.
type dpxYAML struct {
	// PassEntry is the default entry, used when no scope matches.
	PassEntry string `yaml:"pass-entry"`
	// Scoped maps a directory to a pass entry, resolved by longest matching
	// prefix like .doppler.yaml's own scopes. A repo that belongs to another
	// workplace can name that workplace's token without exporting anything.
	Scoped map[string]string `yaml:"scoped"`

	APIHost       string `yaml:"api-host"`
	DashboardHost string `yaml:"dashboard-host"`
}

// DpxConfigPath returns the location of dpx's own config file.
func DpxConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("DPX_CONFIG_DIR")); v != "" {
		return filepath.Join(v, "config.yaml")
	}
	if v := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); v != "" {
		return filepath.Join(v, "dpx", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "dpx", "config.yaml")
	}
	return filepath.Join(home, ".config", "dpx", "config.yaml")
}

// errNoDpxConfig reports that no dpx config names a pass entry, which is the
// normal case for a machine that just uses the Doppler CLI's own store.
var errNoDpxConfig = errors.New("no dpx pass entry configured")

// loadFromPass resolves a token out of the password store for workdir.
//
// It returns errNoDpxConfig when nothing is configured, so Load can fall
// through to the CLI's own sources. Any other error is real — a named entry
// that cannot be read is a misconfiguration worth reporting rather than
// silently falling back to whatever token happens to be in the environment,
// which would be a different workplace under the same command.
func loadFromPass(workdir string) (Config, error) {
	cfg := Config{APIHost: defaultAPIHost, DashboardHost: defaultDashboardHost}

	path := DpxConfigPath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, errNoDpxConfig
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	var y dpxYAML
	if err := yaml.Unmarshal(raw, &y); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}

	entry, scope := y.entryFor(workdir)
	if entry == "" {
		return cfg, errNoDpxConfig
	}

	token, err := passShow(entry)
	if err != nil {
		return cfg, fmt.Errorf("%s: pass entry %q: %w", path, entry, err)
	}

	cfg.Token = token
	if scope == "" {
		cfg.Source = fmt.Sprintf("pass %s (%s)", entry, path)
	} else {
		cfg.Source = fmt.Sprintf("pass %s (%s, scope %s)", entry, path, scope)
	}
	if h := strings.TrimSpace(y.APIHost); h != "" {
		cfg.APIHost = h
	}
	if h := strings.TrimSpace(y.DashboardHost); h != "" {
		cfg.DashboardHost = h
	}
	return cfg, nil
}

// entryFor picks the pass entry for workdir: the longest matching scope, or
// the unscoped default. The returned scope is "" when the default was used.
func (y dpxYAML) entryFor(workdir string) (entry, scope string) {
	if s, ok := longestMatch(keysOf(y.Scoped), workdir); ok {
		if e := strings.TrimSpace(y.Scoped[s]); e != "" {
			return e, s
		}
	}
	return strings.TrimSpace(y.PassEntry), ""
}

// passShow reads one entry out of the password store.
//
// Only stdout is captured: stdin and stderr stay attached to the terminal so
// gpg's pinentry can actually prompt. That is also why there is no timeout —
// a locked key legitimately waits on a human.
func passShow(entry string) (string, error) {
	bin := strings.TrimSpace(os.Getenv("DPX_PASS_BIN"))
	if bin == "" {
		bin = "pass"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return "", fmt.Errorf("%s not found in $PATH", bin)
	}

	var out bytes.Buffer
	cmd := exec.Command(bin, "show", entry)
	cmd.Stdout = &out
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s show failed: %w", bin, err)
	}

	// A pass entry is a password on its first line with optional metadata
	// below, so only the first line is the token.
	line, err := bufio.NewReader(&out).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("entry is empty")
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errors.New("entry is empty")
	}
	return token, nil
}
