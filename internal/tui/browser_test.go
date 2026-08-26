package tui

import "testing"

// newTestModel builds a model positioned at a screen with the given rows,
// without any of the app wiring — browserTarget reads only cursor state.
func newTestModel(scr screen, project, config string, rows []row) *Model {
	m := &Model{screen: scr, curProject: project, curConfig: config}
	m.cur().setRows(rows)
	return m
}

// The target follows the cursor, not the screen: on the project list "open"
// means the highlighted project, not the workplace.
func TestBrowserTargetFollowsCursorOnProjects(t *testing.T) {
	m := newTestModel(screenProjects, "", "", rowsOf("app-a", "app-b"))
	m.projects.cur = 1

	project, config := m.browserTarget()
	if project != "app-b" || config != "" {
		t.Errorf("target = %q/%q, want app-b/", project, config)
	}
}

func TestBrowserTargetOnConfigsUsesHighlightedConfig(t *testing.T) {
	m := newTestModel(screenConfigs, "app-a", "", rowsOf("dev", "prd"))
	m.configs.cur = 1

	project, config := m.browserTarget()
	if project != "app-a" || config != "prd" {
		t.Errorf("target = %q/%q, want app-a/prd", project, config)
	}
}

// The dashboard has no per-secret page, so the secrets screen opens the config
// that contains the secret rather than nothing at all.
func TestBrowserTargetOnSecretsOpensTheConfig(t *testing.T) {
	m := newTestModel(screenSecrets, "app-a", "dev", rowsOf("TOKEN", "DB_HOST"))
	m.secrets.cur = 1

	project, config := m.browserTarget()
	if project != "app-a" || config != "dev" {
		t.Errorf("target = %q/%q, want app-a/dev", project, config)
	}
}

// A filter that matches nothing leaves no row under the cursor; the config
// screen still knows which project it is showing.
func TestBrowserTargetWithEmptyConfigListFallsBackToProject(t *testing.T) {
	m := newTestModel(screenConfigs, "app-a", "", nil)

	project, config := m.browserTarget()
	if project != "app-a" || config != "" {
		t.Errorf("target = %q/%q, want app-a/", project, config)
	}
}

func TestBrowserTargetWithEmptyProjectListIsWorkplace(t *testing.T) {
	m := newTestModel(screenProjects, "", "", nil)

	project, config := m.browserTarget()
	if project != "" || config != "" {
		t.Errorf("target = %q/%q, want the workplace", project, config)
	}
}

func TestDescribeTarget(t *testing.T) {
	for _, tc := range []struct{ project, config, want string }{
		{"p", "c", "p/c"},
		{"p", "", "p"},
		{"", "", "dashboard"},
	} {
		if got := describeTarget(tc.project, tc.config); got != tc.want {
			t.Errorf("describeTarget(%q,%q) = %q, want %q", tc.project, tc.config, got, tc.want)
		}
	}
}
