package tui

import (
	"fmt"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

// copyCurrent puts the selected item's name on the clipboard.
//
// The name and the value are separate keys rather than one key that guesses:
// `y` copying a name on one row and a secret value on the next — depending on
// whether that row happened to be revealed — is how a token ends up in a
// paste that was meant to be a variable name.
func (m *Model) copyCurrent() (tea.Model, tea.Cmd) {
	r := m.cur().currentRow()
	if r == nil {
		return m, nil
	}
	var label string
	switch m.screen {
	case screenProjects:
		label = "project name"
	case screenConfigs:
		label = "config name"
	default:
		label = "secret name"
	}
	return m, m.copy(r.label, label)
}

// copyValue puts the selected secret's value on the clipboard, fetching it
// first when it has not been revealed. It copies the computed value in full,
// independent of what the row or the detail pane had room to display.
func (m *Model) copyValue() (tea.Model, tea.Cmd) {
	if m.screen != screenSecrets {
		return m, nil
	}
	r := m.cur().currentRow()
	if r == nil {
		return m, nil
	}
	if s, ok := m.revealed[r.label]; ok {
		if s.Restricted() {
			m.fail(fmt.Errorf("copy: %s is restricted — this token may not read its value", r.label))
			return m, nil
		}
		return m, m.copy(s.Computed, "value of "+r.label)
	}
	// Not fetched yet: ask for it and finish the copy when it lands, rather
	// than making the user press s and then y.
	m.pendingCopy = r.label
	m.note("fetching " + r.label + " to copy")
	return m, m.revealCmd(m.curProject, m.curConfig)
}

// finishPendingCopy completes a copy that was waiting on a reveal.
func (m *Model) finishPendingCopy() tea.Cmd {
	name := m.pendingCopy
	m.pendingCopy = ""
	if name == "" {
		return nil
	}
	s, ok := m.revealed[name]
	if !ok {
		m.fail(fmt.Errorf("copy: %s was not returned by the reveal", name))
		return nil
	}
	if s.Restricted() {
		m.fail(fmt.Errorf("copy: %s is restricted — this token may not read its value", name))
		return nil
	}
	return m.copy(s.Computed, "value of "+name)
}

// copy writes text to the clipboard and reports what happened. It returns a
// tea.Cmd only for signature symmetry with the callers; the write itself is a
// local pipe.
func (m *Model) copy(text, label string) tea.Cmd {
	if text == "" {
		m.note(label + " is empty — nothing copied")
		return nil
	}
	if err := clipboardWrite(text); err != nil {
		m.fail(fmt.Errorf("copy: %w", err))
		return nil
	}
	m.note("copied " + label)
	return nil
}

// clipboardWrite is the indirection the tests replace; production always runs
// copyToClipboard.
var clipboardWrite = copyToClipboard

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
