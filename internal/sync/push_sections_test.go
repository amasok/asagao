package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

// sectionServer is a fake Asana for sections and tags: project p1 with its sections in
// order, the section of each task, and tags.
type sectionServer struct {
	t        *testing.T
	mu       stdsync.Mutex
	sections []asana.Section // of p1, in order
	taskSec  map[string]string
	tags     map[string]*asana.Tag
	calls    []string
	next     int
	// hook may answer a request itself (returning true).
	hook func(rw http.ResponseWriter, r *http.Request, body map[string]any) bool
}

func (f *sectionServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var body struct{ Data map[string]any }
	_ = json.Unmarshal(b, &body)
	f.calls = append(f.calls, strings.TrimSpace(r.Method+" "+r.URL.Path+" "+string(b)))
	if f.hook != nil && f.hook(rw, r, body.Data) {
		return
	}
	str := func(k string) string { v, _ := body.Data[k].(string); return v }
	index := func(gid string) int {
		return slices.IndexFunc(f.sections, func(s asana.Section) bool { return s.GID == gid })
	}
	notFound := func() { rw.WriteHeader(http.StatusNotFound); fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`) }
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(rw, `{"data":{"gid":"me","name":"Me"}}`)
	case r.URL.Path == "/projects/p1/sections" && r.Method == http.MethodGet:
		js, _ := json.Marshal(f.sections)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/projects/p1/sections" && r.Method == http.MethodPost:
		f.next++
		sec := asana.Section{GID: fmt.Sprintf("sec%d", f.next), Name: str("name")}
		at := len(f.sections)
		for _, k := range []string{"insert_before", "insert_after"} {
			if str(k) != "" && index(str(k)) < 0 {
				notFound() // Asana refuses a section that is not there
				return
			}
		}
		if i := index(str("insert_before")); i >= 0 {
			at = i
		} else if i := index(str("insert_after")); i >= 0 {
			at = i + 1
		}
		f.sections = slices.Insert(f.sections, at, sec)
		js, _ := json.Marshal(sec)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/projects/p1/sections/insert":
		i := index(str("section"))
		if i < 0 {
			notFound()
			return
		}
		sec := f.sections[i]
		f.sections = slices.Delete(f.sections, i, i+1)
		at := len(f.sections)
		if j := index(str("before_section")); j >= 0 {
			at = j
		} else if j := index(str("after_section")); j >= 0 {
			at = j + 1
		} else {
			notFound()
			return
		}
		f.sections = slices.Insert(f.sections, at, sec)
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 2 && parts[0] == "sections" && r.Method == http.MethodDelete:
		i := index(parts[1])
		if i < 0 {
			notFound()
			return
		}
		for _, s := range f.taskSec {
			if s == parts[1] {
				rw.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(rw, `{"errors":[{"message":"section is not empty"}]}`)
				return
			}
		}
		f.sections = slices.Delete(f.sections, i, i+1)
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 3 && parts[0] == "sections" && parts[2] == "addTask":
		if index(parts[1]) < 0 {
			notFound()
			return
		}
		f.taskSec[str("task")] = parts[1]
		fmt.Fprint(rw, `{"data":{}}`)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[0] == "sections" && parts[2] == "tasks":
		if index(parts[1]) < 0 {
			notFound()
			return
		}
		var gids []string
		for gid, sec := range f.taskSec {
			if sec == parts[1] {
				gids = append(gids, gid)
			}
		}
		slices.Sort(gids)
		var items []string
		for _, gid := range gids {
			items = append(items, fmt.Sprintf(`{"gid":%q}`, gid))
		}
		fmt.Fprintf(rw, `{"data":[%s]}`, strings.Join(items, ","))
	case len(parts) == 2 && parts[0] == "tasks":
		fmt.Fprintf(rw, `{"data":{"gid":%q,"name":"A","memberships":[{"project":{"gid":"p1"},"section":{"gid":%q}}]}}`, parts[1], f.taskSec[parts[1]])
	case len(parts) == 2 && parts[0] == "tags":
		tg, ok := f.tags[parts[1]]
		if !ok {
			notFound()
			return
		}
		switch r.Method {
		case http.MethodDelete:
			delete(f.tags, parts[1])
			fmt.Fprint(rw, `{"data":{}}`)
			return
		case http.MethodPut:
			if v, ok := body.Data["name"].(string); ok {
				tg.Name = v
			}
			if v, ok := body.Data["color"]; ok {
				tg.Color, _ = v.(string)
			}
		}
		js, _ := json.Marshal(tg)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		rw.WriteHeader(http.StatusTeapot)
	}
}

func (f *sectionServer) sectionNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sections {
		out = append(out, s.Name)
	}
	return out
}

func (f *sectionServer) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// setupSections: project p1 with Todo (s1, holding t1) and Done (s2), in Asana and the store.
func setupSections(t *testing.T) (*sectionServer, *Syncer, *store.Store) {
	t.Helper()
	f := &sectionServer{t: t,
		sections: []asana.Section{{GID: "s1", Name: "Todo"}, {GID: "s2", Name: "Done"}},
		taskSec:  map[string]string{"t1": "s1"},
		tags:     map[string]*asana.Tag{"g1": {GID: "g1", Name: "急ぎ", Color: "dark-red"}},
	}
	srv := httptest.NewServer(f)
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"))
	must(st.SetMeGID(ctx, "me"))
	must(st.ReplaceSections(ctx, "p1", f.sections))
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}},
		Tags:        []asana.Ref{{GID: "g1", Name: "急ぎ"}}}}))
	must(st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: "dark-red"}}))
	return f, &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}, st
}

func localSectionNames(t *testing.T, st *store.Store) []string {
	t.Helper()
	ps, err := st.ListProjects(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		if p.GID == "p1" {
			for _, s := range p.Sections {
				out = append(out, s.Name)
			}
		}
	}
	return out
}

// A section created offline can take tasks right away; once Asana created it, the move is
// sent with its real gid, and the section is in the same place on both sides.
func TestPushCreateSectionThenMoveIntoIt(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	gid, err := st.CreateSectionLocal(ctx, "p1", "レビュー", "", "s1", now)
	if err != nil || !store.IsLocalGID(gid) {
		t.Fatalf("CreateSectionLocal = %q, %v", gid, err)
	}
	if err := st.MoveTaskLocal(ctx, "t1", "p1", gid, now); err != nil {
		t.Fatal(err)
	}
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"Todo", "レビュー", "Done"}) {
		t.Fatalf("local sections = %v", got)
	}
	// A pull before it is sent keeps it (and the task in it).
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "Todo"}, {GID: "s2", Name: "Done"}}); err != nil {
		t.Fatal(err)
	}
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"Todo", "レビュー", "Done"}) {
		t.Fatalf("local sections after a pull = %v", got)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v (calls %v)", res, err, f.calls)
	}
	if got := f.sectionNames(); !slices.Equal(got, []string{"Todo", "レビュー", "Done"}) {
		t.Fatalf("server sections = %v", got)
	}
	real := f.sections[1].GID
	if f.taskSec["t1"] != real {
		t.Fatalf("t1 is in %q on the server, want %q (calls %v)", f.taskSec["t1"], real, f.calls)
	}
	if got := sectionOf(t, st, "t1"); got != real {
		t.Fatalf("t1 is in %q locally, want %q", got, real)
	}
	if g, _ := st.RealSectionGID(ctx, gid); g != real {
		t.Errorf("RealSectionGID(%s) = %q, want %q", gid, g, real)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 0 {
		t.Errorf("outbox not empty: %d %d %d", p, c, fl)
	}
}

// A create whose answer was lost is looked for in Asana instead of being sent again: a
// section with its name that was not there before is ours, one that was there is not.
func TestPushCreateSectionAnswerLost(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	// A section already named Done must not be taken for the new one.
	gid, err := st.CreateSectionLocal(ctx, "p1", "Done", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	lost := true
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/projects/p1/sections" && lost {
			lost = false
			f.next++
			f.sections = append(f.sections, asana.Section{GID: fmt.Sprintf("sec%d", f.next), Name: "Done"})
			rw.WriteHeader(http.StatusBadGateway) // created, but the answer says otherwise
			return true
		}
		return false
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count("POST /projects/p1/sections "); n != 1 {
		t.Fatalf("created %d times (calls %v)", n, f.calls)
	}
	if got := f.sectionNames(); !slices.Equal(got, []string{"Todo", "Done", "Done"}) {
		t.Fatalf("server sections = %v", got)
	}
	if g, _ := st.RealSectionGID(ctx, gid); g != f.sections[2].GID {
		t.Fatalf("RealSectionGID = %q, want the new one %q", g, f.sections[2].GID)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 0 {
		t.Errorf("outbox not empty: %d %d %d", p, c, fl)
	}
}

func TestPushDeleteAndReorderSections(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	// Not empty: refused locally.
	if err := st.DeleteSectionLocal(ctx, "s1", now); err == nil {
		t.Fatal("deleting a section with a task: no error")
	}
	if err := st.MoveSectionLocal(ctx, "s2", "s1", "", now); err != nil {
		t.Fatal(err)
	}
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"Done", "Todo"}) {
		t.Fatalf("local sections = %v", got)
	}
	if err := st.DeleteSectionLocal(ctx, "s2", now); err != nil {
		t.Fatal(err)
	}
	// The move stays queued before the delete (discarding the delete keeps it).
	ops, _ := st.ListOps(ctx)
	if len(ops) != 2 || ops[0].Kind != store.OpInsertSection || ops[1].Kind != store.OpDeleteSection {
		t.Fatalf("ops = %+v", ops)
	}
	// Last section: refused.
	if err := st.DeleteSectionLocal(ctx, "s1", now); err == nil {
		t.Fatal("deleting the last section: no error")
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if got := f.sectionNames(); !slices.Equal(got, []string{"Todo"}) {
		t.Fatalf("server sections = %v", got)
	}
	// A pull before a delete is sent does not bring the section back.
	f.mu.Lock()
	f.sections = append(f.sections, asana.Section{GID: "s3", Name: "保留"}, asana.Section{GID: "s4", Name: "後で"})
	f.mu.Unlock()
	_ = st.ReplaceSections(ctx, "p1", f.sections)
	if err := st.DeleteSectionLocal(ctx, "s3", now); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveSectionLocal(ctx, "s4", "s1", "", now); err != nil {
		t.Fatal(err)
	}
	_ = st.ReplaceSections(ctx, "p1", f.sections)
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"後で", "Todo"}) {
		t.Fatalf("local sections after a pull = %v", got)
	}
	// Deleted in Asana meanwhile: the delete is done, not failed.
	f.mu.Lock()
	f.sections = slices.DeleteFunc(f.sections, func(s asana.Section) bool { return s.GID == "s3" })
	f.mu.Unlock()
	res, err = s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if got := f.sectionNames(); !slices.Equal(got, []string{"後で", "Todo"}) {
		t.Fatalf("server sections = %v", got)
	}
}

// Discarding a section created offline takes back the moves into it.
func TestDiscardCreateSectionUndoesMoves(t *testing.T) {
	_, _, st := setupSections(t)
	ctx := context.Background()
	gid, _ := st.CreateSectionLocal(ctx, "p1", "レビュー", "", "", now)
	if err := st.MoveTaskLocal(ctx, "t1", "p1", gid, now); err != nil {
		t.Fatal(err)
	}
	create := opByKind(t, st, store.OpCreateSection)
	if err := st.DiscardOp(ctx, create.ID); err != nil {
		t.Fatal(err)
	}
	if got := sectionOf(t, st, "t1"); got != "s1" {
		t.Errorf("t1 is in %q, want it back in s1", got)
	}
	if got := localSectionNames(t, st); !slices.Equal(got, []string{"Todo", "Done"}) {
		t.Errorf("local sections = %v", got)
	}
	if p, c, fl := pendingCount(t, st); p+c+fl != 0 {
		t.Errorf("outbox not empty: %d %d %d", p, c, fl)
	}
	// Deleting a section never sent forgets it without queueing anything.
	gid, _ = st.CreateSectionLocal(ctx, "p1", "x", "", "", now)
	if err := st.DeleteSectionLocal(ctx, gid, now); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := pendingCount(t, st); p != 0 {
		t.Errorf("%d pending after creating and deleting offline", p)
	}
}

func TestPushUpdateAndDeleteTag(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"name": "至急", "color": "light-blue"}, now); err != nil {
		t.Fatal(err)
	}
	// A pull before it is sent keeps the new name and color.
	_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: "dark-red"}})
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Tags: []asana.Ref{{GID: "g1", Name: "急ぎ"}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	if tg, _ := st.GetTag(ctx, "g1"); tg.Name != "至急" || tg.Color != "light-blue" {
		t.Fatalf("tag after a pull = %+v", tg)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if tg := f.tags["g1"]; tg.Name != "至急" || tg.Color != "light-blue" {
		t.Fatalf("server tag = %+v", tg)
	}

	// No color is sent as null.
	_ = st.UpdateTagLocal(ctx, "g1", map[string]any{"color": ""}, now)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count(`PUT /tags/g1 {"data":{"color":null}}`) != 1 {
		t.Errorf("calls %v", f.calls)
	}

	// Renamed in Asana meanwhile: a conflict, not overwritten.
	_ = st.UpdateTagLocal(ctx, "g1", map[string]any{"name": "あとで"}, now)
	f.mu.Lock()
	f.tags["g1"].Name = "別の名前"
	f.mu.Unlock()
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if f.tags["g1"].Name != "別の名前" {
		t.Errorf("overwritten: %q", f.tags["g1"].Name)
	}
	_ = st.DiscardOp(ctx, opByKind(t, st, store.OpUpdateTag).ID)

	// Delete: off every task locally at once; a pull does not bring it back.
	if err := st.DeleteTagLocal(ctx, "g1", now); err != nil {
		t.Fatal(err)
	}
	_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "別の名前"}})
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Tags: []asana.Ref{{GID: "g1", Name: "別の名前"}},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}})
	if tags, _ := st.TaskTags(ctx, "t1"); len(tags) != 0 {
		t.Fatalf("t1 tags after a pull = %v", tags)
	}
	if _, err := st.GetTag(ctx, "g1"); err == nil {
		t.Fatal("deleted tag came back")
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if _, ok := f.tags["g1"]; ok {
		t.Error("tag still on the server")
	}
}

// Discarding a tag deletion puts the tag back on its tasks.
func TestDiscardDeleteTagRestores(t *testing.T) {
	_, _, st := setupSections(t)
	ctx := context.Background()
	if err := st.DeleteTagLocal(ctx, "g1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DiscardOp(ctx, opByKind(t, st, store.OpDeleteTag).ID); err != nil {
		t.Fatal(err)
	}
	tg, err := st.GetTag(ctx, "g1")
	if err != nil || tg.Name != "急ぎ" || tg.Color != "dark-red" || tg.Total != 1 {
		t.Fatalf("tag = %+v, %v", tg, err)
	}
}

// A 402 (the plan lacks the feature) fails the change and hides the feature in the
// project's workspace for a day (#50).
func TestPushPaymentRequiredHidesFeature(t *testing.T) {
	f, s, st := setupSections(t)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if strings.HasPrefix(r.URL.Path, "/projects/p1/sections") && r.Method == http.MethodPost {
			rw.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return true
		}
		return false
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return now }}
	if !sections.Available(ctx) {
		t.Fatal("not available before a 402")
	}
	if err := sections.ReorderHere(ctx, "s2", "s1", ""); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if sections.Available(ctx) {
		t.Fatal("still offered after a 402")
	}
	if _, err := sections.CreateHere(ctx, "p1", "x", "", ""); !app.IsPlanUnavailable(err) {
		t.Fatalf("create after a 402: err = %v", err)
	}
	if got, ok, _ := st.PlanRefusedAt(ctx, plan.Sections, "w1"); !ok || !got.Equal(now) {
		t.Errorf("recorded for w1 = (%v, %v)", got, ok)
	}
	if v, _ := st.SyncState(ctx, "plan_unavailable:"+plan.Sections); v != "" {
		t.Errorf("recorded for every workspace (before #50) = %q", v)
	}
	// Tags are another feature: still offered.
	if !(&app.Tags{Store: st}).Available(ctx) {
		t.Error("tags hidden by a 402 on sections")
	}
}

// A new section placed next to one deleted in Asana meanwhile is created last instead of
// failing (and taking the moves into it along).
func TestPushCreateSectionAnchorGone(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	gid, err := st.CreateSectionLocal(ctx, "p1", "レビュー", "", "s2", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MoveTaskLocal(ctx, "t1", "p1", gid, now); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.sections = f.sections[:1] // Done (s2) deleted by someone else
	f.mu.Unlock()
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v (calls %v)", res, err, f.calls)
	}
	if f.count(`POST /projects/p1/sections {"data":{"name":"レビュー"}}`) != 1 {
		t.Errorf("created with the gone anchor: %v", f.calls)
	}
	if got := f.sectionNames(); !slices.Equal(got, []string{"Todo", "レビュー"}) {
		t.Errorf("server sections = %v", got)
	}
	if f.taskSec["t1"] != f.sections[1].GID {
		t.Errorf("t1 not moved into the new section: %q", f.taskSec["t1"])
	}
}

// Discarding a section delete keeps the rename queued before it.
func TestDiscardDeleteSectionKeepsRename(t *testing.T) {
	_, _, st := setupSections(t)
	ctx := context.Background()
	if err := st.RenameSectionLocal(ctx, "s2", "完了", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSectionLocal(ctx, "s2", now); err != nil {
		t.Fatal(err)
	}
	if err := st.DiscardOp(ctx, opByKind(t, st, store.OpDeleteSection).ID); err != nil {
		t.Fatal(err)
	}
	if op := opByKind(t, st, store.OpRenameSection); op.Payload["name"] != "完了" {
		t.Fatalf("rename = %+v", op)
	}
	if name, err := st.SectionName(ctx, "s2"); err != nil || name != "完了" {
		t.Errorf("s2 = %q, %v", name, err)
	}
}

// A tag without a color is "" here and "none" in Asana's answers (found by
// scripts/realcheck, #31): the same value, so coloring it is not a conflict.
func TestPushUpdateTagNoColorIsNone(t *testing.T) {
	for _, c := range []struct{ stored, asana, want string }{{"", "none", "dark-red"}, {"none", "", "dark-red"}, {"none", "none", "light-blue"}} {
		t.Run(c.stored+"/"+c.asana+"/"+c.want, func(t *testing.T) {
			f, s, st := setupSections(t)
			ctx := context.Background()
			_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: c.stored}})
			f.mu.Lock()
			f.tags["g1"].Color = c.asana
			f.mu.Unlock()
			if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"color": c.want}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Conflicts != 0 {
				t.Fatalf("push = %+v, %v", res, err)
			}
		})
	}
}

// Asana renames the old color names (dark-red comes back as red, found by
// scripts/realcheck #31): the cache keeps Asana's name, so the next edit is no conflict.
func TestPushUpdateTagKeepsAsanasColorName(t *testing.T) {
	f, s, st := setupSections(t)
	ctx := context.Background()
	f.hook = func(rw http.ResponseWriter, r *http.Request, body map[string]any) bool {
		if r.Method == http.MethodPut && r.URL.Path == "/tags/g1" && body["color"] == "dark-red" {
			// as the real Asana: the PUT echoes dark-red, a later GET says red
			f.tags["g1"].Color = "red"
			fmt.Fprint(rw, `{"data":{"gid":"g1","name":"急ぎ","color":"dark-red"}}`)
			return true
		}
		return false
	}
	f.tags["g1"].Color = "none"
	_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: ""}})
	if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"color": "dark-red"}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if err := st.UpdateTagLocal(ctx, "g1", map[string]any{"color": ""}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Conflicts != 0 {
		t.Fatalf("second push = %+v, %v", res, err)
	}
}
