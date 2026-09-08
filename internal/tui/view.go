package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// chrome is the number of rows the header, filter line, status line and footer
// consume, leaving the rest to the list body.
const chrome = 4

func (m *Model) View() string {
	if m.width == 0 {
		// Bubble Tea renders once before the first WindowSizeMsg; painting a
		// layout at width 0 produces a burst of garbage in the scrollback.
		return ""
	}
	if m.small {
		return m.st.warn.Render(fmt.Sprintf(
			"terminal too small\nneed %d×%d, have %d×%d",
			minWidth, minHeight, m.width, m.height))
	}
	if m.screen == screenHelp {
		return m.helpView()
	}

	body := m.cur().render(m.st, m.gl, m.width, m.bodyHeight(), true)
	if m.roleFor != "" {
		body = m.rolePickerView()
	}

	// The list is padded to its own height before the detail pane is appended,
	// so the pane sits on the same rows whether the list is full or a filter
	// has cut it to two entries. Without this it floats up under the last row
	// and the whole lower half of the screen moves on every keystroke.
	if pad := m.bodyHeight() - (strings.Count(body, "\n") + 1); pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	if m.screen == screenSecrets && m.detail != "" {
		body = m.secretDetail(body)
	}

	parts := []string{m.headerView(), body, m.statusView(), m.footerView()}
	out := lipgloss.JoinVertical(lipgloss.Left, parts...)
	if m.errText != "" {
		return m.overlay(out, m.st.modalErr.Render(m.wrapErr()))
	}
	return out
}

// bodyHeight is how many list rows fit.
func (m *Model) bodyHeight() int {
	h := m.height - chrome
	if m.screen == screenSecrets && m.detail != "" {
		h -= m.detailHeight()
	}
	if h < 1 {
		return 1
	}
	return h
}

// detailHeight is how many rows the detail pane takes, separator included.
//
// It grows with the value so a long secret is readable in full, and stops at
// half the screen so the list it belongs to never disappears. Below that it
// stays at a floor rather than collapsing per-secret: a pane whose height
// tracked every short value would shift the list on each cursor move.
func (m *Model) detailHeight() int {
	h := len(m.detailLines()) + 1 // separator
	max := m.height / 2
	if max < detailMin {
		max = detailMin
	}
	if h > max {
		h = max
	}
	if h < detailMin {
		h = detailMin
	}
	return h
}

// ---- header ---------------------------------------------------------------

