package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/cache"
	"github.com/keyolk/dpx/internal/doppler"
)

// editModel sits on the secrets screen of app/<config> with the given values
// already revealed.
func editModel(t *testing.T, config, env string, secrets ...doppler.Secret) *Model {
	t.Helper()
	store := cache.NewStore(t.TempDir()+"/c.json", cache.Empty("w", "wp"))
	store.SetProjectDetail("app", nil, "e",
		[]doppler.Config{{Name: config, Environment: env, Project: "app"}}, "c")
	m := &Model{
		app: &dpxapp.Context{Store: store},
		st:  newStyles(), gl: asciiGlyphs,
		width: 100, height: 30,
		screen:     screenSecrets,
		curProject: "app", curConfig: config, curEnv: env,
		revealed: map[string]doppler.Secret{},
		loading:  map[string]bool{},
	}
	var names []string
	for _, s := range secrets {
		m.revealed[s.Name] = s
		names = append(names, s.Name)
	}
	m.secrets.setRows(rowsOf(names...))
	return m
}

func typeText(m *Model, s string) {
	m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func enter(m *Model) tea.Cmd {
	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	return cmd
}

// Editing a reference by its computed value would replace the reference with
// a frozen copy of what it currently resolves to.
func TestEditStartsFromTheStoredValue(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "URL", Raw: "${HOST}/x", Computed: "db/x"})
	m.handleKey(keyOf("e"))
	if m.input == nil {
		t.Fatal("e did not open the editor")
	}
	if got := m.input.text(); got != "${HOST}/x" {
		t.Errorf("editor starts from %q, want the raw value", got)
	}
}

// The editor owns the keyboard: q is a character, and so is a jamo, which
// must not be rewritten to a Latin shortcut key.
func TestEditorKeepsKeysThatAreShortcutsElsewhere(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "K", Raw: ""})
	m.handleKey(keyOf("e"))
	_, cmd := m.handleKey(keyOf("q"))
	typeText(m, "ㅂ")
	if cmd != nil {
		t.Error("q quit from inside the editor")
	}
	if got := m.input.text(); got != "qㅂ" {
		t.Errorf("editor holds %q, want %q", got, "qㅂ")
	}
}

// A value being edited is masked unless asked for, so editing on a shared
// screen does not display it.
func TestEditorMasksTheValue(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "K", Raw: "hunter2"})
	m.handleKey(keyOf("e"))
	if strings.Contains(stripStyle(m.inputView()), "hunter2") {
		t.Error("value shown while masked")
	}
	m.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(stripStyle(m.inputView()), "hunter2") {
		t.Error("tab did not reveal the value")
	}
}

func TestSubmittingAnUnchangedValueWritesNothing(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "K", Raw: "v"})
	m.handleKey(keyOf("e"))
	if cmd := enter(m); cmd != nil || m.confirm != nil {
		t.Error("an unchanged value was staged for writing")
	}
}

// A write is never one keystroke, and a production one says so.
func TestSubmittingAValueAsksFirstAndNamesProduction(t *testing.T) {
	m := editModel(t, "prd", "prd", doppler.Secret{Name: "K", Raw: "old"})
	m.handleKey(keyOf("e"))
	m.handleKey(tea.KeyMsg{Type: tea.KeyCtrlU})
	typeText(m, "new")
	if cmd := enter(m); cmd != nil {
		t.Fatal("enter wrote without confirmation")
	}
	if m.confirm == nil {
		t.Fatal("no confirmation staged")
	}
	if p := m.confirm.prompt; !strings.Contains(p, "set K on app/prd (PRD)") {
		t.Errorf("prompt %q does not name the production config", p)
	}
	// Any key but y cancels.
	m.handleKey(keyOf("n"))
	if m.confirm != nil {
		t.Error("confirmation survived a no")
	}
}

func TestAddAsksForANameThenAValue(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "EXISTING", Raw: "x"})
	m.handleKey(keyOf("a"))
	typeText(m, "FRESH")
	enter(m)
	if m.input == nil || m.input.kind != inputValue || m.input.name != "FRESH" {
		t.Fatalf("after the name, editor = %+v", m.input)
	}
	typeText(m, "1")
	enter(m)
	if m.confirm == nil || !strings.HasPrefix(m.confirm.prompt, "create FRESH") {
		t.Errorf("confirmation = %+v, want a create", m.confirm)
	}
}

