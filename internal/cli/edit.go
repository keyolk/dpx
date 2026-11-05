package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/doppler"
)

func newSetCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "set <project>/<config> KEY[=VALUE]...",
		Short: "Create or update secrets",
		Long: `Create or update secrets in one change.

KEY=VALUE sets a value from the command line. A bare KEY reads its value
instead: from stdin when stdin is piped (one such KEY per run, one trailing
newline dropped), or from a hidden prompt on a terminal. Prefer the bare form
for anything sensitive — a value in argv is visible in ps and in shell history.

  dpx set my-app/dev LOG_LEVEL=debug FEATURE_X=on
  pbpaste | dpx set my-app/prd DB_PASSWORD
  dpx set my-app/prd TLS_KEY < key.pem

All assignments land as one config log entry, so one rollback undoes the whole
command; the entry's id is printed when the write succeeds. Each existing
secret is updated only if it still holds the value dpx read a moment earlier,
so a concurrent edit makes the write fail rather than be overwritten.
Assignments that would not change anything are skipped.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest, err := splitRef(args, 2)
			if err != nil {
				return err
			}
			if len(rest) == 0 {
				return fmt.Errorf("nothing to set")
			}
			sets, err := parseAssignments(rest, os.Stdin)
			if err != nil {
				return err
			}
			return applyEdit(cmd.Context(), ref[0], ref[1], sets, nil, dryRun)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show what would change without writing")
	return cmd
}

func newUnsetCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "unset <project>/<config> KEY...",
		Aliases: []string{"rm"},
		Short:   "Delete secrets",
		Long: `Delete secrets in one change.

Like set, the deletions land as one config log entry, so the printed rollback
command brings all of them back.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest, err := splitRef(args, 2)
			if err != nil {
				return err
			}
			if len(rest) == 0 {
				return fmt.Errorf("nothing to unset")
			}
			return applyEdit(cmd.Context(), ref[0], ref[1], nil, rest, dryRun)
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show what would change without writing")
	return cmd
}

// assignment is one KEY=VALUE from the command line, with the value already
// resolved from stdin or a prompt where it was not given inline.
type assignment struct {
	name  string
	value string
}

// parseAssignments resolves every argument to a name and value.
func parseAssignments(args []string, stdin *os.File) ([]assignment, error) {
	interactive := term.IsTerminal(stdin.Fd())
	var out []assignment
	seen := map[string]bool{}
	stdinUsed := false
	for _, a := range args {
		name, value, inline := strings.Cut(a, "=")
		if name == "" {
			return nil, fmt.Errorf("bad assignment %q", a)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s is set twice", name)
		}
		seen[name] = true
		if !inline {
			var err error
			switch {
			case interactive:
				value, err = promptHidden(stdin, name)
			case stdinUsed:
				return nil, fmt.Errorf("%s: stdin already supplied another value; use KEY=VALUE or a separate run", name)
			default:
				stdinUsed = true
				value, err = readValue(stdin)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			// An empty read is far more often an empty clipboard or a wrong
			// file than an intended blank, and it would wipe the value.
			if value == "" {
				return nil, fmt.Errorf("%s: read an empty value; write %s= to set one deliberately", name, name)
			}
		}
		out = append(out, assignment{name: name, value: value})
	}
	return out, nil
}

// readValue reads a piped value. Exactly one trailing newline is dropped: it
// is what echo and most editors append, while a value that genuinely ends in
// blank lines (a PEM bundle, say) keeps the rest.
func readValue(r io.Reader) (string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	return s, nil
}

func promptHidden(stdin *os.File, name string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", name)
	b, err := term.ReadPassword(stdin.Fd())
	fmt.Fprintln(os.Stderr)
	if err != nil {
		// Fall back to a visible line rather than failing outright on a
		// terminal that refuses to turn echo off.
		line, rerr := bufio.NewReader(stdin).ReadString('\n')
		if rerr != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	return string(b), nil
}

// planned is one line of what a write is about to do.
type planned struct {
	kind string // "+", "~", "-", or "=" for unchanged
	name string
}

// planChanges compares the requested edits with the config's current secrets
// and builds the change requests for the ones that change something.
func planChanges(current []doppler.Secret, sets []assignment, unsets []string) ([]doppler.Change, []planned, error) {
	byName := make(map[string]doppler.Secret, len(current))
	for _, s := range current {
		byName[s.Name] = s
	}

	var changes []doppler.Change
	var plan []planned
	for _, a := range sets {
		value := a.value
		cur, exists := byName[a.name]
		if !exists {
			changes = append(changes, doppler.Change{Name: a.name, Value: &value})
			plan = append(plan, planned{"+", a.name})
			continue
		}
		restricted := cur.RawVisibility == "restricted"
		if !restricted && cur.Raw == value {
			plan = append(plan, planned{"=", a.name})
			continue
		}
		name := a.name
		ch := doppler.Change{
			Name:         a.name,
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
		plan = append(plan, planned{"~", a.name})
	}
	for _, n := range unsets {
		if _, exists := byName[n]; !exists {
			return nil, nil, fmt.Errorf("no secret %q", n)
		}
		name := n
		changes = append(changes, doppler.Change{Name: n, OriginalName: &name, Delete: true})
		plan = append(plan, planned{"-", n})
	}
	return changes, plan, nil
}

func applyEdit(ctx context.Context, project, config string, sets []assignment, unsets []string, dryRun bool) error {
	c, err := open(ctx, dpxapp.Options{AllowStale: true, DeferFetch: true})
	if err != nil {
		return err
	}
	// The values are read even for a pure deletion: they are what the plan is
	// checked against, and what makes each update conditional.
	current, err := c.RevealSecrets(ctx, project, config)
	if err != nil {
		return err
	}
	changes, plan, err := planChanges(current, sets, unsets)
	if err != nil {
		return fmt.Errorf("%s/%s: %w", project, config, err)
	}

	ref := project + "/" + config
	fmt.Fprintf(os.Stderr, "%s\n", ref)
	for _, p := range plan {
		fmt.Fprintf(os.Stderr, "  %s %s\n", p.kind, p.name)
	}
	if len(changes) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to change")
		return nil
	}
	if dryRun {
		fmt.Fprintln(os.Stderr, "dry run: nothing written")
		return nil
	}

	if err := c.Client.ApplyChanges(ctx, project, config, changes); err != nil {
		var apiErr *doppler.APIError
		if errors.As(err, &apiErr) && apiErr.Stale() {
			return fmt.Errorf("%s: a secret changed after dpx read it, so nothing was written; "+
				"see `dpx history %s` and run the command again", ref, ref)
		}
		return err
	}
	// The write does not answer with the log entry it made, so the newest
	// entry is read back. Another writer landing in between would make this
	// the wrong id, which is why the hint is printed alongside what it undoes
	// rather than run automatically by anything.
	logs, err := c.Client.ConfigLogs(ctx, project, config, 1, 1)
	if err != nil || len(logs) == 0 {
		fmt.Fprintln(os.Stderr, "written; see `dpx history "+ref+"` for the log entry")
		return nil
	}
	fmt.Fprintf(os.Stderr, "written as log %s\nundo: dpx rollback %s %s\n", logs[0].ID, ref, logs[0].ID)
	return nil
}
