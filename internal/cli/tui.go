package cli

import (
	"context"

	dpxapp "github.com/keyolk/dpx/internal/app"
	"github.com/keyolk/dpx/internal/tui"
)

func runTUI(ctx context.Context) error {
	// AllowStale + DeferFetch is what lets the browser paint the cached
	// listing immediately and revalidate in a Bubble Tea command, instead of
	// showing a blank terminal while the network answers.
	c, err := open(ctx, dpxapp.Options{
		AllowStale: true,
		DeferFetch: true,
		Quiet:      true,
	})
	if err != nil {
		return err
	}
	return tui.Run(ctx, c)
}
