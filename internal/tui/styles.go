package tui

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Semantic color tokens. Render paths reference these, never a raw hex
// literal, so theming and NO_COLOR are handled in one place.
var (
	cAccent  = lipgloss.Color("#A78BFA") // selection, titles
	cInfo    = lipgloss.Color("#38BDF8") // identifiers, counts
	cSuccess = lipgloss.Color("#22C55E") // fresh cache, revealed
	cWarn    = lipgloss.Color("#F59E0B") // production, locked, stale
	cError   = lipgloss.Color("#EF4444") // failures
	cDim     = lipgloss.Color("#9CA3AF") // secondary metadata
	cFocus   = lipgloss.Color("#38BDF8")
)

// styles holds every style used in the render path, built once against the
// terminal's detected color profile.
type styles struct {
	title    lipgloss.Style
	dim      lipgloss.Style
	accent   lipgloss.Style
	info     lipgloss.Style
	success  lipgloss.Style
	warn     lipgloss.Style
	err      lipgloss.Style
	selected lipgloss.Style
	match    lipgloss.Style
	header   lipgloss.Style
	footer   lipgloss.Style
	filter   lipgloss.Style
	modal    lipgloss.Style
	modalErr lipgloss.Style
}

func newStyles() *styles {
	// Rendering to stderr keeps profile detection working when stdout is
	// redirected, and matches where Bubble Tea writes.
	r := lipgloss.NewRenderer(os.Stderr)
	return &styles{
		title:    r.NewStyle().Bold(true).Foreground(cAccent),
		dim:      r.NewStyle().Foreground(cDim),
		accent:   r.NewStyle().Foreground(cAccent),
		info:     r.NewStyle().Foreground(cInfo),
		success:  r.NewStyle().Foreground(cSuccess),
		warn:     r.NewStyle().Foreground(cWarn),
		err:      r.NewStyle().Foreground(cError),
		selected: r.NewStyle().Bold(true).Foreground(cAccent),
		match:    r.NewStyle().Bold(true).Foreground(cInfo),
		header:   r.NewStyle().Bold(true).Foreground(cDim),
		footer:   r.NewStyle().Foreground(cDim),
		filter:   r.NewStyle().Foreground(cInfo),
		modal: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cFocus).
			Padding(0, 1),
		modalErr: r.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(cError).
			Padding(0, 1),
	}
}

// glyphs are swapped for ASCII when the terminal or locale cannot be trusted
// with box-drawing characters (plain SSH sessions, Windows consoles).
type glyphSet struct {
	cursor   string
	bullet   string
	lock     string
	ok       string
	fail     string
	ellipsis string
	arrow    string
	group    string
	spinner  []string
}

var unicodeGlyphs = glyphSet{
	cursor: "▸", bullet: "·", lock: "🔒", ok: "✓", fail: "✗",
	ellipsis: "…", arrow: "→", group: "◆",
	spinner: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
}

var asciiGlyphs = glyphSet{
	cursor: ">", bullet: "-", lock: "[L]", ok: "ok", fail: "!!",
	ellipsis: "...", arrow: "->", group: "[g]",
	spinner: []string{"|", "/", "-", "\\"},
}

func detectGlyphs() glyphSet {
	if os.Getenv("DPX_ASCII") != "" {
		return asciiGlyphs
	}
	for _, v := range []string{os.Getenv("LC_ALL"), os.Getenv("LC_CTYPE"), os.Getenv("LANG")} {
		if v != "" {
			if containsUTF8(v) {
				return unicodeGlyphs
			}
			return asciiGlyphs
		}
	}
	return asciiGlyphs
}

func containsUTF8(s string) bool {
	for i := 0; i+4 <= len(s); i++ {
		switch s[i : i+4] {
		case "UTF-", "utf-", "UTF8", "utf8":
			return true
		}
	}
	return false
}
