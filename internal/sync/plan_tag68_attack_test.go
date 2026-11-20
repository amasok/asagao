package sync

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

// #68 attack: unused tags (attached to NO tasks) across 3 workspaces:
//   - w0: default workspace
//   - w1: free workspace (Asana returns 402 on tag writes)
//   - w2: paid workspace (Asana allows tag writes)
//
// Exercises Rename, SetColor, Delete on w1's unused tag -> 402 recorded ONLY for w1 (never
// w0 or w2), w2's unused tag keeps working for Rename/SetColor/Delete, and discarding a
// 402-failed Delete (undoTagOp) restores w1's unused tag WITH workspace_gid='w1'.
func TestUnusedTagsMultiWorkspaceRenameColorDeleteAndUndo402Attack(t *testing.T) {
	ctx := context.Background()
	f, s, st := setupSections(t)
	at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return at }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(st.UpsertWorkspaces(ctx, []asana.Workspace{
		{GID: "w0", Name: "Default"},
		{GID: "w1", Name: "Free"},
		{GID: "w2", Name: "Paid"},
	}))
	// Unused tags: no task in the store carries either of them.
	must(st.UpsertTags(ctx, []asana.Tag{
		{GID: "g_w1", Name: "無料タグ", Color: "dark-red", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "g_w2", Name: "有料タグ", Color: "dark-blue", Workspace: &asana.Ref{GID: "w2"}},
	}))
	f.tags["g_w1"] = &asana.Tag{GID: "g_w1", Name: "無料タグ", Color: "dark-red", Workspace: &asana.Ref{GID: "w1"}}
	f.tags["g_w2"] = &asana.Tag{GID: "g_w2", Name: "有料タグ", Color: "dark-blue", Workspace: &asana.Ref{GID: "w2"}}

	f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
		if r.URL.Path == "/tags/g_w1" && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
			rw.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return true
		}
		return false
	}

	tags := &app.Tags{Store: st, Clock: func() time.Time { return at }}
	checkOnlyW1Marked := func(step string) {
		t.Helper()
		if got, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, "w1"); !ok || !got.Equal(at) {
			t.Fatalf("%s: w1 mark = (%v, %v), want %v", step, got, ok, at)
		}
		for _, ws := range []string{"w0", "w2", ""} {
			if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, ws); ok {
				t.Fatalf("%s: workspace %q wrongly marked", step, ws)
			}
		}
	}

	// 1. Rename unused tag in w1 -> 402 -> recorded for w1 only; w2's Rename still works.
	must(tags.Rename(ctx, "g_w1", "無料改名"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push rename g_w1 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("Rename")
	// Discard failed rename op and verify g_w1 still knows w1.
	ops, err := st.ListOps(ctx)
	must(err)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateTag {
		t.Fatalf("ops after rename 402 = %+v", ops)
	}
	must(st.DiscardOp(ctx, ops[0].ID))
	if ws, err := st.TagWorkspace(ctx, "g_w1"); err != nil || ws != "w1" {
		t.Fatalf("after rename discard: TagWorkspace(g_w1) = %q, %v", ws, err)
	}
	// g_w1 is now blocked locally; g_w2 Rename works and pushes cleanly!
	if err := tags.Rename(ctx, "g_w1", "ブロック"); !app.IsPlanUnavailable(err) {
		t.Fatalf("g_w1 rename after 402: err = %v, want ErrPlanUnavailable", err)
	}
	must(tags.Rename(ctx, "g_w2", "有料改名"))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("push rename g_w2 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("after g_w2 Rename")

	// 2. Clear w1 mark, test SetColor on unused tag in w1 -> 402 -> w1 only; w2 SetColor works.
	must(st.ClearPlanRefusal(ctx, plan.Tags, "w1"))
	must(tags.SetColor(ctx, "g_w1", "dark-green"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push color g_w1 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("SetColor")
	must(discardQueued(ctx, st))
	if err := tags.SetColor(ctx, "g_w1", "dark-pink"); !app.IsPlanUnavailable(err) {
		t.Fatalf("g_w1 color after 402: err = %v, want ErrPlanUnavailable", err)
	}
	must(tags.SetColor(ctx, "g_w2", "dark-purple"))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("push color g_w2 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("after g_w2 SetColor")

	// 3. Clear w1 mark, test Delete on unused tag in w1 -> 402 -> w1 only; DiscardOp (undoTagOp)
	// restores g_w1 WITH workspace_gid='w1'.
	must(st.ClearPlanRefusal(ctx, plan.Tags, "w1"))
	must(tags.Delete(ctx, "g_w1"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("push delete g_w1 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("Delete")

	// Discard the failed OpDeleteTag -> undoTagOp must restore g_w1 with workspace_gid='w1'.
	ops, err = st.ListOps(ctx)
	must(err)
	if len(ops) != 1 || ops[0].Kind != store.OpDeleteTag || ops[0].Payload["workspace"] != "w1" {
		t.Fatalf("delete op after 402 = %+v", ops)
	}
	must(st.DiscardOp(ctx, ops[0].ID))

	if ws, err := st.TagWorkspace(ctx, "g_w1"); err != nil || ws != "w1" {
		t.Fatalf("after 402 delete + undoTagOp: TagWorkspace(g_w1) = %q, %v; want w1", ws, err)
	}
	if tc, err := st.GetTag(ctx, "g_w1"); err != nil || tc.Name != "無料タグ" || tc.Color != "dark-red" {
		t.Fatalf("restored g_w1 = %+v, %v", tc, err)
	}
	// g_w1 is STILL refused by w1's 402 (proving its restored workspace_gid is w1, not w0/empty).
	if err := tags.Delete(ctx, "g_w1"); !app.IsPlanUnavailable(err) {
		t.Fatalf("g_w1 delete after undo: err = %v, want ErrPlanUnavailable", err)
	}
	// Meanwhile g_w2 Delete succeeds!
	must(tags.Delete(ctx, "g_w2"))
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("push delete g_w2 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("after g_w2 Delete")

	// 4. After 24h, deleting the restored g_w1 again still sends workspace='w1' on 402.
	at = at.Add(plan.RecheckAfter)
	must(tags.Delete(ctx, "g_w1"))
	if res, err := s.Push(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("second push delete g_w1 = %+v, %v", res, err)
	}
	checkOnlyW1Marked("second Delete after undoTagOp")
}

// #68 attack: non-402 error responses (401/403/404/429/500/corrupt JSON) during /tags poll
// or tag updates must not wipe existing tags.workspace_gid or wrongly record a 402 mark.
func TestTagsErrorResponsesKeepWorkspaceAndNoPlanMarkAttack(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"401", http.StatusUnauthorized, `{"errors":[{"message":"unauth"}]}`},
		{"403", http.StatusForbidden, `{"errors":[{"message":"forbidden"}]}`},
		{"404", http.StatusNotFound, `{"errors":[{"message":"not found"}]}`},
		{"429", http.StatusTooManyRequests, `{"errors":[{"message":"rate"}]}`},
		{"500", http.StatusInternalServerError, `{"errors":[{"message":"boom"}]}`},
		{"corrupt_json", http.StatusOK, `{"data":[{broken`},
		{"empty_body", http.StatusOK, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupSections(t)
			at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return at }
			if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w0", Name: "A"}, {GID: "w1", Name: "B"}}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "既存", Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
				t.Fatal(err)
			}
			f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
				switch {
				case r.URL.Path == "/workspaces":
					fmt.Fprint(rw, `{"data":[{"gid":"w0","name":"A"},{"gid":"w1","name":"B"}]}`)
					return true
				case r.URL.Path == "/tags" && r.Method == http.MethodGet:
					if tc.status == http.StatusTooManyRequests {
						rw.Header().Set("Retry-After", "0")
					}
					rw.WriteHeader(tc.status)
					fmt.Fprint(rw, tc.body)
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
				}
				return false
			}
			_, _ = s.Run(ctx, true)
			if ws, err := st.TagWorkspace(ctx, "g1"); err != nil || ws != "w1" {
				t.Errorf("after %s /tags: TagWorkspace(g1) = %q, %v; want w1", tc.name, ws, err)
			}
			for _, ws := range []string{"w0", "w1", ""} {
				if _, ok, _ := st.PlanRefusedAt(ctx, plan.Tags, ws); ok {
					t.Errorf("after %s /tags: wrongly marked %q", tc.name, ws)
				}
			}
		})
	}
}

// #68 attack: when PUT /tags/{gid} omits workspace OR returns workspace:{}, pushUpdateTag
// should preserve an existing workspace_gid and backfill an empty workspace_gid from
// GET /tags/{gid}'s workspace.
func TestPushUpdateTagWorkspaceFromServerFallbackAttack(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		putBody string
	}{
		{"omitted_workspace", `{"data":{"gid":"g_empty","name":"新名","color":"dark-red"}}`},
		{"empty_workspace_object", `{"data":{"gid":"g_empty","name":"新名","color":"dark-red","workspace":{}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, st := setupSections(t)
			at := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
			s.now = func() time.Time { return at }
			if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w0", Name: "A"}, {GID: "w1", Name: "B"}}); err != nil {
				t.Fatal(err)
			}
			// Cached unused tag starts with empty workspace_gid (e.g. unused pre-migration tag).
			if err := st.UpsertTags(ctx, []asana.Tag{{GID: "g_empty", Name: "旧名", Color: "dark-red"}}); err != nil {
				t.Fatal(err)
			}
			if ws, err := st.TagWorkspace(ctx, "g_empty"); err != nil || ws != "" {
				t.Fatalf("precondition: TagWorkspace(g_empty) = %q, %v; want empty", ws, err)
			}
			f.hook = func(rw http.ResponseWriter, r *http.Request, _ map[string]any) bool {
				if r.URL.Path == "/tags/g_empty" && r.Method == http.MethodGet {
					fmt.Fprint(rw, `{"data":{"gid":"g_empty","name":"旧名","color":"dark-red","workspace":{"gid":"w1"}}}`)
					return true
				}
				if r.URL.Path == "/tags/g_empty" && r.Method == http.MethodPut {
					fmt.Fprint(rw, tc.putBody)
					return true
				}
				return false
			}
			tags := &app.Tags{Store: st, Clock: func() time.Time { return at }}
			if err := tags.Rename(ctx, "g_empty", "新名"); err != nil {
				t.Fatal(err)
			}
			if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
				t.Fatalf("Push = %+v, %v", res, err)
			}
			if ws, err := st.TagWorkspace(ctx, "g_empty"); err != nil || ws != "w1" {
				t.Errorf("%s: TagWorkspace(g_empty) = %q, %v; want w1 (backfilled from GET /tags/g_empty)", tc.name, ws, err)
			}
		})
	}
}
