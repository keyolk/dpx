package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keyolk/dpx/internal/doppler"
)

func TestSplitRef(t *testing.T) {
	cases := []struct {
		args      []string
		n         int
		parts     []string
		rest      []string
		wantError bool
	}{
		{args: []string{"p/c/K"}, n: 3, parts: []string{"p", "c", "K"}},
		{args: []string{"p/c", "K"}, n: 3, parts: []string{"p", "c", "K"}},
		{args: []string{"p", "c", "K"}, n: 3, parts: []string{"p", "c", "K"}},
		{args: []string{"p/c", "A=1", "B"}, n: 2, parts: []string{"p", "c"}, rest: []string{"A=1", "B"}},
		{args: []string{"p", "c", "A=1"}, n: 2, parts: []string{"p", "c"}, rest: []string{"A=1"}},
		{args: []string{"p/c/K/x"}, n: 3, wantError: true},
		{args: []string{"p/c"}, n: 3, wantError: true},
		{args: []string{"p//K"}, n: 3, wantError: true},
		// The config was forgotten and an assignment took its place.
		{args: []string{"p", "A=1"}, n: 2, wantError: true},
	}
	for _, c := range cases {
		parts, rest, err := splitRef(c.args, c.n)
		if c.wantError {
			if err == nil {
				t.Errorf("splitRef(%q) = %q, want error", c.args, parts)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitRef(%q): %v", c.args, err)
			continue
		}
		if !reflect.DeepEqual(parts, c.parts) || len(rest) != len(c.rest) || (len(rest) > 0 && !reflect.DeepEqual(rest, c.rest)) {
			t.Errorf("splitRef(%q) = %q %q, want %q %q", c.args, parts, rest, c.parts, c.rest)
		}
	}
}

func TestPlanChanges(t *testing.T) {
	current := []doppler.Secret{
		{Name: "SAME", Raw: "v", RawVisibility: "masked"},
		{Name: "EDIT", Raw: "old", RawVisibility: "unmasked", RawValueType: doppler.Type{Type: "string"}},
		{Name: "LOCKED", Raw: "", RawVisibility: "restricted"},
		{Name: "DROP", Raw: "x"},
	}
	sets := []assignment{
		{"SAME", "v"}, {"EDIT", "new"}, {"LOCKED", "s3cret"}, {"FRESH", "1"},
	}
	changes, plan, err := planChanges(current, sets, []string{"DROP"})
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, p := range plan {
		kinds = append(kinds, p.kind+p.name)
	}
	if want := "=SAME ~EDIT ~LOCKED +FRESH -DROP"; strings.Join(kinds, " ") != want {
		t.Errorf("plan = %v, want %s", kinds, want)
	}
	// An unchanged value must not reach the API: it would still make a log
	// entry, and history would show a change that changed nothing.
	if len(changes) != 4 {
		t.Fatalf("%d changes, want 4", len(changes))
	}

	edit := changes[0]
	if edit.OriginalValue == nil || *edit.OriginalValue != "old" {
		t.Error("update is not conditional on the value it replaces")
	}
	if edit.Visibility != "unmasked" || edit.ValueType == nil || edit.ValueType.Type != "string" {
		t.Errorf("update does not carry the existing metadata: %+v", edit)
	}
	locked := changes[1]
	if locked.OriginalValue != nil {
		t.Error("restricted secret asserted its withheld (empty) value; every update would be rejected")
	}
	if locked.Visibility != "restricted" {
		t.Errorf("restricted secret visibility = %q", locked.Visibility)
	}
	if fresh := changes[2]; fresh.OriginalName != nil {
		t.Error("new secret has an originalName, which the API reads as a rename")
	}
	if drop := changes[3]; !drop.Delete || drop.Value != nil {
		t.Errorf("delete = %+v", drop)
	}
}

func TestPlanChangesRejectsMissingUnset(t *testing.T) {
	if _, _, err := planChanges(nil, nil, []string{"NOPE"}); err == nil {
		t.Error("unset of a missing secret succeeded")
	}
}

func TestReadValueDropsOneNewline(t *testing.T) {
	cases := map[string]string{
		"v\n":               "v",
		"v":                 "v",
		"v\r\n":             "v",
		"-----END-----\n\n": "-----END-----\n",
		"line1\nline2\n":    "line1\nline2",
	}
	for in, want := range cases {
		got, err := readValue(strings.NewReader(in))
		if err != nil || got != want {
			t.Errorf("readValue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRedactKeepsShape(t *testing.T) {
	old, nu := "old", "new"
	l := doppler.ConfigLog{Diff: []doppler.LogDiff{
		{Name: "A", Added: &nu, Removed: &old},
		{Name: "B", Added: &nu},
	}}
	r := redact(l)
	if *r.Diff[0].Added != "" || *r.Diff[0].Removed != "" {
		t.Error("values survived redaction")
	}
	if r.Diff[0].Kind() != "~" || r.Diff[1].Kind() != "+" {
		t.Error("redaction changed what kind of change it was")
	}
	if *l.Diff[0].Added != "new" {
		t.Error("redaction mutated the original")
	}
}
