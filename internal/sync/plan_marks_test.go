package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
)

// #50: the 402s of sections, tags, My Tasks' sections, search, typeahead and custom IDs
// are recorded per workspace and tried again after a day, like the
// other features: a free workspace's 402 does not hide them in a paid organization.

func (f *myTasksAsana) listsIn(ws string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lists[ws]
}

// A 402 for My Tasks in one workspace hides its sections there only, for a day; then
// Asana is asked again, and an answer forgets the 402.
func TestMySectionsPlanPerWorkspace(t *testing.T) {
	f := &myTasksAsana{section: "m2", listIn: map[string]int{"w1": http.StatusPaymentRequired}}
	s, st, now := setupMyTasks(t, f)
	ctx := context.Background()
	for _, ws := range []string{"w1", "w2"} {
		if err := s.Puller().syncMySections(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	if at, ok, _ := st.PlanRefusedAt(ctx, plan.MySection, "w1"); !ok || !at.Equal(*now) {
		t.Fatalf("402 in w1 recorded = (%v, %v)", at, ok)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.MySection, "w2"); ok {
		t.Fatal("w2 recorded unexpected")
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return *now }}
	if secs, err := sections.MySections(ctx, "w2"); err != nil || len(secs) != 2 {
		t.Fatalf("w2's sections after a 402 in w1 = %v, %v", secs, err)
	}
	if secs, err := sections.MySections(ctx, "w1"); err != nil || len(secs) != 0 {
		t.Fatalf("w1's sections after a 402 there = %v, %v", secs, err)
	}

	// Within the day w1 is not asked again.
	*now = now.Add(plan.RecheckAfter - time.Minute)
	if err := s.Puller().syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n := f.listsIn("w1"); n != 1 {
		t.Fatalf("w1 asked %d times within the day", n)
	}
	// After it, w1 is asked again (its plan was upgraded): the answer forgets the 402.
	*now = now.Add(time.Minute)
	f.mu.Lock()
	f.listIn = nil
	f.mu.Unlock()
	if err := s.Puller().syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n := f.listsIn("w1"); n != 2 {
		t.Fatalf("w1 asked %d times after the day", n)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.MySection, "w1"); ok {
		t.Fatal("402 not forgotten after an answer")
	}
	if secs, err := sections.MySections(ctx, "w1"); err != nil || len(secs) != 2 {
		t.Fatalf("w1's sections after the answer = %v, %v", secs, err)
	}
}

// A record before #50 (plan_unavailable:my_section, for every workspace and for good) does
// not stop the sync from fetching My Tasks' sections.
func TestMySectionsOldGlobalRecordIgnored(t *testing.T) {
	f := &myTasksAsana{section: "m2"}
	s, st, _ := setupMyTasks(t, f)
	ctx := context.Background()
	if err := st.SetSyncState(ctx, "plan_unavailable:"+plan.MySection, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.Puller().syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if f.listsIn("w1") != 1 {
		t.Fatal("not asked with an old record")
	}
	if secs, err := st.ListMyTaskSections(ctx, "w1"); err != nil || len(secs) != 2 {
		t.Fatalf("sections = %v, %v", secs, err)
	}
}

// A 402 to a move in My Tasks is recorded for the workspace of the move.
func TestPushMoveMySection402PerWorkspace(t *testing.T) {
	f := &myTasksAsana{section: "m2"}
	s, st, now := setupMyTasks(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return *now }}
	if err := sections.MoveMine(ctx, "t1", "今日やる"); err != nil {
		t.Fatal(err)
	}
	f.moveStatus = http.StatusPaymentRequired
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if at, ok, _ := st.PlanRefusedAt(ctx, plan.MySection, "w1"); !ok || !at.Equal(*now) {
		t.Fatalf("recorded for w1 = (%v, %v)", at, ok)
	}
	if v, _ := st.SyncState(ctx, "plan_unavailable:"+plan.MySection); v != "" {
		t.Fatalf("recorded for every workspace = %q", v)
	}
}

// A 402 to a section change in w1 hides sections in w1 only: a project of w2 still has
// them. A 402 to a tag change is recorded for the workspace of its tasks, and a change
// Asana takes a day later forgets it.
func TestPushSectionsAndTags402PerWorkspace(t *testing.T) {
	f, s, st := setupSections(t)
	at := now
	s.now = func() time.Time { return at }
	clock := func() time.Time { return at }
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// w2: project p2 (section s9) and task t9 with tag g9.
	must(st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "p2", Name: "Q"}}, "me"))
	must(st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "s9", Name: "Inbox"}, {GID: "s8", Name: "Later"}}))
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "B", Workspace: &asana.Ref{GID: "w2"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p2"}, Section: &asana.Ref{GID: "s9"}}},
		Tags:        []asana.Ref{{GID: "g9", Name: "後で"}}}}))
	must(st.UpsertTags(ctx, []asana.Tag{{GID: "g9", Name: "後で"}}))

	refuse := true
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if !refuse {
			return false
		}
		if (strings.HasPrefix(r.URL.Path, "/projects/p1/sections") && r.Method == http.MethodPost) || r.URL.Path == "/tags/g1" && r.Method == http.MethodPut {
			rw.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return true
		}
		return false
	}
	sections := &app.Sections{Store: st, Clock: clock}
	tags := &app.Tags{Store: st, Clock: clock}
	must(sections.ReorderHere(ctx, "s2", "s1", ""))
	must(tags.UpdateHere(ctx, "g1", map[string]any{"name": "至急"}))
	if res, err := s.Push(ctx); err != nil || res.Failed != 2 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, feature := range []string{plan.Sections, plan.Tags} {
		if got, ok, _ := st.PlanRefusedAt(ctx, feature, "w1"); !ok || !got.Equal(at) {
			t.Errorf("%s: recorded for w1 = (%v, %v)", feature, got, ok)
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, feature, "w2"); ok {
			t.Errorf("%s: recorded for w2", feature)
		}
		if v, _ := st.SyncState(ctx, "plan_unavailable:"+feature); v != "" {
			t.Errorf("%s: recorded for every workspace = %q", feature, v)
		}
	}
	// w1 refuses them; w2 does not.
	if _, err := sections.CreateHere(ctx, "p1", "x", "", ""); !app.IsPlanUnavailable(err) {
		t.Errorf("section in w1 after its 402: err = %v", err)
	}
	if err := tags.UpdateHere(ctx, "g1", map[string]any{"name": "至急2"}); !app.IsPlanUnavailable(err) {
		t.Errorf("tag in w1 after its 402: err = %v", err)
	}
	if err := sections.ReorderHere(ctx, "s8", "s9", ""); err != nil {
		t.Errorf("section in w2 after a 402 in w1: %v", err)
	}
	if err := tags.UpdateHere(ctx, "g9", map[string]any{"name": "あとで"}); err != nil {
		t.Errorf("tag in w2 after a 402 in w1: %v", err)
	}
	if !sections.Available(ctx) || !tags.Available(ctx) {
		t.Error("menus hidden while w2 has them")
	}
	must(discardQueued(ctx, st)) // the w2 changes are not for this fake

	// A day later they are offered in w1 again; an accepted change forgets the 402.
	at = at.Add(plan.RecheckAfter)
	refuse = false
	must(tags.UpdateHere(ctx, "g1", map[string]any{"name": "至急"}))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push a day later = %+v, %v", res, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w1"); ok {
		t.Error("tags' 402 not forgotten after an answer")
	}
}

