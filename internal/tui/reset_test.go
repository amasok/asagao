package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

func resetHarness(t *testing.T) (*harness, string) {
	t.Helper()
	dir := t.TempDir()
	m := New(context.Background(), Deps{Store: fixtureStore(t), Now: func() time.Time { return testNow }, BackupDir: dir})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 30})
	h.run(m.Init())
	return h, dir
}

// The reset asks first, showing how many unsent changes would go; "やめる" keeps everything.
func TestResetAsksWithUnsentCount(t *testing.T) {
	h, dir := resetHarness(t)
	ctx, st := context.Background(), h.m.deps.Store
	_, _ = st.AddCommentLocal(ctx, "t2", "大事なメモ", store.UserRef{GID: "me"}, testNow)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)

	h.run(h.m.startReset())
	if h.m.picker == nil || h.m.picker.kind != pickReset || !strings.Contains(h.m.picker.title, "2 件") {
		t.Fatalf("confirmation = %+v", h.m.picker)
	}
	h.key("enter") // the first item: やめる
	if n, _ := st.UnsentCount(ctx); n != 2 {
		t.Fatalf("cancel must keep everything: %d unsent", n)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("cancel must not write a backup: %v", files)
	}

	h.run(h.m.startReset())
	h.key("j", "enter") // 未送信の 2 件ごと初期化する
	files, _ := os.ReadDir(dir)
	if len(files) != 1 || !strings.Contains(h.m.status, files[0].Name()) || !strings.Contains(h.m.status, "2 件") {
		t.Fatalf("status should show the backup: %q (%v)", h.m.status, files)
	}
	if n, _ := st.UnsentCount(ctx); n != 0 {
		t.Fatalf("outbox should be empty: %d", n)
	}
	if _, err := st.GetTask(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("local data should be gone: %v", err)
	}
	if strings.Contains(h.screen(), "ソファを買う") {
		t.Fatalf("the screen should show the empty store:\n%s", h.screen())
	}
	// what was deleted is still listed, in full, marked as deleted by the reset
	h.key("!")
	if s := h.screen(); !strings.Contains(s, "（初期化で破棄）") || !strings.Contains(s, "大事なメモ") {
		t.Fatalf("outbox after reset:\n%s", s)
	}
}

// If changes are added between the question and the answer, nothing is deleted and the
// user is asked again with the new count.
func TestResetAsksAgainWhenChangesAppear(t *testing.T) {
	h, dir := resetHarness(t)
	ctx, st := context.Background(), h.m.deps.Store
	h.run(h.m.startReset())
	if !strings.Contains(h.m.picker.title, "ログイン情報は残り") {
		t.Fatalf("no unsent changes: %q", h.m.picker.title)
	}
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow) // meanwhile
	h.key("j", "enter")
	if h.m.picker == nil || !strings.Contains(h.m.picker.title, "1 件") || !strings.Contains(h.m.status, "増えた") {
		t.Fatalf("should ask again: status=%q picker=%+v", h.m.status, h.m.picker)
	}
	if _, err := st.GetTask(ctx, "t1"); err != nil {
		t.Fatalf("nothing should be deleted: %v", err)
	}
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Fatalf("no backup yet: %v", files)
	}
}

func TestAccountChangedStatus(t *testing.T) {
	h, _ := resetHarness(t)
	ctx, st := context.Background(), h.m.deps.Store
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)
	h.run(h.m.loadOutbox())
	h.send(syncDoneMsg{err: fmt.Errorf("%w (other / me)", sync.ErrAccountChanged), at: testNow})
	for _, want := range []string{"1 件", "ログインし直せば", "space I で初期化"} {
		if !strings.Contains(h.m.status, want) {
			t.Errorf("status missing %q: %q", want, h.m.status)
		}
	}
	if s, _ := h.m.syncStatus(); !strings.Contains(s, "アカウント") {
		t.Errorf("header = %q", s)
	}
}
