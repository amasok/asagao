package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

const briefTable = `<table><tr><td>予算</td><td>100万</td></tr></table>`

// briefStoreWith is the overview fixture with p1's brief (nil: p1 has none).
func briefStoreWith(t *testing.T, brief *asana.ProjectBrief) *store.Store {
	t.Helper()
	st := overviewStore(t)
	state := store.BriefNone
	if brief != nil {
		state = store.BriefAvailable
	}
	if err := st.SaveProjectOverview(context.Background(), "p1", nil, state, brief, testNow); err != nil {
		t.Fatal(err)
	}
	return st
}

// #60: b in the overview writes the brief in the editor; its table comes as an asana-html
// block and goes back unchanged, while the text around it is changed.
func TestBriefEditInOverview(t *testing.T) {
	st := briefStoreWith(t, &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内",
		HTMLText: "<body><h1>計画</h1>年内\n" + briefTable + "</body>"})
	ctx := context.Background()
	var seen string
	h := overviewHarness(t, st, func(s string) string {
		seen = s
		s = strings.Replace(s, "\ntitle: 目的\n", "\ntitle: 新しい目的\n", 1)
		return strings.Replace(s, "年内", "春までに", 1)
	})
	h.key(" ", "O")
	if screen := h.screen(); !strings.Contains(screen, "ブリーフ「目的」") {
		t.Fatalf("overview lacks the brief:\n%s", screen)
	}
	h.key("b")
	for _, want := range []string{"title: 目的\n", "# 計画", "```asana-html\n" + briefTable + "\n```"} {
		if !strings.Contains(seen, want) {
			t.Fatalf("editor file lacks %q:\n%s", want, seen)
		}
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateBrief {
		t.Fatalf("ops = %+v", ops)
	}
	html, _ := ops[0].Payload["html_text"].(string)
	if ops[0].Payload["title"] != "新しい目的" || !strings.Contains(html, briefTable) || !strings.Contains(html, "<h1>計画</h1>春までに") {
		t.Fatalf("payload = %+v", ops[0].Payload)
	}
	if screen := h.screen(); !strings.Contains(screen, "ブリーフ「新しい目的」") || !strings.Contains(screen, "ブリーフの変更は未送信です") {
		t.Fatalf("overview not reloaded:\n%s", screen)
	}
}

// Closing the editor without changes queues nothing, even with a table in the brief.
func TestBriefEditUnchanged(t *testing.T) {
	st := briefStoreWith(t, &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内",
		HTMLText: "<body>年内\n" + briefTable + "</body>"})
	h := overviewHarness(t, st, func(s string) string { return s })
	h.key(" ", "O", "b")
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("queued without a change: %+v", ops)
	}
	if !strings.Contains(h.m.status, "変更はありません") {
		t.Fatalf("status = %q", h.m.status)
	}
}

// A project without a brief gets one with b; an empty file makes none.
func TestBriefCreateInOverview(t *testing.T) {
	st := briefStoreWith(t, nil)
	h := overviewHarness(t, st, func(s string) string { return s })
	h.key(" ", "O")
	if screen := h.screen(); !strings.Contains(screen, "ブリーフはありません") {
		t.Fatalf("overview:\n%s", screen)
	}
	h.key("b")
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("an empty brief was queued: %+v", ops)
	}
	h.m.deps.RunEditor = fakeEditor(t, func(s string) string {
		return strings.Replace(s, "\ntitle: \n", "\ntitle: 概要\n", 1) + "本文\n"
	})
	h.key("b")
	ops, _ := st.ListOps(context.Background())
	if len(ops) != 1 || ops[0].Payload["title"] != "概要" || ops[0].Payload["text"] != "本文" {
		t.Fatalf("ops = %+v", ops)
	}
}

// A broken asana-html block is refused: nothing queued, the file kept.
func TestBriefEditBadBlock(t *testing.T) {
	st := briefStoreWith(t, &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内", HTMLText: "<body>" + briefTable + "</body>"})
	h := overviewHarness(t, st, func(s string) string { return strings.Replace(s, "</table>", "", 1) })
	h.key(" ", "O", "b")
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("queued: %+v", ops)
	}
	if !strings.Contains(h.m.status, "asagao-brief-") || !strings.Contains(h.m.status, "asana-html") {
		t.Fatalf("status = %q", h.m.status)
	}
}

// B deletes the brief after a confirmation; "やめる" (the default) keeps it.
func TestBriefDeleteInOverview(t *testing.T) {
	st := briefStoreWith(t, &asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内", HTMLText: "<body>年内</body>"})
	h := overviewHarness(t, st, func(s string) string { return s })
	h.key(" ", "O", "B")
	if screen := h.screen(); !strings.Contains(screen, "ブリーフを削除しますか") {
		t.Fatalf("no confirmation shown:\n%s", screen)
	}
	h.key("enter") // やめる
	if ops, _ := st.ListOps(context.Background()); len(ops) != 0 {
		t.Fatalf("deleted without confirming: %+v", ops)
	}
	h.key("B", "j", "enter")
	ops, _ := st.ListOps(context.Background())
	if len(ops) != 1 || ops[0].Payload["delete"] != true {
		t.Fatalf("ops = %+v", ops)
	}
	if screen := h.screen(); !strings.Contains(screen, "ブリーフの削除は未送信です") {
		t.Fatalf("overview:\n%s", screen)
	}
}

// While Asana refuses the brief in the workspace, b says so and opens no editor.
func TestBriefRefusedInWorkspace(t *testing.T) {
	st := briefStoreWith(t, nil)
	if err := st.RecordPlanRefusal(context.Background(), plan.ProjectBrief, "w1", testNow); err != nil {
		t.Fatal(err)
	}
	opened := false
	h := overviewHarness(t, st, func(s string) string { opened = true; return s })
	h.key(" ", "O", "b")
	if opened || !strings.Contains(h.m.status, "プロジェクトブリーフ") {
		t.Fatalf("opened=%v status=%q", opened, h.m.status)
	}
}
