package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// A comment and a status update are written with the time of the key press (when the
// command is made), not the time the command runs, as before stage a4.
func TestTUICommentAndStatusUseTheTimeOfTheKeyPress(t *testing.T) {
	ctx := context.Background()
	st := fixtureStore(t)
	pressed := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	cur := pressed
	m := New(ctx, Deps{Store: st, Now: func() time.Time { return cur }})
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cmds := []tea.Cmd{
		m.addComment("t1", "a"),
		m.applyComment(commentDoneMsg{gid: "t2", path: write("c.md", commentTemplate+"b")}),
		m.applyStatus(statusEditDoneMsg{project: "p1", path: write("s.md", "---\nstatus: on_track\ntitle: 週報\n---\n\nc")}),
	}
	cur = pressed.Add(time.Hour)
	for _, cmd := range cmds {
		if cmd == nil {
			t.Fatalf("no command (status %q)", m.status)
		}
		if msg, ok := cmd().(writeDoneMsg); !ok || msg.err != nil {
			t.Fatalf("write = %+v", msg)
		}
	}
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 {
		t.Fatalf("%d ops, want 3", len(ops))
	}
	want := pressed.UTC().Format(time.RFC3339)
	for _, op := range ops {
		if op.CreatedAt != want {
			t.Errorf("%s queued at %s, want the key press %s", op.Kind, op.CreatedAt, want)
		}
	}
}
