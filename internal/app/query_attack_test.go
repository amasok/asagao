package app_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// Every store read Query gives (QueryStore's methods) returns exactly what the store's method of the same name returns, for
// the same arguments: all results in the same order and the same errors, with a live and
// with a cancelled context, on a store with data in it (and with offline entries queued).
func TestQueryMatchesStoreForEveryMethod(t *testing.T) {
	st := resolveStore(t)
	queue(t, st, store.OpUpdateTask)
	queue(t, st, store.OpCreateTask)
	q := app.Query{QueryStore: st}
	qv, sv := reflect.ValueOf(q), reflect.ValueOf(st)
	qt := reflect.TypeOf((*app.QueryStore)(nil)).Elem() // Query's own reads are tested on their own

	live := context.Background()
	cancelled, cancel := context.WithCancel(live)
	cancel()
	timeArg := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

	argsFor := func(m reflect.Method, ctx context.Context, s string) []reflect.Value {
		var in []reflect.Value
		for i := 1; i < m.Type.NumIn(); i++ { // 0 is the receiver
			switch at := m.Type.In(i); {
			case at == reflect.TypeOf((*context.Context)(nil)).Elem():
				in = append(in, reflect.ValueOf(ctx))
			case at.Kind() == reflect.String:
				in = append(in, reflect.ValueOf(s))
			case at == reflect.TypeOf([]string(nil)):
				in = append(in, reflect.ValueOf([]string{s, "t2", "nope"}))
			case at.Kind() == reflect.Bool:
				in = append(in, reflect.ValueOf(s == "me"))
			case at.Kind() == reflect.Int:
				in = append(in, reflect.ValueOf(2))
			case at == reflect.TypeOf(time.Time{}):
				in = append(in, reflect.ValueOf(timeArg))
			case at == reflect.TypeOf(readmodel.FilterSpec{}):
				in = append(in, reflect.ValueOf(readmodel.FilterSpec{Assignee: s, Project: s, Completion: "open"}))
			default:
				in = append(in, reflect.Zero(at))
			}
		}
		return in
	}
	n := 0
	for i := 0; i < qt.NumMethod(); i++ {
		m, _ := qv.Type().MethodByName(qt.Method(i).Name)
		sm := sv.MethodByName(m.Name)
		if !sm.IsValid() {
			t.Errorf("store has no %s", m.Name)
			continue
		}
		for _, ctx := range []context.Context{live, cancelled} {
			for _, s := range []string{"t1", "p1", "me", "", "nope", "t1a"} {
				in := argsFor(m, ctx, s)
				got := qv.MethodByName(m.Name).Call(in[0:])
				want := sm.Call(in)
				if len(got) != len(want) {
					t.Fatalf("%s: %d results, store %d", m.Name, len(got), len(want))
				}
				for k := range got {
					g, w := got[k].Interface(), want[k].Interface()
					ge, isErr := g.(error)
					if we, ok := w.(error); isErr && ok {
						if ge.Error() != we.Error() || reflect.TypeOf(ge) != reflect.TypeOf(we) {
							t.Errorf("%s(%q, ctx err %v) error: %v, store %v", m.Name, s, ctx.Err(), ge, we)
						}
					} else if !reflect.DeepEqual(g, w) {
						t.Errorf("%s(%q, ctx err %v) result %d: %+v, store %+v", m.Name, s, ctx.Err(), k, g, w)
					}
				}
				n++
			}
		}
	}
	if qt.NumMethod() < 25 || n < 300 {
		t.Errorf("only %d methods / %d calls compared", qt.NumMethod(), n)
	}
}
