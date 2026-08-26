package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/dpx/internal/fuzzy"
)

// row is one renderable line: a primary label that the filter matches
// against, plus pre-styled trailing metadata.
type row struct {
	label string
	meta  string
	// tag is a short leading marker (env slug, "root", …) shown before the
	// label and excluded from filtering, so typing "prd" matches a config
	// named prd rather than every config in the prd environment.
	tag      string
	tagStyle lipgloss.Style
	dimmed   bool
}

// list is the scrolling, filterable list used by every level of the browser.
//
// It keeps the filter as state rather than recomputing it per keystroke in the
// view: matches carry the positions that matched, and the view highlights them.
type list struct {
	rows    []row
	filter  string
	matches []fuzzy.Match
	cur     int
	top     int
}

func (l *list) setRows(rows []row) {
	l.rows = rows
	l.reFilter()
}

// reFilter rescores every row and clamps the cursor. The cursor is kept on the
// same underlying row when it survives the new filter — losing your place on
// every keystroke is what makes incremental filters unusable.
func (l *list) reFilter() {
	var keepIdx = -1
	if m := l.current(); m != nil {
		keepIdx = m.Index
	}
	labels := make([]string, len(l.rows))
	for i, r := range l.rows {
		labels[i] = r.label
	}
	l.matches = fuzzy.Filter(labels, l.filter)

	l.cur = 0
	if keepIdx >= 0 {
		for i, m := range l.matches {
			if m.Index == keepIdx {
				l.cur = i
				break
			}
		}
	}
	l.clamp(0)
}

// current returns the selected match, or nil when the list is empty.
func (l *list) current() *fuzzy.Match {
	if l.cur < 0 || l.cur >= len(l.matches) {
		return nil
	}
	return &l.matches[l.cur]
}

// currentRow returns the selected row, or nil.
func (l *list) currentRow() *row {
	m := l.current()
	if m == nil {
		return nil
	}
	return &l.rows[m.Index]
}

func (l *list) move(delta, height int) {
	if len(l.matches) == 0 {
		return
	}
	l.cur += delta
	l.clamp(height)
}

func (l *list) top_(height int) { l.cur = 0; l.clamp(height) }
func (l *list) bottom(height int) {
	l.cur = len(l.matches) - 1
	l.clamp(height)
}

// clamp keeps the cursor in range and scrolls the window to contain it.
func (l *list) clamp(height int) {
	if len(l.matches) == 0 {
		l.cur, l.top = 0, 0
		return
	}
	if l.cur < 0 {
		l.cur = 0
	}
	if l.cur >= len(l.matches) {
		l.cur = len(l.matches) - 1
	}
	if height <= 0 {
		return
	}
	if l.cur < l.top {
		l.top = l.cur
	}
	if l.cur >= l.top+height {
		l.top = l.cur - height + 1
	}
	if max := len(l.matches) - height; l.top > max {
		if max < 0 {
			max = 0
		}
		l.top = max
	}
	if l.top < 0 {
		l.top = 0
	}
}

// render draws the visible window. width is the full body width; height is the
// number of rows available.
func (l *list) render(st *styles, gl glyphSet, width, height int, focused bool) string {
	if len(l.matches) == 0 {
		if l.filter != "" {
			return st.dim.Render("  no match for " + l.filter)
		}
		return st.dim.Render("  (empty)")
	}
	l.clamp(height)

	var b strings.Builder
	end := l.top + height
	if end > len(l.matches) {
		end = len(l.matches)
	}
	for i := l.top; i < end; i++ {
		m := l.matches[i]
		r := l.rows[m.Index]
		sel := i == l.cur && focused

		cursor := "  "
		if sel {
			cursor = st.accent.Render(gl.cursor) + " "
		}

		tag := ""
		if r.tag != "" {
			tag = r.tagStyle.Render(padRight(r.tag, 4)) + " "
		}

		// The meta column is right-aligned against the terminal edge, so the
		// label's budget is whatever remains after the fixed-width parts.
		metaW := lipgloss.Width(r.meta)
		if metaW > 0 {
			metaW++ // separating space
		}
		labelW := width - lipgloss.Width(cursor) - lipgloss.Width(tag) - metaW
		if labelW < 8 {
			labelW = 8
		}

		label := highlight(st, r.label, m.Positions, labelW, gl, sel, r.dimmed)
		line := cursor + tag + label
		if r.meta != "" {
			gap := width - lipgloss.Width(line) - lipgloss.Width(r.meta)
			if gap < 1 {
				gap = 1
			}
			line += strings.Repeat(" ", gap) + r.meta
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// highlight renders a label with the matched runes emphasized, truncating to
// width. Truncation happens before styling so that ANSI sequences never count
// toward the cell budget.
func highlight(st *styles, label string, positions []int, width int, gl glyphSet, sel, dimmed bool) string {
	runes := []rune(label)
	truncated := false
	if lipgloss.Width(label) > width {
		// Cut by cell width, not rune count: a CJK project name is two cells
		// per rune and a rune-count cut would overflow the row.
		budget := width - lipgloss.Width(gl.ellipsis)
		if budget < 1 {
			budget = 1
		}
		w := 0
		cut := len(runes)
		for i, r := range runes {
			rw := lipgloss.Width(string(r))
			if w+rw > budget {
				cut = i
				break
			}
			w += rw
		}
		runes = runes[:cut]
		truncated = true
	}

	inMatch := make([]bool, len(runes))
	for _, p := range positions {
		if p >= 0 && p < len(runes) {
			inMatch[p] = true
		}
	}

	base := st.dim
	switch {
	case sel:
		base = st.selected
	case !dimmed:
		base = lipgloss.NewStyle()
	}

	var b strings.Builder
	for i, r := range runes {
		if inMatch[i] {
			b.WriteString(st.match.Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	if truncated {
		b.WriteString(st.dim.Render(gl.ellipsis))
	}
	return b.String()
}

// padRight pads to n cells, measured by display width.
func padRight(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}
