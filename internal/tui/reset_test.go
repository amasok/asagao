package tui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	_ "modernc.org/sqlite"

	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
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
	ctx, st := context.Background(), h.m.deps.Store.(*store.Store)
	_, _ = st.AddCommentLocal(ctx, "t2", "大事なメモ", store.UserRef{GID: "me"}, testNow)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)

	h.run(h.m.startReset())
	if h.m.picker == nil || h.m.picker.kind != pickReset || !strings.Contains(h.m.picker.title, "2 件") ||
		!strings.Contains(h.m.picker.title, "破棄した変更の一覧（!）") {
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
// user is asked again with the new count, both with none and with some unsent changes.
func TestResetAsksAgainWhenChangesAppear(t *testing.T) {
	for _, before := range []int{0, 1} {
		t.Run(fmt.Sprint(before, " unsent"), func(t *testing.T) {
			h, dir := resetHarness(t)
			ctx, st := context.Background(), h.m.deps.Store.(*store.Store)
			if before == 1 {
				_, _ = st.AddCommentLocal(ctx, "t2", "確認したメモ", store.UserRef{GID: "me"}, testNow)
			}
			h.run(h.m.startReset())
			if want := fmt.Sprintf("%d 件", before); before > 0 && !strings.Contains(h.m.picker.title, want) {
				t.Fatalf("title = %q, want %q", h.m.picker.title, want)
			}
			_, _ = st.AddCommentLocal(ctx, "t2", "あとから足したメモ", store.UserRef{GID: "me"}, testNow) // meanwhile
			h.key("j", "enter")
			want := fmt.Sprintf("%d 件", before+1)
			if h.m.picker == nil || !strings.Contains(h.m.picker.title, want) || !strings.Contains(h.m.status, "増えた") {
				t.Fatalf("should ask again with %s: status=%q picker=%+v", want, h.m.status, h.m.picker)
			}
			if n, _ := st.UnsentCount(ctx); n != before+1 {
				t.Fatalf("nothing should be deleted: %d unsent", n)
			}
			if files, _ := os.ReadDir(dir); len(files) != 0 {
				t.Fatalf("no backup yet: %v", files)
			}
			h.key("j", "enter") // confirmed with the new count
			if n, _ := st.UnsentCount(ctx); n != 0 {
				t.Fatalf("now everything confirmed is deleted: %d", n)
			}
		})
	}
}

func TestAccountChangedStatus(t *testing.T) {
	h, _ := resetHarness(t)
	ctx, st := context.Background(), h.m.deps.Store.(*store.Store)
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

// Another asagao syncing is not "offline": the last sync time stays and a retry is
// scheduled quietly.
func TestSyncBusyIsNotOffline(t *testing.T) {
	h, _ := resetHarness(t)
	h.m.lastSync = testNow
	h.m.syncing = true
	_, cmd := h.m.Update(syncDoneMsg{err: fmt.Errorf("x: %w", sync.ErrPushBusy), at: testNow.Add(time.Minute)})
	if cmd == nil {
		t.Fatal("a retry should be scheduled")
	}
	if h.m.syncErr != nil || !h.m.lastSync.Equal(testNow) {
		t.Fatalf("busy must not count as an error or a sync: err=%v last=%v", h.m.syncErr, h.m.lastSync)
	}
	s, ok := h.m.syncStatus()
	if !ok || !strings.Contains(s, "別の asagao が同期中") || strings.Contains(s, "オフライン") {
		t.Fatalf("header = %q", s)
	}
	// the next successful sync clears it
	h.m.Update(syncDoneMsg{at: testNow.Add(2 * time.Minute)})
	if s, _ := h.m.syncStatus(); strings.Contains(s, "別の asagao") {
		t.Fatalf("header after a sync = %q", s)
	}
}

// A broken record of a dropped change must not keep the outbox from opening.
func TestOutboxOpensWhenDiscardsCannotBeRead(t *testing.T) {
	h, _ := resetHarness(t)
	ctx, st := context.Background(), h.m.deps.Store.(*store.Store)
	_ = st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "ローカル"}, testNow)
	db, err := sql.Open("sqlite", st.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO discarded_changes(kind, summary, discarded_at, payload) VALUES('add_comment', 'x', '2026-11-13T00:00:00Z', '{broken')`); err != nil {
		t.Fatal(err)
	}
	h.key("!")
	if len(h.m.ops) != 1 || !strings.Contains(h.screen(), "ローカル") || !strings.Contains(h.m.status, "破棄した変更の記録を読み込めませんでした") {
		t.Fatalf("ops=%d status=%q\n%s", len(h.m.ops), h.m.status, h.screen())
	}
}

// Busy retries back off (5s, 10s, 20s, 40s, then every minute) and start over after a sync.
func TestBusyRetryBacksOff(t *testing.T) {
	var got []time.Duration
	for n := 0; n < 7; n++ {
		got = append(got, busyRetryDelay(n))
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute, time.Minute}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
	h, _ := resetHarness(t)
	busy := syncDoneMsg{err: sync.ErrPushBusy, at: testNow}
	for i := 0; i < 3; i++ {
		h.m.Update(busy)
	}
	if h.m.busyRetries != 3 {
		t.Fatalf("busyRetries = %d", h.m.busyRetries)
	}
	h.m.Update(syncDoneMsg{at: testNow})
	if h.m.busyRetries != 0 {
		t.Fatalf("a sync should reset the back-off: %d", h.m.busyRetries)
	}
}
