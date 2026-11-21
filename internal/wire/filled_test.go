package wire

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/config"
)

// What the wiring builds has every port filled: a field of the use cases (app.Ports, and
// each use case a UseCases method gives) or of the sync engine (Syncer and each of its
// parts) that is a nil interface, pointer or func is a port forgotten when it was added,
// which would only fail (a nil dereference) when that path runs. The walk goes through the
// structs of internal/app and internal/sync, not into the adapters (the store, the client).

// mayBeNil are the fields left nil on purpose ("type.field" as reflect prints the type).
var mayBeNil = map[string]string{
	"sync.env.now":          "nil is time.Now (replaced in tests only)",
	"sync.env.sleep":        "nil waits for real (replaced in tests only)",
	"sync.Syncer.now":       "nil is time.Now (replaced in tests only)",
	"sync.Syncer.sleep":     "nil waits for real (replaced in tests only)",
	"sync.Syncer.Progress":  "optional: the entry points that show progress set it",
	"sync.Puller.Progress":  "optional (Syncer.Progress)",
	"sync.Syncer.Download":  "nil is a plain HTTP GET",
	"sync.Details.Download": "nil is a plain HTTP GET (Syncer.Download)",
}

// wiredPackages are the packages whose structs the walk goes into.
var wiredPackages = []string{"/internal/app", "/internal/sync"}

func TestWiringFillsEveryPort(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore(ctx, filepath.Join(t.TempDir(), "store.db")) // never the real cache
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var found []string
	allowed := func(string, reflect.Type) bool { return false }

	uc := UseCases(st)
	found = append(found, nilPorts("UseCases", reflect.ValueOf(uc), allowed)...)
	parts, called := eachPart("UseCases", reflect.ValueOf(uc))
	found = append(found, parts...)
	wantCalled(t, called, "Tasks", "Comments", "Copies", "Outbox", "PushNow", "GitLink", "QuickAdd", "Query", "ViewState")

	api := newClient(http.DefaultClient, "http://127.0.0.1:1") // never sent anything
	s := Syncer(api, st, config.Default())
	found = append(found, nilPorts("Syncer", reflect.ValueOf(s), allowed)...)
	parts, called = eachPart("Syncer", reflect.ValueOf(s))
	found = append(found, parts...)
	wantCalled(t, called, "Pusher", "Puller", "Details", "Goals", "Templates", "Search", "Projects")

	if len(found) > 0 {
		t.Errorf("ports the wiring leaves nil (fill them, or list them in mayBeNil with the reason):\n\t%s", strings.Join(found, "\n\t"))
	}
}

// eachPart calls every method of v that hands out a use case or a part of the sync engine
// (one that does not take a context, nor returns an error) and walks what it gives. The
// arguments are filled in (a func, a non-empty string, an empty map …), except interfaces,
// which are nil: a field of such a type is what the caller gave, and may be nil.
func eachPart(name string, v reflect.Value) (found, called []string) {
	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()
	for i := range v.NumMethod() {
		m, mt := v.Method(i), v.Type().Method(i)
		ft := m.Type()
		var given []reflect.Type
		args := make([]reflect.Value, ft.NumIn())
		skip := ft.IsVariadic() || ft.NumOut() != 1 || ft.Out(0).Implements(errType)
		for j := range ft.NumIn() {
			in := ft.In(j)
			if in == ctxType {
				skip = true
				break
			}
			args[j] = argument(in)
			if in.Kind() == reflect.Interface {
				given = append(given, in)
			}
		}
		if skip {
			continue
		}
		called = append(called, mt.Name)
		out := m.Call(args)[0]
		allowed := func(_ string, ft reflect.Type) bool { return slices.Contains(given, ft) }
		found = append(found, nilPorts(name+"."+mt.Name+"()", out, allowed)...)
	}
	return found, called
}

// wantCalled fails unless eachPart called each of names (so that a change of their
// signatures does not take them out of the walk unseen).
func wantCalled(t *testing.T, called []string, names ...string) {
	t.Helper()
	for _, n := range names {
		if !slices.Contains(called, n) {
			t.Errorf("%s() was not walked (called: %v)", n, called)
		}
	}
}

// argument is a value of type t for a call: not nil, except for an interface.
func argument(t reflect.Type) reflect.Value {
	switch t.Kind() {
	case reflect.Func:
		return reflect.MakeFunc(t, func([]reflect.Value) []reflect.Value {
			out := make([]reflect.Value, t.NumOut())
			for i := range out {
				out[i] = reflect.Zero(t.Out(i))
			}
			return out
		})
	case reflect.Map:
		return reflect.MakeMap(t)
	case reflect.String:
		return reflect.ValueOf("x").Convert(t)
	case reflect.Pointer:
		return reflect.New(t.Elem())
	}
	return reflect.Zero(t)
}

// nilPorts returns the path of each nil interface, pointer or func reachable from v
// through the structs of wiredPackages, but those of mayBeNil and those allowed says may
// be nil (by path and type).
func nilPorts(path string, v reflect.Value, allowed func(path string, t reflect.Type) bool) []string {
	var found []string
	seen := map[uintptr]bool{}
	var walk func(path string, v reflect.Value)
	walk = func(path string, v reflect.Value) {
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Func:
			if v.IsNil() {
				if !allowed(path, v.Type()) {
					found = append(found, path+" ("+v.Type().String()+")")
				}
				return
			}
			if v.Kind() == reflect.Func {
				return
			}
			if v.Kind() == reflect.Pointer {
				if seen[v.Pointer()] {
					return
				}
				seen[v.Pointer()] = true
			}
			walk(path, v.Elem())
		case reflect.Struct:
			if !wired(v.Type()) {
				return
			}
			for i := range v.NumField() {
				f := v.Type().Field(i)
				if _, ok := mayBeNil[v.Type().String()+"."+f.Name]; ok {
					continue
				}
				walk(path+"."+f.Name, v.Field(i))
			}
		}
	}
	walk(path, v)
	return found
}

// wired reports whether t is a struct of wiredPackages.
func wired(t reflect.Type) bool {
	for _, p := range wiredPackages {
		if strings.HasSuffix(t.PkgPath(), p) {
			return true
		}
	}
	return false
}

// The walk finds what it is for.
func TestNilPortsFindsThem(t *testing.T) {
	ports := app.Ports{}
	got := nilPorts("Ports", reflect.ValueOf(ports), func(string, reflect.Type) bool { return false })
	if len(got) != reflect.TypeFor[app.Ports]().NumField() || got[0] != "Ports.Tasks (app.TaskStore)" {
		t.Errorf("nilPorts(empty Ports) = %v", got)
	}
	uc := app.NewUseCases(app.Ports{}, nil)
	clock := app.Clock(time.Now)
	got = nilPorts("Tasks", reflect.ValueOf(uc.Tasks(clock, nil)), func(_ string, ft reflect.Type) bool {
		return ft == reflect.TypeFor[app.NameFinder]()
	})
	if strings.Join(got, ",") != "Tasks.Store (app.TaskStore),Tasks.Text (app.RichText)" {
		t.Errorf("nilPorts(Tasks) = %v", got)
	}
}
