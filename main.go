// Command dpx browses Doppler projects, configs and secrets from the terminal.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/keyolk/dpx/internal/cli"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	cli.Version = version

	// Ctrl+C during a fetch should unwind cleanly rather than leave a
	// half-written cache or an alt-screen terminal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "dpx: "+err.Error())
		os.Exit(1)
	}
}
