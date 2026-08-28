// Package tui implements dpx's interactive Doppler browser.
//
// The shape is a drill-down stack rather than a persistent multi-panel: the
// hierarchy is strictly three levels deep and each level is a plain list, so a
// stack stays readable in a 60-column tmux split where three side-by-side
// panes would not.
//
//	projects → configs → secret names → [reveal one value]
//
// Values are the one thing that is never fetched implicitly. Navigating shows
// names only; a value is fetched when someone asks for it, and never written
// to disk.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/doppler"
)

type screen int

const (
	screenProjects screen = iota
	screenConfigs
	screenSecrets
	screenMembers
	screenHelp
)

const (
	minWidth  = 50
	minHeight = 12
)

// Model is the Bubble Tea model.
type Model struct {
	ctx context.Context
	app *dpxapp.Context

	st     *styles
	gl     glyphSet
	width  int
	height int
	small  bool

	screen   screen
	helpBack screen

	projects list
	configs  list
	secrets  list
	members  list

	curProject string
	curConfig  string
	curEnv     string

	// revealed holds the values for curConfig once someone asked for them.
	// It is dropped on every navigation, so a value never outlives the screen
	// that asked for it.
	revealed  map[string]doppler.Secret
	revealAll bool
	// detail is the secret currently expanded in the detail pane.
	detail string
	// pendingCopy is a secret whose value was asked for by `yv` before it had
	// been fetched; the reveal that follows completes the copy.
	pendingCopy string
	// pendingKey is the first key of a chord awaiting its second, "" when no
	// chord is open.
	pendingKey string

	// memberBack is the screen the member list was opened from, so esc returns
	// to where the reader was rather than to a fixed level.
	memberBack screen
	// roleFor is the member whose role picker is open, "" when it is not.
	roleFor string
	// roles is the picker's own list, built from the workplace's roles.
	roles list
	// confirm is a pending destructive action awaiting a y/n answer.
	confirm *confirmation
	// memberRows binds each rendered member row back to its API record, which
	// a role change needs and the row's label cannot carry.
	memberRows []memberRow

	filtering bool
	// inflight counts background fetches, so the spinner runs while any is
	// outstanding and stops at zero rather than on a timer.
	inflight int
	spinTick int
	status   string
	statusOK bool
	errText  string

	// loading marks project/config keys with a fetch already in flight, so
	// moving the cursor across a list does not queue the same fetch twice.
	loading map[string]bool
}

// Run starts the browser.
func Run(ctx context.Context, app *dpxapp.Context) error {
	m := &Model{
		ctx:      ctx,
		app:      app,
		st:       newStyles(),
		gl:       detectGlyphs(),
		screen:   screenProjects,
		revealed: map[string]doppler.Secret{},
		loading:  map[string]bool{},
	}
	m.rebuildProjects()

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	// The cache is written on every mutation, but a final flush catches
	// anything a fetch landed just before quit.
	if perr := app.Store.Persist(); perr != nil && err == nil {
		return perr
	}
	return err
}

func (m *Model) Init() tea.Cmd {
	// A stale or cold listing refreshes in the background; the cached rows are
	// already on screen by then.
	if m.app.Stale {
		return m.refreshProjectsCmd()
	}
	return nil
}

// ---- messages -------------------------------------------------------------

type projectsMsg struct{ err error }

type projectLoadedMsg struct {
	project string
	err     error
}

type secretNamesMsg struct {
	project, config string
	err             error
}

type revealMsg struct {
	project, config string
	secrets         []doppler.Secret
	err             error
}

type membersMsg struct {
	project string
	err     error
}

type memberWriteMsg struct {
	project string
	// verb describes what was attempted, for the status line and for the
	// error when it failed.
	verb string
	err  error
}

type spinMsg struct{}

// confirmation is a destructive action held until the user answers. Revoking
// someone's access is not undoable from here, so it never happens on a single
// keystroke.
type confirmation struct {
	prompt string
	run    func() tea.Cmd
}

// spinCmd schedules the next spinner frame. It is only ever scheduled while a
// fetch is in flight, which is what keeps the app at 0 fps when idle.
func spinCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *Model) refreshProjectsCmd() tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		return projectsMsg{err: m.app.RefreshProjects(m.ctx)}
	})
}

func (m *Model) loadProjectCmd(project string) tea.Cmd {
	if m.loading[project] {
		return nil
	}
	m.loading[project] = true
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		return projectLoadedMsg{project: project, err: m.app.LoadProject(m.ctx, project)}
	})
}

func (m *Model) loadSecretNamesCmd(project, config string) tea.Cmd {
	key := project + "\x00" + config
	if m.loading[key] {
		return nil
	}
	m.loading[key] = true
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		return secretNamesMsg{
			project: project, config: config,
			err: m.app.LoadSecretNames(m.ctx, project, config),
		}
	})
}

func (m *Model) loadMembersCmd(project string) tea.Cmd {
	key := "members\x00" + project
	if m.loading[key] {
		return nil
	}
	m.loading[key] = true
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		return membersMsg{project: project, err: m.app.LoadMembers(m.ctx, project)}
	})
}

func (m *Model) setRoleCmd(project string, mem doppler.Member, role, who string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		return memberWriteMsg{
			project: project,
			verb:    who + " → " + role,
			err:     m.app.SetMemberRole(m.ctx, project, mem, role),
		}
	})
}

func (m *Model) removeMemberCmd(project string, mem doppler.Member, who string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		return memberWriteMsg{
			project: project,
			verb:    "removed " + who,
			err:     m.app.RemoveMember(m.ctx, project, mem),
		}
	})
}

