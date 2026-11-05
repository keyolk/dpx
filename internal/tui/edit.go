package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/doppler"
)

// edit.go adds writing to the browser: set, add and delete on the secrets
// screen, and a history screen per config that can roll an entry back.
//
// Every write goes through app.Edit, the same path `dpx set` takes, so the TUI
// gets the same guarantees: one log entry per write, conditional on the value
// dpx read, metadata preserved. Every write is also confirmed, and the log id
// it produced is kept so `u` can take it back.

// historyDepth is how many log entries the history screen loads. Each entry
// costs a detail request, so this is a screenful rather than everything.
const historyDepth = 50

// inputKind is what a line of input is for.
type inputKind int

const (
	inputValue inputKind = iota
	inputNewName
)

// input is the one-line editor on the status row.
//
// It is deliberately minimal — append, delete backwards, clear — because what
// it edits is a secret value, where the operation is almost always "replace
// it". A value is masked unless revealed with tab, so editing in a shared
// screen does not put it on the screen as a side effect.
type input struct {
	kind   inputKind
	name   string // the secret being edited
	prompt string
	value  []rune
	shown  bool
}

func (in *input) text() string { return string(in.value) }

// lastWrite is the most recent write made from this session, which `u` on the
// secrets screen undoes.
type lastWrite struct {
	project, config, logID, what string
}

// ---- messages -------------------------------------------------------------

type editMsg struct {
	project, config string
	res             dpxapp.EditResult
	err             error
}

type historyMsg struct {
	project, config string
	logs            []doppler.ConfigLog
	err             error
}

type rollbackMsg struct {
	project, config string
	log             doppler.ConfigLog
	err             error
}

type editRevealMsg struct {
	project, config, name string
	secrets               []doppler.Secret
	err                   error
}

func (m *Model) editCmd(project, config string, sets []dpxapp.Assignment, unsets []string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		res, err := m.app.Edit(m.ctx, project, config, sets, unsets, false)
		return editMsg{project: project, config: config, res: res, err: err}
	})
}

func (m *Model) historyCmd(project, config string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		logs, err := m.app.History(m.ctx, project, config, historyDepth)
		return historyMsg{project: project, config: config, logs: logs, err: err}
	})
}

func (m *Model) rollbackCmd(project, config, id string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		l, err := m.app.Rollback(m.ctx, project, config, id)
		return rollbackMsg{project: project, config: config, log: l, err: err}
	})
}

// editRevealCmd fetches values so an edit can start from the current one.
// It is separate from revealCmd because its result opens an editor instead of
// displaying anything.
func (m *Model) editRevealCmd(project, config, name string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		secrets, err := m.app.RevealSecrets(m.ctx, project, config)
		return editRevealMsg{project: project, config: config, name: name, secrets: secrets, err: err}
	})
}

// handleEditMsg routes the messages this file defines. It returns false for
// anything else.
func (m *Model) handleEditMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case editMsg:
		m.inflight--
		return m.onEdit(msg), true
	case historyMsg:
		m.inflight--
		if msg.err != nil {
			m.fail(msg.err)
			return nil, true
		}
		if m.screen == screenHistory && msg.project == m.curProject && msg.config == m.historyConfig {
			m.historyLogs = msg.logs
			m.rebuildHistory()
		}
		return nil, true
	case rollbackMsg:
		m.inflight--
		if msg.err != nil && msg.log.ID == "" {
			m.fail(msg.err)
			return nil, true
		}
		m.last = &lastWrite{project: msg.project, config: msg.config, logID: msg.log.ID, what: "rollback"}
		m.dropValues()
		if msg.err != nil {
			// The revert landed; only refreshing the names failed.
			m.fail(msg.err)
		} else {
			m.note("rolled back as " + msg.log.ID + "  " + m.gl.bullet + "  u undoes it")
		}
		return m.afterWrite(msg.project, msg.config), true
	case editRevealMsg:
		m.inflight--
		if msg.err != nil {
			m.fail(msg.err)
			return nil, true
		}
		if msg.project != m.curProject || msg.config != m.curConfig || m.screen != screenSecrets {
			return nil, true
		}
		for _, s := range msg.secrets {
			m.revealed[s.Name] = s
		}
		m.startValueInput(msg.name)
		return nil, true
	}
	return nil, false
}

