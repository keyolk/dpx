package tui

import (
	"strings"
	"testing"
	"time"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/cache"
	"github.com/keyolk/dpx/internal/doppler"
)

// memberModel builds a model sitting on the access list of one project, with
// the store pre-filled as if the fetch had already landed.
func memberModel(t *testing.T, members []doppler.Member, users []doppler.WorkplaceUser, roles []doppler.ProjectRole) *Model {
	t.Helper()
	store := cache.NewStore(t.TempDir()+"/cache.json", cache.Empty("w", "workplace"))
	store.SetMembers("app", members, "etag")
	store.SetUsers([]doppler.Page[doppler.WorkplaceUser]{{ETag: "e", Items: users}})
	store.SetRoles(roles, "re")

	m := &Model{
		app:        &dpxapp.Context{Store: store},
		st:         newStyles(),
		gl:         asciiGlyphs,
		width:      100,
		height:     30,
		screen:     screenMembers,
		curProject: "app",
		loading:    map[string]bool{},
	}
	m.rebuildMembers()
	return m
}

func defaultRoles() []doppler.ProjectRole {
	return []doppler.ProjectRole{
		{Identifier: "viewer", Name: "Viewer"},
		{Identifier: "collaborator", Name: "Collaborator"},
		{Identifier: "admin", Name: "Admin"},
	}
}

// A page of UUIDs answers no question anyone opens this screen to ask.
func TestMembersAreLabelledByPersonNotSlug(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "admin"}, AccessAllEnvironments: true}},
		[]doppler.WorkplaceUser{{ID: "u1", User: struct {
			Email    string `json:"email"`
			Name     string `json:"name"`
			Username string `json:"username"`
		}{Email: "a@b.c", Name: "Alex Lyu"}}},
		defaultRoles())

	out := stripStyle(m.members.render(m.st, asciiGlyphs, 100, 5, true))
	if !strings.Contains(out, "Alex Lyu") {
		t.Errorf("member shown as a slug rather than a name: %q", out)
	}
	if !strings.Contains(out, "Admin") {
		t.Errorf("role missing: %q", out)
	}
}

// A grant that cannot be attributed to a person is exactly the one worth
// seeing, so an unresolvable slug is shown rather than hidden.
func TestUnresolvedSlugIsShownNotDropped(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "ghost-slug", Role: doppler.Role{Identifier: "viewer"}}},
		nil, defaultRoles())

	if len(m.members.rows) != 1 {
		t.Fatalf("got %d rows, want the unresolved member kept", len(m.members.rows))
	}
	if !strings.Contains(stripStyle(m.members.render(m.st, asciiGlyphs, 100, 2, true)), "ghost-slug") {
		t.Error("an unattributable grant was hidden")
	}
}

// "all environments" and an explicit list are different kinds of access; a
// member limited to dev must not read the same as one who can reach prd.
func TestEnvScopeDistinguishesAllFromAnExplicitList(t *testing.T) {
	m := memberModel(t, nil, nil, defaultRoles())
	for _, tc := range []struct {
		mem  doppler.Member
		want string
	}{
		{doppler.Member{AccessAllEnvironments: true}, "all envs"},
		{doppler.Member{Environments: []string{"dev", "stg"}}, "dev,stg"},
		{doppler.Member{}, "no envs"},
	} {
		if got := m.envScope(tc.mem); got != tc.want {
			t.Errorf("envScope = %q, want %q", got, tc.want)
		}
	}
}

// Revoking access is not undoable from here, so it must never happen on one
// keystroke.
func TestRevokeStagesAConfirmationRatherThanActing(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "viewer"}}},
		nil, defaultRoles())

	_, cmd := m.handleKey(keyOf("x"))

	if cmd != nil {
		t.Fatal("x issued a command instead of asking first")
	}
	if m.confirm == nil {
		t.Fatal("x staged no confirmation")
	}
	if !strings.Contains(m.confirm.prompt, "revoke") {
		t.Errorf("prompt does not say what will happen: %q", m.confirm.prompt)
	}
}

// A confirmation that any key could accept is not a confirmation.
func TestOnlyYConfirmsEverythingElseCancels(t *testing.T) {
	for _, key := range []string{"n", "esc", "j", "enter", "q"} {
		m := memberModel(t,
			[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "viewer"}}},
			nil, defaultRoles())
		m.handleKey(keyOf("x"))

		_, cmd := m.handleKey(keyOf(key))

		if cmd != nil {
			t.Errorf("%q ran the destructive action", key)
		}
		if m.confirm != nil {
			t.Errorf("%q left the confirmation open", key)
		}
	}
}

// Raising someone to admin grants the most and is the easiest to hit by
// accident from a list, so it is confirmed like a removal.
func TestPromotionToAdminIsConfirmed(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "viewer"}}},
		nil, defaultRoles())

	m.handleKey(keyOf("R"))
	if m.roleFor == "" {
		t.Fatal("R did not open the role picker")
	}
	m.roles.cur = 2 // admin
	_, cmd := m.applyRole()

	if cmd != nil {
		t.Fatal("a promotion to admin ran without confirmation")
	}
	if m.confirm == nil || !strings.Contains(m.confirm.prompt, "admin") {
		t.Errorf("no admin confirmation: %+v", m.confirm)
	}
}

