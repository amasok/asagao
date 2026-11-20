package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// myTasksAsana is fakeAsana with a My Tasks list (utl1: 今日やる m1, あとで m2) and my
// task t1 in m2. listStatus answers the list request with that status instead.
type myTasksAsana struct {
	fakeAsana
	listStatus  int
	listIn      map[string]int // per workspace: answer the list request with that status (#50)
	moveStatus  int            // answer addTask with that status
	mu          stdsync.Mutex
	lists       map[string]int // list requests per workspace
	section     string         // t1's assignee_section
	omitSection bool           // the tasks response leaves assignee_section out
	moved       []string
}

func (f *myTasksAsana) handler() http.Handler {
	base := f.fakeAsana.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/me/user_task_list":
			ws := r.URL.Query().Get("workspace")
			f.mu.Lock()
			if f.lists == nil {
				f.lists = map[string]int{}
			}
			f.lists[ws]++
			status := f.listIn[ws]
			f.mu.Unlock()
			if status == 0 {
				status = f.listStatus
			}
			if status != 0 {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
				return
			}
			fmt.Fprint(w, `{"data":{"gid":"utl1","name":"My Tasks"}}`)
		case r.URL.Path == "/projects/utl1/sections":
			fmt.Fprint(w, `{"data":[{"gid":"m1","name":"今日やる"},{"gid":"m2","name":"あとで"}]}`)
		case r.URL.Path == "/sections/m1/addTask" || r.URL.Path == "/sections/m2/addTask":
			if f.moveStatus != 0 {
				w.WriteHeader(f.moveStatus)
				fmt.Fprint(w, `{"errors":[{"message":"no"}]}`)
				return
			}
			f.moved = append(f.moved, r.URL.Path)
			fmt.Fprint(w, `{"data":{}}`)
		case r.URL.Path == "/tasks" && r.URL.Query().Get("assignee") == "me":
			sec := fmt.Sprintf(`,"assignee_section":{"gid":%q,"name":%q}`, f.section, map[string]string{"m1": "今日やる", "m2": "あとで"}[f.section])
			if f.section == "" {
				sec = `,"assignee_section":null`
			}
			if f.omitSection {
				sec = ""
			}
			fmt.Fprintf(w, `{"data":[{"gid":"t1","name":"My task","assignee":{"gid":"u1","name":"Me"},"workspace":{"gid":"w1"},
				"memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]%s}]}`, sec)
		default:
			base.ServeHTTP(w, r)
		}
	})
}

func setupMyTasks(t *testing.T, f *myTasksAsana) (*Syncer, *store.Store, *time.Time) {
	t.Helper()
	f.t = t
	f.projectTasks = `[]`
	srv := httptest.NewServer(f.handler())
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
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	return &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}, now: func() time.Time { return now }}, st, &now
}

func myTask(t *testing.T, st *store.Store) readmodel.TaskSummary {
	t.Helper()
	d, err := st.GetTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	return d.TaskSummary
}

// A sync reads the sections of My Tasks and the section each of my tasks is in; a move
// between them is queued, survives a pull, and is sent as addTask.
func TestSyncAndMoveMySections(t *testing.T) {
	f := &myTasksAsana{section: "m2"}
	s, st, now := setupMyTasks(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return *now }}
	secs, err := sections.MySections(ctx, "")
	if err != nil || len(secs) != 2 || secs[0].Name != "今日やる" || secs[1].Name != "あとで" {
		t.Fatalf("my sections = %v, %v", secs, err)
	}
	if ms := myTask(t, st).MySection; ms == nil || ms.GID != "m2" || ms.Name != "あとで" {
		t.Fatalf("t1 my section = %+v", ms)
	}
	if err := sections.MoveMine(ctx, "t1", "今日やる"); err != nil {
		t.Fatal(err)
	}
	// A pull before it is sent keeps the move.
	s.PullOnly = true
	*now = now.Add(time.Minute)
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	s.PullOnly = false
	if ms := myTask(t, st).MySection; ms == nil || ms.GID != "m1" {
		t.Fatalf("t1 after a pull = %+v", ms)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || len(f.moved) != 1 || f.moved[0] != "/sections/m1/addTask" {
		t.Fatalf("push = %+v, %v, moved %v", res, err, f.moved)
	}
	// A response without the field leaves it as it is; null clears it.
	f.section = "m1"
	f.omitSection = true
	*now = now.Add(time.Hour)
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if ms := myTask(t, st).MySection; ms == nil || ms.GID != "m1" {
		t.Fatalf("t1 after a response without assignee_section = %+v", ms)
	}
	f.section, f.omitSection = "", false
	*now = now.Add(time.Hour)
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if ms := myTask(t, st).MySection; ms != nil {
		t.Fatalf("t1 after assignee_section null = %+v", ms)
	}
}

// Asana answering 402 for My Tasks hides its sections; other failures keep trying.
func TestMySectionsPaymentRequired(t *testing.T) {
	f := &myTasksAsana{section: "m2", listStatus: http.StatusNotFound}
	s, st, _ := setupMyTasks(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatalf("a 404 for the list fails the sync: %v", err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.MySection, "w1"); ok {
		t.Fatal("a 404 hid the feature")
	}
	f.listStatus = http.StatusPaymentRequired
	if err := s.syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	sections := &app.Sections{Store: st, Clock: s.now}
	if secs, _ := sections.MySections(ctx, ""); len(secs) != 0 {
		t.Errorf("my sections after a 402 = %v", secs)
	}
	if err := sections.MoveMine(ctx, "t1", "あとで"); !app.IsPlanUnavailable(err) {
		t.Errorf("MoveMine after a 402: err = %v", err)
	}
}
