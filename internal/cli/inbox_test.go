package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
)

// inboxStore makes the store the CLI opens (XDG_DATA_HOME is a temp dir) with three
// notifications: 1200001 unread (about the local task "棚を組む"), 1200002 read, 1300002 unread.
// The commands run here never reach Asana: listing reads the store, and the marks tested
// through the CLI stop before sending.
func inboxStore(t *testing.T, state store.InboxState) (*store.Store, string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	ctx := context.Background()
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st := createStoreAt(t, filepath.Join(config.DataDir(), "store.db"))
	task, err := createTask(ctx, st, createOpts{name: "棚を組む"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ns := []asana.Notification{
		{GID: "1200001", CreatedAt: now.Add(-time.Minute), CreatedBy: &asana.Ref{GID: "u2", Name: "Partner"},
			StoryResourceSubtype: "comment_added", Story: &asana.Story{Text: "見てね"},
			WorkObjects: []asana.WorkRef{{GID: task, ResourceType: "task", Name: "棚を組む"}}},
		{GID: "1200002", IsRead: true, CreatedAt: now.Add(-time.Hour), StoryResourceSubtype: "assigned",
			WorkObjects: []asana.WorkRef{{GID: "tz", ResourceType: "task", Name: "遠いタスク"}}},
		{GID: "1300002", CreatedAt: now.Add(-2 * time.Hour), StoryResourceSubtype: "liked"},
	}
	if err := st.ReplaceNotifications(ctx, "w1", ns); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboxState(ctx, state); err != nil {
		t.Fatal(err)
	}
	return st, task
}

func TestInboxCommandListsNotifications(t *testing.T) {
	inboxStore(t, store.InboxState{Status: store.InboxOK})
	out, err := runCLI(t, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1200001", "1200002", "1300002", "棚を組む", "Partner", "見てね", "3 件（未読 2 件）"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "1200001"), strings.Index(out, "1300002"); i > j {
		t.Errorf("not newest first:\n%s", out)
	}

	var got inboxOutput
	raw, _, err := runCLISplit(t, "--json", "inbox", "--unread", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("json %q: %v", raw, err)
	}
	if !got.Available || got.Notice != "" || got.Unread != 2 || len(got.Notifications) != 1 ||
		got.Notifications[0].GID != "1200001" || got.Notifications[0].Read || !got.Notifications[0].TaskCached || got.Recent != nil {
		t.Fatalf("json %s", raw)
	}
}

// When the notifications cannot be fetched, inbox says why and shows recent updates.
func TestInboxCommandFallsBackToRecent(t *testing.T) {
	inboxStore(t, store.InboxState{Status: store.InboxForbidden, Error: "Forbidden"})
	out, err := runCLI(t, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"auth login", "最近の更新", "棚を組む"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "1200001") {
		t.Errorf("stale notifications shown while unavailable:\n%s", out)
	}
	raw, _, err := runCLISplit(t, "--json", "inbox")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("json %q: %v", raw, err)
	}
	ns, isList := m["notifications"].([]any)
	recent, _ := m["recent"].([]any)
	if m["available"] != false || !strings.Contains(m["notice"].(string), "auth login") || !isList || len(ns) != 0 || len(recent) != 1 {
		t.Fatalf("json %s", raw)
	}

	// Explicit scopes without notifications:read: the hint names the config.
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "asagao")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[auth]\nscopes = [\"tasks:read\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "inbox"); err != nil || !strings.Contains(out, "auth.scopes") {
		t.Fatalf("scopes hint: %v\n%s", err, out)
	}
}

func TestResolveNotificationArguments(t *testing.T) {
	st, _ := inboxStore(t, store.InboxState{Status: store.InboxOK})
	ctx := context.Background()
	for _, tc := range []struct {
		args    []string
		all     bool
		read    bool
		want    string
		wantErr string
	}{
		{args: []string{"0001"}, want: "1200001"},
		{args: []string{"1200002"}, want: "1200002"},
		{args: []string{" 300002 "}, want: "1300002"},
		{args: []string{"0002"}, wantErr: "2 件"},
		{args: []string{"999"}, wantErr: "999"},
		{args: []string{""}, wantErr: "空"},
		{args: []string{"%"}, wantErr: "見つかりません"},       // not a LIKE wildcard
		{args: []string{"1_00001"}, wantErr: "見つかりません"}, // not a LIKE wildcard
		{all: true, read: true, want: "1200001,1300002"},
		{all: true, read: false, want: "1200002"},
	} {
		gids, err := resolveNotifications(ctx, st, tc.args, tc.all, tc.read)
		if tc.wantErr != "" {
			if tc.wantErr == "見つかりません" {
				if !errors.Is(err, store.ErrNotFound) {
					t.Errorf("%v: got %v %v, want not found", tc.args, gids, err)
				}
				continue
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%v: got %v %v, want error with %q", tc.args, gids, err, tc.wantErr)
			}
			continue
		}
		if err != nil || strings.Join(gids, ",") != tc.want {
			t.Errorf("%v all=%t read=%t: %v %v, want %s", tc.args, tc.all, tc.read, gids, err, tc.want)
		}
	}

	// read --all marks every unread one; unread --all brings them all back.
	gids, _ := resolveNotifications(ctx, st, nil, true, true)
	if n, err := st.MarkNotificationsLocal(ctx, gids, true, time.Now()); err != nil || n != 2 {
		t.Fatalf("read --all: %d %v", n, err)
	}
	if n, _ := st.UnreadNotifications(ctx); n != 0 {
		t.Fatalf("unread after read --all = %d", n)
	}
	if ops, _ := st.PendingOps(ctx); len(ops) != 3 { // 2 marks + the create of the local task
		t.Fatalf("queued %d", len(ops))
	}
	gids, _ = resolveNotifications(ctx, st, nil, true, false)
	if n, err := st.MarkNotificationsLocal(ctx, gids, false, time.Now()); err != nil || n != 3 {
		t.Fatalf("unread --all: %d %v", n, err)
	}
}

// The mark commands that stop before sending (nothing to send, or refused).
func TestInboxMarkCommandWithoutSending(t *testing.T) {
	st, _ := inboxStore(t, store.InboxState{Status: store.InboxOK})
	ctx := context.Background()
	if _, err := runCLI(t, "inbox", "read"); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("no ID: %v", err)
	}
	if out, err := runCLI(t, "inbox", "read", "0002", "1200002"); err == nil {
		t.Fatalf("ambiguous ID accepted:\n%s", out)
	}
	if out, err := runCLI(t, "inbox", "read", "1200002"); err != nil || !strings.Contains(out, "すでに既読") {
		t.Fatalf("already read: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "inbox", "unread", "0001"); err != nil || !strings.Contains(out, "すでに未読") {
		t.Fatalf("already unread: %v\n%s", err, out)
	}

	// Asana refused marks before (no notifications:write): the list says why, and a mark the
	// user asks for is still queued, to be tried once (Asana may allow it again).
	if err := st.SetInboxWriteForbidden(ctx, true); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "inbox"); err != nil || !strings.Contains(out, "notifications:write") || !strings.Contains(out, "1200001") {
		t.Fatalf("list while marks are refused: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "inbox", "read", "0001"); err != nil {
		t.Fatalf("mark while refused: %v", err)
	}
	if n, _ := st.UnreadNotifications(ctx); n != 1 {
		t.Fatalf("the mark should be applied locally and queued: unread %d", n)
	}
}
