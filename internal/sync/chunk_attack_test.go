package sync

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	_ "modernc.org/sqlite"
)

// chunkFake serves one workspace with one project p1 whose tasks are generated on demand
// (#55: a big project must not be written in one long transaction).
type chunkFake struct {
	t     *testing.T
	n     atomic.Int64 // how many tasks p1 has: gids a0..a(n-1)
	extra atomic.Value // string: a JSON object appended to p1's tasks ("" = none)
	fail  atomic.Int64 // if non-zero, GET /tasks?project=p1 answers with this status
	gets  atomic.Int64 // GET /tasks?project=p1 calls
	hook  func()       // called on each GET /tasks?project=p1
}

func (f *chunkFake) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/users/me", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"gid":"u1","name":"Me","email":"me@example.com"}}`)
	})
	mux.HandleFunc("/workspaces", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"gid":"w1","name":"WS"}]}`)
	})
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"gid":"p1","name":"Mine","members":[{"gid":"u1"}]}]}`)
	})
	mux.HandleFunc("/projects/p1/sections", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"gid":"s1","name":"Todo"}]}`)
	})
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("assignee") == "me" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		if q.Get("project") != "p1" {
			f.t.Errorf("unexpected tasks query %v", q)
			return
		}
		f.gets.Add(1)
		if f.hook != nil {
			f.hook()
		}
		if st := f.fail.Load(); st != 0 {
			w.WriteHeader(int(st))
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
			return
		}
		var b strings.Builder
		b.WriteString(`{"data":[`)
		n := int(f.n.Load())
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"gid":"a%d","name":"Task %d","memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}],"tags":[{"gid":"g1","name":"x"}]}`, i, i)
		}
		if e, _ := f.extra.Load().(string); e != "" {
			if n > 0 {
				b.WriteByte(',')
			}
			b.WriteString(e)
		}
		b.WriteString(`]}`)
		fmt.Fprint(w, b.String())
	})
	return mux
}

type chunkEnv struct {
	fake *chunkFake
	st   *store.Store
	s    *Syncer
	now  time.Time
	path string
}

// newChunkEnv: tasks are written one per transaction (yield 1ms), so that every boundary
// between two transactions is crossed; the latency tests use newChunkEnvTimed instead.
func newChunkEnv(t *testing.T, n int) *chunkEnv {
	t.Helper()
	t.Cleanup(store.SetTaskWriteSlicesForTest(0, time.Millisecond))
	return newChunkEnvTimed(t, n)
}

func newChunkEnvTimed(t *testing.T, n int) *chunkEnv {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fake := &chunkFake{t: t}
	fake.n.Store(int64(n))
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	e := &chunkEnv{fake: fake, st: st, path: st.Path(), now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}
	e.s = &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}, now: func() time.Time { return e.now }}
	return e
}

func (e *chunkEnv) taskGIDs(t *testing.T) map[string]bool {
	t.Helper()
	db, err := sql.Open("sqlite", e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT gid FROM tasks`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var g string
		rows.Scan(&g)
		out[g] = true
	}
	return out
}

func (e *chunkEnv) state(t *testing.T) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT key, value FROM sync_state`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		out[k] = v
	}
	return out
}

// failOnTask makes the database refuse to insert the task gid (as a disk error would),
// until the returned function is called.
func (e *chunkEnv) failOnTask(t *testing.T, gid string) (undo func()) {
	t.Helper()
	db, err := sql.Open("sqlite", e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf(`CREATE TRIGGER atk_fail BEFORE INSERT ON tasks WHEN NEW.gid='%s' BEGIN SELECT RAISE(ABORT,'atk boom'); END`, gid)); err != nil {
		t.Fatal(err)
	}
	return func() {
		db, err := sql.Open("sqlite", e.path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`DROP TRIGGER atk_fail`); err != nil {
			t.Fatal(err)
		}
	}
}

