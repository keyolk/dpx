// Package cache keeps a local, revalidatable snapshot of the workplace's
// projects, environments and configs.
//
// Two properties drive the design:
//
// Doppler rate-limits reads (960/min for listings, 480/min for secrets), and
// a workplace of several hundred projects is as many config listings — enough to burn most of
// a minute's budget on a single cold walk. So the walk happens once and the
// result is kept.
//
// Every endpoint dpx reads returns a weak ETag and honors If-None-Match, so a
// refresh is a revalidation sweep, not a re-download: unchanged entries cost a
// 304 with no body. That is why entries store their ETag next to their data
// rather than relying on a TTL, which would either expire too early to help or
// serve data known to be stale.
//
// Secret *values* are never persisted. Only names are cached — a name list is
// what the browser renders, and a values file on disk is a credential store
// this tool has no business creating.
package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/keyolk/dpx/internal/doppler"
)

// Version is bumped when the on-disk shape changes; a mismatch is treated as a
// cold start rather than a parse error.
const Version = 2

// Snapshot is the whole local view of one workplace, as persisted.
type Snapshot struct {
	Version       int    `json:"version"`
	WorkplaceID   string `json:"workplaceId"`
	WorkplaceName string `json:"workplaceName"`

	FetchedAt time.Time `json:"fetchedAt"`

	// ProjectPages keeps the paged listing with per-page ETags.
	ProjectPages []doppler.Page[doppler.Project] `json:"projectPages"`

	// Projects is keyed by project name.
	Projects map[string]*ProjectEntry `json:"projects"`

	// UserPages resolves member slugs to names. It is workplace-wide rather
	// than per-project because the same people recur across every project.
	UserPages []doppler.Page[doppler.WorkplaceUser] `json:"userPages,omitempty"`
	// RoleETag/Roles are the project roles the workplace defines, including
	// custom ones, so the role picker offers what exists rather than a
	// hardcoded set.
	RoleETag string                `json:"roleEtag,omitempty"`
	Roles    []doppler.ProjectRole `json:"roles,omitempty"`
	// GroupETag/Groups resolve group members, which hold project access
	// alongside individual users.
	GroupETag string          `json:"groupEtag,omitempty"`
	Groups    []doppler.Group `json:"groups,omitempty"`
}

// ProjectEntry is one project's lazily-filled detail.
//
// A project is only expanded when someone navigates into it, so most entries
// stay nil-filled after a cold walk. That keeps the first run to one listing
// request instead of 400.
type ProjectEntry struct {
	EnvETag  string                `json:"envEtag,omitempty"`
	Envs     []doppler.Environment `json:"envs,omitempty"`
	CfgETag  string                `json:"cfgEtag,omitempty"`
	Configs  []doppler.Config      `json:"configs,omitempty"`
	LoadedAt time.Time             `json:"loadedAt,omitempty"`

	// Secrets is keyed by config name and holds names only, never values.
	Secrets map[string]*SecretNames `json:"secrets,omitempty"`

	// Members is who can reach this project. Cached like every other listing:
	// it is an access grant, not a credential.
	MemberETag string           `json:"memberEtag,omitempty"`
	Members    []doppler.Member `json:"members,omitempty"`
	MembersAt  time.Time        `json:"membersAt,omitempty"`
}

// MembersLoaded reports whether the member list has been fetched at least once.
func (e *ProjectEntry) MembersLoaded() bool { return e != nil && !e.MembersAt.IsZero() }

// SecretNames is one config's cached name list.
type SecretNames struct {
	ETag     string    `json:"etag"`
	Names    []string  `json:"names"`
	LoadedAt time.Time `json:"loadedAt"`
}

// Loaded reports whether a project's configs have been walked at least once.
func (e *ProjectEntry) Loaded() bool { return e != nil && !e.LoadedAt.IsZero() }

// Empty returns an initialized snapshot for a cold start.
func Empty(workplaceID, workplaceName string) *Snapshot {
	return &Snapshot{
		Version:       Version,
		WorkplaceID:   workplaceID,
		WorkplaceName: workplaceName,
		Projects:      map[string]*ProjectEntry{},
	}
}

// Age reports how stale the project listing is.
func (s *Snapshot) Age() time.Duration {
	if s.FetchedAt.IsZero() {
		return 1<<62 - 1
	}
	return time.Since(s.FetchedAt)
}

// ---- concurrent access ----------------------------------------------------

