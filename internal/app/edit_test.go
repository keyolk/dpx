package app

import (
	"strings"
	"testing"

	"github.com/keyolk/dpx/internal/doppler"
)

func TestPlanChanges(t *testing.T) {
	current := []doppler.Secret{
		{Name: "SAME", Raw: "v", RawVisibility: "masked"},
		{Name: "EDIT", Raw: "old", RawVisibility: "unmasked", RawValueType: doppler.Type{Type: "string"}},
		{Name: "LOCKED", Raw: "", RawVisibility: "restricted"},
		{Name: "DROP", Raw: "x"},
	}
	sets := []Assignment{
		{"SAME", "v"}, {"EDIT", "new"}, {"LOCKED", "s3cret"}, {"FRESH", "1"},
	}
	changes, plan, err := PlanChanges(current, sets, []string{"DROP"})
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, p := range plan {
		kinds = append(kinds, p.Kind+p.Name)
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
	if _, _, err := PlanChanges(nil, nil, []string{"NOPE"}); err == nil {
		t.Error("unset of a missing secret succeeded")
	}
}
