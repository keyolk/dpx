package doppler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The wire format is what Doppler validates, so the test asserts on the JSON
// the server receives rather than on the Go structs.
func TestApplyChangesSendsChangeRequests(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v3/configs/config/secrets" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		w.Write([]byte(`{"secrets":{}}`))
	}))
	defer srv.Close()

	name, value, orig := "A", "new", "old"
	changes := []Change{
		{Name: "NEW", Value: &value},
		{Name: "A", OriginalName: &name, Value: &value, OriginalValue: &orig, Visibility: "restricted"},
		{Name: "GONE", OriginalName: &name, Delete: true},
	}
	if err := New(srv.URL, "tok").ApplyChanges(context.Background(), "p", "c", changes); err != nil {
		t.Fatal(err)
	}

	if got["project"] != "p" || got["config"] != "c" {
		t.Errorf("project/config = %v/%v", got["project"], got["config"])
	}
	if _, ok := got["secrets"]; ok {
		t.Error("sent both secrets and change_requests; the API takes one")
	}
	reqs := got["change_requests"].([]any)
	create := reqs[0].(map[string]any)
	if v, ok := create["originalName"]; !ok || v != nil {
		t.Errorf("new secret originalName = %v (present %v), want explicit null", v, ok)
	}
	update := reqs[1].(map[string]any)
	if update["originalValue"] != "old" || update["visibility"] != "restricted" {
		t.Errorf("update lost its precondition or visibility: %v", update)
	}
	del := reqs[2].(map[string]any)
	if del["shouldDelete"] != true {
		t.Errorf("delete = %v", del)
	}
	// The API requires value to be present; null means "leave as is".
	if v, ok := del["value"]; !ok || v != nil {
		t.Errorf("delete value = %v (present %v), want explicit null", v, ok)
	}
}

func TestRollbackReturnsNewLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("log") != "L1" {
			t.Errorf("got %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"log":{"id":"L2","rollback":true,"diff":[{"name":"A","added":null,"removed":"x"}]}}`))
	}))
	defer srv.Close()

	l, err := New(srv.URL, "tok").Rollback(context.Background(), "p", "c", "L1")
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "L2" || !l.Rollback || len(l.Diff) != 1 || l.Diff[0].Kind() != "-" {
		t.Errorf("log = %+v", l)
	}
}

func TestLogDiffKind(t *testing.T) {
	v := "x"
	masked, restricted := 0, visRestricted
	cases := []struct {
		d    LogDiff
		want string
	}{
		{LogDiff{Added: &v}, "+"},
		{LogDiff{Removed: &v}, "-"},
		{LogDiff{Added: &v, Removed: &v}, "~"},
		// Shapes observed from the live API: a restricted value is null on
		// its side, and only the visibility says that side existed.
		{LogDiff{RemovedVis: &restricted}, "-"},
		{LogDiff{AddedVis: &restricted}, "+"},
		{LogDiff{Removed: &v, RemovedVis: &masked, AddedVis: &restricted}, "~"},
		{LogDiff{AddedVis: &restricted, RemovedVis: &restricted}, "~"},
	}
	for _, c := range cases {
		if got := c.d.Kind(); got != c.want {
			t.Errorf("Kind(%+v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestStale(t *testing.T) {
	e := &APIError{Status: 400, Message: "The secrets on this page are out of date, please refresh and try again."}
	if !e.Stale() {
		t.Error("Doppler's precondition failure was not recognized")
	}
	if (&APIError{Status: 400, Message: "Invalid secret name"}).Stale() {
		t.Error("an unrelated 400 was taken for a precondition failure")
	}
}

func TestRedactKeepsShape(t *testing.T) {
	old, nu := "old", "new"
	l := ConfigLog{Diff: []LogDiff{
		{Name: "A", Added: &nu, Removed: &old},
		{Name: "B", Added: &nu},
	}}
	r := l.Redacted()
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
