package cli

import (
	"context"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/sync"
)

// Store is what the commands do with the local store itself, besides reading and writing
// the cache through the use cases: closing it, copying it for --dry-run, and the account's
// state that logging in changes.
type Store interface {
	// SetInboxWriteForbidden records whether the login may mark notifications.
	SetInboxWriteForbidden(ctx context.Context, forbidden bool) error
	// SyncedMeGID is the account the local data was synced with ("" before the first sync).
	SyncedMeGID(ctx context.Context) (string, error)
	// CopyTo writes a copy of the store to a new file (for --dry-run).
	CopyTo(ctx context.Context, dst string) error
	// SetAttachmentDir is where the attachment files it may delete are (a dry run's own).
	SetAttachmentDir(dir string)
	Close() error
}

// Local is the local store the commands work on, with what internal/wire builds on it:
// the use cases (which the TUI and the MCP server are started with too) and the syncer. It
// comes from Wiring.OpenStore; the CLI does not import the store.
type Local struct {
	Store
	App *usecase.UseCases
	// Syncer is a Syncer over the store for api (nil: offline), with the [sync] settings of cfg.
	Syncer func(api sync.AsanaAPI, cfg settings.Config) *sync.Syncer
}

// StoreOpener turns a function that opens a concrete store (wire.OpenStore), and wire's
// functions that build the use cases and a syncer over it (wire.UseCases, wire.Syncer),
// into Wiring.OpenStore. On an error the Local is nil.
func StoreOpener[S Store](
	open func(ctx context.Context, path string) (S, error),
	useCases func(st S) *usecase.UseCases,
	syncer func(api sync.AsanaAPI, st S, cfg settings.Config) *sync.Syncer,
) func(ctx context.Context, path string) (*Local, error) {
	return func(ctx context.Context, path string) (*Local, error) {
		st, err := open(ctx, path)
		if err != nil {
			return nil, err
		}
		return &Local{
			Store:  st,
			App:    useCases(st),
			Syncer: func(api sync.AsanaAPI, cfg settings.Config) *sync.Syncer { return syncer(api, st, cfg) },
		}, nil
	}
}
