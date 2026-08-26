package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI serves the endpoints Open and the loaders touch, counting requests
// and payload bytes so the tests can assert on wire behavior rather than on
// internal bookkeeping.
type fakeAPI struct {
	projects  []string
	configs   []string
	requests  atomic.Int64
	bodyBytes atomic.Int64
	etagSeq   atomic.Int64
}

func (f *fakeAPI) serve() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/workplace", func(w http.ResponseWriter, r *http.Request) {
		f.write(w, r, "wp", map[string]any{
			"workplace": map[string]string{"id": "wp1", "name": "Test"}, "success": true,
		})
	})
	mux.HandleFunc("/v3/projects", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)
		var items []map[string]string
		if page == 1 {
			for _, n := range f.projects {
				items = append(items, map[string]string{"id": n, "name": n})
			}
		}
		f.write(w, r, fmt.Sprintf("projects-%d-%d", page, len(f.projects)),
			map[string]any{"projects": items, "success": true})
	})
	mux.HandleFunc("/v3/environments", func(w http.ResponseWriter, r *http.Request) {
		f.write(w, r, "envs", map[string]any{
			"environments": []map[string]any{{"slug": "dev", "name": "Development"}},
			"success":      true,
		})
	})
	mux.HandleFunc("/v3/configs", func(w http.ResponseWriter, r *http.Request) {
		var items []map[string]any
		for _, n := range f.configs {
			items = append(items, map[string]any{"name": n, "environment": "dev", "root": n == "dev"})
		}
		f.write(w, r, fmt.Sprintf("configs-%d", len(f.configs)),
			map[string]any{"configs": items, "success": true})
	})
	mux.HandleFunc("/v3/configs/config/secrets/names", func(w http.ResponseWriter, r *http.Request) {
		f.write(w, r, "names", map[string]any{"names": []string{"A", "B"}, "success": true})
	})
	mux.HandleFunc("/v3/configs/config/secrets", func(w http.ResponseWriter, r *http.Request) {
		f.write(w, r, "", map[string]any{
			"secrets": map[string]any{
				"A": map[string]any{"raw": "1", "computed": "1"},
			},
			"success": true,
		})
	})
	return httptest.NewServer(mux)
}

// write emits the payload, honoring If-None-Match when tag is non-empty.
func (f *fakeAPI) write(w http.ResponseWriter, r *http.Request, tag string, body any) {
	f.requests.Add(1)
	if tag != "" {
		etag := `W/"` + tag + `"`
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
	}
	raw, _ := json.Marshal(body)
	f.bodyBytes.Add(int64(len(raw)))
	w.Write(raw)
}

// openAt builds a Context against the fake API with an isolated cache.
//
// The cache location is redirected through $HOME rather than by rewriting
// Path afterwards, because the Store holds its own copy of the path — a test
// that patched only Context.Path would assert against a file the code never
// writes, and would pass no matter what the code did.
func openAt(t *testing.T, srv *httptest.Server, home string, opts Options) *Context {
	t.Helper()
	t.Setenv("DOPPLER_TOKEN", "dp.test")
	t.Setenv("DOPPLER_API_HOST", srv.URL)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	c, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.HasPrefix(c.Path, home) {
		t.Fatalf("cache path %q escaped the test HOME %q", c.Path, home)
	}
	return c
}

func TestSecretValuesNeverReachDisk(t *testing.T) {
	f := &fakeAPI{projects: []string{"p1"}, configs: []string{"dev"}}
	srv := f.serve()
	defer srv.Close()
	dir := t.TempDir()

	c := openAt(t, srv, dir, Options{DeferFetch: true})
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := c.LoadSecretNames(context.Background(), "p1", "dev"); err != nil {
		t.Fatalf("names: %v", err)
	}
	secrets, err := c.RevealSecrets(context.Background(), "p1", "dev")
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if len(secrets) == 0 {
		t.Fatal("reveal returned nothing to check")
	}
	if err := c.Store.Persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}

	raw, err := os.ReadFile(c.Path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	// The cache must hold the name but never the value; a secrets browser that
	// writes values to disk is a credential store nobody asked for.
	if !contains(raw, "A") {
		t.Error("secret name was not cached")
	}
	if contains(raw, `"computed"`) || contains(raw, `"raw"`) {
		t.Error("cache contains secret value fields")
	}
}

