package cli

import (
	"context"

	"github.com/amasok/asagao/internal/mcpserver"
	"github.com/amasok/asagao/internal/tui"
)

// Store is the local cache as the commands use it: what the TUI and the MCP server get
// (they are started with it), and what the commands write themselves. Store
// satisfies it; it comes from Wiring.OpenStore, and the CLI does not import the store.
type Store interface {
	tui.Store
	mcpserver.Store

	SetInboxWriteForbidden(ctx context.Context, forbidden bool) error
	// CopyTo writes a copy of the store to a new file (for --dry-run).
	CopyTo(ctx context.Context, dst string) error
	Close() error
}

// StoreOpener turns a function that opens a concrete store (wire.OpenStore) into
// Wiring.OpenStore. On an error the Store is nil, not an interface holding a nil pointer.
func StoreOpener[S Store](open func(ctx context.Context, path string) (S, error)) func(ctx context.Context, path string) (Store, error) {
	return func(ctx context.Context, path string) (Store, error) {
		st, err := open(ctx, path)
		if err != nil {
			return nil, err
		}
		return st, nil
	}
}
