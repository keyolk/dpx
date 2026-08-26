package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeScoped writes a .doppler.yaml with the given scopes.
func writeScoped(t *testing.T, scopes map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".doppler.yaml")
	body := "scoped:\n"
	for k, v := range scopes {
		body += "    " + k + ":\n        token: " + v + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPrefersEnvToken(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "dp.env")
	path := writeScoped(t, map[string]string{"/": "dp.file"})

	cfg, err := Load(path, "/anywhere")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Token != "dp.env" {
		t.Errorf("token = %q, want the environment's", cfg.Token)
	}
}

// The Doppler CLI scopes tokens by directory and takes the longest matching
// prefix. Taking "/" unconditionally would use the wrong workplace inside a
// repo the user scoped elsewhere.
func TestLoadPicksLongestMatchingScope(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "")
	path := writeScoped(t, map[string]string{
		"/":                 "dp.root",
		"/home/u/src":      "dp.src",
		"/home/u/src/repo": "dp.repo",
	})

	for _, tc := range []struct{ dir, want string }{
		{"/home/u/src/repo", "dp.repo"},
		{"/home/u/src/repo/sub", "dp.repo"},
		{"/home/u/src/other", "dp.src"},
		{"/tmp", "dp.root"},
	} {
		cfg, err := Load(path, tc.dir)
		if err != nil {
			t.Fatalf("load(%s): %v", tc.dir, err)
		}
		if cfg.Token != tc.want {
			t.Errorf("token for %s = %q, want %q", tc.dir, cfg.Token, tc.want)
		}
	}
}

// A prefix match on the raw string would make /a/bcd match a scope of /a/bc.
func TestScopeMatchRespectsPathBoundary(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "")
	path := writeScoped(t, map[string]string{"/": "dp.root", "/home/u/src": "dp.src"})

	cfg, err := Load(path, "/home/u/srcother")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Token != "dp.root" {
		t.Errorf("token = %q; /home/u/srcother must not match scope /home/u/src", cfg.Token)
	}
}

func TestLoadMissingFileIsActionable(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "")
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), "/")
	if err == nil {
		t.Fatal("missing config produced no error")
	}
	if !contains(err.Error(), "doppler login") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

// Two tokens see different slices of a workplace, so a cache shared between
// them would show projects the current token cannot read.
func TestCacheKeyDiffersPerToken(t *testing.T) {
	a := Config{Token: "dp.a", APIHost: "https://api.doppler.com"}
	b := Config{Token: "dp.b", APIHost: "https://api.doppler.com"}
	if a.CacheKey() == b.CacheKey() {
		t.Error("two tokens share a cache key")
	}
	if contains(a.CacheKey(), "dp.a") {
		t.Error("cache key leaks the token into the filename")
	}
}

func TestCacheKeyDiffersPerHost(t *testing.T) {
	a := Config{Token: "t", APIHost: "https://api.doppler.com"}
	b := Config{Token: "t", APIHost: "https://api.example.internal"}
	if a.CacheKey() == b.CacheKey() {
		t.Error("two API hosts share a cache key")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
