package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/keyolk/dpx/internal/doppler"
)

// memberRow ties a rendered row back to the member it came from. The list only
// carries strings, and a role change needs the member's type and slug, so the
// binding is kept alongside rather than parsed back out of the label.
type memberRow struct {
	member doppler.Member
	label  string
}

// rebuildMembers builds the access list for the current project.
//
// Rows are labelled by person, not by slug: a page of UUIDs answers no
// question anyone opens this screen to ask. A slug with no matching workplace
// user is shown as the raw slug rather than hidden — a grant that cannot be
// attributed is exactly the one worth seeing.
func (m *Model) rebuildMembers() {
	members := m.app.Store.Members(m.curProject)
	name := map[string]string{}
	for _, u := range m.app.Store.Users() {
		n := u.User.Name
		if n == "" {
			n = u.User.Email
		}
		name[u.ID] = n
	}
	// Groups hold project access alongside individual users, and a workplace
	// that grants by team has more group rows than user rows. Resolving only
	// users would leave most of the list as UUIDs.
	for _, g := range m.app.Store.Groups() {
		name[g.Slug] = g.Name
	}

	m.memberRows = m.memberRows[:0]
	for _, mem := range members {
		label := mem.Slug
		if n, ok := name[mem.Slug]; ok && n != "" {
			label = n
		}
		// The kind of principal changes what a grant means — revoking a group
		// takes access from everyone in it — so it is never left implicit.
		switch mem.Type {
		case "group":
			label = m.gl.group + " " + label
		case "service_account":
			label += " (service account)"
		}
		m.memberRows = append(m.memberRows, memberRow{member: mem, label: label})
	}
	sort.SliceStable(m.memberRows, func(i, j int) bool {
		return strings.ToLower(m.memberRows[i].label) < strings.ToLower(m.memberRows[j].label)
	})

	roleLabel := map[string]string{}
	for _, r := range m.app.Store.Roles() {
		roleLabel[r.Identifier] = r.Name
	}

	rows := make([]row, 0, len(m.memberRows))
	for _, mr := range m.memberRows {
		mem := mr.member
		name := roleLabel[mem.Role.Identifier]
		if name == "" {
			name = mem.Role.Identifier
		}
		meta := []cell{{text: name, style: m.roleStyle(mem.Role.Identifier)}}
		meta = append(meta, cell{text: m.envScope(mem), style: m.st.dim})
		rows = append(rows, row{label: mr.label, meta: meta})
	}
	m.members.setRows(rows)
}

// envScope describes which environments a grant reaches. "all environments" and
// an explicit list are different kinds of access, and a member limited to dev
// must not read the same as one who can reach production.
func (m *Model) envScope(mem doppler.Member) string {
	if mem.AccessAllEnvironments {
		return "all envs"
	}
	if len(mem.Environments) == 0 {
		return "no envs"
	}
	return strings.Join(mem.Environments, ",")
}

// roleStyle colors a role by how much it can do. admin is the one a reader
// must not skim past; no_access is inert. Everything else stays neutral, so
// the two ends keep their meaning.
func (m *Model) roleStyle(identifier string) lipgloss.Style {
	switch identifier {
	case "owner", "admin":
		return m.st.warn
	case "no_access":
		return m.st.dim
	default:
		return m.st.info
	}
}

// currentMember returns the member under the cursor on the member screen.
func (m *Model) currentMember() *memberRow {
	if m.screen != screenMembers {
		return nil
	}
	cur := m.members.current()
	if cur == nil || cur.Index >= len(m.memberRows) {
		return nil
	}
	return &m.memberRows[cur.Index]
}

// openMembers switches to the access list for whatever project the cursor
// implies, remembering where to return.
func (m *Model) openMembers() (tea.Model, tea.Cmd) {
	project := m.curProject
	if m.screen == screenProjects {
		r := m.cur().currentRow()
		if r == nil {
			return m, nil
		}
		project = r.label
	}
	if project == "" {
		return m, nil
	}
	m.memberBack = m.screen
	m.curProject = project
	m.screen = screenMembers
	m.members = list{}
	m.rebuildMembers()
	return m, m.loadMembersCmd(project)
}

// ---- role picker ----------------------------------------------------------

// openRolePicker lists the roles the workplace defines, for the member under
// the cursor.
func (m *Model) openRolePicker() (tea.Model, tea.Cmd) {
	mr := m.currentMember()
	if mr == nil {
		return m, nil
	}
	roles := m.app.Store.Roles()
	if len(roles) == 0 {
		m.note("roles not loaded yet — press r")
		return m, nil
	}

	m.roleFor = mr.label
	m.roles = list{}
	rows := make([]row, 0, len(roles))
	for _, r := range roles {
		name := r.Name
		if name == "" {
			name = r.Identifier
		}
		var meta []cell
		if r.IsCustom {
			meta = append(meta, cell{text: "custom", style: m.st.info})
		}
		if r.Identifier == mr.member.Role.Identifier {
			meta = append(meta, cell{text: "current", style: m.st.success})
		}
		rows = append(rows, row{label: name, meta: meta})
	}
	m.roles.setRows(rows)
	// Start on the role they already hold: the common case is a small step
	// from it, and the current role is the reference point for the change.
	for i, r := range roles {
		if r.Identifier == mr.member.Role.Identifier {
			m.roles.cur = i
			break
		}
	}
	return m, nil
}

// applyRole commits the highlighted role to the member the picker was opened
// for.
func (m *Model) applyRole() (tea.Model, tea.Cmd) {
	mr := m.currentMember()
	cur := m.roles.current()
	if mr == nil || cur == nil {
		m.roleFor = ""
		return m, nil
	}
	roles := m.app.Store.Roles()
	if cur.Index >= len(roles) {
		m.roleFor = ""
		return m, nil
	}
	target := roles[cur.Index]
	m.roleFor = ""
	if target.Identifier == mr.member.Role.Identifier {
		m.note("already " + target.Identifier)
		return m, nil
	}
	// Raising someone to admin is the change that grants the most and is the
	// easiest to make by accident from a list, so it is confirmed like a
	// removal rather than applied on enter.
	if target.Identifier == "admin" || target.Identifier == "owner" {
		m.confirm = &confirmation{
			prompt: "grant " + target.Identifier + " on " + m.curProject + " to " + mr.label + "?",
			run: func() tea.Cmd {
				return m.setRoleCmd(m.curProject, mr.member, target.Identifier, mr.label)
			},
		}
		return m, nil
	}
	return m, m.setRoleCmd(m.curProject, mr.member, target.Identifier, mr.label)
}

// askRemoveMember stages a revocation behind a confirmation.
func (m *Model) askRemoveMember() (tea.Model, tea.Cmd) {
	mr := m.currentMember()
	if mr == nil {
		return m, nil
	}
	m.confirm = &confirmation{
		prompt: "revoke " + mr.label + "'s access to " + m.curProject + "?",
		run: func() tea.Cmd {
			return m.removeMemberCmd(m.curProject, mr.member, mr.label)
		},
	}
	return m, nil
}
