package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/dpx/internal/doppler"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.small = msg.Width < minWidth || msg.Height < minHeight
		return m, nil

	case spinMsg:
		m.spinTick++
		if m.inflight > 0 {
			return m, spinCmd()
		}
		// Nothing outstanding: stop ticking rather than redraw forever.
		return m, nil

	case projectsMsg:
		m.inflight--
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		m.rebuildProjects()
		m.note(fmt.Sprintf("%d projects", len(m.projects.rows)))
		return m, nil

	case projectLoadedMsg:
		m.inflight--
		delete(m.loading, msg.project)
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		// The project list shows a config count per row, so a load that
		// finished for a project the user has since left still updates it.
		m.rebuildProjects()
		if m.screen == screenConfigs && msg.project == m.curProject {
			m.rebuildConfigs()
		}
		return m, nil

	case secretNamesMsg:
		m.inflight--
		delete(m.loading, msg.project+"\x00"+msg.config)
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		if m.screen == screenConfigs && msg.project == m.curProject {
			m.rebuildConfigs()
		}
		if m.screen == screenSecrets && msg.project == m.curProject && msg.config == m.curConfig {
			m.rebuildSecrets()
		}
		return m, nil

	case revealMsg:
		m.inflight--
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		// A reveal that lands after the user navigated away is dropped rather
		// than shown: it belongs to a config that is no longer on screen.
		if msg.project != m.curProject || msg.config != m.curConfig {
			return m, nil
		}
		for _, s := range msg.secrets {
			m.revealed[s.Name] = s
		}
		m.rebuildSecrets()
		m.note(fmt.Sprintf("revealed %d values", len(msg.secrets)))
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		return m.handleFilterKey(msg)
	}
	if m.screen == screenHelp {
		// Help is a dead end by design: every key returns, which is what the
		// footer promises. Trapping the reader behind a specific key is the
		// kind of thing a help screen exists to prevent.
		m.screen = m.helpBack
		return m, nil
	}
	if m.errText != "" {
		// Any key dismisses the error overlay; it is informational, not a
		// prompt, and trapping the user in it would be worse than losing it.
		m.errText = ""
		return m, nil
	}

	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.helpBack = m.screen
		m.screen = screenHelp
		return m, nil
	case "esc", "h", "left":
		return m.back()
	case "enter", "l", "right":
		return m.forward()
	case "j", "down":
		m.cur().move(1, m.bodyHeight())
		return m, m.prefetch()
	case "k", "up":
		m.cur().move(-1, m.bodyHeight())
		return m, m.prefetch()
	case "ctrl+d", "pgdown":
		m.cur().move(m.bodyHeight()/2, m.bodyHeight())
		return m, m.prefetch()
	case "ctrl+u", "pgup":
		m.cur().move(-m.bodyHeight()/2, m.bodyHeight())
		return m, m.prefetch()
	case "g", "home":
		m.cur().top_(m.bodyHeight())
		return m, m.prefetch()
	case "G", "end":
		m.cur().bottom(m.bodyHeight())
		return m, m.prefetch()
	case "/":
		m.filtering = true
		return m, nil
	case "r":
		return m, m.refreshCurrent()
	case "s":
		if m.screen == screenSecrets {
			return m, m.revealCurrent(false)
		}
		return m, nil
	case "S":
		if m.screen == screenSecrets {
			return m, m.revealCurrent(true)
		}
		return m, nil
	case "y":
		return m.copyCurrent()
	}
	return m, nil
}

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

// cur returns the list belonging to the current screen.
func (m *Model) cur() *list {
	switch m.screen {
	case screenConfigs:
		return &m.configs
	case screenSecrets:
		return &m.secrets
	default:
		return &m.projects
	}
}

func (m *Model) forward() (tea.Model, tea.Cmd) {
	r := m.cur().currentRow()
	if r == nil {
		return m, nil
	}
	switch m.screen {
	case screenProjects:
		m.curProject = r.label
		m.screen = screenConfigs
		m.configs = list{}
		m.rebuildConfigs()
		// Entering a project always revalidates it: the ETags make a no-op
		// entry nearly free, and a project someone navigates into is exactly
		// where a stale config list is most misleading.
		return m, m.loadProjectCmd(m.curProject)
	case screenConfigs:
		m.curConfig = r.label
		m.curEnv = r.tag
		m.screen = screenSecrets
		m.secrets = list{}
		// Values never survive leaving a config.
		m.revealed = map[string]doppler.Secret{}
		m.revealAll = false
		m.detail = ""
		m.rebuildSecrets()
		return m, m.loadSecretNamesCmd(m.curProject, m.curConfig)
	case screenSecrets:
		// Enter on a secret toggles its detail pane rather than revealing it;
		// revealing is a separate, deliberate key.
		if m.detail == r.label {
			m.detail = ""
		} else {
			m.detail = r.label
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) back() (tea.Model, tea.Cmd) {
	switch m.screen {
	case screenSecrets:
		m.screen = screenConfigs
		m.revealed = map[string]doppler.Secret{}
		m.revealAll = false
		m.detail = ""
		m.rebuildConfigs()
	case screenConfigs:
		m.screen = screenProjects
		m.rebuildProjects()
	}
	return m, nil
}

// prefetch loads what the cursor is now pointing at, when it has never been
// loaded. Moving through a project list fills each project's config count as
// you pass it; an already-cached project costs nothing because the fetch is
// skipped entirely rather than revalidated.
func (m *Model) prefetch() tea.Cmd {
	r := m.cur().currentRow()
	if r == nil {
		return nil
	}
	switch m.screen {
	case screenProjects:
		if e := m.app.Store.Entry(r.label); !e.Loaded() {
			return m.loadProjectCmd(r.label)
		}
	case screenConfigs:
		if m.app.Store.SecretNames(m.curProject, r.label) == nil {
			return m.loadSecretNamesCmd(m.curProject, r.label)
		}
	}
	return nil
}

// refreshCurrent revalidates whatever the current screen shows.
func (m *Model) refreshCurrent() tea.Cmd {
	switch m.screen {
	case screenProjects:
		return m.refreshProjectsCmd()
	case screenConfigs:
		delete(m.loading, m.curProject)
		return m.loadProjectCmd(m.curProject)
	case screenSecrets:
		delete(m.loading, m.curProject+"\x00"+m.curConfig)
		return m.loadSecretNamesCmd(m.curProject, m.curConfig)
	}
	return nil
}

// revealCurrent fetches values for the current config.
//
// Doppler has no per-secret read endpoint — the config's secrets come as one
// payload — so revealing one name and revealing all cost the same request. The
// difference is what gets displayed: `s` shows only the selected secret, `S`
// shows the whole config.
func (m *Model) revealCurrent(all bool) tea.Cmd {
	if m.cur().currentRow() == nil {
		return nil
	}
	m.revealAll = all
	if !all {
		m.detail = m.cur().currentRow().label
	}
	if len(m.revealed) > 0 {
		// Already fetched for this config; just re-render with the new scope.
		m.rebuildSecrets()
		return nil
	}
	return m.revealCmd(m.curProject, m.curConfig)
}

func (m *Model) note(s string) {
	m.status, m.statusOK, m.errText = s, true, ""
}

func (m *Model) fail(err error) {
	m.errText = err.Error()
	m.status, m.statusOK = "", false
}
