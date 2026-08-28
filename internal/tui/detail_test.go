package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/dpx/internal/doppler"
)

func detailModel(t *testing.T, w, h int, s doppler.Secret) *Model {
	t.Helper()
	m := &Model{
		screen: screenSecrets, st: newStyles(), gl: asciiGlyphs,
		width: w, height: h,
		curProject: "app", curConfig: "dev",
		detail:   s.Name,
		revealed: map[string]doppler.Secret{s.Name: s},
	}
	m.secrets.setRows(rowsOf(s.Name))
	return m
}

// The reason the pane grows: a JWT or a connection string cut at one line
// tells you it exists, not what it is.
func TestDetailPaneGrowsToShowALongValue(t *testing.T) {
	long := strings.Repeat("j", 300)
	m := detailModel(t, 100, 40, doppler.Secret{Name: "JWT", Raw: long, Computed: long})

	body := strings.Repeat("\n", m.bodyHeight()-1)
	out := m.secretDetail(body)

	// Strip ANSI by measuring against the raw value in chunks: the value wraps,
	// so assert every chunk survived.
	rendered := stripStyle(out)
	for i := 0; i < 300; i += 50 {
		chunk := long[i : i+50]
		if !strings.Contains(rendered, chunk) {
			t.Fatalf("value truncated at offset %d; the pane must wrap it", i)
		}
	}
}

// The pane may not eat the list it belongs to.
func TestDetailPaneNeverExceedsHalfTheScreen(t *testing.T) {
	long := strings.Repeat("x", 5000)
	m := detailModel(t, 80, 30, doppler.Secret{Name: "HUGE", Raw: long, Computed: long})

	if h := m.detailHeight(); h > 15 {
		t.Errorf("detail height %d on a 30-row screen; want <= 15", h)
	}
	if m.bodyHeight() < 1 {
		t.Error("the list was squeezed out entirely")
	}
}

// A value that does not fit must say so rather than ending mid-string as if
// that were the whole thing.
func TestTruncatedDetailSaysTheValueContinues(t *testing.T) {
	long := strings.Repeat("x", 5000)
	m := detailModel(t, 80, 30, doppler.Secret{Name: "HUGE", Raw: long, Computed: long})

	out := stripStyle(m.secretDetail(""))
	if !strings.Contains(out, "value continues") {
		t.Errorf("a clipped value was presented as complete:\n%s", out)
	}
}

// Short values keep the pane at its floor, so moving the cursor between
// neighbouring secrets does not shift the list boundary under it.
func TestShortValuesKeepThePaneAtItsFloor(t *testing.T) {
	a := detailModel(t, 100, 30, doppler.Secret{Name: "PORT", Raw: "5432", Computed: "5432"})
	b := detailModel(t, 100, 30, doppler.Secret{Name: "HOST", Raw: "db.internal", Computed: "db.internal"})

	if a.detailHeight() != b.detailHeight() {
		t.Errorf("pane height moved between two short secrets: %d vs %d",
			a.detailHeight(), b.detailHeight())
	}
	if a.detailHeight() != detailMin {
		t.Errorf("height = %d, want the floor %d", a.detailHeight(), detailMin)
	}
}

// The pane the layout reserves must be the pane that is drawn; a mismatch
// pushes the status line off the bottom of the terminal.
func TestDetailPaneMatchesReservedHeight(t *testing.T) {
	for _, v := range []string{"short", strings.Repeat("m", 400), strings.Repeat("q", 9000)} {
		m := detailModel(t, 90, 32, doppler.Secret{Name: "V", Raw: v, Computed: v})
		out := m.secretDetail("")
		got := strings.Count(out, "\n") // body("") contributes no newline of its own
		if want := m.detailHeight(); got != want {
			t.Errorf("value of %d chars: pane drew %d rows, layout reserved %d", len(v), got, want)
		}
	}
}

// Every pane row must fit the terminal; a wrapped line that overflows is what
// makes the whole frame jitter.
func TestDetailPaneRowsFitTheWidth(t *testing.T) {
	v := strings.Repeat("z", 400)
	const width = 72
	m := detailModel(t, width, 30, doppler.Secret{Name: "V", Raw: v, Computed: v, Note: "a note"})

	for i, line := range strings.Split(m.secretDetail(""), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("pane row %d is %d cells, want <= %d", i, w, width)
		}
	}
}

// stripStyle removes ANSI escapes so a test can assert on the text.
func stripStyle(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