// Store wraps a Snapshot with the locking the TUI needs: background fetches
// write while the render path reads.
//
// Every accessor returns copies of slices it hands out. A view that holds a
// slice into the snapshot would race with the fetch that replaces it, and the
// symptom — a row list that changes length between the bounds check and the
// index — is exactly the kind of panic that only shows up under a real
// refresh.
type Store struct {
	mu    sync.RWMutex
	snap  *Snapshot
	path  string
	dirty bool
}

// NewStore wraps a snapshot.
func NewStore(path string, snap *Snapshot) *Store {
	return &Store{path: path, snap: snap}
}

// Snapshot returns the underlying snapshot. Callers must not mutate it; it
// exists for whole-snapshot operations like Save.
func (st *Store) Snapshot() *Snapshot {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap
}

// WorkplaceName returns the workplace label.
func (st *Store) WorkplaceName() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.WorkplaceName
}

// FetchedAt returns when the project listing was last confirmed.
func (st *Store) FetchedAt() time.Time {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.FetchedAt
}

// Projects returns every project, sorted by name.
func (st *Store) Projects() []doppler.Project {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := doppler.Flatten(st.snap.ProjectPages)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ProjectPages returns the paged listing for a revalidation sweep.
func (st *Store) ProjectPages() []doppler.Page[doppler.Project] {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.ProjectPages
}

// SetProjects replaces the project listing.
func (st *Store) SetProjects(pages []doppler.Page[doppler.Project]) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.snap.ProjectPages = pages
	st.snap.FetchedAt = time.Now()
	st.dirty = true
}

// Entry returns a project's cached detail, or nil when it was never expanded.
func (st *Store) Entry(project string) *ProjectEntry {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.Projects[project]
}

// Configs returns a project's configs, sorted so that each environment's root
// config leads its branches — the order someone reading the list expects,
// rather than the API's creation order.
func (st *Store) Configs(project string) []doppler.Config {
	st.mu.RLock()
	defer st.mu.RUnlock()
	e := st.snap.Projects[project]
	if e == nil {
		return nil
	}
	out := make([]doppler.Config, len(e.Configs))
	copy(out, e.Configs)
	order := envOrder(e.Envs)
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := order[out[i].Environment], order[out[j].Environment]
		if oi != oj {
			return oi < oj
		}
		if out[i].Root != out[j].Root {
			return out[i].Root
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// envOrder maps environment slug to its position in the project's own
// environment listing, which is the order the Doppler dashboard shows
// (dev → stg → prd), not alphabetical.
func envOrder(envs []doppler.Environment) map[string]int {
	m := make(map[string]int, len(envs))
	for i, e := range envs {
		m[e.Slug] = i
	}
	return m
}

// Envs returns a project's environments in dashboard order.
func (st *Store) Envs(project string) []doppler.Environment {
	st.mu.RLock()
	defer st.mu.RUnlock()
	e := st.snap.Projects[project]
	if e == nil {
		return nil
	}
	out := make([]doppler.Environment, len(e.Envs))
	copy(out, e.Envs)
	return out
}

// SetProjectDetail records a project's environments and configs.
func (st *Store) SetProjectDetail(project string, envs []doppler.Environment, envETag string, cfgs []doppler.Config, cfgETag string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.snap.Projects[project]
	if e == nil {
		e = &ProjectEntry{}
		st.snap.Projects[project] = e
	}
	// A 304 arrives as nil items with the same ETag: keep what we had.
	if envs != nil {
		e.Envs = envs
	}
	if envETag != "" {
		e.EnvETag = envETag
	}
	if cfgs != nil {
		e.Configs = cfgs
	}
	if cfgETag != "" {
		e.CfgETag = cfgETag
	}
	e.LoadedAt = time.Now()
	st.dirty = true
}

// Members returns a project's cached access list.
func (st *Store) Members(project string) []doppler.Member {
	st.mu.RLock()
	defer st.mu.RUnlock()
	e := st.snap.Projects[project]
	if e == nil {
		return nil
	}
	out := make([]doppler.Member, len(e.Members))
	copy(out, e.Members)
	return out
}

// MemberETag returns the ETag that revalidates a project's member list.
func (st *Store) MemberETag(project string) string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if e := st.snap.Projects[project]; e != nil {
		return e.MemberETag
	}
	return ""
}

// SetMembers records a project's access list. A 304 arrives as nil members
// with the same ETag, which keeps what was already there.
func (st *Store) SetMembers(project string, members []doppler.Member, etag string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.snap.Projects[project]
	if e == nil {
		e = &ProjectEntry{}
		st.snap.Projects[project] = e
	}
	if members != nil {
		e.Members = members
	}
	if etag != "" {
		e.MemberETag = etag
	}
	e.MembersAt = time.Now()
	st.dirty = true
}

// InvalidateMembers drops a project's cached access list, so the next read
// refetches rather than revalidating. A mutation the API accepted makes the
// cached list wrong, and an ETag sweep would happily confirm the stale one.
func (st *Store) InvalidateMembers(project string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if e := st.snap.Projects[project]; e != nil {
		e.Members, e.MemberETag, e.MembersAt = nil, "", time.Time{}
		st.dirty = true
	}
}

// Users returns every workplace user, for resolving member slugs to names.
func (st *Store) Users() []doppler.WorkplaceUser {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return doppler.Flatten(st.snap.UserPages)
}

// UserPages returns the paged user listing for a revalidation sweep.
func (st *Store) UserPages() []doppler.Page[doppler.WorkplaceUser] {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.UserPages
}

// SetUsers replaces the workplace user listing.
func (st *Store) SetUsers(pages []doppler.Page[doppler.WorkplaceUser]) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.snap.UserPages = pages
	st.dirty = true
}

