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
			m.pendingCopy = ""
			m.fail(msg.err)
			return m, nil
		}
		// A reveal that lands after the user navigated away is dropped rather
		// than shown: it belongs to a config that is no longer on screen.
		if msg.project != m.curProject || msg.config != m.curConfig {
			m.pendingCopy = ""
			return m, nil
		}
		for _, s := range msg.secrets {
			m.revealed[s.Name] = s
		}
		m.rebuildSecrets()
		if m.pendingCopy != "" {
			return m, m.finishPendingCopy()
		}
		m.note(fmt.Sprintf("revealed %d values", len(msg.secrets)))
		return m, nil

	case membersMsg:
		m.inflight--
		delete(m.loading, "members\x00"+msg.project)
		if msg.err != nil {
			m.fail(msg.err)
			return m, nil
		}
		if m.screen == screenMembers && msg.project == m.curProject {
			m.rebuildMembers()
		}
		return m, nil

	case memberWriteMsg:
		m.inflight--
		if msg.err != nil {
			m.fail(msg.err)
			// The cache was invalidated before the write was known to fail, so
			// refetch either way rather than leaving the screen empty.
			return m, m.loadMembersCmd(msg.project)
		}
		m.note(msg.verb)
		return m, m.loadMembersCmd(msg.project)

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
		m.pendingKey = ""
		return m, nil
	}
	if m.confirm != nil {
		return m.handleConfirmKey(msg)
	}
	if m.roleFor != "" {
		return m.handleRoleKey(msg)
	}
	if m.pendingKey != "" {
		return m.handleChordKey(msg)
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
		// `y` alone does nothing: it opens a chord that names what to copy.
		// A bare copy key that guesses between a variable name and a secret
		// value is how a token lands in the wrong paste.
		m.pendingKey = "y"
		return m, nil
	case "o":
		return m.openInBrowser()
	case "m":
		if m.screen == screenProjects || m.screen == screenConfigs {
			return m.openMembers()
		}
		return m, nil
	case "R":
		if m.screen == screenMembers {
			return m.openRolePicker()
		}
		return m, nil
	case "x":
		if m.screen == screenMembers {
			return m.askRemoveMember()
		}
		return m, nil
	}
	return m, nil
}

// handleConfirmKey answers a staged destructive action. Only an explicit "y"
// proceeds; every other key cancels, so a confirmation cannot be dismissed
// into an accidental yes.
func (m *Model) handleConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.confirm
	m.confirm = nil
	if msg.String() == "y" {
		return m, c.run()
	}
	m.note("cancelled")
	return m, nil
}

// handleRoleKey drives the role picker, which is a list overlaid on the member
// screen rather than a fourth level of the drill-down stack.
func (m *Model) handleRoleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.roleFor = ""
		return m, nil
	case "enter", "l", "right":
		return m.applyRole()
	case "j", "down":
		m.roles.move(1, m.bodyHeight())
	case "k", "up":
		m.roles.move(-1, m.bodyHeight())
	case "g", "home":
		m.roles.top_(m.bodyHeight())
	case "G", "end":
		m.roles.bottom(m.bodyHeight())
	}
	return m, nil
}

// handleChordKey resolves the second key of a chord. Anything unrecognized
// cancels rather than falling through to a top-level binding — a stray key
// after `y` must not scroll the list or quit.
func (m *Model) handleChordKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	prefix := m.pendingKey
	m.pendingKey = ""
	if prefix != "y" {
		return m, nil
	}
	switch msg.String() {
	case "c":
		return m.copyCurrent()
	case "v":
		return m.copyValue()
	case "esc":
		return m, nil
	}
	m.note("y" + msg.String() + " is not a copy target — yc name, yv value")
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
	case screenMembers:
		return &m.members
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
		m.pendingCopy = ""
		m.rebuildSecrets()
		return m, m.loadSecretNamesCmd(m.curProject, m.curConfig)
	case screenMembers:
		// Enter opens the role picker: on a list of grants, "open" means
		// "change this one", and there is no level below a member.
		return m.openRolePicker()
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
	case screenMembers:
		// The member list is opened from two levels, so it returns to the one
		// it came from rather than to a fixed parent.
		m.screen = m.memberBack
		m.memberRows = nil
		if m.screen == screenProjects {
			m.curProject = ""
			m.rebuildProjects()
		} else {
			m.rebuildConfigs()
		}
	case screenSecrets:
		m.screen = screenConfigs
		m.revealed = map[string]doppler.Secret{}
		m.revealAll = false
		m.detail = ""
		m.pendingCopy = ""
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
	case screenMembers:
		delete(m.loading, "members\x00"+m.curProject)
		return m.loadMembersCmd(m.curProject)
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