func (m *Model) onEdit(msg editMsg) tea.Cmd {
	if errors.Is(msg.err, dpxapp.ErrStale) {
		m.fail(fmt.Errorf("%w — press r and try again", msg.err))
		return nil
	}
	if msg.err != nil {
		m.fail(msg.err)
		return nil
	}
	if !msg.res.Changed {
		m.note("unchanged — nothing written")
		return nil
	}
	var parts []string
	for _, p := range msg.res.Plan {
		if p.Kind != "=" {
			parts = append(parts, p.Kind+p.Name)
		}
	}
	what := strings.Join(parts, " ")
	m.dropValues()
	if msg.res.LogID != "" {
		m.last = &lastWrite{project: msg.project, config: msg.config, logID: msg.res.LogID, what: what}
		m.note(what + " written  " + m.gl.bullet + "  u undoes it")
	} else {
		m.note(what + " written")
	}
	return m.afterWrite(msg.project, msg.config)
}

// dropValues forgets every fetched value. After a write the ones on screen
// may be the old ones, and showing a value that is no longer stored is worse
// than showing none.
func (m *Model) dropValues() {
	m.revealed = map[string]doppler.Secret{}
	m.revealAll = false
}

// afterWrite redraws whatever screen shows the config that changed.
func (m *Model) afterWrite(project, config string) tea.Cmd {
	if project != m.curProject {
		return nil
	}
	switch {
	case m.screen == screenSecrets && config == m.curConfig:
		m.rebuildSecrets()
	case m.screen == screenHistory && config == m.historyConfig:
		return m.historyCmd(project, config)
	}
	return nil
}

// ---- starting edits -------------------------------------------------------

// beginEdit opens the editor on the selected secret, fetching the values
// first so it starts from the current one.
func (m *Model) beginEdit() (tea.Model, tea.Cmd) {
	r := m.cur().currentRow()
	if m.screen != screenSecrets || r == nil {
		return m, nil
	}
	if isDopplerMeta(r.label) {
		m.note(r.label + " is set by Doppler and cannot be edited")
		return m, nil
	}
	if _, ok := m.revealed[r.label]; ok {
		m.startValueInput(r.label)
		return m, nil
	}
	m.note("fetching " + r.label + " to edit")
	return m, m.editRevealCmd(m.curProject, m.curConfig, r.label)
}

func (m *Model) startValueInput(name string) {
	in := &input{kind: inputValue, name: name, prompt: name + " = "}
	if s, ok := m.revealed[name]; ok && !s.Restricted() {
		// The stored form, not the computed one: editing the computed value
		// of a reference would silently replace the reference with a copy.
		in.value = []rune(s.Raw)
	}
	m.input = in
}

func (m *Model) beginAdd() (tea.Model, tea.Cmd) {
	if m.screen != screenSecrets {
		return m, nil
	}
	m.input = &input{kind: inputNewName, prompt: "new secret: ", shown: true}
	return m, nil
}

func (m *Model) askDelete() (tea.Model, tea.Cmd) {
	r := m.cur().currentRow()
	if m.screen != screenSecrets || r == nil {
		return m, nil
	}
	if isDopplerMeta(r.label) {
		m.note(r.label + " is set by Doppler and cannot be deleted")
		return m, nil
	}
	name, project, config := r.label, m.curProject, m.curConfig
	m.confirm = &confirmation{
		prompt: "delete " + name + " from " + m.whereLabel(config) + "?",
		run: func() tea.Cmd {
			return m.editCmd(project, config, nil, []string{name})
		},
	}
	return m, nil
}

// askUndo stages a rollback of this session's last write.
func (m *Model) askUndo() (tea.Model, tea.Cmd) {
	w := m.last
	if w == nil || w.project != m.curProject || w.config != m.curConfig {
		m.note("nothing written to this config in this session — H for its history")
		return m, nil
	}
	m.confirm = &confirmation{
		prompt: "undo " + w.what + " on " + m.whereLabel(w.config) + "?",
		run: func() tea.Cmd {
			return m.rollbackCmd(w.project, w.config, w.logID)
		},
	}
	return m, nil
}

// whereLabel names a config for a confirmation prompt, with production called
// out: a prompt that reads the same for dev and prd is one that gets answered
// on reflex.
func (m *Model) whereLabel(config string) string {
	where := m.curProject + "/" + config
	if env := m.envOf(config); isProd(env) {
		where += " (" + strings.ToUpper(env) + ")"
	}
	return where
}

func (m *Model) envOf(config string) string {
	for _, c := range m.app.Store.Configs(m.curProject) {
		if c.Name == config {
			return c.Environment
		}
	}
	return ""
}

// ---- the input line -------------------------------------------------------

func (m *Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	in := m.input
	switch msg.Type {
	case tea.KeyEsc:
		m.input = nil
		m.note("cancelled")
		return m, nil
	case tea.KeyEnter:
		return m.submitInput()
	case tea.KeyBackspace:
		if len(in.value) > 0 {
			in.value = in.value[:len(in.value)-1]
		}
		return m, nil
	case tea.KeyCtrlU:
		in.value = nil
		return m, nil
	case tea.KeyCtrlW:
		in.value = []rune(dropWord(in.text()))
		return m, nil
	case tea.KeyTab:
		if in.kind == inputValue {
			in.shown = !in.shown
		}
		return m, nil
	case tea.KeySpace:
		in.value = append(in.value, ' ')
		return m, nil
	case tea.KeyRunes:
		in.value = append(in.value, msg.Runes...)
		return m, nil
	}
	return m, nil
}

