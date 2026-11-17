package sync

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// depServer is a fake Asana with tasks t1 and t2; gone lists tasks deleted in Asana.
type depServer struct {
	mu    stdsync.Mutex
	calls []string
	gone  map[string]bool
}

func (d *depServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(rw, `{"data":{"gid":"me","name":"Me"}}`)
	case len(parts) >= 2 && parts[0] == "tasks" && d.gone[parts[1]]:
		rw.WriteHeader(http.StatusNotFound)
		fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`)
	case r.Method == http.MethodPost && len(parts) == 3:
		b, _ := io.ReadAll(r.Body)
		d.calls = append(d.calls, r.URL.Path+" "+string(b))
		if strings.Contains(string(b), "gone") { // a dependency deleted in Asana
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`)
			return
		}
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 2:
		fmt.Fprintf(rw, `{"data":{"gid":%q,"name":"x"}}`, parts[1])
	default:
		rw.WriteHeader(http.StatusNotFound)
	}
}

func setupDeps(t *testing.T) (*depServer, *Syncer, *store.Store) {
	t.Helper()
	ds := &depServer{gone: map[string]bool{}}
	srv := httptest.NewServer(ds)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	if err := st.SetSyncState(ctx, store.KeyMeGID, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A"}, {GID: "t2", Name: "B"}, {GID: "gone", Name: "C"}}); err != nil {
		t.Fatal(err)
	}
	return ds, &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}, st
}

func TestPushDependencies(t *testing.T) {
	ds, s, st := setupDeps(t)
	ctx := context.Background()
	if err := st.SetDependencyLocal(ctx, "t1", "t2", true, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if err := st.SetDependencyLocal(ctx, "t1", "t2", false, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	want := []string{`/tasks/t1/addDependencies {"data":{"dependencies":["t2"]}}`, `/tasks/t1/removeDependencies {"data":{"dependencies":["t2"]}}`}
	if strings.Join(ds.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %q", ds.calls)
	}
}

func TestPushDependencyOnDeletedTaskIsDropped(t *testing.T) {
	_, s, st := setupDeps(t)
	ctx := context.Background()
	if err := st.SetDependencyLocal(ctx, "t1", "gone", true, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil || len(d.Dependencies) != 0 { // the local link is undone, t1 kept
		t.Fatalf("t1 = %+v, %v", d, err)
	}
}