func TestCacheFileIsOwnerOnly(t *testing.T) {
	f := &fakeAPI{projects: []string{"p1"}}
	srv := f.serve()
	defer srv.Close()
	dir := t.TempDir()

	c := openAt(t, srv, dir, Options{DeferFetch: true})
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := c.Store.Persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	info, err := os.Stat(c.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache mode = %o, want 600", perm)
	}
}

// Re-entering a project that has not changed must transfer nothing: the two
// listings are conditional on their stored ETags.
func TestLoadProjectRevalidatesWithoutTransfer(t *testing.T) {
	f := &fakeAPI{projects: []string{"p1"}, configs: []string{"dev", "dev_test"}}
	srv := f.serve()
	defer srv.Close()

	c := openAt(t, srv, t.TempDir(), Options{DeferFetch: true})
	if err := c.LoadProject(context.Background(), "p1"); err != nil {
		t.Fatalf("first load: %v", err)
	}
	before := f.bodyBytes.Load()

	if err := c.LoadProject(context.Background(), "p1"); err != nil {
		t.Fatalf("second load: %v", err)
	}
	if got := f.bodyBytes.Load() - before; got != 0 {
		t.Errorf("re-entering an unchanged project transferred %d bytes, want 0", got)
	}
	if got := len(c.Store.Configs("p1")); got != 2 {
		t.Errorf("configs after revalidation = %d, want 2", got)
	}
}

// --refresh means "do not trust the TTL", not "discard the ETags": throwing
// them away would turn the cheapest refresh into a full re-download.
func TestRefreshStillUsesStoredETags(t *testing.T) {
	f := &fakeAPI{projects: []string{"p1", "p2"}}
	srv := f.serve()
	defer srv.Close()
	dir := t.TempDir()

	c := openAt(t, srv, dir, Options{DeferFetch: true})
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := c.Store.Persist(); err != nil {
		t.Fatalf("persist: %v", err)
	}
	before := f.bodyBytes.Load()

	// A second RefreshProjects on the same store is what `dpx refresh` does
	// after loading the cache.
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if got := f.bodyBytes.Load() - before; got != 0 {
		t.Errorf("refresh transferred %d bytes over an unchanged listing, want 0", got)
	}
}

// A listing that grew must be picked up even though its cached pages 304.
func TestRefreshPicksUpNewProjects(t *testing.T) {
	f := &fakeAPI{projects: []string{"p1"}}
	srv := f.serve()
	defer srv.Close()

	c := openAt(t, srv, t.TempDir(), Options{DeferFetch: true})
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	f.projects = append(f.projects, "p2")
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if got := len(c.Store.Projects()); got != 2 {
		t.Errorf("projects = %d after growth, want 2", got)
	}
}

func TestCacheOnlyWithoutCacheFails(t *testing.T) {
	t.Setenv("DOPPLER_TOKEN", "dp.test")
	t.Setenv("DOPPLER_API_HOST", "http://127.0.0.1:1")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := Open(context.Background(), Options{CacheOnly: true})
	if err == nil {
		t.Fatal("cache-only mode with no cache returned no error")
	}
}

func TestStaleIsReportedNotHidden(t *testing.T) {
	f := &fakeAPI{projects: []string{"p1"}}
	srv := f.serve()
	defer srv.Close()

	c := openAt(t, srv, t.TempDir(), Options{DeferFetch: true, TTL: time.Nanosecond})
	if err := c.RefreshProjects(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if c.Stale {
		t.Error("context reported stale immediately after a refresh")
	}
}

func contains(hay []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(hay); i++ {
		if string(hay[i:i+len(n)]) == needle {
			return true
		}
	}
	return false
}