// discardQueued drops every entry of the outbox (the store's own discard).
func discardQueued(ctx context.Context, st interface {
	ListOps(ctx context.Context) ([]outbox.Op, error)
	DiscardOp(ctx context.Context, id int64) error
}) error {
	ops, err := st.ListOps(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err := st.DiscardOp(ctx, op.ID); err != nil {
			return err
		}
	}
	return nil
}

// The search API's 402 is recorded for the workspace searched, and the search API is
// asked again after a day (an answer forgets the 402). A 402 of another workspace, or a
// record before #50, does not stop it.
func TestSearchPlanPerWorkspaceAndRecheck(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{}
	s, st := setupSearch(t, f)
	at := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	for _, f := range []string{plan.Search, plan.Typeahead, plan.CustomID} {
		if err := st.RecordPlanRefusal(ctx, f, "w2", at); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSyncState(ctx, "plan_unavailable:"+f, at.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || res.ByName || f.searchCalls != 1 {
		t.Fatalf("search with 402s elsewhere = %+v, %v, %d calls", res, err, f.searchCalls)
	}
	if _, err := s.FindUsers(ctx, "hana"); err != nil || f.typeCalls["user"] != 1 {
		t.Fatalf("typeahead with 402s elsewhere: %v, %v", err, f.typeCalls)
	}
	if _, err := s.TaskByCustomID(ctx, "ENG-123"); errors.Is(err, ErrCustomIDUnavailable) {
		t.Fatalf("custom ID with 402s elsewhere: %v", err)
	}

	f.searchStatus = http.StatusPaymentRequired
	if res, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || !res.ByName {
		t.Fatalf("search after a 402 = %+v, %v", res, err)
	}
	if got, ok, _ := st.PlanRefusedAt(ctx, plan.Search, "w1"); !ok || !got.Equal(at) {
		t.Fatalf("recorded for w1 = (%v, %v)", got, ok)
	}
	at = at.Add(plan.RecheckAfter - time.Second)
	if _, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || f.searchCalls != 2 {
		t.Fatalf("within the day: %v, %d calls", err, f.searchCalls)
	}
	at = at.Add(time.Second)
	f.searchStatus = 0
	if res, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || res.ByName || f.searchCalls != 3 {
		t.Fatalf("a day later = %+v, %v, %d calls", res, err, f.searchCalls)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Search, "w1"); ok {
		t.Fatal("402 not forgotten after an answer")
	}
}

// A 402 to a section create (and delete) is recorded for its project's workspace, and one
// to a tag delete for the workspace of the tasks it was on (the tag is gone from them).
func TestPushCreateSectionAndDeleteTag402PerWorkspace(t *testing.T) {
	f, s, st := setupSections(t)
	at := now
	s.now = func() time.Time { return at }
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// The default workspace is w0: a record there would hide the wrong one.
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w0", Name: "A"}, {GID: "w1", Name: "B"}}))
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if (r.URL.Path == "/projects/p1/sections" && r.Method == http.MethodPost) ||
			(r.URL.Path == "/tags/g1" && r.Method == http.MethodDelete) || (r.URL.Path == "/sections/s2" && r.Method == http.MethodDelete) {
			rw.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return true
		}
		return false
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return at }}
	tags := &app.Tags{Store: st, Clock: func() time.Time { return at }}
	if _, err := sections.CreateHere(ctx, "p1", "新しい", "", ""); err != nil {
		t.Fatal(err)
	}
	must(tags.DeleteHere(ctx, "g1"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 2 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, feature := range []string{plan.Sections, plan.Tags} {
		if got, ok, _ := st.PlanRefusedAt(ctx, feature, "w1"); !ok || !got.Equal(at) {
			t.Errorf("%s: recorded for w1 = (%v, %v)", feature, got, ok)
		}
		if _, ok, _ := st.PlanRefusedAt(ctx, feature, "w0"); ok {
			t.Errorf("%s: recorded for the default workspace", feature)
		}
	}
	must(discardQueued(ctx, st))

	// A delete of a section: its project's workspace too.
	at = at.Add(plan.RecheckAfter)
	must(st.ClearPlanRefusal(ctx, plan.Sections, "w1"))
	must(sections.DeleteHere(ctx, "s2"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push of the delete = %+v, %v", res, err)
	}
	if got, ok, _ := st.PlanRefusedAt(ctx, plan.Sections, "w1"); !ok || !got.Equal(at) {
		t.Errorf("delete: recorded for w1 = (%v, %v)", got, ok)
	}
}

// #68: a tag that no task carries still remembers its workspace (from the poll's
// GET /tags?workspace= and from FindTags typeahead), so a 402 to renaming or deleting it
// is recorded for its own workspace rather than the default one.
func TestUnusedTagWorkspaceAnd402PerWorkspace(t *testing.T) {
	ctx := context.Background()
	f, s, st := setupSections(t)
	at := now
	s.now = func() time.Time { return at }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Two workspaces: w0 is the default, w1 is the second.
	// Sync poll fetches unused tags g0 (in w0) and g_unused (in w1) without any tasks carrying them.
	f.tags["g0"] = &asana.Tag{GID: "g0", Name: "既定タグ"}
	f.tags["g_unused"] = &asana.Tag{GID: "g_unused", Name: "未使用タグ"}
	refuse := false
	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		switch {
		case r.URL.Path == "/workspaces":
			fmt.Fprint(rw, `{"data":[{"gid":"w0","name":"A"},{"gid":"w1","name":"B"}]}`)
			return true
		case r.URL.Path == "/tags" && r.Method == http.MethodGet:
			if r.URL.Query().Get("workspace") == "w1" {
				// Even if the response omits workspace, sync records w1.
				fmt.Fprint(rw, `{"data":[{"gid":"g_unused","name":"未使用タグ"}]}`)
			} else {
				fmt.Fprint(rw, `{"data":[{"gid":"g0","name":"既定タグ","workspace":{"gid":"w0"}}]}`)
			}
			return true
		case r.URL.Path == "/users" || r.URL.Path == "/projects" || r.URL.Path == "/tasks" ||
			r.URL.Path == "/users/me/favorites" || r.URL.Path == "/users/me/teams" ||
			r.URL.Path == "/users/me/notifications" ||
			strings.HasSuffix(r.URL.Path, "/custom_fields") || strings.HasSuffix(r.URL.Path, "/sections"):
			fmt.Fprint(rw, `{"data":[]}`)
			return true
		case r.URL.Path == "/users/me/user_task_list":
			fmt.Fprint(rw, `{"data":{"gid":"utl"}}`)
			return true
		case refuse && r.URL.Path == "/tags/g_unused" && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
			rw.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return true
		}
		return false
	}
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	for gid, want := range map[string]string{"g0": "w0", "g_unused": "w1"} {
		if got, err := st.TagWorkspace(ctx, gid); err != nil || got != want {
			t.Fatalf("after sync: TagWorkspace(%s) = %q, %v; want %q", gid, got, err, want)
		}
	}

	// Updating an unused tag in w1 gets a 402 -> recorded for w1 only, not default w0.
	refuse = true
	tags := &app.Tags{Store: st, Clock: func() time.Time { return at }}
	must(tags.UpdateHere(ctx, "g_unused", map[string]any{"name": "新名前"}))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push update unused tag = %+v, %v", res, err)
	}
	if got, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w1"); !ok || !got.Equal(at) {
		t.Errorf("update unused tag: w1 = (%v, %v), want %v", got, ok, at)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w0"); ok {
		t.Error("update unused tag: default w0 wrongly marked")
	}
	// g_unused (w1) is refused; g0 (w0) is still allowed.
	if err := tags.UpdateHere(ctx, "g_unused", map[string]any{"name": "別名"}); !app.IsPlanUnavailable(err) {
		t.Errorf("g_unused in w1 after 402: err = %v", err)
	}
	if err := tags.UpdateHere(ctx, "g0", map[string]any{"name": "既定改名"}); err != nil {
		t.Errorf("g0 in w0 after 402 in w1: err = %v", err)
	}
	must(discardQueued(ctx, st))

	// Deleting an unused tag in w1 gets a 402 -> still recorded for w1 only, not default w0.
	must(st.ClearPlanRefusal(ctx, plan.Tags, "w1"))
	must(tags.DeleteHere(ctx, "g_unused"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push delete unused tag = %+v, %v", res, err)
	}
	if got, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w1"); !ok || !got.Equal(at) {
		t.Errorf("delete unused tag: w1 = (%v, %v), want %v", got, ok, at)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w0"); ok {
		t.Error("delete unused tag: default w0 wrongly marked")
	}
}

