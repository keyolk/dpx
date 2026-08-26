package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/cache"
	"github.com/keyolk/dpx/internal/doppler"
)

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projects [filter]",
		Short: "List projects",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := open(cmd.Context(), dpxapp.Options{})
			if err != nil {
				return err
			}
			projects := c.Store.Projects()
			if len(args) > 0 {
				projects = filterProjects(projects, args[0])
			}
			if flagJSON {
				return writeJSON(projects)
			}
			tw := newTab()
			for _, p := range projects {
				fmt.Fprintf(tw, "%s\t%s\n", p.Name, p.Description)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&flagJSON, "json", false, "output JSON")
	return cmd
}

func newConfigsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "configs <project>",
		Short: "List a project's configs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := open(cmd.Context(), dpxapp.Options{})
			if err != nil {
				return err
			}
			if err := c.LoadProject(cmd.Context(), args[0]); err != nil {
				return err
			}
			cfgs := c.Store.Configs(args[0])
			if flagJSON {
				return writeJSON(cfgs)
			}
			tw := newTab()
			for _, cfg := range cfgs {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", cfg.Environment, cfg.Name, configFlags(cfg))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&flagJSON, "json", false, "output JSON")
	return cmd
}

func configFlags(c doppler.Config) string {
	var out []string
	if c.Root {
		out = append(out, "root")
	}
	if c.Locked {
		out = append(out, "locked")
	}
	if c.Inheriting {
		for _, in := range c.Inherits {
			out = append(out, "inherits:"+in.Config)
		}
	}
	return strings.Join(out, " ")
}

func newSecretsCmd() *cobra.Command {
	var withValues bool
	cmd := &cobra.Command{
		Use:   "secrets <project> <config>",
		Short: "List a config's secret names",
		Long: `List a config's secret names.

Names are served from the local cache and revalidated with an ETag. Values are
only fetched with --values, and are never cached to disk.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, config := args[0], args[1]
			c, err := open(cmd.Context(), dpxapp.Options{})
			if err != nil {
				return err
			}
			if withValues {
				secrets, err := c.RevealSecrets(cmd.Context(), project, config)
				if err != nil {
					return err
				}
				sort.Slice(secrets, func(i, j int) bool { return secrets[i].Name < secrets[j].Name })
				if flagJSON {
					return writeJSON(secrets)
				}
				tw := newTab()
				for _, s := range secrets {
					fmt.Fprintf(tw, "%s\t%s\n", s.Name, s.Computed)
				}
				return tw.Flush()
			}

			if err := c.LoadSecretNames(cmd.Context(), project, config); err != nil {
				return err
			}
			sn := c.Store.SecretNames(project, config)
			if sn == nil {
				return fmt.Errorf("no secrets for %s/%s", project, config)
			}
			sort.Strings(sn.Names)
			if flagJSON {
				return writeJSON(sn.Names)
			}
			for _, n := range sn.Names {
				fmt.Println(n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withValues, "values", false, "fetch values (never cached to disk)")
	cmd.Flags().BoolVar(&flagJSON, "json", false, "output JSON")
	return cmd
}

func newGetCmd() *cobra.Command {
	var raw bool
	cmd := &cobra.Command{
		Use:   "get <project> <config> <secret>",
		Short: "Print one secret's value",
		Long: `Print one secret's value.

The value goes to stdout with no trailing newline decoration, so it composes
with $(...) — which is also why nothing else is ever printed to stdout by this
command; diagnostics go to stderr.`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, config, name := args[0], args[1], args[2]
			c, err := open(cmd.Context(), dpxapp.Options{})
			if err != nil {
				return err
			}
			secrets, err := c.RevealSecrets(cmd.Context(), project, config)
			if err != nil {
				return err
			}
			for _, s := range secrets {
				if s.Name != name {
					continue
				}
				if raw {
					fmt.Print(s.Raw)
				} else {
					fmt.Print(s.Computed)
				}
				return nil
			}
			return fmt.Errorf("no secret %q in %s/%s", name, project, config)
		},
	}
	cmd.Flags().BoolVar(&raw, "raw", false, "print the stored value without resolving references")
	return cmd
}

func newRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh",
		Short: "Revalidate the project listing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := open(cmd.Context(), dpxapp.Options{Refresh: true})
			if err != nil {
				return err
			}
			r := c.Client.Rate()
			fmt.Fprintf(os.Stderr, "%d projects  ·  %d/%d requests left this window\n",
				len(c.Store.Projects()), r.Remaining, r.Limit)
			return nil
		},
	}
}

func newCacheCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect or clear the local cache",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "info",
			Short: "Show what the cache holds",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := open(cmd.Context(), dpxapp.Options{AllowStale: true, CacheOnly: true})
				if err != nil {
					if errors.Is(err, dpxapp.ErrNoCache) {
						fmt.Println("no cache")
						return nil
					}
					return err
				}
				snap := c.Store.Snapshot()
				loaded, secrets := countLoaded(snap)
				tw := newTab()
				fmt.Fprintf(tw, "path\t%s\n", c.Path)
				fmt.Fprintf(tw, "workplace\t%s\n", snap.WorkplaceName)
				fmt.Fprintf(tw, "fetched\t%s\n", snap.FetchedAt.Format("2006-01-02 15:04:05"))
				fmt.Fprintf(tw, "projects\t%d in %d page(s)\n", len(c.Store.Projects()), len(snap.ProjectPages))
				fmt.Fprintf(tw, "expanded\t%d project(s)\n", loaded)
				fmt.Fprintf(tw, "secret names\t%d config(s)\n", secrets)
				return tw.Flush()
			},
		},
		&cobra.Command{
			Use:   "clear",
			Short: "Delete the local cache",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				c, err := open(cmd.Context(), dpxapp.Options{AllowStale: true, DeferFetch: true})
				if err != nil {
					return err
				}
				if err := c.Invalidate(); err != nil {
					return err
				}
				fmt.Fprintln(os.Stderr, "cache cleared")
				return nil
			},
		},
	)
	return cmd
}

func countLoaded(s *cache.Snapshot) (projects, configs int) {
	for _, e := range s.Projects {
		if e.Loaded() {
			projects++
		}
		configs += len(e.Secrets)
	}
	return projects, configs
}

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:                   "completion <bash|zsh|fish>",
		Short:                 "Emit a shell completion script",
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish"},
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return root.GenZshCompletion(os.Stdout)
			case "fish":
				return root.GenFishCompletion(os.Stdout, true)
			}
			return fmt.Errorf("unsupported shell %q", args[0])
		},
	}
}

// ---- helpers --------------------------------------------------------------

func filterProjects(in []doppler.Project, substr string) []doppler.Project {
	substr = strings.ToLower(substr)
	var out []doppler.Project
	for _, p := range in {
		if strings.Contains(strings.ToLower(p.Name), substr) {
			out = append(out, p)
		}
	}
	return out
}

func newTab() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
