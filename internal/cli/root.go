// Package cli implements dpx's command-line interface.
package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	dpxapp "github.com/keyolk/dpx/internal/app"
)

// Global flags shared by every command.
var (
	flagConfig  string
	flagRefresh bool
	flagTTL     time.Duration
	flagJSON    bool
)

// Version is set at build time via -ldflags.
var Version = "dev"

// ExecuteContext runs the root command with a cancellable context, so SIGINT
// unwinds an in-flight fetch instead of killing the process mid-write.
func ExecuteContext(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "dpx",
		Short: "Doppler project and secret browser",
		Long: `dpx browses Doppler projects, configs and secrets from the terminal.

Authentication comes from a pass entry named in ~/.config/dpx/config.yaml, or
failing that from $DOPPLER_TOKEN or the token store the official doppler CLI
writes to ~/.doppler/.doppler.yaml — so a machine already logged in needs no
setup, and one that keeps its token in pass never writes it to disk in the
clear.

Listings are cached on disk and revalidated with ETags: a refresh that finds
nothing changed transfers no data. Secret values are fetched on demand and are
never written to disk.

Running dpx with no subcommand opens the interactive browser.`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context())
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flagConfig, "config", "", "path to .doppler.yaml (default ~/.doppler/.doppler.yaml)")
	pf.BoolVar(&flagRefresh, "refresh", false, "revalidate before running")
	pf.DurationVar(&flagTTL, "ttl", dpxapp.DefaultTTL, "how long a cached listing is used without revalidation")

	root.AddCommand(
		newProjectsCmd(),
		newConfigsCmd(),
		newSecretsCmd(),
		newGetCmd(),
		newOpenCmd(),
		newRefreshCmd(),
		newCacheCmd(),
		newCompletionCmd(root),
	)
	return root
}

// open builds the shared app context using the global flags.
func open(ctx context.Context, opts dpxapp.Options) (*dpxapp.Context, error) {
	opts.ConfigPath = flagConfig
	if !opts.Refresh {
		opts.Refresh = flagRefresh
	}
	if opts.TTL == 0 {
		opts.TTL = flagTTL
	}
	return dpxapp.Open(ctx, opts)
}
