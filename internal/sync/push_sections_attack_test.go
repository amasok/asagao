package sync

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// A section created and removed offline, and every move into it, sends nothing.
func TestAttackCreateThenDeleteOfflineSendsNothing(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	gid, err := st.CreateSectionLocal(ctx, "p1", "一時", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MoveSectionLocal(ctx, gid, "s1", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSectionLocal(ctx, gid, now); err != nil {
		t.Fatal(err)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 0 {
		t.Fatalf("outbox not empty: %d %d %d", p, c, fl)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "POST") || strings.HasPrefix(c, "DELETE") || strings.HasPrefix(c, "PUT") {
			t.Errorf("unexpected write: %s", c)
		}
	}
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"Todo", "Done"}) {
		t.Errorf("local sections = %v", got)
	}
}

// Several sections made offline, reordered offline, arrive in the same order.
func TestAttackManyOfflineSectionsKeepOrder(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	a, _ := st.CreateSectionLocal(ctx, "p1", "A", "", "", now)
	b, _ := st.CreateSectionLocal(ctx, "p1", "B", "", a, now)
	c, _ := st.CreateSectionLocal(ctx, "p1", "C", a, "", now)
	if err := st.MoveSectionLocal(ctx, b, "s1", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveTaskLocal(ctx, "t1", "p1", c, now); err != nil {
		t.Fatal(err)
	}
	want := localSectionNames(t, st)
	for i := 0; i < 3; i++ {
		if res, err := s.Push(ctx); err != nil {
			t.Fatalf("push %d: %v %+v", i, err, res)
		}
	}
	if got := f.sectionNames(); !slices.Equal(got, want) {
		t.Fatalf("server %v, local %v (calls %v)", got, want, f.calls)
	}
	if p, c2, fl := pendingCount(t, st); p+c2+fl != 0 {
		t.Fatalf("outbox left: %d %d %d (calls %v)", p, c2, fl, f.calls)
	}
	if got := localSectionNames(t, st); !slices.Equal(got, want) {
		t.Errorf("local after push %v, want %v", got, want)
	}
	if sec := f.taskSec["t1"]; sec == "" || sec == "s1" || strings.HasPrefix(sec, "local") {
		t.Errorf("t1 on server in %q", sec)
	}
}

// 429 / 5xx / network failure on a create that never reached Asana: retried until it
// goes through, and it exists exactly once.
func TestAttackCreateRetriedAfterFailuresMakesOne(t *testing.T) {
	for _, code := range []int{429, 500, 503, 502} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f, s, st := setupSections(t)
			ctx := context.Background()
			gid, _ := st.CreateSectionLocal(ctx, "p1", "新", "", "", now)
			if err := st.MoveTaskLocal(ctx, "t1", "p1", gid, now); err != nil {
				t.Fatal(err)
			}
			fails := 2
			f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
				if r.Method == http.MethodPost && r.URL.Path == "/projects/p1/sections" && fails > 0 {
					fails--
					rw.Header().Set("Retry-After", "0")
					rw.WriteHeader(code)
					return true
				}
				return false
			}
			clk := now
			s.now = func() time.Time { return clk }
			for i := 0; i < 6; i++ {
				_, _ = s.Push(ctx)
				clk = clk.Add(2 * time.Minute) // past the settle time of an unclear send
			}
			if n := strings.Count(strings.Join(f.sectionNames(), ","), "新"); n != 1 {
				t.Fatalf("%d sections named 新 (calls %v)", n, f.calls)
			}
			if sec := f.taskSec["t1"]; sec == "s1" || sec == "" {
				t.Errorf("t1 never moved: %q (calls %v)", sec, f.calls)
			}
			// The section and the task are still there locally.
			if got := localSectionNames(t, st); !slices.Contains(got, "新") {
				t.Errorf("section lost locally: %v", got)
			}
		})
	}
}

// While the create keeps failing, nothing that depends on it is sent and nothing is lost.
func TestAttackDependentsWaitWhileCreateFails(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	gid, _ := st.CreateSectionLocal(ctx, "p1", "新", "", "", now)
	_ = st.MoveTaskLocal(ctx, "t1", "p1", gid, now)
	_ = st.MoveSectionLocal(ctx, gid, "s1", "", now)
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/projects/p1/sections" {
			rw.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		return false
	}
	for i := 0; i < 3; i++ {
		_, _ = s.Push(ctx)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "local") {
			t.Errorf("local gid leaked to Asana: %s", c)
		}
	}
	if got := localSectionNames(t, st); !slices.Contains(got, "新") {
		t.Errorf("section lost: %v", got)
	}
	if sectionOf(t, st, "t1") != gid {
		t.Errorf("t1 left the new section locally")
	}
}

