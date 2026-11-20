package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// get_task shows indentation as spaces and tabs, not as "&#32;" / "&#9;", and giving that
// Markdown back to update_task keeps the indentation in what is sent to Asana. (#22)
func TestGetTaskShowsIndentationAsWhitespace(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	const html = "<body>手順\n  字下げ\n\tタブ\n\n    四つ</body>"
	const plain = "  先頭\n\tタブ"
	if err := st.UpsertTasks(ctx, []asana.Task{
		{GID: "t8", Name: "リッチ", HTMLNotes: html, Notes: "手順\n  字下げ\n\tタブ\n\n    四つ", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "t9", Name: "プレーン", Notes: plain, Workspace: &asana.Ref{GID: "w1"}},
	}); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{App: wire.UseCases(st)})
	for gid, want := range map[string]string{"t8": "手順  \n  字下げ  \n\tタブ\n\n    四つ", "t9": "  先頭  \n\tタブ"} {
		var task GetTaskOutput
		call(t, cs, "get_task", map[string]any{"gid": gid, "offline": true}, &task)
		if md := task.DescriptionMarkdown; md != want || strings.Contains(md, "&#") {
			t.Fatalf("%s: description_markdown = %q, want %q", gid, md, want)
		}
		var w WriteOutput
		call(t, cs, "update_task", map[string]any{"gid": gid, "notes": task.DescriptionMarkdown}, &w)
	}
	if d, _ := st.GetTask(ctx, "t8"); d.HTMLNotes != html {
		t.Fatalf("html_notes after round trip = %q, want %q", d.HTMLNotes, html)
	}
	if d, _ := st.GetTask(ctx, "t9"); d.Notes != plain || d.HTMLNotes != "<body>"+plain+"</body>" {
		t.Fatalf("notes after round trip = %q / %q", d.Notes, d.HTMLNotes)
	}
}
