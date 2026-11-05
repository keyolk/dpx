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

// parseAssignments resolves every argument to a name and value.
func parseAssignments(args []string, stdin *os.File) ([]dpxapp.Assignment, error) {
	interactive := term.IsTerminal(stdin.Fd())
	var out []dpxapp.Assignment
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
		out = append(out, dpxapp.Assignment{Name: name, Value: value})
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

func applyEdit(ctx context.Context, project, config string, sets []dpxapp.Assignment, unsets []string, dryRun bool) error {
	c, err := open(ctx, dpxapp.Options{AllowStale: true, DeferFetch: true})
	if err != nil {
		return err
	}
	ref := project + "/" + config
	res, err := c.Edit(ctx, project, config, sets, unsets, dryRun)
	if res.Plan != nil {
		fmt.Fprintf(os.Stderr, "%s\n", ref)
		for _, p := range res.Plan {
			fmt.Fprintf(os.Stderr, "  %s %s\n", p.Kind, p.Name)
		}
	}
	if errors.Is(err, dpxapp.ErrStale) {
		return fmt.Errorf("%s: %w; see `dpx history %s` and run the command again", ref, err, ref)
	}
	if err != nil {
		return err
	}
	switch {
	case !res.Changed:
		fmt.Fprintln(os.Stderr, "nothing to change")
	case dryRun:
		fmt.Fprintln(os.Stderr, "dry run: nothing written")
	case res.LogID == "":
		fmt.Fprintln(os.Stderr, "written; see `dpx history "+ref+"` for the log entry")
	default:
		fmt.Fprintf(os.Stderr, "written as log %s\nundo: dpx rollback %s %s\n", res.LogID, ref, res.LogID)
	}
	return nil
}
