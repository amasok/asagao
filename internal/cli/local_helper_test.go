package cli

import (
	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/wire"
)

// localFor is st as Wiring.OpenStore gives it to the commands (StoreOpener with wire's
// functions).
func localFor(st *store.Store) *Local {
	return &Local{Store: st, App: wire.UseCases(st), Syncer: func(api sync.AsanaAPI, cfg settings.Config) *sync.Syncer {
		return wire.Syncer(api, st, cfg)
	}}
}
