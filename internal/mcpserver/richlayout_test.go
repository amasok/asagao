package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/wire"
	"regexp"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// layoutTag matches the tags Asana accepts only in tasks and project briefs: headings,
// rules, images and tables (https://developers.asana.com/docs/rich-text).
var layoutTag = regexp.MustCompile(`(?i)<(h[1-6]|hr|img|table|tr|td|object)\b`)

// add_comment and create_status_update with a heading, a rule and an image queue rich text
// without the tags Asana rejects there (#63), keeping what was written; update_task keeps
// them in the notes.
func TestMCPRichTextTagsPerTarget(t *testing.T) {
	st := seed(t)
	ctx := context.Background()
	cs := connect(t, &Server{App: wire.UseCases(st)})
	md := "# 予定\n\n---\n\n![図](asana:42)"
	var w WriteOutput
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"add_comment", map[string]any{"gid": "t1", "text": md}},
		{"create_status_update", map[string]any{"project": "p1", "status": "on_track", "title": "週報", "text": md}},
		{"update_task", map[string]any{"gid": "t2", "notes": md}},
	} {
		if res := call(t, cs, c.tool, c.args, &w); res.IsError {
			t.Fatalf("%s: %s", c.tool, toolError(res))
		}
	}
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[store.Kind]bool{}
	for _, op := range ops {
		seen[op.Kind] = true
		if op.Kind == store.OpUpdateTask {
			h, _ := op.Payload["html_notes"].(string)
			for _, want := range []string{"<h1>予定</h1>", "<hr/>", `<img data-asana-gid="42"/>`} {
				if !strings.Contains(h, want) {
					t.Errorf("html_notes should keep %s: %q", want, h)
				}
			}
			continue
		}
		h, _ := op.Payload["html_text"].(string)
		if m := layoutTag.FindString(h); m != "" {
			t.Errorf("%s queued with %s>, which Asana rejects there: %q", op.Kind, m, h)
		}
		for _, want := range []string{"<strong>予定</strong>", "---", "![図](asana:42)"} {
			if !strings.Contains(h, want) {
				t.Errorf("%s lost %q: %q", op.Kind, want, h)
			}
		}
	}
	if !seen[store.OpAddComment] || !seen[store.OpCreateStatus] || !seen[store.OpUpdateTask] {
		t.Fatalf("ops = %v", seen)
	}
}
