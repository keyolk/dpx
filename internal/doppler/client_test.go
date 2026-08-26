package doppler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// projectServer serves a paged project listing that honors If-None-Match, so
// the tests can assert on what actually crosses the wire rather than on the
// client's own bookkeeping.
type projectServer struct {
	pages [][]string
	// bodyBytes counts the payload the server actually wrote, which is the
	// number the cache exists to keep at zero.
	bodyBytes atomic.Int64
	requests  atomic.Int64
}

// etag is derived from the page's contents, as a real one is — an ETag keyed
// only on the page number would report "unchanged" for a page whose contents
// changed, which is the bug this test exists to catch.
func (ps *projectServer) etag(page int) string {
	var names []string
	if page-1 < len(ps.pages) {
		names = ps.pages[page-1]
	}
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return fmt.Sprintf(`W/"%x"`, sum[:8])
}

func (ps *projectServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.requests.Add(1)
		page := 1
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &page)

		var names []string
		if page-1 < len(ps.pages) {
			names = ps.pages[page-1]
		}
		tag := ps.etag(page)
		if r.Header.Get("If-None-Match") == tag && len(names) > 0 {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		projects := make([]Project, len(names))
		for i, n := range names {
			projects[i] = Project{ID: n, Name: n}
		}
		body, _ := json.Marshal(map[string]any{"projects": projects, "success": true})
		ps.bodyBytes.Add(int64(len(body)))
		w.Header().Set("ETag", tag)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	})
}

func names(n, offset int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("proj-%04d", offset+i)
	}
	return out
}

func TestProjectsRevalidatesWithoutTransfer(t *testing.T) {
	ps := &projectServer{pages: [][]string{names(perPage, 0), names(7, perPage)}}
	srv := httptest.NewServer(ps.handler())
	defer srv.Close()
	c := New(srv.URL, "tok")

	first, err := c.Projects(context.Background(), nil)
	if err != nil {
		t.Fatalf("cold fetch: %v", err)
	}
	if got := len(Flatten(first)); got != perPage+7 {
		t.Fatalf("cold fetch returned %d projects, want %d", got, perPage+7)
	}
	cold := ps.bodyBytes.Load()
	if cold == 0 {
		t.Fatal("cold fetch transferred nothing")
	}

	// The whole point of the ETag cache: a refresh that finds no change must
	// move no payload at all.
	second, err := c.Projects(context.Background(), first)
	if err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	if warm := ps.bodyBytes.Load() - cold; warm != 0 {
		t.Errorf("revalidation transferred %d bytes, want 0", warm)
	}
	if got := len(Flatten(second)); got != perPage+7 {
		t.Errorf("revalidation returned %d projects, want %d", got, perPage+7)
	}
}

// A 304 on a full page must not end the listing: the page after it still has
// to be checked, or a growing workplace silently loses its tail.
func TestProjectsFollowsPageAfterNotModified(t *testing.T) {
	ps := &projectServer{pages: [][]string{names(perPage, 0), names(3, perPage)}}
	srv := httptest.NewServer(ps.handler())
	defer srv.Close()
	c := New(srv.URL, "tok")

	first, _ := c.Projects(context.Background(), nil)

	// The second page grows while the first stays byte-identical.
	ps.pages[1] = names(9, perPage)
	second, err := c.Projects(context.Background(), first)
	if err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	if got := len(Flatten(second)); got != perPage+9 {
		t.Fatalf("got %d projects after growth, want %d", got, perPage+9)
	}
	if second[0].ETag != ps.etag(1) {
		t.Errorf("page 1 lost its etag: %q", second[0].ETag)
	}
}

// A short cached page ends the listing without a probe request, which is what
// keeps a warm refresh at exactly one request per cached page.
func TestProjectsShortCachedPageStopsListing(t *testing.T) {
	ps := &projectServer{pages: [][]string{names(4, 0)}}
	srv := httptest.NewServer(ps.handler())
	defer srv.Close()
	c := New(srv.URL, "tok")

	first, _ := c.Projects(context.Background(), nil)
	before := ps.requests.Load()

	if _, err := c.Projects(context.Background(), first); err != nil {
		t.Fatalf("revalidate: %v", err)
	}
	if got := ps.requests.Load() - before; got != 1 {
		t.Errorf("warm refresh made %d requests, want 1", got)
	}
}

func TestAPIErrorCarriesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"messages":["Invalid Auth Token"],"success":false}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "bad").Projects(context.Background(), nil)
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("got %T, want *APIError", err)
	}
	if !apiErr.Unauthorized() {
		t.Error("401 not reported as unauthorized")
	}
	if apiErr.Message != "Invalid Auth Token" {
		t.Errorf("message = %q, want the API's own text", apiErr.Message)
	}
}

func asAPIError(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}
	return ok
}

func TestRateStateTracksHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("x-ratelimit-limit", "960")
		w.Header().Set("x-ratelimit-remaining", "917")
		w.Header().Set("x-ratelimit-reset", "1787736100")
		w.Write([]byte(`{"projects":[],"success":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if _, err := c.Projects(context.Background(), nil); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if r := c.Rate(); r.Limit != 960 || r.Remaining != 917 {
		t.Errorf("rate = %+v, want limit 960 remaining 917", r)
	}
}
