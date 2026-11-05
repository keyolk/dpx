package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/keyolk/dpx/internal/doppler"
)

// ErrStale is returned when a write was refused because a secret no longer
// held the value it was conditioned on.
var ErrStale = errors.New("a secret changed after dpx read it, so nothing was written")

// historyWorkers bounds the per-entry fetches history makes. The listing
// carries no diff, so naming what each entry changed costs one request per
// entry; eight at once keeps that fast without spending the rate-limit window
// in a burst.
const historyWorkers = 8

// Assignment is one requested value for a secret.
type Assignment struct {
	Name  string
	Value string
}

// Planned is one line of what a write is about to do.
type Planned struct {
	Kind string // "+", "~", "-", or "=" for unchanged
	Name string
}

// EditResult is what an edit did, or would do on a dry run.
type EditResult struct {
	Plan []Planned
	// Changed is false when every assignment matched what was stored, in
	// which case nothing was sent.
	Changed bool
	// LogID is the config log entry the write created, "" on a dry run or
	// when it could not be read back.
	LogID string
}

// PlanChanges compares the requested edits with the config's current secrets
// and builds the change requests for the ones that change something.
func PlanChanges(current []doppler.Secret, sets []Assignment, unsets []string) ([]doppler.Change, []Planned, error) {
	byName := make(map[string]doppler.Secret, len(current))
	for _, s := range current {
		byName[s.Name] = s
	}

	var changes []doppler.Change
	var plan []Planned
	for _, a := range sets {
		value := a.Value
		cur, exists := byName[a.Name]
		if !exists {
			changes = append(changes, doppler.Change{Name: a.Name, Value: &value})
			plan = append(plan, Planned{"+", a.Name})
			continue
		}
		restricted := cur.RawVisibility == "restricted"
		if !restricted && cur.Raw == value {
			plan = append(plan, Planned{"=", a.Name})
			continue
		}
		name := a.Name
		ch := doppler.Change{
			Name:         a.Name,
			OriginalName: &name,
			Value:        &value,
			Visibility:   cur.RawVisibility,
		}
		if cur.RawValueType.Type != "" {
			vt := cur.RawValueType
			ch.ValueType = &vt
		}
		// A restricted value comes back empty, so there is nothing to hold
		// the write to; asserting "" would reject every update to it.
		if !restricted {
			orig := cur.Raw
			ch.OriginalValue = &orig
		}
		changes = append(changes, ch)
		plan = append(plan, Planned{"~", a.Name})
	}
	for _, n := range unsets {
		if _, exists := byName[n]; !exists {
			return nil, nil, fmt.Errorf("no secret %q", n)
		}
		name := n
		changes = append(changes, doppler.Change{Name: n, OriginalName: &name, Delete: true})
		plan = append(plan, Planned{"-", n})
	}
	return changes, plan, nil
}

// Edit plans and, unless dryRun, applies one batch of edits as a single
// config log entry.
//
// The values are read even for a pure deletion: they are what the plan is
// checked against, and what makes each update conditional.
func (c *Context) Edit(ctx context.Context, project, config string, sets []Assignment, unsets []string, dryRun bool) (EditResult, error) {
	current, err := c.RevealSecrets(ctx, project, config)
	if err != nil {
		return EditResult{}, err
	}
	changes, plan, err := PlanChanges(current, sets, unsets)
	if err != nil {
		return EditResult{}, fmt.Errorf("%s/%s: %w", project, config, err)
	}
	res := EditResult{Plan: plan, Changed: len(changes) > 0}
	if !res.Changed || dryRun {
		return res, nil
	}

	if err := c.Client.ApplyChanges(ctx, project, config, changes); err != nil {
		var apiErr *doppler.APIError
		if errors.As(err, &apiErr) && apiErr.Stale() {
			return res, ErrStale
		}
		return res, err
	}
	// The names listing is now out of date for any create or delete. Dropping
	// its ETag is not needed — the next revalidation sees a new one — but the
	// cached names are, so they are refetched rather than shown stale.
	if err := c.LoadSecretNames(ctx, project, config); err != nil {
		return res, err
	}

	// The write does not answer with the log entry it made, so the newest
	// entry is read back. Another writer landing in between would make this
	// the wrong id, which is why it is shown to the user rather than acted on
	// by anything automatically. A failure here leaves the write intact.
	if logs, err := c.Client.ConfigLogs(ctx, project, config, 1, 1); err == nil && len(logs) > 0 {
		res.LogID = logs[0].ID
	}
	return res, nil
}

// History lists the newest n entries of a config's log, each with its diff.
func (c *Context) History(ctx context.Context, project, config string, n int) ([]doppler.ConfigLog, error) {
	const pageSize = 100
	var logs []doppler.ConfigLog
	for page := 1; len(logs) < n; page++ {
		got, err := c.Client.ConfigLogs(ctx, project, config, page, min(pageSize, n))
		if err != nil {
			return nil, err
		}
		logs = append(logs, got...)
		if len(got) < min(pageSize, n) {
			break
		}
	}
	if len(logs) > n {
		logs = logs[:n]
	}

	errs := make([]error, len(logs))
	sem := make(chan struct{}, historyWorkers)
	var wg sync.WaitGroup
	for i := range logs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			full, err := c.Client.ConfigLog(ctx, project, config, logs[i].ID)
			if err != nil {
				errs[i] = err
				return
			}
			logs[i].Diff = full.Diff
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return logs, nil
}

// Rollback reverts one log entry and refreshes the config's cached names,
// since a revert can recreate or remove secrets.
func (c *Context) Rollback(ctx context.Context, project, config, id string) (doppler.ConfigLog, error) {
	l, err := c.Client.Rollback(ctx, project, config, id)
	if err != nil {
		return l, err
	}
	return l, c.LoadSecretNames(ctx, project, config)
}
