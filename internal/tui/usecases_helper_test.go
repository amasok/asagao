package tui

import (
	stdsync "sync"

	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/wire"
)

// localStore is what a test hands the TUI as its cache: *store.Store, or a wrapper of one.
type localStore interface {
	wire.UseCaseStore
	sync.LocalStore
}

// testStores remembers the store under each Deps.App onStore made, for storeOf.
var testStores stdsync.Map // *app.UseCases → localStore

// onStore is d over st: the use cases wire makes over it and a syncer for what is cached,
// as cli gives the TUI.
func onStore(st localStore, d Deps) Deps {
	d.App = wire.UseCases(st)
	d.LocalSyncer = func() *sync.Syncer { return &sync.Syncer{Store: st} }
	testStores.Store(d.App, st)
	return d
}

// storeOf is the store m's use cases were made over by onStore.
func storeOf(m *Model) *store.Store {
	v, _ := testStores.Load(m.deps.App)
	st, ok := v.(*store.Store)
	if !ok {
		panic("storeOf: the model's Deps were not made by onStore over a *store.Store")
	}
	return st
}