func (m *Model) submitInput() (tea.Model, tea.Cmd) {
	in := m.input
	m.input = nil
	project, config := m.curProject, m.curConfig

	if in.kind == inputNewName {
		name := strings.TrimSpace(in.text())
		switch {
		case name == "":
			m.note("cancelled")
			return m, nil
		case strings.ContainsAny(name, " =/"):
			m.fail(fmt.Errorf("%q is not a secret name", name))
			return m, nil
		}
		for _, r := range m.secrets.rows {
			if r.label == name {
				m.fail(fmt.Errorf("%s already exists — e edits it", name))
				return m, nil
			}
		}
		m.input = &input{kind: inputValue, name: name, prompt: name + " = "}
		return m, nil
	}

	value := in.text()
	verb := "set"
	if _, exists := m.revealed[in.name]; !exists {
		verb = "create"
	}
	if s, ok := m.revealed[in.name]; ok && !s.Restricted() && s.Raw == value {
		m.note("unchanged — nothing written")
		return m, nil
	}
	m.confirm = &confirmation{
		prompt: verb + " " + in.name + " on " + m.whereLabel(config) + "?",
		run: func() tea.Cmd {
			return m.editCmd(project, config, []dpxapp.Assignment{{Name: in.name, Value: value}}, nil)
		},
	}
	return m, nil
}

// inputView renders the input line, keeping its end in view: the cursor sits
// at the end, and that is the part being typed.
func (m *Model) inputView() string {
	in := m.input
	text := in.text()
	if !in.shown {
		text = strings.Repeat("•", len(in.value))
	}
	// A pasted newline would break the one-row layout.
	text = strings.ReplaceAll(text, "\n", "⏎")

	prompt := m.st.info.Render(in.prompt)
	cursor := m.st.accent.Render("▌")
	room := m.width - lipgloss.Width(prompt) - 1
	if room < 4 {
		room = 4
	}
	if r := []rune(text); lipgloss.Width(text) > room {
		// Trim from the left until it fits, so wide runes are counted right.
		for len(r) > 0 && lipgloss.Width(string(r))+1 > room {
			r = r[1:]
		}
		text = m.gl.ellipsis + string(r)
	}
	return prompt + text + cursor
}

// ---- history screen -------------------------------------------------------

// openHistory switches to the change history of the config the cursor
// implies: the highlighted one on the config list, the open one below that.
func (m *Model) openHistory() (tea.Model, tea.Cmd) {
	config := m.curConfig
	if m.screen == screenConfigs {
		r := m.cur().currentRow()
		if r == nil {
			return m, nil
		}
		config = r.label
	}
	if config == "" {
		return m, nil
	}
	m.historyBack = m.screen
	m.historyConfig = config
	m.historyLogs = nil
	m.history = list{}
	m.screen = screenHistory
	m.rebuildHistory()
	return m, m.historyCmd(m.curProject, config)
}

func (m *Model) rebuildHistory() {
	rows := make([]row, 0, len(m.historyLogs))
	for _, l := range m.historyLogs {
		meta := []cell{
			{text: l.CreatedAt.Local().Format("01-02 15:04"), style: m.st.dim},
			{text: l.User.Label(), style: m.st.info},
		}
		rows = append(rows, row{label: l.Summary(), meta: meta, dimmed: len(l.Diff) == 0})
	}
	m.history.setRows(rows)
}

func (m *Model) currentLog() *doppler.ConfigLog {
	cur := m.history.current()
	if m.screen != screenHistory || cur == nil || cur.Index >= len(m.historyLogs) {
		return nil
	}
	return &m.historyLogs[cur.Index]
}

// askRollback stages a revert of the highlighted entry.
func (m *Model) askRollback() (tea.Model, tea.Cmd) {
	l := m.currentLog()
	if l == nil {
		return m, nil
	}
	if len(l.Diff) == 0 {
		m.note("this entry changed no secrets — nothing to roll back")
		return m, nil
	}
	var inv []string
	for _, d := range l.Diff {
		inv = append(inv, doppler.Inverse(d.Kind())+d.Name)
	}
	project, config, id := m.curProject, m.historyConfig, l.ID
	m.confirm = &confirmation{
		prompt: "roll back (" + strings.Join(inv, " ") + ") on " + m.whereLabel(config) + "?",
		run: func() tea.Cmd {
			return m.rollbackCmd(project, config, id)
		},
	}
	return m, nil
}