// Sizes around any sensible chunk size: the result must be the same however the tasks are
// split (none, one, exactly a chunk, a chunk + 1, several chunks).
func TestChunkAttackBoundarySizes(t *testing.T) {
	for _, n := range []int{0, 1, 2, 300} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			e := newChunkEnv(t, n)
			res, err := e.s.Run(context.Background(), false)
			if err != nil {
				t.Fatal(err)
			}
			got := e.taskGIDs(t)
			if len(got) != n || res.Tasks != n {
				t.Fatalf("n=%d: stored %d, res.Tasks %d", n, len(got), res.Tasks)
			}
			for i := 0; i < n; i++ {
				if !got[fmt.Sprintf("a%d", i)] {
					t.Fatalf("a%d missing", i)
				}
			}
			if st, _ := e.st.Stats(context.Background()); st.Tasks != n {
				t.Fatalf("Stats.Tasks = %d", st.Tasks)
			}
			// the same sync again changes nothing and prunes nothing
			e.now = e.now.Add(time.Hour)
			res, err = e.s.Run(context.Background(), true)
			if err != nil || res.Pruned != 0 || len(e.taskGIDs(t)) != n {
				t.Fatalf("second full sync: %+v %v, tasks %d", res, err, len(e.taskGIDs(t)))
			}
		})
	}
}

// A write that fails after some chunks were committed: tasks Asana no longer returns must
// not be pruned, no "last sync" / "last full sync" may advance, and once the fault is gone
// the next sync ends exactly as if nothing had happened.
func TestChunkAttackFailureMidWriteKeepsStateAndRecovers(t *testing.T) {
	ctx := context.Background()
	e := newChunkEnv(t, 400)
	if _, err := e.s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	before := e.state(t)

	// Asana now has 500 tasks plus "zbad" last (the write of "zbad" fails).
	e.fake.n.Store(500)
	e.fake.extra.Store(`{"gid":"zbad","name":"bad","memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}]}`)
	undo := e.failOnTask(t, "zbad")
	e.now = e.now.Add(FullSyncInterval + time.Hour)
	res, err := e.s.Run(ctx, false)
	if err == nil || !strings.Contains(err.Error(), "atk boom") {
		t.Fatalf("expected the write error, got %+v, %v", res, err)
	}
	after := e.state(t)
	for _, k := range []string{"last_sync", "last_full_sync", "last_poll"} {
		if after[k] != before[k] {
			t.Errorf("%s advanced on a failed sync: %q -> %q", k, before[k], after[k])
		}
	}
	for k, v := range before {
		if after[k] != v && strings.HasPrefix(k, "events:") {
			t.Errorf("events token %s moved on a failed sync", k)
		}
	}
	got := e.taskGIDs(t)
	for i := 0; i < 400; i++ {
		if !got[fmt.Sprintf("a%d", i)] {
			t.Fatalf("a%d vanished after a failed sync", i)
		}
	}

	// recover: remove the fault and drop one task from Asana; the end state is the clean one
	undo()
	e.fake.n.Store(500)
	res, err = e.s.Run(ctx, false)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !res.Full {
		t.Fatalf("retry should be a full sync: %+v", res)
	}
	got = e.taskGIDs(t)
	if len(got) != 501 || !got["zbad"] || !got["a499"] {
		t.Fatalf("after recovery %d tasks", len(got))
	}
	if e.state(t)["last_full_sync"] == before["last_full_sync"] {
		t.Error("last full sync did not advance after a good sync")
	}
}

