package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/keyolk/dpx/internal/doppler"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	snap := Empty("wp1", "Acme")
	st := NewStore(path, snap)
	st.SetProjects([]doppler.Page[doppler.Project]{{
		ETag:  `W/"abc"`,
		Items: []doppler.Project{{Name: "b"}, {Name: "a"}},
	}})
	st.SetSecretNames("a", "dev", []string{"TOKEN"}, `W/"n"`)
	if err := st.Persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil for a cache just written")
	}
	if got.WorkplaceName != "Acme" {
		t.Errorf("workplace = %q", got.WorkplaceName)
	}
	if sn := NewStore(path, got).SecretNames("a", "dev"); sn == nil || len(sn.Names) != 1 {
		t.Errorf("secret names did not survive the round trip: %+v", sn)
	}
}

// A cache from an older dpx must not be parsed into the current shape — the
// fields that moved would read back as zero values, which is worse than a cold
// start because it looks like real data.
func TestLoadRejectsForeignVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := save(path, &Snapshot{Version: Version + 1, WorkplaceName: "old"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got != nil {
		t.Errorf("load returned a snapshot for version %d, want a cold start", Version+1)
	}
}

func TestLoadTreatsCorruptCacheAsColdStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := writeFile(path, "{not json"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Errorf("corrupt cache returned an error (%v); it should be a cold start", err)
	}
	if got != nil {
		t.Error("corrupt cache produced a snapshot")
	}
}

func TestLoadMissingCacheIsNotAnError(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || got != nil {
		t.Errorf("Load(missing) = %v, %v; want nil, nil", got, err)
	}
}

// Configs must come back grouped by the project's own environment order with
// each environment's root config first — the API returns creation order, which
// interleaves branches with roots.
func TestConfigsSortByEnvironmentThenRoot(t *testing.T) {
	st := NewStore("", Empty("wp", "w"))
	st.SetProjectDetail("p",
		[]doppler.Environment{{Slug: "dev"}, {Slug: "stg"}, {Slug: "prd"}}, "e",
		[]doppler.Config{
			{Name: "prd", Environment: "prd", Root: true},
			{Name: "dev_test", Environment: "dev"},
			{Name: "stg", Environment: "stg", Root: true},
			{Name: "dev", Environment: "dev", Root: true},
		}, "c")

	var got []string
	for _, c := range st.Configs("p") {
		got = append(got, c.Name)
	}
	want := []string{"dev", "dev_test", "stg", "prd"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("config order = %v, want %v", got, want)
		}
	}
}

// A 304 arrives as nil items: the entry must keep what it had rather than be
// blanked, or a revalidation would empty the cache it was meant to confirm.
func TestSetProjectDetailKeepsDataOnNotModified(t *testing.T) {
	st := NewStore("", Empty("wp", "w"))
	st.SetProjectDetail("p",
		[]doppler.Environment{{Slug: "dev"}}, "e1",
		[]doppler.Config{{Name: "dev", Environment: "dev"}}, "c1")

	st.SetProjectDetail("p", nil, "e1", nil, "c1")

	if got := st.Configs("p"); len(got) != 1 {
		t.Errorf("configs = %v after revalidation, want the cached one kept", got)
	}
	if got := st.Envs("p"); len(got) != 1 {
		t.Errorf("envs = %v after revalidation, want the cached one kept", got)
	}
}

// Projects() hands its result to the render path; returning the stored slice
// would let a concurrent fetch reallocate under a view that is iterating it.
func TestProjectsReturnsIndependentSlice(t *testing.T) {
	st := NewStore("", Empty("wp", "w"))
	st.SetProjects([]doppler.Page[doppler.Project]{{Items: []doppler.Project{{Name: "a"}, {Name: "b"}}}})

	got := st.Projects()
	got[0].Name = "mutated"
	if again := st.Projects(); again[0].Name != "a" {
		t.Errorf("mutating the returned slice changed the store: %q", again[0].Name)
	}
}

func writeFile(path, s string) error {
	return os.WriteFile(path, []byte(s), 0o600)
}
