// Package dashboard builds Doppler dashboard URLs and opens them.
//
// The URL shapes mirror what the official CLI's `doppler open dashboard`
// produces, so a link from dpx lands on the same page the CLI would open.
package dashboard

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// URL returns the dashboard address for the deepest location given.
//
// project and config narrow the target progressively: both empty opens the
// workplace, project alone opens that project, and both opens the config's
// secrets page. There is no config-without-project form — a config name is
// only meaningful inside its project — so that combination falls back to the
// project-less URL rather than producing a link that 404s.
func URL(host, project, config string) string {
	base := strings.TrimRight(host, "/")
	switch {
	case project != "" && config != "":
		return fmt.Sprintf("%s/workplace/projects/%s/configs/%s",
			base, url.PathEscape(project), url.PathEscape(config))
	case project != "":
		return fmt.Sprintf("%s/workplace/projects/%s", base, url.PathEscape(project))
	default:
		return base
	}
}

// Validate reports whether a URL is safe to hand to a browser. Open applies it
// itself; callers that print a URL instead of opening it apply it too, since a
// printed URL is usually about to be opened by something else.
func Validate(rawURL string) error { return validate(rawURL) }

// Open launches the URL in the user's browser.
//
// $BROWSER is honored before the platform default: it is the convention every
// other terminal tool follows, and someone who set it did so precisely to stop
// tools from guessing.
func Open(rawURL string) error {
	if err := validate(rawURL); err != nil {
		return err
	}

	if b := strings.TrimSpace(os.Getenv("BROWSER")); b != "" {
		// $BROWSER may carry arguments ("firefox --new-tab"), so it is split
		// rather than executed whole.
		parts := strings.Fields(b)
		return run(parts[0], append(parts[1:], rawURL)...)
	}

	switch runtime.GOOS {
	case "darwin":
		return run("open", rawURL)
	case "windows":
		return run("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		return run("xdg-open", rawURL)
	}
}

// validate refuses anything that is not an http(s) URL.
//
// The dashboard host ultimately comes from a config file, and handing an
// arbitrary string to the platform opener is how a "file://" entry there turns
// into something the user did not ask for.
func validate(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("bad dashboard URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing to open %q: only http and https are allowed", rawURL)
	}
	if u.Host == "" {
		return fmt.Errorf("refusing to open %q: no host", rawURL)
	}
	return nil
}

// run is swapped out in tests so nothing actually launches a browser.
var run = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open browser (%s): %w", name, err)
	}
	// The browser outlives dpx, so the process is released rather than waited
	// on — waiting would block the TUI until the browser window closed.
	return cmd.Process.Release()
}