// The section was deleted in Asana: moves into it fail cleanly, nothing is duplicated.
func TestAttackMoveIntoSectionDeletedInAsana(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	if err := st.MoveTaskLocal(ctx, "t1", "p1", "s2", now); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.sections = f.sections[:1]
	f.mu.Unlock()
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 {
		t.Errorf("sent %d moves into a deleted section", res.Sent)
	}
	// A move into a section that is gone is dropped (and reported), not retried forever.
	if len(res.Discarded) != 1 {
		t.Errorf("discarded = %+v, want the one move reported", res.Discarded)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 0 {
		t.Errorf("outbox: %d %d %d", p, c, fl)
	}
}

// Asana refuses a delete ("not empty": somebody put a task there): the op fails, the
// section is not forgotten, and the delete is not retried forever as sent.
func TestAttackDeleteSectionNotEmptyOnServer(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	if err := st.DeleteSectionLocal(ctx, "s2", now); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.taskSec["t9"] = "s2"
	f.mu.Unlock()
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 0 || res.Failed != 1 {
		t.Fatalf("push = %+v", res)
	}
	if got := f.sectionNames(); !slices.Equal(got, []string{"Todo", "Done"}) {
		t.Errorf("server sections = %v", got)
	}
	// Discarding the failed delete brings the section back locally.
	op := opByKind(t, st, store.OpDeleteSection)
	if op == nil {
		t.Fatal("delete op gone")
	}
	if err := st.DiscardOp(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"Todo", "Done"}) {
		t.Errorf("local after discard = %v", got)
	}
}

// Two pushes at once must not create the section twice.
func TestAttackConcurrentPushCreatesOnce(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	_, _ = st.CreateSectionLocal(ctx, "p1", "並行", "", "", now)
	var wg stdsync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Push(ctx) }()
	}
	wg.Wait()
	_, _ = s.Push(ctx)
	if n := f.count("POST /projects/p1/sections "); n != 1 {
		t.Fatalf("created %d times (calls %v)", n, f.calls)
	}
}

// A section name with terminal control characters never reaches the screen raw.
func TestAttackSectionAndTagNamesAreCleaned(t *testing.T) {
	f, _, st := setupSections(t)
	ctx := context.Background()
	evil := "\x1b[2J\x1b]0;pwn\x07赤‮\x9b"
	f.mu.Lock()
	f.sections = append(f.sections, asana.Section{GID: "s9", Name: evil})
	f.tags["g1"].Name = evil
	f.tags["g1"].Color = "\x1b[31mred"
	f.mu.Unlock()
	if err := st.ReplaceSections(ctx, "p1", f.sections); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: evil, Color: "\x1b[31mred"}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range localSectionNames(t, st) {
		if hasCtl(n) {
			t.Errorf("section name has control chars: %q", n)
		}
	}
	tags, err := st.ListTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range tags {
		if hasCtl(fmt.Sprintf("%+v", tg)) {
			t.Errorf("tag has control chars: %+v", tg)
		}
	}
	// Names made locally too.
	gid, err := st.CreateSectionLocal(ctx, "p1", "x\x1b[2Jy", "", "", now)
	if err == nil {
		for _, n := range localSectionNames(t, st) {
			if strings.Contains(n, "\x1b") {
				t.Errorf("local section name %q", n)
			}
		}
		_ = gid
	}
}

// Tag changes: 404 means done (gone), 429 keeps the op, conflicts are not overwritten.
func TestAttackTagFailures(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"name": "新名"}, now); err != nil {
		t.Fatal(err)
	}
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.Method == http.MethodPut {
			rw.Header().Set("Retry-After", "0")
			rw.WriteHeader(http.StatusTooManyRequests)
			return true
		}
		return false
	}
	_, _ = s.Push(ctx)
	if p, c, fl := pendingCount(t, st); p+c+fl != 1 {
		t.Fatalf("op lost after 429: %d %d %d", p, c, fl)
	}
	if f.tags["g1"].Name != "急ぎ" {
		t.Fatalf("server tag changed: %q", f.tags["g1"].Name)
	}
	// Somebody renamed it in Asana meanwhile: conflict, the server value stays.
	f.hook = nil
	f.mu.Lock()
	f.tags["g1"].Name = "他人"
	f.mu.Unlock()
	_, _ = s.Push(ctx)
	if f.tags["g1"].Name != "他人" {
		t.Errorf("conflicting rename overwrote the server: %q", f.tags["g1"].Name)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 1 {
		t.Errorf("conflict op gone: %d %d %d", p, c, fl)
	}
}

func TestAttackDeleteTagGoneAndFailing(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	if err := st.DeleteTagLocal(ctx, "g1", now); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	delete(f.tags, "g1")
	f.mu.Unlock()
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 0 {
		t.Errorf("outbox: %d %d %d", p, c, fl)
	}
}

// hasCtl reports a C0/C1 control character (an invalid byte is not one).
func hasCtl(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r >= 0x7f && r <= 0x9f {
			return true
		}
	}
	return false
}
