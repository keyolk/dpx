package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/keyolk/dpx/internal/dashboard"
)

// openInBrowser opens the dashboard page for whatever the cursor is on.
//
// The target follows the cursor rather than the current screen: on the project
// list that is the highlighted project, not the workplace. Opening the page
// you are looking at is the only reading of "open" that does not require
// checking which level you happen to be on.
//
// The secrets screen is the exception — the dashboard has no per-secret page,
// so it opens the config that contains it.
func (m *Model) openInBrowser() (tea.Model, tea.Cmd) {
	project, config := m.browserTarget()
	u := dashboard.URL(m.app.Cfg.DashboardHost, project, config)

	if err := dashboard.Open(u); err != nil {
		m.fail(fmt.Errorf("open: %w", err))
		return m, nil
	}
	m.note("opened " + describeTarget(project, config))
	return m, nil
}

// browserTarget resolves the cursor position to a project/config pair.
func (m *Model) browserTarget() (project, config string) {
	r := m.cur().currentRow()
	switch m.screen {
	case screenProjects:
		if r != nil {
			return r.label, ""
		}
	case screenConfigs:
		if r != nil {
			return m.curProject, r.label
		}
		return m.curProject, ""
	case screenSecrets:
		return m.curProject, m.curConfig
	}
	return "", ""
}

func describeTarget(project, config string) string {
	switch {
	case project != "" && config != "":
		return project + "/" + config
	case project != "":
		return project
	default:
		return "dashboard"
	}
}