// Groups returns the workplace's groups, for resolving group members.
func (st *Store) Groups() []doppler.Group {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]doppler.Group, len(st.snap.Groups))
	copy(out, st.snap.Groups)
	return out
}

// GroupETag returns the ETag that revalidates the group listing.
func (st *Store) GroupETag() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.GroupETag
}

// SetGroups records the workplace's groups.
func (st *Store) SetGroups(groups []doppler.Group, etag string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if groups != nil {
		st.snap.Groups = groups
	}
	if etag != "" {
		st.snap.GroupETag = etag
	}
	st.dirty = true
}

// Roles returns the project roles the workplace defines.
func (st *Store) Roles() []doppler.ProjectRole {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]doppler.ProjectRole, len(st.snap.Roles))
	copy(out, st.snap.Roles)
	return out
}

// RoleETag returns the ETag that revalidates the role listing.
func (st *Store) RoleETag() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.snap.RoleETag
}

// SetRoles records the project roles.
func (st *Store) SetRoles(roles []doppler.ProjectRole, etag string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if roles != nil {
		st.snap.Roles = roles
	}
	if etag != "" {
		st.snap.RoleETag = etag
	}
	st.dirty = true
}

// SecretNames returns a config's cached names, or nil.
func (st *Store) SecretNames(project, config string) *SecretNames {
	st.mu.RLock()
	defer st.mu.RUnlock()
	e := st.snap.Projects[project]
	if e == nil || e.Secrets == nil {
		return nil
	}
	sn := e.Secrets[config]
	if sn == nil {
		return nil
	}
	out := &SecretNames{ETag: sn.ETag, LoadedAt: sn.LoadedAt, Names: make([]string, len(sn.Names))}
	copy(out.Names, sn.Names)
	return out
}

// SetSecretNames records a config's name list.
func (st *Store) SetSecretNames(project, config string, names []string, etag string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e := st.snap.Projects[project]
	if e == nil {
		e = &ProjectEntry{}
		st.snap.Projects[project] = e
	}
	if e.Secrets == nil {
		e.Secrets = map[string]*SecretNames{}
	}
	sn := e.Secrets[config]
	if sn == nil {
		sn = &SecretNames{}
		e.Secrets[config] = sn
	}
	if names != nil {
		sn.Names = names
	}
	if etag != "" {
		sn.ETag = etag
	}
	sn.LoadedAt = time.Now()
	st.dirty = true
}

// Persist writes the snapshot when anything changed since the last write.
func (st *Store) Persist() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.dirty {
		return nil
	}
	if err := save(st.path, st.snap); err != nil {
		return err
	}
	st.dirty = false
	return nil
}

// ---- persistence ----------------------------------------------------------

// Path returns the on-disk cache location for a token identity.
func Path(key string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		base = filepath.Join(os.TempDir(), "dpx")
	}
	return filepath.Join(base, "dpx", key+".json")
}

// save writes the snapshot atomically, so a kill mid-write leaves the previous
// cache intact rather than a truncated file.
func save(path string, s *Snapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace cache: %w", err)
	}
	return nil
}

// Load reads a snapshot. A missing, corrupt, or older-version cache returns
// (nil, nil): none of those should brick the tool, they are all cold starts.
func Load(path string) (*Snapshot, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil
	}
	if s.Version != Version {
		return nil, nil
	}
	if s.Projects == nil {
		s.Projects = map[string]*ProjectEntry{}
	}
	return &s, nil
}

// Clear removes the on-disk cache.
func Clear(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
