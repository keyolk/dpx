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

func metaOf(text string) []cell {
	return []cell{{text: text, style: lipgloss.NewStyle()}}
}

// A label long enough to fill the row must not push the meta column past the
// terminal edge.
func TestRenderKeepsMetaInsideWidth(t *testing.T) {
	st := newStyles()
	var l list
	l.setRows([]row{{label: strings.Repeat("x", 200), meta: metaOf("42 cfg")}})

	const width = 40
	out := l.render(st, asciiGlyphs, width, 1, true)
	if w := lipgloss.Width(out); w > width {
		t.Errorf("rendered width %d > %d: %q", w, width, out)
	}
	if !strings.Contains(out, "42 cfg") {
		t.Error("meta column was dropped rather than fitted")
	}
}

// The whole point of the column layout: meta starts just past the longest
// visible label, not flush against the terminal edge, so a short name and its
// value read as a pair instead of sitting at opposite ends of a wide screen.
func TestMetaColumnFollowsLongestLabelNotTerminalEdge(t *testing.T) {
	st := newStyles()
	var l list
	l.setRows([]row{
		{label: "DB_HOST", meta: metaOf("postgres.internal")},
		{label: "PORT", meta: metaOf("5432")},
	})

	const width = 120
	lines := strings.Split(l.render(st, asciiGlyphs, width, 2, true), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}

	col := strings.Index(lines[0], "postgres.internal")
	if col < 0 {
		t.Fatal("value missing from the first row")
	}
	if col > 30 {
		// "  DB_HOST" is 9 cells; anything near the far edge means the meta is
		// still flush-right.
		t.Errorf("meta starts at column %d on a %d-wide terminal; it should follow the label", col, width)
	}
	if got := strings.Index(lines[1], "5432"); got != col {
		t.Errorf("meta columns disagree: %d vs %d — the column must be shared", got, col)
	}
}

// A long value gets whatever the terminal has left, rather than a width the
// row builder guessed. A 60-cell value must survive on a wide terminal.
func TestLongValueUsesAvailableWidth(t *testing.T) {
	st := newStyles()
	value := strings.Repeat("k", 60)
	var l list
	l.setRows([]row{{label: "TOKEN", meta: metaOf(value)}})

	out := l.render(st, asciiGlyphs, 120, 1, true)
	if !strings.Contains(out, value) {
		t.Errorf("60-cell value was clipped on a 120-cell terminal: %q", out)
	}
}

// When the label column cannot be squeezed enough to leave a usable meta
// column, the name wins — a value clipped to a few cells is worse than none.
func TestNarrowTerminalDropsMetaRatherThanTheName(t *testing.T) {
	st := newStyles()
	var l list
	l.setRows([]row{{label: "SOME_LONG_SECRET_NAME", meta: metaOf(strings.Repeat("v", 40))}})

	const width = 26
	out := l.render(st, asciiGlyphs, width, 1, true)
	if w := lipgloss.Width(out); w > width {
		t.Errorf("rendered width %d > %d: %q", w, width, out)
	}
	if !strings.Contains(out, "SOME_LONG") {
		t.Errorf("the name was sacrificed to the value: %q", out)
	}
}

// Rows without a tag still align with tagged ones; a mixed list that shifted
// its labels by four cells per row would be unreadable.
func TestUntaggedRowsAlignWithTaggedOnes(t *testing.T) {
	st := newStyles()
	var l list
	l.setRows([]row{
		{label: "prod", tag: "prd", tagStyle: lipgloss.NewStyle()},
		{label: "misc"},
	})

	lines := strings.Split(l.render(st, asciiGlyphs, 60, 2, true), "\n")
	a, b := strings.Index(lines[0], "prod"), strings.Index(lines[1], "misc")
	if a != b {
		t.Errorf("labels start at %d and %d; tagless rows must be padded to match", a, b)
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
