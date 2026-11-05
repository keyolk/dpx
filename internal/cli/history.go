package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/doppler"
)

// detailWorkers bounds the per-entry fetches history makes. The listing
// carries no diff, so naming what each entry changed costs one request per
// entry; eight at once keeps that fast without spending the rate-limit window
// in a burst.
const detailWorkers = 8

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
						l = redact(l)
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
			logs, err := loadHistory(cmd.Context(), c.Client, project, config, window)
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
				logs[i] = redact(logs[i])
			}
			if flagJSON {
				return writeJSON(logs)
			}
			tw := newTab()
			for _, l := range logs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					l.ID, l.CreatedAt.Local().Format("2006-01-02 15:04"), who(l.User), summarize(l))
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
				target.ID, target.CreatedAt.Local().Format("2006-01-02 15:04"), who(target.User))
			for _, d := range target.Diff {
				fmt.Fprintf(os.Stderr, "  %s %s\n", inverse(d.Kind()), d.Name)
			}
			if dryRun {
				fmt.Fprintln(os.Stderr, "dry run: nothing written")
				return nil
			}

			l, err := c.Client.Rollback(cmd.Context(), project, config, id)
			if err != nil {
				return err
			}
			ref2 := project + "/" + config
			fmt.Fprintf(os.Stderr, "written as log %s\nundo: dpx rollback %s %s\n", l.ID, ref2, l.ID)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "show what would be reverted without writing")
	return cmd
}

// loadHistory lists the newest n entries and fills in each one's diff.
func loadHistory(ctx context.Context, cl *doppler.Client, project, config string, n int) ([]doppler.ConfigLog, error) {
	const pageSize = 100
	var logs []doppler.ConfigLog
	for page := 1; len(logs) < n; page++ {
		got, err := cl.ConfigLogs(ctx, project, config, page, min(pageSize, n))
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
	sem := make(chan struct{}, detailWorkers)
	var wg sync.WaitGroup
	for i := range logs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			full, err := cl.ConfigLog(ctx, project, config, logs[i].ID)
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

func touching(logs []doppler.ConfigLog, key string) []doppler.ConfigLog {
	var out []doppler.ConfigLog
	for _, l := range logs {
		for _, d := range l.Diff {
			if d.Name == key {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// redact drops the values from a log entry, keeping only which side existed —
// enough to tell a creation from a change from a deletion.
func redact(l doppler.ConfigLog) doppler.ConfigLog {
	hidden := ""
	out := l
	out.Diff = make([]doppler.LogDiff, len(l.Diff))
	for i, d := range l.Diff {
		r := doppler.LogDiff{Name: d.Name, AddedVis: d.AddedVis, RemovedVis: d.RemovedVis}
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

func summarize(l doppler.ConfigLog) string {
	var parts []string
	for _, d := range l.Diff {
		parts = append(parts, d.Kind()+d.Name)
	}
	s := strings.Join(parts, " ")
	if s == "" {
		// No secret-level diff: a rename, a lock, an inheritance change. The
		// API's own sentence is the only description there is.
		s = l.Text
	}
	if l.Rollback {
		s += "  (rollback)"
	}
	return s
}

func printLog(w io.Writer, l doppler.ConfigLog, withValues bool) {
	fmt.Fprintf(w, "id    %s\n", l.ID)
	fmt.Fprintf(w, "time  %s\n", l.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "user  %s\n", who(l.User))
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

func who(u doppler.LogUser) string {
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

// inverse is what reverting a change does to each secret.
func inverse(kind string) string {
	switch kind {
	case "+":
		return "-"
	case "-":
		return "+"
	}
	return kind
}
