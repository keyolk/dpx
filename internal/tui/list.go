package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/dpx/internal/fuzzy"
)

// cell is one styled chunk of a row's metadata. Metadata is carried unstyled
// and unclipped so the renderer — the only place that knows the terminal
// width — decides how much of it fits, instead of each row builder guessing a
// fixed budget.
type cell struct {
	text  string
	style lipgloss.Style
}

// row is one renderable line: a primary label that the filter matches
// against, plus trailing metadata chunks.
type row struct {
	label string
	meta  []cell
	// tag is a short leading marker (env slug, "root", …) shown before the
	// label and excluded from filtering, so typing "prd" matches a config
	// named prd rather than every config in the prd environment.
	tag      string
	tagStyle lipgloss.Style
	dimmed   bool
}

func (r *row) hasMeta() bool {
	for _, c := range r.meta {
		if c.text != "" {
			return true
		}
	}
	return false
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
//
// Metadata is laid out in a column that starts just past the longest visible
// label, not flush against the terminal edge. Flush-right leaves a canyon of
// blank cells between a short name and its value on a wide terminal, and it
// forces every value to be pre-truncated to a guessed width; a shared column
// keeps the two readable as a pair and hands whatever is left to the value.
func (l *list) render(st *styles, gl glyphSet, width, height int, focused bool) string {
	if len(l.matches) == 0 {
		if l.filter != "" {
			return st.dim.Render("  no match for " + l.filter)
		}
		return st.dim.Render("  (empty)")
	}
	l.clamp(height)

	end := l.top + height
	if end > len(l.matches) {
		end = len(l.matches)
	}
	labelCol, metaW := l.columns(width, l.top, end)

	var b strings.Builder
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
			tag = r.tagStyle.Render(padRight(r.tag, tagWidth)) + " "
		} else if l.hasTags(l.top, end) {
			tag = strings.Repeat(" ", tagWidth+1)
		}

		label := highlight(st, r.label, m.Positions, labelCol, gl, sel, r.dimmed)
		line := cursor + tag + label
		if metaW > 0 && r.hasMeta() {
			// Pad to the column rather than to the row width: the meta starts
			// at the same offset on every line, which is what makes a column
			// scannable.
			if pad := labelCol - lipgloss.Width(label); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			line += strings.Repeat(" ", metaGutter) + renderMeta(st, gl, r.meta, metaW)
		}
		b.WriteString(line)
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

const (
	// tagWidth is the fixed width of the leading env slug column, so labels
	// start at the same offset whether the slug is "dev" or "stg".
	tagWidth = 4
	// metaGutter separates the label column from the meta column.
	metaGutter = 2
	// metaMin is the width below which a meta column is not worth showing at
	// all: a value clipped to a dozen cells tells you less than nothing.
	metaMin = 12
	// labelMin keeps a name readable even when the values are long.
	labelMin = 12
)

// columns sizes the label and meta columns for the visible window. The label
// column is as wide as the widest visible label, so short names do not drag a
// wide gap behind them, and it yields to the meta column when the labels are
// long enough to squeeze it out.
func (l *list) columns(width, from, to int) (labelCol, metaW int) {
	prefix := 2 // cursor
	if l.hasTags(from, to) {
		prefix += tagWidth + 1
	}
	avail := width - prefix
	if avail < labelMin {
		avail = labelMin
	}

	labelMax, anyMeta := 0, false
	for i := from; i < to; i++ {
		r := l.rows[l.matches[i].Index]
		if w := lipgloss.Width(r.label); w > labelMax {
			labelMax = w
		}
		if r.hasMeta() {
			anyMeta = true
		}
	}
	if !anyMeta {
		return avail, 0
	}

	labelCol = labelMax
	if cap := avail - metaGutter - metaMin; labelCol > cap {
		labelCol = cap
	}
	if labelCol < labelMin {
		labelCol = labelMin
	}
	metaW = avail - labelCol - metaGutter
	if metaW < metaMin {
		// The terminal is too narrow for both; the name wins, since it is what
		// the filter matches and what every other key acts on.
		return avail, 0
	}
	return labelCol, metaW
}

func (l *list) hasTags(from, to int) bool {
	for i := from; i < to; i++ {
		if l.rows[l.matches[i].Index].tag != "" {
			return true
		}
	}
	return false
}

// renderMeta joins the metadata chunks into the meta column, clipping the
// last one that fits rather than dropping the whole column.
func renderMeta(st *styles, gl glyphSet, cells []cell, width int) string {
	var b strings.Builder
	used := 0
	for _, c := range cells {
		if c.text == "" {
			continue
		}
		if used > 0 {
			if used+1 >= width {
				break
			}
			b.WriteString(" ")
			used++
		}
		w := lipgloss.Width(c.text)
		if used+w > width {
			t := truncGlyph(c.text, width-used, gl)
			b.WriteString(c.style.Render(t))
			used = width
			break
		}
		b.WriteString(c.style.Render(c.text))
		used += w
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

// truncGlyph cuts plain text to n cells, ending with the glyph set's ellipsis
// so an ASCII terminal does not get a "…" it cannot draw.
func truncGlyph(s string, n int, gl glyphSet) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	budget := n - lipgloss.Width(gl.ellipsis)
	if budget < 1 {
		// Not even the ellipsis fits; a partial marker reads as content.
		return ""
	}
	w := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > budget {
			return s[:i] + gl.ellipsis
		}
		w += rw
	}
	return s
}
