package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

// lookalikeSetup is a Syncer over a fake Asana whose POST /tasks fails (500) without
// creating anything, and whose project p1 lists the tasks in listed (JSON objects).
func lookalikeSetup(t *testing.T, listed string, posts *atomic.Int32) (*Syncer, *store.Store) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case r.URL.Path == "/tasks" && r.Method == http.MethodPost:
			posts.Add(1)
			w.WriteHeader(500)
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
		case r.URL.Path == "/tasks" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"data":[`+listed+`]}`)
		case r.Method == http.MethodGet && len(r.URL.Path) > 7 && r.URL.Path[:7] == "/tasks/":
			fmt.Fprint(w, `{"data":`+listed+`}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"errors":[{"message":"nf"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	if err := st.SetMeGID(ctx, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	return &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}, st
}

// A create that never reached Asana, while an unrelated task of the same name and due
// date (no start date) made by me just before sits in the project: the unrelated task
// must not be taken for the create (ambiguity it may be, but never a "found" one whose
// start date is then reported dropped, hiding start dates in the workspace).
func TestReconcileDoesNotAdoptALookalikeWithoutStart(t *testing.T) {
	var posts atomic.Int32
	created := time.Now().UTC().Format(time.RFC3339)
	listed := `{"gid":"other","name":"見積もり","due_on":"2026-12-10","start_on":null,"workspace":{"gid":"w1"},
		"created_at":"` + created + `","created_by":{"gid":"me"},"memberships":[{"project":{"gid":"p1"}}]}`
	s, st := lookalikeSetup(t, listed, &posts)
	ctx := context.Background()
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, _ := s.Push(ctx)
	if len(res.Discarded) != 0 {
		t.Errorf("a task nobody created with a start date was reported as dropping it: %+v", res.Discarded)
	}
	if _, hidden, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); hidden {
		t.Error("start dates hidden in w1 because of an unrelated look-alike task")
	}
}

// The same task found twice (two pushes after a lost answer) must not make a second
// create or record the dropped start date twice.
func TestReconcileDroppedStartIsRecordedOnce(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart, f.loseCreate = true, true
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	// Start dates are known to be dropped in w1 (a create without one is taken for it only then).
	if err := st.RecordPlanRefusal(ctx, plan.StartOn, "w1", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 1 {
		t.Errorf("pending discards = %+v, want exactly 1", ds)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Errorf("ops left: %+v", ops)
	}
}

// With a section the create is followed by a move; the dropped start date is still
// reported once, and no entry is left looping.
func TestReconcileDroppedStartWithSection(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	f.ignoreStart, f.loseCreate = true, true
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "見積もり", ProjectGID: "p1", SectionGID: "s1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	// Start dates are known to be dropped in w1 (a create without one is taken for it only then).
	if err := st.RecordPlanRefusal(ctx, plan.StartOn, "w1", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	starts := 0
	for _, d := range res.Discarded {
		if d.Payload["start_on"] != nil { // the fake has no sections: its move is dropped as gone
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("the dropped start date reported %d times: %+v", starts, res.Discarded)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Errorf("ops left: %+v", ops)
	}
}
