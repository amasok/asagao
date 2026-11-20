package tui

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/store"
)

// layoutTag matches the tags Asana accepts only in tasks and project briefs: headings,
// rules, images and tables (https://developers.asana.com/docs/rich-text).
var layoutTag = regexp.MustCompile(`(?i)<(h[1-6]|hr|img|table|tr|td|object)\b`)

// A comment (from the input line or the editor) and a status update written in the TUI with
// a heading, a rule and an image are queued without the tags Asana rejects there (#63),
// keeping what was written.
func TestTUICommentAndStatusWithoutLayoutTags(t *testing.T) {
	ctx := context.Background()
	st := fixtureStore(t)
	m := New(ctx, onStore(st, Deps{Now: func() time.Time { return testNow }}))
	md := "# 予定\n\n---\n\n![図](asana:42)"
	run := func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			t.Fatalf("no command (status %q)", m.status)
		}
		if msg, ok := cmd().(writeDoneMsg); !ok || msg.err != nil {
			t.Fatalf("write = %+v", msg)
		}
	}
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run(m.addComment("t1", md))
	run(m.applyComment(commentDoneMsg{gid: "t2", path: write("c.md", commentTemplate+md)}))
	run(m.applyStatus(statusEditDoneMsg{project: "p1", path: write("s.md", "---\nstatus: on_track\ntitle: 週報\n---\n\n"+md)}))
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[store.Kind]int{}
	for _, op := range ops {
		kinds[op.Kind]++
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
	if kinds[store.OpAddComment] != 2 || kinds[store.OpCreateStatus] != 1 {
		t.Fatalf("ops = %v", kinds)
	}
}
