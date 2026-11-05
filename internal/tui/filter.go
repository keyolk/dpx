package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	l := m.cur()
	switch msg.String() {
	case "esc":
		m.filtering = false
		l.filter = ""
		l.reFilter()
		return m, nil
	case "enter":
		m.filtering = false
		return m, nil
	case "backspace":
		if r := []rune(l.filter); len(r) > 0 {
			l.filter = string(r[:len(r)-1])
			l.reFilter()
		}
		return m, nil
	case "ctrl+w":
		l.filter = dropWord(l.filter)
		l.reFilter()
		return m, nil
	case "up", "down":
		// Arrows navigate without leaving the filter, so a match can be picked
		// without retyping.
		d := 1
		if msg.String() == "up" {
			d = -1
		}
		l.move(d, m.bodyHeight())
		return m, nil
	}
	if msg.Type == tea.KeyRunes {
		l.filter += string(msg.Runes)
		l.reFilter()
		return m, m.prefetch()
	}
	if msg.Type == tea.KeySpace {
		l.filter += " "
		l.reFilter()
	}
	return m, nil
}

// dropWord removes the trailing word, matching readline's ctrl+w.
func dropWord(s string) string {
	s = strings.TrimRight(s, " ")
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[:i+1]
	}
	return ""
}
