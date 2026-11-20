package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/wire"
)

// The store satisfies what the commands do with it themselves.
var _ Store = (*store.Store)(nil)

// StoreOpener gives nil with the error, and otherwise the store with the use cases and a
// syncer over that same store, the syncer taking the settings it is given (#71).
func TestStoreOpener(t *testing.T) {
	boom := errors.New("boom")
	open := StoreOpener(func(context.Context, string) (*store.Store, error) { return nil, boom }, wire.UseCases, wire.Syncer)
	if st, err := open(context.Background(), "x"); st != nil || !errors.Is(err, boom) {
		t.Fatalf("on an error: %#v, %v", st, err)
	}
	var made []*store.Store
	open = StoreOpener(store.Open, func(st *store.Store) *usecase.UseCases {
		made = append(made, st)
		return wire.UseCases(st)
	}, func(api sync.AsanaAPI, st *store.Store, cfg settings.Config) *sync.Syncer {
		made = append(made, st)
		return wire.Syncer(api, st, cfg)
	})
	st, err := open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok := st.Store.(*store.Store); !ok || st.App == nil {
		t.Fatalf("Local = %+v", st)
	}
	cfg := settings.Config{Sync: settings.SyncConfig{CompletedDays: 9}}
	s := st.Syncer(nil, cfg)
	if s.API != nil || s.Cfg.CompletedDays != 9 {
		t.Errorf("Syncer = %+v, want offline with the settings given", s)
	}
	if len(made) != 2 || made[0] != st.Store || made[1] != st.Store || s.Store != sync.LocalStore(made[0]) {
		t.Errorf("the use cases and the syncer were not made over the store opened: %v", made)
	}
}
