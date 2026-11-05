package doppler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Change is one entry of a secrets update.
//
// Every write dpx makes goes through change_requests rather than the plain
// `secrets` map, for two reasons. A whole command — several sets and deletes
// together — lands as one config log entry, so one rollback undoes it. And a
// change request can carry the value it expects to replace (originalValue),
// which turns a lost update into a rejected request instead of a silent
// overwrite of someone else's edit.
type Change struct {
	Name         string  `json:"name"`
	OriginalName *string `json:"originalName"`
	// Value nil with Delete set is a deletion; the API requires the field to
	// be present either way.
	Value         *string `json:"value"`
	OriginalValue *string `json:"originalValue,omitempty"`
	// Visibility and ValueType are sent back as they already are for an
	// existing secret. The docs do not say what omitting them does to an
	// existing secret, and a restricted secret quietly becoming masked is not
	// a risk worth taking to save a few bytes.
	Visibility string `json:"visibility,omitempty"`
	ValueType  *Type  `json:"valueType,omitempty"`
	Delete     bool   `json:"shouldDelete,omitempty"`
}

// ApplyChanges writes a batch of changes to one config.
func (c *Client) ApplyChanges(ctx context.Context, project, config string, changes []Change) error {
	body := map[string]any{
		"project":         project,
		"config":          config,
		"change_requests": changes,
	}
	return c.send(ctx, "POST", "/v3/configs/config/secrets", nil, body)
}

// LogUser is who made a config change.
type LogUser struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// ConfigLog is one entry of a config's change history.
//
// The listing omits Diff; only the single-log endpoint carries it, and when it
// does it carries the values on both sides. That is why history is printed by
// name unless values are asked for explicitly.
type ConfigLog struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Rollback  bool      `json:"rollback"`
	CreatedAt time.Time `json:"created_at"`
	User      LogUser   `json:"user"`
	Diff      []LogDiff `json:"diff"`
}

// LogDiff is one secret's before and after within a log entry.
//
// A nil value does not mean the secret was absent: a restricted value is
// withheld as null too. Whether a side existed is told by its visibility,
// which the API sets whenever that side had a secret at all.
type LogDiff struct {
	Name       string  `json:"name"`
	Added      *string `json:"added"`
	Removed    *string `json:"removed"`
	AddedVis   *int    `json:"addedVisType"`
	RemovedVis *int    `json:"removedVisType"`
}

// visRestricted is the visibility code the log uses for a restricted value.
const visRestricted = 2

// HasAdded reports whether the secret exists after the change.
func (d LogDiff) HasAdded() bool { return d.Added != nil || d.AddedVis != nil }

// HasRemoved reports whether the secret existed before the change.
func (d LogDiff) HasRemoved() bool { return d.Removed != nil || d.RemovedVis != nil }

// AddedRestricted reports whether the new value was withheld.
func (d LogDiff) AddedRestricted() bool { return d.AddedVis != nil && *d.AddedVis == visRestricted }

// RemovedRestricted reports whether the old value was withheld.
func (d LogDiff) RemovedRestricted() bool {
	return d.RemovedVis != nil && *d.RemovedVis == visRestricted
}

// Kind classifies the change as "+" (created), "-" (deleted) or "~".
func (d LogDiff) Kind() string {
	switch {
	case !d.HasRemoved() && d.HasAdded():
		return "+"
	case !d.HasAdded() && d.HasRemoved():
		return "-"
	}
	return "~"
}

// Stale reports whether a write was refused because a secret no longer held
// the value it was conditioned on. Doppler words this for its web page, which
// is meaningless from a CLI, so callers rephrase it.
func (e *APIError) Stale() bool {
	return e.Status == 400 && strings.Contains(e.Message, "out of date")
}

// ConfigLogs lists a config's change history, newest first.
func (c *Client) ConfigLogs(ctx context.Context, project, config string, page, perPage int) ([]ConfigLog, error) {
	q := url.Values{
		"project":  {project},
		"config":   {config},
		"page":     {strconv.Itoa(page)},
		"per_page": {strconv.Itoa(perPage)},
	}
	res, err := c.get(ctx, "/v3/configs/config/logs", q, "")
	if err != nil {
		return nil, err
	}
	var env struct {
		Logs []ConfigLog `json:"logs"`
	}
	if err := json.Unmarshal(res.Body, &env); err != nil {
		return nil, fmt.Errorf("parse config logs: %w", err)
	}
	return env.Logs, nil
}

// ConfigLog fetches one log entry with its diff.
func (c *Client) ConfigLog(ctx context.Context, project, config, id string) (ConfigLog, error) {
	q := url.Values{"project": {project}, "config": {config}, "log": {id}}
	res, err := c.get(ctx, "/v3/configs/config/logs/log", q, "")
	if err != nil {
		return ConfigLog{}, err
	}
	return parseLog(res.Body)
}

// Rollback reverts the change a log entry recorded, and returns the new log
// entry the revert itself created — which can in turn be rolled back.
func (c *Client) Rollback(ctx context.Context, project, config, id string) (ConfigLog, error) {
	q := url.Values{"project": {project}, "config": {config}, "log": {id}}
	body, err := c.sendRead(ctx, "POST", "/v3/configs/config/logs/log/rollback", q, nil)
	if err != nil {
		return ConfigLog{}, err
	}
	return parseLog(body)
}

func parseLog(body []byte) (ConfigLog, error) {
	var env struct {
		Log ConfigLog `json:"log"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ConfigLog{}, fmt.Errorf("parse config log: %w", err)
	}
	return env.Log, nil
}

// Label is the shortest name that identifies who made a change.
func (u LogUser) Label() string {
	switch {
	case u.Email != "":
		return u.Email
	case u.Username != "":
		return u.Username
	case u.Name != "":
		return u.Name
	}
	return "-"
}

// Inverse is what reverting a change of this kind does to the secret.
func Inverse(kind string) string {
	switch kind {
	case "+":
		return "-"
	case "-":
		return "+"
	}
	return kind
}

// Summary names the secrets an entry touched, each prefixed by its kind.
func (l ConfigLog) Summary() string {
	parts := make([]string, 0, len(l.Diff))
	for _, d := range l.Diff {
		parts = append(parts, d.Kind()+d.Name)
	}
	s := strings.Join(parts, " ")
	if s == "" {
		// No secret-level diff: a rename, a lock, an integration change. The
		// API's own sentence is the only description there is.
		s = l.Text
	}
	if l.Rollback {
		s += "  (rollback)"
	}
	return s
}

// Touches reports whether the entry changed the named secret.
func (l ConfigLog) Touches(name string) bool {
	for _, d := range l.Diff {
		if d.Name == name {
			return true
		}
	}
	return false
}

// Redacted drops the values from an entry, keeping only which side existed —
// enough to tell a creation from a change from a deletion.
func (l ConfigLog) Redacted() ConfigLog {
	hidden := ""
	out := l
	out.Diff = make([]LogDiff, len(l.Diff))
	for i, d := range l.Diff {
		r := LogDiff{Name: d.Name, AddedVis: d.AddedVis, RemovedVis: d.RemovedVis}
		if d.Added != nil {
			r.Added = &hidden
		}
		if d.Removed != nil {
			r.Removed = &hidden
		}
		out.Diff[i] = r
	}
	return out
}
