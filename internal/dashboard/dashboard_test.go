package dashboard

import (
	"strings"
	"testing"
)

func TestURLShapes(t *testing.T) {
	const host = "https://dashboard.doppler.com"
	for _, tc := range []struct {
		name            string
		project, config string
		want            string
	}{
		{"workplace", "", "", "https://dashboard.doppler.com"},
		{"project", "app-frontend", "", "https://dashboard.doppler.com/workplace/projects/app-frontend"},
		{"config", "app-frontend", "dev", "https://dashboard.doppler.com/workplace/projects/app-frontend/configs/dev"},
		// A config name means nothing without its project, so this must not
		// build a URL that would 404.
		{"config without project", "", "dev", "https://dashboard.doppler.com"},
	} {
		if got := URL(host, tc.project, tc.config); got != tc.want {
			t.Errorf("%s: URL = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestURLTrimsTrailingSlash(t *testing.T) {
	got := URL("https://dash.example.com/", "p", "")
	want := "https://dash.example.com/workplace/projects/p"
	if got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

// Project and config names come from the API and can contain characters that
// would otherwise change the URL's shape.
func TestURLEscapesNames(t *testing.T) {
	got := URL("https://d.example.com", "a/b", "c d")
	if strings.Contains(got, "a/b") {
		t.Errorf("URL = %q; the slash in the project name was not escaped", got)
	}
	if strings.Contains(got, "c d") {
		t.Errorf("URL = %q; the space in the config name was not escaped", got)
	}
}

// A self-hosted instance's dashboard must be used, not the public one.
func TestURLUsesGivenHost(t *testing.T) {
	got := URL("https://doppler.internal.example", "p", "dev")
	if !strings.HasPrefix(got, "https://doppler.internal.example/") {
		t.Errorf("URL = %q, want the given host", got)
	}
}

// withRun swaps the launcher and returns what it was called with.
func withRun(t *testing.T) *[]string {
	t.Helper()
	orig := run
	var got []string
	run = func(name string, args ...string) error {
		got = append([]string{name}, args...)
		return nil
	}
	t.Cleanup(func() { run = orig })
	return &got
}

func TestOpenHonorsBrowserEnv(t *testing.T) {
	got := withRun(t)
	t.Setenv("BROWSER", "firefox --new-tab")

	if err := Open("https://d.example.com/x"); err != nil {
		t.Fatalf("open: %v", err)
	}
	// $BROWSER may carry arguments; executing it whole would look for a binary
	// literally named "firefox --new-tab".
	want := []string{"firefox", "--new-tab", "https://d.example.com/x"}
	if len(*got) != len(want) {
		t.Fatalf("launched %v, want %v", *got, want)
	}
	for i := range want {
		if (*got)[i] != want[i] {
			t.Fatalf("launched %v, want %v", *got, want)
		}
	}
}

// The dashboard host comes from a config file, so a non-http entry there must
// not be handed to the platform opener.
func TestOpenRefusesNonHTTPSchemes(t *testing.T) {
	got := withRun(t)
	t.Setenv("BROWSER", "")

	for _, bad := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.com",
		"not a url at all",
	} {
		if err := Open(bad); err == nil {
			t.Errorf("Open(%q) was allowed", bad)
		}
	}
	if len(*got) != 0 {
		t.Errorf("a refused URL still launched %v", *got)
	}
}

func TestOpenRefusesHostlessURL(t *testing.T) {
	withRun(t)
	t.Setenv("BROWSER", "")
	if err := Open("https:///workplace"); err == nil {
		t.Error("a URL with no host was allowed")
	}
}

func TestOpenAllowsHTTPForSelfHosted(t *testing.T) {
	got := withRun(t)
	t.Setenv("BROWSER", "")
	// A self-hosted dashboard on a private network may legitimately be plain
	// http, so it is allowed rather than forced to https.
	if err := Open("http://doppler.internal:8080/workplace"); err != nil {
		t.Fatalf("plain http was refused: %v", err)
	}
	if len(*got) == 0 {
		t.Error("nothing was launched")
	}
}
