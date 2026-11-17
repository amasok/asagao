package archtest

import (
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

func callsOf(t *testing.T, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	return forbiddenCalls(fset, f)
}

// A store write is still a store write when the variable that holds the store is named
// like a package the file imports (a store in a variable called "store" or "sync").
func TestForbiddenCallsStoreWriteOnVariableNamedLikeAnImport(t *testing.T) {
	got := callsOf(t, `package x

import (
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

func f(sync *store.Store) {
	_ = sync.MoveTaskLocal(ctx)
}
`)
	want := []string{"x.go:9: .MoveTaskLocal"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Writes through a struct field, a call result, an index and a method value are found.
func TestForbiddenCallsStoreWriteShapes(t *testing.T) {
	got := callsOf(t, `package x

func f() {
	_ = a.b.c.SetFollowerLocal(ctx)
	_ = get().RemoveTagLocal(ctx)
	_ = stores[0].AddTagLocal(ctx)
	_ = (*st).ApplyTagEdit(ctx)
	g := st.SetDependencyLocal
	defer st.SetCustomFieldLocal(ctx)
	go st.MarkNotificationsLocal(ctx)
}
`)
	if len(got) != 7 {
		t.Errorf("got %d: %v", len(got), got)
	}
}

// Names that are not store writes are left alone.
func TestForbiddenCallsIgnoresNonWrites(t *testing.T) {
	got := callsOf(t, `package x

import "github.com/amasok/asagao/internal/sync"

func f() {
	_ = sync.KeepLocal
	_ = st.Local()
	_ = st.local()
	_ = st.moveTaskLocal()
	_ = st.ApplyTagEdits()
	_ = st.LocalGID()
}
`)
	if len(got) != 0 {
		t.Errorf("got %v", got)
	}
}
