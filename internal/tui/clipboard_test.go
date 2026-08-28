package tui

import (
	"testing"

	"github.com/keyolk/dpx/internal/doppler"
)

// stubClipboard replaces the clipboard writer for one test and returns a
// pointer to whatever was last written.
func stubClipboard(t *testing.T) *string {
	t.Helper()
	var got string
	prev := clipboardWrite
	clipboardWrite = func(s string) error { got = s; return nil }
	t.Cleanup(func() { clipboardWrite = prev })
	return &got
}

func secretsModel(t *testing.T, revealed map[string]doppler.Secret, names ...string) *Model {
	t.Helper()
	m := &Model{
		screen: screenSecrets, st: newStyles(), gl: asciiGlyphs,
		curProject: "app", curConfig: "dev",
		revealed: revealed,
	}
	m.secrets.setRows(rowsOf(names...))
	return m
}

// The two keys must not overlap: `y` copying a value on a revealed row is how
// a token lands in a paste that was meant to be a variable name.
func TestCopyNameNeverCopiesTheValue(t *testing.T) {
	got := stubClipboard(t)
	m := secretsModel(t, map[string]doppler.Secret{
		"TOKEN": {Name: "TOKEN", Raw: "sk-live-xyz", Computed: "sk-live-xyz"},
	}, "TOKEN")
	m.revealAll = true

	m.copyCurrent()

	if *got != "TOKEN" {
		t.Errorf("copied %q, want the name TOKEN", *got)
	}
}

func TestCopyValueCopiesTheRevealedValue(t *testing.T) {
	got := stubClipboard(t)
	m := secretsModel(t, map[string]doppler.Secret{
		"TOKEN": {Name: "TOKEN", Raw: "sk-live-xyz", Computed: "sk-live-xyz"},
	}, "TOKEN")

	m.copyValue()

	if *got != "sk-live-xyz" {
		t.Errorf("copied %q, want the value", *got)
	}
}

// The row and the detail pane both clip to the terminal; the clipboard must
// not — a truncated secret pasted into a config is a silent breakage.
func TestCopyValueCopiesInFullRegardlessOfDisplayWidth(t *testing.T) {
	got := stubClipboard(t)
	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	m := secretsModel(t, map[string]doppler.Secret{
		"BIG": {Name: "BIG", Raw: long, Computed: long},
	}, "BIG")
	m.width, m.height = 80, 24

	m.copyValue()

	if *got != long {
		t.Errorf("copied %d chars, want all %d", len(*got), len(long))
	}
}

// Pressing the value key on an unfetched secret should fetch it and finish the
// copy, not make the user press s first.
func TestCopyValueFetchesThenCopies(t *testing.T) {
	got := stubClipboard(t)
	m := secretsModel(t, map[string]doppler.Secret{}, "TOKEN")

	m.copyValue()
	if m.pendingCopy != "TOKEN" {
		t.Fatalf("pendingCopy = %q, want TOKEN", m.pendingCopy)
	}

	m.revealed["TOKEN"] = doppler.Secret{Name: "TOKEN", Raw: "v", Computed: "v"}
	m.finishPendingCopy()

	if *got != "v" {
		t.Errorf("copied %q, want v", *got)
	}
	if m.pendingCopy != "" {
		t.Error("pendingCopy survived the copy")
	}
}

// A restricted secret has an empty computed value; copying it would be a
// silent empty paste.
func TestCopyValueRefusesRestrictedSecret(t *testing.T) {
	got := stubClipboard(t)
	m := secretsModel(t, map[string]doppler.Secret{
		"LOCKED": {Name: "LOCKED", ComputedVisibility: "restricted"},
	}, "LOCKED")

	m.copyValue()

	if *got != "" {
		t.Errorf("copied %q from a restricted secret", *got)
	}
	if m.errText == "" {
		t.Error("a restricted secret copied silently")
	}
}

// The value key belongs to the secrets screen; on a project list there is no
// value to copy and it must not fall through to copying the name.
func TestCopyValueIsANoOpOutsideSecrets(t *testing.T) {
	got := stubClipboard(t)
	m := &Model{screen: screenProjects, st: newStyles(), gl: asciiGlyphs}
	m.projects.setRows(rowsOf("app-a"))

	m.copyValue()

	if *got != "" {
		t.Errorf("copied %q on the project screen", *got)
	}
}
