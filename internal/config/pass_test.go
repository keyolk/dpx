package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMain isolates dpx's own config for the whole package.
//
// Without this, every existing test would read the developer's real
// ~/.config/dpx/config.yaml — which now outranks everything else — and a
// machine with a pass entry configured would fail tests that a clean CI passes.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "dpx-config-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("DPX_CONFIG_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// writeDpxConfig points DPX_CONFIG_DIR at a fresh directory holding body.
func writeDpxConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DPX_CONFIG_DIR", dir)
}

// fakePass installs a stub `pass` that prints the token mapped to the entry it
// is asked for, and exits non-zero for anything else — so a test can tell
// "resolved the wrong entry" from "resolved nothing".
func fakePass(t *testing.T, entries map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("stub uses a POSIX shell script")
	}

	var cases string
	for entry, out := range entries {
		cases += "  " + entry + ") printf '%s\\n' '" + out + "' ;;\n"
	}
	script := "#!/bin/sh\n" +
		"[ \"$1\" = show ] || exit 64\n" +
		"case \"$2\" in\n" + cases +
		"  *) echo \"no such entry: $2\" >&2; exit 1 ;;\n" +
		"esac\n"

	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DPX_PASS_BIN", path)
}

func TestLoadPrefersPassOverEnvToken(t *testing.T) {
	// The whole point of the ordering: a shell that exports $DOPPLER_TOKEN
	// globally must not shadow the entry the user configured.
	t.Setenv("DOPPLER_TOKEN", "dp.env")
	writeDpxConfig(t, "pass-entry: sendbird/doppler/token\n")
	fakePass(t, map[string]string{"sendbird/doppler/token": "dp.pass"})

	cfg, err := Load(writeScoped(t, map[string]string{"/": "dp.file"}), "/anywhere")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Token != "dp.pass" {
		t.Errorf("token = %q, want the pass entry's", cfg.Token)
	}
	if !strings.Contains(cfg.Source, "sendbird/doppler/token") {
		t.Errorf("source = %q, want it to name the pass entry", cfg.Source)
	}
}

func TestLoadPicksLongestMatchingPassScope(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "")
	writeDpxConfig(t, `pass-entry: default/entry
scoped:
    /home/u/src: src/entry
    /home/u/src/repo: repo/entry
`)
	fakePass(t, map[string]string{
		"default/entry": "dp.default",
		"src/entry":     "dp.src",
		"repo/entry":    "dp.repo",
	})

	for _, tc := range []struct{ dir, want string }{
		{"/home/u/src/repo", "dp.repo"},
		{"/home/u/src/repo/sub", "dp.repo"},
		{"/home/u/src/other", "dp.src"},
		// No scope matches, so the unscoped default is used rather than
		// falling through to the Doppler CLI's store.
		{"/tmp", "dp.default"},
	} {
		cfg, err := Load("", tc.dir)
		if err != nil {
			t.Fatalf("load %s: %v", tc.dir, err)
		}
		if cfg.Token != tc.want {
			t.Errorf("dir %s: token = %q, want %q", tc.dir, cfg.Token, tc.want)
		}
	}
}

// A pass entry holds the password on its first line and optional metadata
// below; taking the whole file would send the metadata as the token.
func TestLoadTakesFirstLineOfPassEntry(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "")
	writeDpxConfig(t, "pass-entry: e\n")
	fakePass(t, map[string]string{"e": `dp.pt.abc
url: https://dashboard.doppler.com
user: someone`})

	cfg, err := Load("", "/anywhere")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Token != "dp.pt.abc" {
		t.Errorf("token = %q, want only the first line", cfg.Token)
	}
}

// A named entry that cannot be read is a misconfiguration. Falling back to
// $DOPPLER_TOKEN would silently browse a different workplace than the one the
// user pinned, which is worse than failing.
func TestLoadFailsWhenNamedPassEntryIsMissing(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "dp.env")
	writeDpxConfig(t, "pass-entry: nope/entry\n")
	fakePass(t, map[string]string{"other/entry": "dp.other"})

	_, err := Load("", "/anywhere")
	if err == nil {
		t.Fatal("load succeeded, want an error naming the unreadable entry")
	}
	if !strings.Contains(err.Error(), "nope/entry") {
		t.Errorf("error = %v, want it to name the entry", err)
	}
}

// A config file with no pass entry is not an error: it may exist only to set
// api-host, and the Doppler CLI's own sources still apply.
func TestLoadFallsThroughWhenNoPassEntryConfigured(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "dp.env")
	writeDpxConfig(t, "api-host: https://example.invalid\n")

	cfg, err := Load("", "/anywhere")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Token != "dp.env" {
		t.Errorf("token = %q, want the environment's", cfg.Token)
	}
}

func TestLoadAppliesPassConfigHosts(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "")
	writeDpxConfig(t, `pass-entry: e
api-host: https://api.example.invalid
dashboard-host: https://dash.example.invalid
`)
	fakePass(t, map[string]string{"e": "dp.pass"})

	cfg, err := Load("", "/anywhere")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIHost != "https://api.example.invalid" {
		t.Errorf("api host = %q", cfg.APIHost)
	}
	if cfg.DashboardHost != "https://dash.example.invalid" {
		t.Errorf("dashboard host = %q", cfg.DashboardHost)
	}
}