// #68: FindTags (typeahead) stores the searched workspace on found tags.
func TestFindTagsStoresWorkspace(t *testing.T) {
	ctx := context.Background()
	f := &searchServer{}
	s, st := setupSearch(t, f)
	if n, err := s.FindTags(ctx, "買"); err != nil || n != 1 {
		t.Fatalf("FindTags = %d, %v", n, err)
	}
	if ws, err := st.TagWorkspace(ctx, "g7"); err != nil || ws != "w1" {
		t.Fatalf("TagWorkspace(g7) = %q, %v; want w1", ws, err)
	}
}

// #68: creating a tag offline in a non-default workspace (w2) and editing it before push
// leaves OpUpdateTag.TaskGID as the local gid even after OpAddTag replaces it with the
// real gid; a 402 (and a later success) on PUT /tags/{real} still marks/clears w2 only,
// never the default workspace w1.
func TestOfflineTagUpdate402AndClearPerWorkspace(t *testing.T) {
	ctx := context.Background()
	f, s, st := setupSections(t)
	at := now
	s.now = func() time.Time { return at }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// w1 is default; w2 is non-default with task t2.
	must(st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "Default"}, {GID: "w2", Name: "Paid"}}))
	must(st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "B", Workspace: &asana.Ref{GID: "w2"}}}))

	localGID, err := st.AddTagLocal(ctx, "t2", "", "仮タグ", at)
	must(err)
	tags := &app.Tags{Store: st, Clock: func() time.Time { return at }}
	must(tags.UpdateHere(ctx, localGID, map[string]any{"name": "本タグ"}))

	refusePut := true
	f.hook = func(rw http.ResponseWriter, r *http.Request, body map[string]any) bool {
		switch {
		case r.URL.Path == "/tags" && r.Method == http.MethodPost:
			f.tags["g999"] = &asana.Tag{GID: "g999", Name: "仮タグ", Workspace: &asana.Ref{GID: "w2"}}
			fmt.Fprint(rw, `{"data":{"gid":"g999","name":"仮タグ"}}`)
			return true
		case r.URL.Path == "/tasks/t2/addTag" && r.Method == http.MethodPost:
			fmt.Fprint(rw, `{"data":{}}`)
			return true
		case refusePut && r.URL.Path == "/tags/g999" && r.Method == http.MethodPut:
			rw.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return true
		}
		return false
	}

	// Push sends add_tag (replaces localGID -> g999) and then update_tag (TaskGID is still
	// localGID), which gets 402 -> recorded for w2 only, NOT default w1.
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 1 {
		t.Fatalf("push with 402 = %+v, %v", res, err)
	}
	if got, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w2"); !ok || !got.Equal(at) {
		t.Errorf("402 recorded for w2 = (%v, %v), want %v", got, ok, at)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w1"); ok {
		t.Error("402 wrongly recorded for default w1")
	}

	// When the failed update_tag (still carrying localGID) is retried and succeeds,
	// w2's 402 mark is cleared (and w1 stays untouched).
	refusePut = false
	must(st.RetryOp(ctx, opByKind(t, st, outbox.KindUpdateTag).ID))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("retry push = %+v, %v", res, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w2"); ok {
		t.Error("402 for w2 not cleared after success")
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w1"); ok {
		t.Error("default w1 unexpectedly marked")
	}
}
