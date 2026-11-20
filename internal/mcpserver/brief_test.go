package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

const mcpBriefTable = `<table><tr><td>予算</td><td>100万</td></tr></table>`

// #60: get_project_brief gives the body with its table as an asana-html block; giving it
// back unchanged queues nothing, an edit around the block keeps the table, and delete
// queues the delete.
func TestProjectBriefTools(t *testing.T) {
	st := seedProjects(t)
	ctx := context.Background()
	if err := st.SaveProjectOverview(ctx, "p1", nil, store.BriefAvailable,
		&asana.ProjectBrief{GID: "b1", Title: "目的", Text: "年内", HTMLText: "<body>年内\n" + mcpBriefTable + "</body>"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var b BriefDetailOutput
	call(t, cs, "get_project_brief", map[string]any{"project": "新居"}, &b)
	if b.State != "available" || b.Title != "目的" || !strings.Contains(b.Markdown, "```asana-html\n"+mcpBriefTable+"\n```") {
		t.Fatalf("brief = %+v", b)
	}
	var w ProjectWriteOutput
	call(t, cs, "update_project_brief", map[string]any{"project": "新居", "markdown": b.Markdown}, &w)
	if ops, _ := st.ListOps(ctx); len(ops) != 0 || w.Note != "no change" {
		t.Fatalf("unchanged body queued: %+v %+v", ops, w)
	}
	md := strings.Replace(b.Markdown, "年内", "春まで", 1)
	if msg := toolError(call(t, cs, "update_project_brief", map[string]any{"project": "新居", "markdown": md, "title": "計画"}, &w)); msg != "" {
		t.Fatal(msg)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateBrief || ops[0].Payload["title"] != "計画" ||
		!strings.Contains(ops[0].Payload["html_text"].(string), mcpBriefTable) || w.Sent == nil || *w.Sent {
		t.Fatalf("ops = %+v out %+v", ops, w)
	}
	// get_project shows the brief as get_project_brief does
	var p ProjectOutput
	call(t, cs, "get_project", map[string]any{"project": "新居"}, &p)
	if p.Brief == nil || !strings.Contains(p.Brief.Markdown, "```asana-html") {
		t.Fatalf("get_project brief = %+v", p.Brief)
	}
	call(t, cs, "update_project_brief", map[string]any{"project": "新居", "delete": true}, &w)
	if ops, _ := st.ListOps(ctx); len(ops) != 1 || ops[0].Payload["delete"] != true {
		t.Fatalf("after delete: %+v", ops)
	}
	call(t, cs, "get_project_brief", map[string]any{"project": "新居"}, &b)
	if b.State != "none" || !b.Pending {
		t.Fatalf("after delete: %+v", b)
	}
}

// Refused: nothing to do, delete with a body, a broken asana-html block, and the brief
// while Asana refuses it in the workspace.
func TestProjectBriefToolRefuses(t *testing.T) {
	st := seedProjects(t)
	ctx := context.Background()
	_ = st.SaveProjectOverview(ctx, "p1", nil, store.BriefNone, nil, time.Now())
	cs := connect(t, &Server{Store: st})
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"project": "新居"}, "nothing to update"},
		{map[string]any{"project": "新居", "delete": true, "title": "x"}, "delete"},
		{map[string]any{"project": "新居", "markdown": "```asana-html\n<script></script>\n```"}, "asana-html"},
		{map[string]any{"project": "新居", "delete": true}, "ブリーフがありません"},
	} {
		if msg := toolError(call(t, cs, "update_project_brief", tc.args, nil)); !strings.Contains(msg, tc.want) {
			t.Errorf("%v: %q, want %q", tc.args, msg, tc.want)
		}
	}
	_ = st.RecordPlanRefusal(ctx, plan.ProjectBrief, "w1", testNow)
	if msg := toolError(call(t, cs, "update_project_brief", map[string]any{"project": "新居", "title": "x"}, nil)); !strings.Contains(msg, "プロジェクトブリーフ") {
		t.Errorf("refused workspace: %q", msg)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("queued: %+v", ops)
	}
}
