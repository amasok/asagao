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
	"github.com/amasok/asagao/internal/readmodel"
)

// #50: the 402s of sections, tags, My Tasks' sections, search, typeahead and custom IDs
// are recorded per workspace (readmodel.PlanKey) and tried again after a day, like the
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
		if err := s.syncMySections(ctx, ws); err != nil {
			t.Fatal(err)
		}
	}
	key := readmodel.PlanKey(readmodel.PlanMySection, "w1")
	if v, _ := st.SyncState(ctx, key); v != now.Format(time.RFC3339) {
		t.Fatalf("402 in w1 recorded = %q", v)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanMySection, "w2")); v != "" {
		t.Fatalf("w2 recorded = %q", v)
	}
	sections := &app.Sections{Store: st, Clock: func() time.Time { return *now }}
	if secs, err := sections.MySections(ctx, "w2"); err != nil || len(secs) != 2 {
		t.Fatalf("w2's sections after a 402 in w1 = %v, %v", secs, err)
	}
	if secs, err := sections.MySections(ctx, "w1"); err != nil || len(secs) != 0 {
		t.Fatalf("w1's sections after a 402 there = %v, %v", secs, err)
	}

	// Within the day w1 is not asked again.
	*now = now.Add(readmodel.PlanRecheckAfter - time.Minute)
	if err := s.syncMySections(ctx, "w1"); err != nil {
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
	if err := s.syncMySections(ctx, "w1"); err != nil {
		t.Fatal(err)
	}
	if n := f.listsIn("w1"); n != 2 {
		t.Fatalf("w1 asked %d times after the day", n)
	}
	if v, _ := st.SyncState(ctx, key); v != "" {
		t.Fatalf("402 not forgotten after an answer: %q", v)
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
	if err := st.SetSyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanMySection, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.syncMySections(ctx, "w1"); err != nil {
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
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanMySection, "w1")); v != now.Format(time.RFC3339) {
		t.Fatalf("recorded for w1 = %q", v)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanMySection); v != "" {
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
	for _, feature := range []string{readmodel.PlanSections, readmodel.PlanTags} {
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(feature, "w1")); v != at.Format(time.RFC3339) {
			t.Errorf("%s: recorded for w1 = %q", feature, v)
		}
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(feature, "w2")); v != "" {
			t.Errorf("%s: recorded for w2 = %q", feature, v)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+feature); v != "" {
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
	at = at.Add(readmodel.PlanRecheckAfter)
	refuse = false
	must(tags.UpdateHere(ctx, "g1", map[string]any{"name": "至急"}))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push a day later = %+v, %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1")); v != "" {
		t.Errorf("tags' 402 not forgotten after an answer: %q", v)
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
	for _, k := range []string{readmodel.PlanKey(readmodel.PlanSearch, "w2"), readmodel.PlanKey(readmodel.PlanTypeahead, "w2"),
		readmodel.PlanKey(readmodel.PlanCustomID, "w2"), readmodel.KeyPlanUnavailable + readmodel.PlanSearch,
		readmodel.KeyPlanUnavailable + readmodel.PlanTypeahead, readmodel.KeyPlanUnavailable + readmodel.PlanCustomID} {
		if err := st.SetSyncState(ctx, k, at.Format(time.RFC3339)); err != nil {
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
	key := readmodel.PlanKey(readmodel.PlanSearch, "w1")
	if v, _ := st.SyncState(ctx, key); v != at.Format(time.RFC3339) {
		t.Fatalf("recorded for w1 = %q", v)
	}
	at = at.Add(readmodel.PlanRecheckAfter - time.Second)
	if _, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || f.searchCalls != 2 {
		t.Fatalf("within the day: %v, %d calls", err, f.searchCalls)
	}
	at = at.Add(time.Second)
	f.searchStatus = 0
	if res, err := s.SearchTasks(ctx, "x", nil, 10); err != nil || res.ByName || f.searchCalls != 3 {
		t.Fatalf("a day later = %+v, %v, %d calls", res, err, f.searchCalls)
	}
	if v, _ := st.SyncState(ctx, key); v != "" {
		t.Fatalf("402 not forgotten after an answer: %q", v)
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
	for _, feature := range []string{readmodel.PlanSections, readmodel.PlanTags} {
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(feature, "w1")); v != at.Format(time.RFC3339) {
			t.Errorf("%s: recorded for w1 = %q", feature, v)
		}
		if v, _ := st.SyncState(ctx, readmodel.PlanKey(feature, "w0")); v != "" {
			t.Errorf("%s: recorded for the default workspace = %q", feature, v)
		}
	}
	must(discardQueued(ctx, st))

	// A delete of a section: its project's workspace too.
	at = at.Add(readmodel.PlanRecheckAfter)
	must(st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanSections, "w1"), ""))
	must(sections.DeleteHere(ctx, "s2"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push of the delete = %+v, %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanSections, "w1")); v != at.Format(time.RFC3339) {
		t.Errorf("delete: recorded for w1 = %q", v)
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
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1")); v != at.Format(time.RFC3339) {
		t.Errorf("update unused tag: w1 = %q, want %q", v, at.Format(time.RFC3339))
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w0")); v != "" {
		t.Errorf("update unused tag: default w0 wrongly marked = %q", v)
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
	must(st.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1"), ""))
	must(tags.DeleteHere(ctx, "g_unused"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push delete unused tag = %+v, %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1")); v != at.Format(time.RFC3339) {
		t.Errorf("delete unused tag: w1 = %q, want %q", v, at.Format(time.RFC3339))
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w0")); v != "" {
		t.Errorf("delete unused tag: default w0 wrongly marked = %q", v)
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
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w2")); v != at.Format(time.RFC3339) {
		t.Errorf("402 recorded for w2 = %q, want %q", v, at.Format(time.RFC3339))
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1")); v != "" {
		t.Errorf("402 wrongly recorded for default w1 = %q", v)
	}

	// When the failed update_tag (still carrying localGID) is retried and succeeds,
	// w2's 402 mark is cleared (and w1 stays untouched).
	refusePut = false
	must(st.RetryOp(ctx, opByKind(t, st, outbox.KindUpdateTag).ID))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("retry push = %+v, %v", res, err)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w2")); v != "" {
		t.Errorf("402 for w2 not cleared after success: %q", v)
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanTags, "w1")); v != "" {
		t.Errorf("default w1 unexpectedly marked: %q", v)
	}
}