func (m *Model) headerView() string {
	// The breadcrumb is the whole location indicator: the drill-down stack has
	// no persistent panels to show where you are.
	crumbs := []string{m.st.title.Render(m.app.Store.WorkplaceName())}
	if m.curProject != "" && m.screen != screenProjects {
		crumbs = append(crumbs, m.st.accent.Render(m.curProject))
	}
	if m.curConfig != "" && m.screen == screenSecrets {
		crumbs = append(crumbs, m.envStyle(m.curEnv).Render(m.curConfig))
	}
	if m.screen == screenMembers {
		crumbs = append(crumbs, m.st.info.Render("access"))
	}
	left := strings.Join(crumbs, m.st.dim.Render(" / "))

	right := m.st.dim.Render(m.cacheLabel())
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		// Too narrow for both: the location wins, since the cache age is
		// available in the help screen and the location is not.
		return truncStyled(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// cacheLabel reports how the current view was served. It exists because the
// whole point of the ETag cache is invisible otherwise: without it a user
// cannot tell a 3ms cached listing from a fresh fetch, and cannot tell whether
// what they are reading is current.
func (m *Model) cacheLabel() string {
	if m.inflight > 0 {
		frame := m.gl.spinner[m.spinTick%len(m.gl.spinner)]
		return frame + " syncing"
	}
	at := m.app.Store.FetchedAt()
	if at.IsZero() {
		return "no cache"
	}
	return "cached " + shortAge(time.Since(at)) + " ago"
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ---- status & footer ------------------------------------------------------

func (m *Model) statusView() string {
	l := m.cur()
	count := fmt.Sprintf("%d/%d", len(l.matches), len(l.rows))

	var left string
	switch {
	case m.confirm != nil:
		left = m.st.warn.Render(m.confirm.prompt + "  [y/N]")
	case m.roleFor != "":
		left = m.st.info.Render("role for " + m.roleFor)
	case m.filtering:
		left = m.st.filter.Render("/"+l.filter) + m.st.accent.Render("▌")
	case l.filter != "":
		left = m.st.filter.Render("/" + l.filter)
	case m.status != "":
		style := m.st.dim
		if m.statusOK {
			style = m.st.success
		}
		left = style.Render(m.status)
	}

	right := m.st.dim.Render(count)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncStyled(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) footerView() string {
	var hints []string
	switch {
	case m.filtering:
		hints = []string{"type to filter", "↑↓ move", "enter accept", "esc clear"}
	case m.pendingKey == "y":
		// The chord is open: the footer stops advertising everything else and
		// shows only what can come next, so the prefix is never a dead end.
		hints = []string{"yc copy name", "yv copy value", "esc cancel"}
	case m.confirm != nil:
		hints = []string{"y confirm", "any other key cancels"}
	case m.roleFor != "":
		hints = []string{"enter apply", "↑↓ move", "esc cancel"}
	case m.screen == screenMembers:
		hints = []string{"enter role", "x revoke", "esc back", "/ filter", "r refresh", "? help"}
	case m.screen == screenProjects:
		hints = []string{"enter open", "m access", "/ filter", "o browser", "r refresh", "yc copy", "? help"}
	case m.screen == screenConfigs:
		hints = []string{"enter open", "esc back", "m access", "/ filter", "o browser", "yc copy", "? help"}
	case m.screen == screenSecrets:
		hints = []string{"s reveal", "S reveal all", "yc name", "yv value", "enter detail", "esc back", "? help"}
	}
	// Hints are dropped from the right rather than truncated mid-word: half a
	// keybinding is noise, and the list is already ordered by how often each
	// action is needed, so the ones that survive are the useful ones.
	sep := "  " + m.gl.bullet + "  "
	for n := len(hints); n > 0; n-- {
		line := strings.Join(hints[:n], sep)
		if lipgloss.Width(line) <= m.width {
			return m.st.footer.Render(line)
		}
	}
	return ""
}

// ---- secret detail --------------------------------------------------------

// detailMin is the floor for the detail pane. Short values keep it here so the
// list boundary does not jump as the cursor moves between secrets.
const detailMin = 6

// detailLabelW is the width of the "computed"/"raw"/"note" gutter.
const detailLabelW = 9

// detailLines renders the pane's content as one line per screen row, so the
// caller can size the pane by counting them. Wrapping a long value into a
// single string and then trimming by element — which is what a []string of
// blocks invites — is how a value that wraps to five rows silently overflows
// the layout it was measured for.
func (m *Model) detailLines() []string {
	name := m.detail
	w := m.width - detailLabelW
	if w < 20 {
		w = 20
	}

	lines := []string{m.st.header.Render(name)}
	field := func(label string, style lipgloss.Style, text string) {
		for i, l := range strings.Split(wrapCells(text, w, 0), "\n") {
			gutter := label
			if i > 0 {
				gutter = ""
			}
			lines = append(lines, m.st.dim.Render(padRight(gutter, detailLabelW))+style.Render(l))
		}
	}

	s, ok := m.revealed[name]
	switch {
	case !ok:
		lines = append(lines, m.st.dim.Render("value not fetched  "+m.gl.bullet+"  press s to reveal"))
	case s.Restricted():
		// A restricted secret returns an empty string, not an error; saying
		// "restricted" is the difference between that and an empty value.
		lines = append(lines, m.st.warn.Render("restricted — this token may not read the value"))
	default:
		field("computed", m.st.success, s.Computed)
		if s.Referenced() {
			// The stored form differs from the computed one, meaning the value
			// is assembled from references. Showing only the result hides
			// where it actually comes from.
			field("raw", m.st.info, s.Raw)
		}
	}
	if ok && s.Note != "" {
		field("note", m.st.dim, s.Note)
	}
	return lines
}

func (m *Model) secretDetail(body string) string {
	lines := m.detailLines()
	h := m.detailHeight() - 1 // the separator is not one of these rows

	if len(lines) > h {
		// The value is longer than the pane can hold; say so rather than
		// ending mid-value as if that were all of it. `y` copies it whole.
		lines = lines[:h]
		lines[h-1] = m.st.dim.Render("… value continues  " + m.gl.bullet + "  press yv to copy it in full")
	}
	for len(lines) < h {
		lines = append(lines, "")
	}

	sep := m.st.dim.Render(strings.Repeat("─", m.width))
	return body + "\n" + sep + "\n" + strings.Join(lines, "\n")
}

// ---- role picker ----------------------------------------------------------

// rolePickerView draws the role list in place of the member list. It replaces
// rather than overlays because the two are the same shape, and a floating box
// over a list of names is harder to read than the list it is choosing from.
func (m *Model) rolePickerView() string {
	head := m.st.header.Render("role for " + m.roleFor)
	h := m.bodyHeight() - 1
	if h < 1 {
		h = 1
	}
	body := m.roles.render(m.st, m.gl, m.width, h, true)
	if pad := h - (strings.Count(body, "\n") + 1); pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	return head + "\n" + body
}

// ---- help -----------------------------------------------------------------

func (m *Model) helpView() string {
	rows := [][2]string{
		{"j / k, ↓ / ↑", "move"},
		{"ctrl+d / ctrl+u", "half page"},
		{"g / G", "top / bottom"},
		{"enter, l, →", "open (secrets: toggle detail)"},
		{"esc, h, ←", "back"},
		{"/", "filter (fzf-style subsequence)"},
		{"r", "revalidate the current level"},
		{"s", "reveal the selected secret's value"},
		{"S", "reveal every value in the config"},
		{"yc", "copy the name (project / config / secret)"},
		{"yv", "copy the secret's value, fetching it if needed"},
		{"o", "open the dashboard page in a browser"},
		{"m", "who can access this project"},
		{"enter / x", "on access: change role / revoke"},
		{"?", "this help"},
		{"q", "quit / leave help"},
		{"ctrl+c", "quit from anywhere, including the filter"},
	}
	var b strings.Builder
	b.WriteString(m.st.title.Render("dpx — Doppler browser") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + m.st.accent.Render(padRight(r[0], 18)) + m.st.dim.Render(r[1]) + "\n")
	}

	b.WriteString("\n" + m.st.header.Render("caching") + "\n")
	// Explaining the cache here rather than nowhere: the tool's behavior
	// around freshness is otherwise invisible, and "is this current?" is the
	// question a secrets browser must answer honestly.
	for _, line := range []string{
		"Listings are cached on disk and revalidated with ETags, so a refresh",
		"that finds no change transfers nothing. Secret values are fetched on",
		"demand and never written to disk.",
		"",
		m.st.dim.Render("cache: ") + cachePathLabel(m.app.Path),
		m.st.dim.Render("rate:  ") + m.rateLabel(),
	} {
		b.WriteString("  " + line + "\n")
	}
	b.WriteString("\n" + m.st.footer.Render("any key returns"))
	return b.String()
}

func (m *Model) rateLabel() string {
	r := m.app.Client.Rate()
	if r.Limit == 0 {
		return m.st.dim.Render("not yet observed")
	}
	return fmt.Sprintf("%d/%d remaining this window", r.Remaining, r.Limit)
}

func cachePathLabel(p string) string { return p }

// ---- helpers --------------------------------------------------------------

// overlay centers a modal over the rendered screen.
func (m *Model) overlay(base, modal string) string {
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, modal,
		lipgloss.WithWhitespaceChars(" "))
}

func (m *Model) wrapErr() string {
	w := m.width - 8
	if w < 20 {
		w = 20
	}
	return m.st.err.Render("error") + "\n" + wrapCells(m.errText, w, 0) + "\n\n" +
		m.st.dim.Render("any key dismisses")
}

// truncStyled cuts an already-styled string by cell width, leaving the ANSI
// intact — lipgloss's own truncation is ANSI-aware, unlike a byte slice.
func truncStyled(s string, n int) string {
	if lipgloss.Width(s) <= n {
		return s
	}
	return lipgloss.NewStyle().MaxWidth(n).Render(s)
}

// wrapCells hard-wraps plain text at n cells, indenting continuation lines.
func wrapCells(s string, n, indent int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", indent)
	var out []string
	var cur strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > n {
			out = append(out, cur.String())
			cur.Reset()
			w = 0
		}
		cur.WriteRune(r)
		w += rw
	}
	out = append(out, cur.String())
	// Only the first line sits at the caller's column; the rest are indented
	// under it.
	for i := 1; i < len(out); i++ {
		out[i] = pad + out[i]
	}
	return strings.Join(out, "\n")
}