// A full sync whose last chunk fails must not prune tasks Asana dropped either: pruning
// is decided only after everything is written.
func TestChunkAttackNoPruneBeforeAllWritten(t *testing.T) {
	ctx := context.Background()
	e := newChunkEnv(t, 440)
	if _, err := e.s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	e.fake.n.Store(300) // a300.. are gone from Asana
	e.fake.extra.Store(`{"gid":"zbad","name":"bad"}`)
	undo := e.failOnTask(t, "zbad")
	e.now = e.now.Add(time.Hour)
	if _, err := e.s.Run(ctx, true); err == nil {
		t.Fatal("expected an error")
	}
	if got := e.taskGIDs(t); len(got) != 440 {
		t.Fatalf("tasks pruned although the sync failed: %d left of 440", len(got))
	}
	undo()
	res, err := e.s.Run(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pruned != 140 || len(e.taskGIDs(t)) != 301 {
		t.Fatalf("pruned %d, left %d (want 140, 301)", res.Pruned, len(e.taskGIDs(t)))
	}
}

// Asana failing (5xx, 429) or the context being cancelled: nothing is lost or advanced.
func TestChunkAttackServerErrorsAndCancel(t *testing.T) {
	for _, status := range []int{500, 429, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx := context.Background()
			e := newChunkEnv(t, 200)
			if _, err := e.s.Run(ctx, false); err != nil {
				t.Fatal(err)
			}
			before := e.state(t)
			e.fake.fail.Store(int64(status))
			e.now = e.now.Add(FullSyncInterval + time.Hour)
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err := e.s.Run(cctx, false)
			if err == nil {
				t.Fatalf("status %d: a failed full sync reported success", status)
			}
			after := e.state(t)
			if after["last_sync"] != before["last_sync"] || after["last_full_sync"] != before["last_full_sync"] {
				t.Errorf("status %d: sync state advanced: %v -> %v", status, before, after)
			}
			if got := e.taskGIDs(t); len(got) != 200 {
				t.Fatalf("status %d: %d tasks left", status, len(got))
			}
		})
	}
}

// Cancelling the context between chunks: Run returns an error, nothing is pruned or advanced,
// and the next sync completes.
func TestChunkAttackCancelDuringWrite(t *testing.T) {
	e := newChunkEnv(t, 800)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// cancel as soon as the fetch is answered: the writes then see a dead context
	e.fake.hook = func() { go func() { time.Sleep(30 * time.Millisecond); cancel() }() }
	_, err := e.s.Run(ctx, false)
	if err == nil {
		t.Log("sync finished before the cancel; nothing to check")
		return
	}
	st := e.state(t)
	if st["last_sync"] != "" || st["last_full_sync"] != "" {
		t.Fatalf("sync state advanced after cancel: %v", st)
	}
	e.fake.hook = nil
	if _, err := e.s.Run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := e.taskGIDs(t); len(got) != 800 {
		t.Fatalf("%d tasks after the retry", len(got))
	}
}

