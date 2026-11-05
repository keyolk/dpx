package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/doppler"
)

func newHistoryCmd() *cobra.Command {
	var (
		limit      int
		scan       int
		logID      string
		withValues bool
	)
	cmd := &cobra.Command{
		Use:     "history <project>/<config> [KEY]",
		Aliases: []string{"log"},
		Short:   "Show a config's change history",
		Long: `Show a config's change history, newest first, with the secrets each change
touched (+ created, ~ changed, - deleted).

With KEY, only changes that touched it are listed, looking back through the
last --scan entries.

--log <id> shows one entry in full. Values are hidden unless --values is
given, since a log entry holds both the old and the new value.

  dpx history my-app/prd
  dpx history my-app/prd DB_PASSWORD
  dpx history my-app/prd --log Xa3k... --values`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest, err := splitRef(args, 2)
			if err != nil {
				return err
			}
			if len(rest) > 1 {
				return fmt.Errorf("unexpected %q", rest[1])
			}
			c, err := open(cmd.Context(), dpxapp.Options{AllowStale: true, DeferFetch: true})
			if err != nil {
				return err
			}
			project, config := ref[0], ref[1]

			if logID != "" {
				l, err := c.Client.ConfigLog(cmd.Context(), project, config, logID)
				if err != nil {
					return err
				}
				if flagJSON {
					if !withValues {
						l = l.Redacted()
					}
					return writeJSON(l)
				}
				printLog(os.Stdout, l, withValues)
				return nil
			}

			var key string
			if len(rest) == 1 {
				key = rest[0]
			}
			window := limit
			if key != "" {
				window = scan
			}
			logs, err := c.History(cmd.Context(), project, config, window)
			if err != nil {
				return err
			}
			if key != "" {
				logs = touching(logs, key)
				if len(logs) > limit {
					logs = logs[:limit]
				}
			}
			for i := range logs {
				logs[i] = logs[i].Redacted()
			}
			if flagJSON {
				return writeJSON(logs)
			}
			tw := newTab()
			for _, l := range logs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					l.ID, l.CreatedAt.Local().Format("2006-01-02 15:04"), l.User.Label(), l.Summary())
			}
			return tw.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "entries to show")
	cmd.Flags().IntVar(&scan, "scan", 100, "with KEY: entries to look back through")
	cmd.Flags().StringVar(&logID, "log", "", "show one log entry in full")
	cmd.Flags().BoolVar(&withValues, "values", false, "with --log: show old and new values")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "output JSON")
	return cmd
}

func newRollbackCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "rollback <project>/<config> <log-id>",
		Short: "Revert the change a log entry recorded",
		Long: `Revert the change a log entry recorded.

The revert is itself a new log entry, so a rollback can be rolled back; its id
is printed. --dry-run shows which secrets the revert would touch.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, rest, err := splitRef(args, 2)
			if err != nil {
				return err
			}
			if len(rest) != 1 {
				return fmt.Errorf("want one log id")
			}
			project, config, id := ref[0], ref[1], rest[0]
			c, err := open(cmd.Context(), dpxapp.Options{AllowStale: true, DeferFetch: true})
			if err != nil {
				return err
			}

			target, err := c.Client.ConfigLog(cmd.Context(), project, config, id)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s/%s: reverting %s (%s, %s)\n", project, config,
				target.ID, target.CreatedAt.Local().Format("2006-01-02 15:04"), target.User.Label())
			for _, d := range target.Diff {
				fmt.Fprintf(os.Stderr, "  %s %s\n", doppler.Inverse(d.Kind()), d.Name)
			}
			if dryRun {
				fmt.Fprintln(os.Stderr, "dry run: nothing written")
				return nil
			}

			l, err := c.Rollback(cmd.Context(), project, config, id)
			if err != nil && l.ID == "" {
				return err
			}
			ref2 := project + "/" + config
			fmt.Fprintf(os.Stderr, "written as log %s\nundo: dpx rollback %s %s\n", l.ID, ref2, l.ID)
			return err
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show what would be reverted without writing")
	return cmd
}

func touching(logs []doppler.ConfigLog, key string) []doppler.ConfigLog {
	var out []doppler.ConfigLog
	for _, l := range logs {
		if l.Touches(key) {
			out = append(out, l)
		}
	}
	return out
}

func printLog(w io.Writer, l doppler.ConfigLog, withValues bool) {
	fmt.Fprintf(w, "id    %s\n", l.ID)
	fmt.Fprintf(w, "time  %s\n", l.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "user  %s\n", l.User.Label())
	fmt.Fprintf(w, "text  %s\n", l.Text)
	if l.Rollback {
		fmt.Fprintln(w, "      (rollback)")
	}
	for _, d := range l.Diff {
		fmt.Fprintf(w, "%s %s\n", d.Kind(), d.Name)
		if !withValues {
			continue
		}
		if d.HasRemoved() {
			fmt.Fprintf(w, "    - %s\n", shownValue(d.Removed, d.RemovedRestricted()))
		}
		if d.HasAdded() {
			fmt.Fprintf(w, "    + %s\n", shownValue(d.Added, d.AddedRestricted()))
		}
	}
}

func shownValue(v *string, restricted bool) string {
	if v == nil || restricted {
		return "(restricted)"
	}
	return indentValue(*v)
}

// indentValue keeps a multi-line value under its marker instead of spilling
// into the left margin where the next secret's name goes.
func indentValue(v string) string {
	return strings.ReplaceAll(v, "\n", "\n      ")
}