// A lateral move between non-privileged roles does not need a prompt; making
// every change a confirmation trains people to press y without reading.
func TestLateralRoleChangeAppliesWithoutAPrompt(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "viewer"}}},
		nil, defaultRoles())

	m.handleKey(keyOf("R"))
	m.roles.cur = 1 // collaborator
	_, cmd := m.applyRole()

	if m.confirm != nil {
		t.Error("a lateral role change asked for confirmation")
	}
	if cmd == nil {
		t.Error("a lateral role change produced no command")
	}
}

// The picker opens on the role the member already holds: that is the
// reference point for the change.
func TestRolePickerStartsOnTheCurrentRole(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "collaborator"}}},
		nil, defaultRoles())

	m.handleKey(keyOf("R"))

	if m.roles.cur != 1 {
		t.Errorf("picker opened on index %d, want collaborator at 1", m.roles.cur)
	}
}

// Selecting the role someone already has is a no-op, not a request.
func TestReapplyingTheSameRoleIssuesNoRequest(t *testing.T) {
	m := memberModel(t,
		[]doppler.Member{{Type: "workplace_user", Slug: "u1", Role: doppler.Role{Identifier: "viewer"}}},
		nil, defaultRoles())

	m.handleKey(keyOf("R"))
	_, cmd := m.applyRole() // cursor is already on viewer

	if cmd != nil {
		t.Error("re-selecting the current role issued a request")
	}
	if m.confirm != nil {
		t.Error("re-selecting the current role asked for confirmation")
	}
}

// The member screen is reached from two levels; esc must return to the one it
// was opened from.
func TestMembersReturnToWhereItWasOpenedFrom(t *testing.T) {
	for _, from := range []screen{screenProjects, screenConfigs} {
		m := memberModel(t, nil, nil, defaultRoles())
		m.memberBack = from

		m.back()

		if m.screen != from {
			t.Errorf("opened from %v, returned to %v", from, m.screen)
		}
	}
}

// The write keys belong to the access screen; on a secret list `x` must not
// revoke anything.
func TestWriteKeysAreInertOutsideTheAccessScreen(t *testing.T) {
	m := memberModel(t, nil, nil, defaultRoles())
	m.screen = screenSecrets
	m.secrets.setRows(rowsOf("TOKEN"))

	for _, key := range []string{"x", "R"} {
		if _, cmd := m.handleKey(keyOf(key)); cmd != nil {
			t.Errorf("%q acted on the secrets screen", key)
		}
		if m.confirm != nil {
			t.Errorf("%q staged a confirmation on the secrets screen", key)
		}
	}
}

// last_fetch_at is the closest thing the API gives to "is this config live";
// a config nothing has pulled must read differently from one pulled minutes
// ago.
func TestConfigRowReportsWhenItWasLastPulled(t *testing.T) {
	recent := time.Now().Add(-90 * time.Second)
	store := cache.NewStore(t.TempDir()+"/c.json", cache.Empty("w", "wp"))
	store.SetProjectDetail("app",
		[]doppler.Environment{{Slug: "dev"}},
		"ee",
		[]doppler.Config{
			{Name: "dev", Environment: "dev", LastFetchAt: &recent},
			{Name: "dev_old", Environment: "dev"},
		},
		"ce")

	m := &Model{
		app: &dpxapp.Context{Store: store}, st: newStyles(), gl: asciiGlyphs,
		screen: screenConfigs, curProject: "app", loading: map[string]bool{},
	}
	m.rebuildConfigs()

	out := stripStyle(m.configs.render(m.st, asciiGlyphs, 110, 5, true))
	if !strings.Contains(out, "pulled 1m ago") {
		t.Errorf("recent pull not reported: %q", out)
	}
	if !strings.Contains(out, "never pulled") {
		t.Errorf("an unpulled config was not distinguished: %q", out)
	}
}

// A workplace that grants by team has more group rows than user rows;
// resolving only users would leave most of the list as UUIDs.
func TestGroupMembersResolveToTeamNames(t *testing.T) {
	store := cache.NewStore(t.TempDir()+"/c.json", cache.Empty("w", "wp"))
	store.SetMembers("app", []doppler.Member{
		{Type: "group", Slug: "g1", Role: doppler.Role{Identifier: "admin"}, AccessAllEnvironments: true},
	}, "e")
	store.SetGroups([]doppler.Group{{Slug: "g1", Name: "Team_Core Platform"}}, "ge")
	store.SetRoles(defaultRoles(), "re")

	m := &Model{
		app: &dpxapp.Context{Store: store}, st: newStyles(), gl: asciiGlyphs,
		screen: screenMembers, curProject: "app", loading: map[string]bool{},
	}
	m.rebuildMembers()

	out := stripStyle(m.members.render(m.st, asciiGlyphs, 100, 3, true))
	if !strings.Contains(out, "Team_Core Platform") {
		t.Errorf("group shown as a slug: %q", out)
	}
	// Revoking a group takes access from everyone in it, so the row must not
	// read like an individual.
	if !strings.Contains(out, asciiGlyphs.group) {
		t.Errorf("a group row is indistinguishable from a person: %q", out)
	}
}