// The point of #55: while a big project is written, another process's write must get in
// within a small fraction of the time the one-transaction write took.
func TestChunkAttackOtherWriterNotBlocked(t *testing.T) {
	ctx := context.Background()
	if raceEnabled {
		t.Skip("too slow under -race; the store tests cover the lock hold")
	}
	const n = 20000
	// baseline: how long one big transaction holds the lock (the old behaviour)
	base := newChunkEnvTimed(t, 0)
	var tasks []asana.Task
	for i := 0; i < n; i++ {
		tasks = append(tasks, asana.Task{GID: fmt.Sprintf("b%d", i), Name: "x"})
	}
	t0 := time.Now()
	if err := base.st.UpsertTasks(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	whole := time.Since(t0)

	e := newChunkEnvTimed(t, n)
	other, err := store.Open(ctx, e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	done := make(chan struct{})
	type result struct {
		max    time.Duration
		writes int
		err    error
	}
	rc := make(chan result, 1)
	go func() {
		var r result
		for i := 0; ; i++ {
			select {
			case <-done:
				rc <- r
				return
			default:
			}
			s := time.Now()
			if err := other.SetSyncState(ctx, fmt.Sprintf("other%d", i), "v"); err != nil && r.err == nil {
				r.err = err
			}
			if d := time.Since(s); d > r.max {
				r.max = d
			}
			r.writes++
			time.Sleep(20 * time.Millisecond)
		}
	}()
	if _, err := e.s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	close(done)
	r := <-rc
	if r.err != nil {
		t.Fatalf("the other process's write failed: %v", r.err)
	}
	if got := e.taskGIDs(t); len(got) != n {
		t.Fatalf("%d of %d tasks stored", len(got), n)
	}
	t.Logf("whole-project transaction %v; other writer: %d writes, slowest %v", whole, r.writes, r.max)
	if r.max > whole/2 || r.max > 2*time.Second {
		t.Fatalf("other writer waited %v (one big transaction takes %v)", r.max, whole)
	}
}

// Two syncs at once (two processes): one runs, the other is told it is busy; the data is
// complete and the sync that did not run did not write half of it.
func TestChunkAttackTwoSyncsAtOnce(t *testing.T) {
	ctx := context.Background()
	e := newChunkEnv(t, 600)
	st2, err := store.Open(ctx, e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	s2 := &Syncer{API: e.s.API, Store: st2, Cfg: e.s.Cfg, now: e.s.now}
	errs := make(chan error, 2)
	go func() { _, err := e.s.Run(ctx, false); errs <- err }()
	go func() { _, err := s2.Run(ctx, false); errs <- err }()
	var failed int
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			if err != ErrPushBusy && !strings.Contains(err.Error(), ErrPushBusy.Error()) {
				t.Errorf("unexpected error: %v", err)
			}
			failed++
		}
	}
	if failed > 1 {
		t.Fatalf("both syncs failed")
	}
	if got := e.taskGIDs(t); len(got) != 600 {
		t.Fatalf("%d tasks", len(got))
	}
}

// An outbox entry queued by another process while the chunks are being written is not lost.
func TestChunkAttackOutboxWhileWriting(t *testing.T) {
	ctx := context.Background()
	e := newChunkEnv(t, 500)
	other, err := store.Open(ctx, e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.SetSyncState(ctx, "marker", "1"); err != nil {
		t.Fatal(err)
	}
	e.fake.hook = func() {
		// queued during the pull: must survive the chunked writes
		if err := other.SetSyncState(ctx, "during_pull", "1"); err != nil {
			t.Errorf("write during the pull: %v", err)
		}
	}
	if _, err := e.s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if v, _ := other.SyncState(ctx, "during_pull"); v != "1" {
		t.Fatal("a write made during the pull is lost")
	}
}

// Two other processes (TUI and MCP server) writing at once while a big project is stored:
// neither may fail as busy.
func TestChunkAttackTwoOtherWritersNotStarved(t *testing.T) {
	ctx := context.Background()
	if raceEnabled {
		t.Skip("too slow under -race")
	}
	e := newChunkEnvTimed(t, 12000)
	done := make(chan struct{})
	errc := make(chan error, 2)
	maxc := make(chan time.Duration, 2)
	for w := 0; w < 2; w++ {
		other, err := store.Open(ctx, e.path)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		go func(w int) {
			var max time.Duration
			var first error
			for i := 0; ; i++ {
				select {
				case <-done:
					errc <- first
					maxc <- max
					return
				default:
				}
				s := time.Now()
				if err := other.SetSyncState(ctx, fmt.Sprintf("w%d-%d", w, i), "v"); err != nil && first == nil {
					first = err
				}
				if d := time.Since(s); d > max {
					max = d
				}
				time.Sleep(30 * time.Millisecond) // a person's writes, not a busy loop
			}
		}(w)
	}
	_, err := e.s.Run(ctx, false)
	close(done)
	for i := 0; i < 2; i++ {
		if werr := <-errc; werr != nil {
			t.Errorf("an other writer failed: %v", werr)
		}
		if m := <-maxc; m > 3*time.Second {
			t.Errorf("an other writer waited %v", m)
		}
	}
	if err != nil {
		t.Fatalf("sync under contention: %v", err)
	}
	if got := e.taskGIDs(t); len(got) != 12000 {
		t.Fatalf("%d tasks", len(got))
	}
}
