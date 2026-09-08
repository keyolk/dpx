package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/cache"
)

// keymapModel is newTestModel plus the store the navigation keys reach through
// prefetch, so a key can be driven end to end without a live API client.
func keymapModel(t *testing.T, labels ...string) *Model {
	t.Helper()
	m := newTestModel(screenProjects, "", "", rowsOf(labels...))
	m.app = &dpxapp.Context{
		Store: cache.NewStore(t.TempDir()+"/cache.json", cache.Empty("w", "workplace")),
	}
	m.loading = map[string]bool{}
	return m
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// isQuit reports whether a returned command is tea.Quit, by running it and
// checking for the QuitMsg. tea.Quit is a plain func, so it cannot be compared
// directly.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestNormalizeCJKKeyMapsJamoByPhysicalPosition(t *testing.T) {
	for jamo, want := range map[string]string{
		"ㅂ": "q", "ㅁ": "a", "ㅋ": "z", "ㅓ": "j", "ㅏ": "k",
		"ㅃ": "Q", "ㄲ": "R",
	} {
		if got := normalizeCJKKey(keyMsg(jamo)).String(); got != want {
			t.Errorf("normalizeCJKKey(%q) = %q, want %q", jamo, got, want)
		}
	}
}

func TestNormalizeCJKKeyLeavesEverythingElseAlone(t *testing.T) {
	// Latin keys, digits, and composed syllables pass through: a composed
	// syllable only reaches the TUI as committed text, never as a shortcut.
	for _, k := range []string{"q", "R", "0", "가"} {
		if got := normalizeCJKKey(keyMsg(k)).String(); got != k {
			t.Errorf("normalizeCJKKey(%q) = %q, want it unchanged", k, got)
		}
	}
	for _, in := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyCtrlC}, {Type: tea.KeyEsc}} {
		if got := normalizeCJKKey(in); got.String() != in.String() {
			t.Errorf("normalizeCJKKey(%q) = %q, want it unchanged", in.String(), got.String())
		}
	}
}

func TestNormalizeCJKKeyLeavesPasteAndAltAlone(t *testing.T) {
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'ㅂ'}, Paste: true}
	if got := normalizeCJKKey(paste); got.String() != paste.String() {
		t.Errorf("pasted jamo was rewritten to %q", got.String())
	}
	alt := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'ㅂ'}, Alt: true}
	if got := normalizeCJKKey(alt); got.String() != alt.String() {
		t.Errorf("alt chord was rewritten to %q", got.String())
	}
}

// Under a Korean input source every shortcut arrives as a jamo. They have to
// map back, or the browser is unusable until the input source is switched.
func TestHangulShortcutsFireLikeLatin(t *testing.T) {
	t.Run("navigation", func(t *testing.T) {
		m := keymapModel(t, "app-a", "app-b")
		// `ㅓ` is the physical `j`, `ㅏ` the physical `k`.
		m.handleKey(keyMsg("ㅓ"))
		if m.cur().cur != 1 {
			t.Fatalf("cursor = %d after ㅓ; want 1", m.cur().cur)
		}
		m.handleKey(keyMsg("ㅏ"))
		if m.cur().cur != 0 {
			t.Fatalf("cursor = %d after ㅏ; want 0", m.cur().cur)
		}
	})
	t.Run("jump to top", func(t *testing.T) {
		// `ㅎ` is the physical `g`, which jumps to the top of the list.
		m := keymapModel(t, "a", "b", "c")
		m.cur().cur = 2
		m.handleKey(keyMsg("ㅎ"))
		if m.cur().cur != 0 {
			t.Fatalf("cursor = %d after ㅎ (physical g); want 0", m.cur().cur)
		}
	})
	t.Run("quit", func(t *testing.T) {
		// `ㅂ` sits on the physical `q` key.
		m := keymapModel(t)
		if _, cmd := m.handleKey(keyMsg("ㅂ")); !isQuit(cmd) {
			t.Fatal("ㅂ (physical q) did not quit")
		}
	})
}

// A jamo typed into the filter is the query — a Korean config name would
// otherwise be unsearchable.
func TestFilterKeepsHangulVerbatim(t *testing.T) {
	m := keymapModel(t, "app-a")
	m.handleKey(keyMsg("/"))
	if !m.filtering {
		t.Fatal("/ did not open the filter")
	}
	m.handleKey(keyMsg("ㅂ"))
	if got := m.cur().filter; got != "ㅂ" {
		t.Fatalf("filter = %q, want the jamo verbatim", got)
	}
}

// --- ctrl+c ------------------------------------------------------------------

// ctrl+c was not bound at all, so the reflex that kills every other CLI did
// nothing and the filter box had no exit but its own bindings.
func TestCtrlCQuitsFromEveryState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Model)
	}{
		{"project list", func(*Model) {}},
		{"filtering", func(m *Model) { m.filtering = true }},
		{"help screen", func(m *Model) { m.screen = screenHelp }},
		{"error overlay", func(m *Model) { m.errText = "boom" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := keymapModel(t, "app-a")
			tc.setup(m)
			if _, cmd := m.handleKey(keyMsg("ctrl+c")); !isQuit(cmd) {
				t.Fatalf("ctrl+c did not quit from %s", tc.name)
			}
		})
	}
}
