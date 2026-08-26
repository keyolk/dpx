package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func rowsOf(labels ...string) []row {
	out := make([]row, len(labels))
	for i, l := range labels {
		out[i] = row{label: l}
	}
	return out
}

// Losing your place on every keystroke is what makes an incremental filter
// unusable, so the cursor must follow the row it was on when that row survives.
func TestFilterKeepsCursorOnSurvivingRow(t *testing.T) {
	var l list
	l.setRows(rowsOf("svc-db-cron", "svc-db-reporting", "app-frontend"))
	l.cur = 1 // svc-db-reporting

	l.filter = "report"
	l.reFilter()

	if got := l.currentRow(); got == nil || got.label != "svc-db-reporting" {
		t.Fatalf("cursor landed on %v, want svc-db-reporting", got)
	}
}

func TestFilterResetsCursorWhenRowFilteredOut(t *testing.T) {
	var l list
	l.setRows(rowsOf("alpha", "beta", "gamma"))
	l.cur = 1

	l.filter = "gam"
	l.reFilter()

	if got := l.currentRow(); got == nil || got.label != "gamma" {
		t.Fatalf("cursor = %v, want gamma", got)
	}
}

func TestClampScrollsWindowToContainCursor(t *testing.T) {
	var l list
	l.setRows(rowsOf("a", "b", "c", "d", "e", "f", "g", "h"))

	l.bottom(3)
	if l.cur != 7 {
		t.Fatalf("cursor = %d, want 7", l.cur)
	}
	if l.top != 5 {
		t.Errorf("top = %d; the window must end on the cursor (5..7)", l.top)
	}

	l.top_(3)
	if l.cur != 0 || l.top != 0 {
		t.Errorf("cur/top = %d/%d after g, want 0/0", l.cur, l.top)
	}
}

func TestMoveIsClampedAtBothEnds(t *testing.T) {
	var l list
	l.setRows(rowsOf("a", "b"))

	l.move(-5, 10)
	if l.cur != 0 {
		t.Errorf("cur = %d after moving up past the top", l.cur)
	}
	l.move(99, 10)
	if l.cur != 1 {
		t.Errorf("cur = %d after moving down past the bottom", l.cur)
	}
}

func TestEmptyListHasNoCurrentRow(t *testing.T) {
	var l list
	l.setRows(nil)
	if l.currentRow() != nil {
		t.Error("empty list reported a current row")
	}
	l.move(1, 10) // must not panic
}

// Rows are cut by display width, not rune count: a CJK name is two cells per
// rune and a rune-count cut overflows the row.
func TestRenderRespectsCellWidth(t *testing.T) {
	st := newStyles()
	var l list
	l.setRows(rowsOf("프로젝트-데이터베이스-크론-작업", "short"))

	const width = 24
	out := l.render(st, asciiGlyphs, width, 2, true)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d is %d cells wide, want <= %d: %q", i, w, width, line)
		}
	}
}

// The meta column is right-aligned against the terminal edge; a long label
// must not push it past the edge.
func TestRenderKeepsMetaInsideWidth(t *testing.T) {
	st := newStyles()
	var l list
	l.setRows([]row{{label: strings.Repeat("x", 200), meta: "42 cfg"}})

	const width = 40
	out := l.render(st, asciiGlyphs, width, 1, true)
	if w := lipgloss.Width(out); w > width {
		t.Errorf("rendered width %d > %d: %q", w, width, out)
	}
	if !strings.Contains(out, "42 cfg") {
		t.Error("meta column was dropped rather than fitted")
	}
}

func TestDropWordMatchesReadline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"aws chat db", "aws chat "},
		{"aws ", ""},
		{"aws", ""},
		{"", ""},
	} {
		if got := dropWord(tc.in); got != tc.want {
			t.Errorf("dropWord(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
