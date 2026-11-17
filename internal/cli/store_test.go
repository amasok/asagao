package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// The store satisfies the commands' port as it is.
var _ Store = (*store.Store)(nil)

// StoreOpener gives a nil Store with the error, not an interface holding a nil *store.Store
// (which would not be nil), and the store itself otherwise.
func TestStoreOpener(t *testing.T) {
	boom := errors.New("boom")
	open := StoreOpener(func(context.Context, string) (*store.Store, error) { return nil, boom })
	if st, err := open(context.Background(), "x"); st != nil || !errors.Is(err, boom) {
		t.Fatalf("on an error: %#v, %v", st, err)
	}
	open = StoreOpener(store.Open)
	st, err := open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok := st.(*store.Store); !ok {
		t.Fatalf("Store = %T", st)
	}
}