// Adding a name that exists would silently become an edit of it.
func TestAddRefusesAnExistingName(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "EXISTING", Raw: "x"})
	m.handleKey(keyOf("a"))
	typeText(m, "EXISTING")
	enter(m)
	if m.input != nil || m.errText == "" {
		t.Error("an existing name was accepted as new")
	}
}

func TestDopplerVariablesAreNotEditable(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "DOPPLER_CONFIG", Raw: "dev"})
	m.handleKey(keyOf("e"))
	m.handleKey(keyOf("x"))
	if m.input != nil || m.confirm != nil {
		t.Error("a Doppler-injected variable was offered for editing")
	}
}

// u takes back only what this session wrote to the config on screen; another
// config's write is not its to undo.
func TestUndoIsScopedToTheConfigItWrote(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "K"})
	m.handleKey(keyOf("u"))
	if m.confirm != nil {
		t.Error("undo staged with nothing written")
	}

	m.last = &lastWrite{project: "app", config: "stg", logID: "L1", what: "~K"}
	m.handleKey(keyOf("u"))
	if m.confirm != nil {
		t.Error("undo reached across to another config")
	}

	m.last.config = "dev"
	m.handleKey(keyOf("u"))
	if m.confirm == nil || !strings.Contains(m.confirm.prompt, "undo ~K") {
		t.Errorf("confirmation = %+v", m.confirm)
	}
}

// A successful write must not leave the old values on screen.
func TestWriteDropsRevealedValues(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "K", Raw: "old", Computed: "old"})
	m.Update(editMsg{project: "app", config: "dev", res: dpxapp.EditResult{
		Changed: true, LogID: "L9", Plan: []dpxapp.Planned{{Kind: "~", Name: "K"}},
	}})
	if len(m.revealed) != 0 {
		t.Error("stale values survived the write")
	}
	if m.last == nil || m.last.logID != "L9" {
		t.Errorf("last write = %+v, want L9", m.last)
	}
}

func historyModel(t *testing.T, logs ...doppler.ConfigLog) *Model {
	m := editModel(t, "dev", "dev")
	m.screen = screenHistory
	m.historyConfig = "dev"
	m.historyLogs = logs
	m.rebuildHistory()
	return m
}

// Rolling back says what the revert will do, not what the entry did.
func TestRollbackPromptShowsTheInverse(t *testing.T) {
	v := "x"
	m := historyModel(t, doppler.ConfigLog{ID: "L1", Diff: []doppler.LogDiff{{Name: "A", Added: &v}}})
	m.handleKey(keyOf("u"))
	if m.confirm == nil || !strings.Contains(m.confirm.prompt, "roll back (-A)") {
		t.Errorf("confirmation = %+v", m.confirm)
	}
}

func TestRollbackOfANonSecretEntryIsRefused(t *testing.T) {
	m := historyModel(t, doppler.ConfigLog{ID: "L1", Text: "Enabled an integration"})
	m.handleKey(keyOf("u"))
	if m.confirm != nil {
		t.Error("staged a rollback of an entry that changed no secrets")
	}
}

// The history list can be filtered by secret name, since that is the label.
func TestHistoryRowsAreLabelledBySecrets(t *testing.T) {
	v := "x"
	m := historyModel(t,
		doppler.ConfigLog{ID: "L1", Diff: []doppler.LogDiff{{Name: "DB_PASSWORD", Added: &v, Removed: &v}}},
		doppler.ConfigLog{ID: "L2", Diff: []doppler.LogDiff{{Name: "LOG_LEVEL", Added: &v}}},
	)
	m.history.filter = "dbpass"
	m.history.reFilter()
	if len(m.history.matches) != 1 || m.currentLog().ID != "L1" {
		t.Errorf("filter matched %d rows", len(m.history.matches))
	}
}

func TestHistoryReturnsToWhereItWasOpenedFrom(t *testing.T) {
	m := editModel(t, "dev", "dev", doppler.Secret{Name: "K"})
	m.openHistory()
	if m.screen != screenHistory || m.historyConfig != "dev" {
		t.Fatalf("H opened %v for %q", m.screen, m.historyConfig)
	}
	m.inflight = 0
	m.back()
	if m.screen != screenSecrets {
		t.Errorf("returned to %v, want the secrets screen", m.screen)
	}
}
