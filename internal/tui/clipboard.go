package tui

import (
	"fmt"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// copyCurrent puts the selected item on the clipboard.
//
// What gets copied depends on the level, and on the secrets screen it is the
// value when one has been revealed — copying the name of a secret you just
// revealed is never what was wanted, and copying a value that was never
// fetched would be a silent empty paste.
func (m *Model) copyCurrent() (tea.Model, tea.Cmd) {
	r := m.cur().currentRow()
	if r == nil {
		return m, nil
	}

	var text, label string
	switch m.screen {
	case screenProjects:
		text, label = r.label, "project name"
	case screenConfigs:
		text, label = r.label, "config name"
	case screenSecrets:
		if s, ok := m.revealed[r.label]; ok && (m.revealAll || r.label == m.detail) {
			text, label = s.Computed, "value of "+r.label
		} else {
			text, label = r.label, "secret name"
		}
	}
	if text == "" {
		return m, nil
	}

	if err := copyToClipboard(text); err != nil {
		m.fail(fmt.Errorf("copy: %w", err))
		return m, nil
	}
	m.note("copied " + label)
	return m, nil
}

// copyToClipboard shells out to the platform's clipboard tool.
//
// The command runs synchronously because it is a local pipe that returns in
// microseconds; routing it through a tea.Cmd would add a message round-trip
// for no benefit. A missing tool surfaces as an error rather than a silent
// no-op, which is what a "copied!" message over an empty clipboard would be.
func copyToClipboard(s string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if path, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command(path)
		} else if path, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command(path, "-selection", "clipboard")
		} else {
			return fmt.Errorf("no clipboard tool found (install wl-copy or xclip)")
		}
	default:
		return fmt.Errorf("clipboard unsupported on %s", runtime.GOOS)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write([]byte(s)); err != nil {
		stdin.Close()
		return err
	}
	if err := stdin.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}