func (m *Model) revealCmd(project, config string) tea.Cmd {
	m.inflight++
	return tea.Batch(spinCmd(), func() tea.Msg {
		secrets, err := m.app.RevealSecrets(m.ctx, project, config)
		return revealMsg{project: project, config: config, secrets: secrets, err: err}
	})
}

// ---- row construction -----------------------------------------------------

func (m *Model) rebuildProjects() {
	projects := m.app.Store.Projects()
	rows := make([]row, 0, len(projects))
	for _, p := range projects {
		var meta []cell
		if p.Description != "" {
			meta = append(meta, cell{text: p.Description, style: m.st.dim})
		}
		if e := m.app.Store.Entry(p.Name); e.Loaded() {
			// Only a walked project can report its config count; showing a
			// blank rather than 0 keeps "not looked at yet" distinct from
			// "genuinely has none".
			meta = append(meta, cell{text: fmt.Sprintf("%d cfg", len(e.Configs)), style: m.st.dim})
		}
		rows = append(rows, row{label: p.Name, meta: meta})
	}
	m.projects.setRows(rows)
}

func (m *Model) rebuildConfigs() {
	cfgs := m.app.Store.Configs(m.curProject)
	envName := map[string]string{}
	for _, e := range m.app.Store.Envs(m.curProject) {
		envName[e.Slug] = e.Name
	}

	rows := make([]row, 0, len(cfgs))
	for _, c := range cfgs {
		var meta []cell
		if c.Root {
			meta = append(meta, cell{text: "root", style: m.st.dim})
		}
		if c.Locked {
			meta = append(meta, cell{text: m.gl.lock, style: m.st.warn})
		}
		if c.Inheriting && len(c.Inherits) > 0 {
			// An inheriting config's secrets come from elsewhere; without this
			// the values look like they live here.
			var from []string
			for _, in := range c.Inherits {
				from = append(from, in.Config)
			}
			meta = append(meta, cell{text: m.gl.arrow + " " + strings.Join(from, ","), style: m.st.info})
		}
		if sn := m.app.Store.SecretNames(m.curProject, c.Name); sn != nil {
			meta = append(meta, cell{text: fmt.Sprintf("%d", len(sn.Names)), style: m.st.dim})
		}
		// last_fetch_at is when a client actually pulled this config. It is the
		// closest thing Doppler's API exposes to "is this config live" — there
		// is no sync endpoint — and it is what separates a config a deployment
		// reads every hour from one nothing has touched in months.
		if c.LastFetchAt != nil {
			meta = append(meta, cell{
				text:  "pulled " + shortAge(time.Since(*c.LastFetchAt)) + " ago",
				style: m.fetchStyle(*c.LastFetchAt),
			})
		} else {
			meta = append(meta, cell{text: "never pulled", style: m.st.dim})
		}
		rows = append(rows, row{
			label:    c.Name,
			tag:      c.Environment,
			tagStyle: m.envStyle(c.Environment),
			meta:     meta,
		})
	}
	m.configs.setRows(rows)
}

// fetchStyle ages a config's last pull. Only the extremes are colored: a
// config pulled minutes ago is live, one untouched for a month is a candidate
// for deletion, and everything between is unremarkable enough that coloring it
// would drown both signals.
func (m *Model) fetchStyle(at time.Time) lipgloss.Style {
	switch d := time.Since(at); {
	case d < time.Hour:
		return m.st.success
	case d > 30*24*time.Hour:
		return m.st.warn
	default:
		return m.st.dim
	}
}

// envStyle colors an environment slug by risk. Production is the one a reader
// must never mistake for staging, so it gets the warning color and every other
// environment stays neutral — coloring all of them would make none of them
// stand out.
func (m *Model) envStyle(slug string) lipgloss.Style {
	switch strings.ToLower(slug) {
	case "prd", "prod", "production":
		return m.st.warn
	case "stg", "staging":
		return m.st.info
	default:
		return m.st.dim
	}
}

func (m *Model) rebuildSecrets() {
	sn := m.app.Store.SecretNames(m.curProject, m.curConfig)
	var names []string
	if sn != nil {
		names = sn.Names
	}
	sort.Strings(names)

	rows := make([]row, 0, len(names))
	for _, n := range names {
		var meta []cell
		// A fetched value is only shown for the secret that was asked about,
		// unless the whole config was revealed. One request brings every
		// value, but showing all of them because someone asked about one would
		// put the rest of the config on screen unasked.
		shown := m.revealAll || n == m.detail
		if s, ok := m.revealed[n]; ok && shown {
			// The value is handed over whole; the renderer clips it to
			// whatever the terminal actually has. A value pre-cut to a fixed
			// budget here would stay cut on a 200-column screen.
			switch {
			case s.Restricted():
				meta = []cell{{text: "restricted", style: m.st.warn}}
			case s.Referenced():
				// The stored value is a reference; showing the computed form
				// alone would hide that fact.
				meta = []cell{{text: s.Computed, style: m.st.info}}
			default:
				meta = []cell{{text: s.Computed, style: m.st.success}}
			}
		} else if isDopplerMeta(n) {
			meta = []cell{{text: "doppler", style: m.st.dim}}
		}
		rows = append(rows, row{label: n, meta: meta, dimmed: isDopplerMeta(n)})
	}
	m.secrets.setRows(rows)
}

// isDopplerMeta marks the four variables Doppler injects into every config.
// They are never what someone is looking for, so they are dimmed rather than
// hidden — hiding them would make the count disagree with the dashboard.
func isDopplerMeta(name string) bool {
	switch name {
	case "DOPPLER_PROJECT", "DOPPLER_CONFIG", "DOPPLER_ENVIRONMENT", "DOPPLER_ENCLAVE_PROJECT", "DOPPLER_ENCLAVE_CONFIG":
		return true
	}
	return false
}
