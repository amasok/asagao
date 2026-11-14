package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// inboxServer adds the notification endpoints to fakeAsana.
type inboxServer struct {
	fakeAsana
	listStatus int      // != 0: GET notifications answers this status
	markStatus int      // != 0: markRead / markUnread answer this status
	failed     []string // gids markRead / markUnread report as failed
	paged      bool     // answer in two pages (offset)
	extra      string   // more notifications (JSON objects, with a leading comma)
	marks      []map[string]any
}

func (f *inboxServer) handler() http.Handler {
	mux := f.fakeAsana.handler().(*http.ServeMux)
	mux.HandleFunc("GET /users/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			fmt.Fprint(w, `{"errors":[{"message":"Forbidden"}]}`)
			return
		}
		if r.URL.Query().Get("workspace") != "w1" {
			f.t.Errorf("workspace = %q", r.URL.Query().Get("workspace"))
		}
		n2 := `{"gid":"n2","is_read":false,"created_at":"2026-11-12T10:00:00Z","created_by":{"gid":"u2","name":"Hanako"},
			 "story_resource_subtype":"comment_added","story":{"gid":"st1","text":"見てください"},
			 "work_objects":[{"gid":"tx","resource_type":"task","name":"Followed task"}]}`
		n1 := `{"gid":"n1","is_read":true,"created_at":"2026-11-11T10:00:00Z","created_by":{"gid":"u2","name":"Hanako"},
			 "story_resource_subtype":"assigned","work_objects":[{"gid":"t1","resource_type":"task","name":"My task"}]}`
		if f.paged {
			if r.URL.Query().Get("offset") == "" {
				fmt.Fprintf(w, `{"data":[%s],"next_page":{"offset":"page2"}}`, n2)
			} else {
				fmt.Fprintf(w, `{"data":[%s],"next_page":null}`, n1)
			}
			return
		}
		fmt.Fprintf(w, `{"data":[%s,%s%s],"next_page":null}`, n2, n1, f.extra)
	})
	mux.HandleFunc("/tasks/tx", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"gid":"tx","name":"Followed task","modified_at":"2026-11-12T00:00:00Z"}}`)
	})
	mark := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct{ Data map[string]any }
		if err := json.Unmarshal(b, &body); err != nil {
			f.t.Errorf("mark body %s: %v", b, err)
		}
		body.Data["read"] = strings.HasSuffix(r.URL.Path, "/markRead")
		f.marks = append(f.marks, body.Data)
		if f.markStatus != 0 {
			w.WriteHeader(f.markStatus)
			fmt.Fprint(w, `{"errors":[{"message":"Forbidden"}]}`)
			return
		}
		failed, _ := json.Marshal(f.failed)
		fmt.Fprintf(w, `{"data":{"failed_gids":%s}}`, nonNull(failed))
	}
	mux.HandleFunc("POST /users/me/notifications/markRead", mark)
	mux.HandleFunc("POST /users/me/notifications/markUnread", mark)
	return mux
}

func nonNull(b []byte) string {
	if string(b) == "null" {
		return "[]"
	}
	return string(b)
}

func setupInbox(t *testing.T, f *inboxServer) (*Syncer, *store.Store) {
	t.Helper()
	f.t = t
	f.projectTasks = `[]`
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	return &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}, now: func() time.Time { return now }}, st
}

func TestSyncCachesInboxAndMarksRead(t *testing.T) {
	f := &inboxServer{}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.InboxState(ctx); !state.Available() {
		t.Fatalf("inbox state %+v", state)
	}
	ns, err := st.ListNotifications(ctx, store.NotificationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 || ns[0].GID != "n2" || ns[0].Read || ns[0].ActorName != "Hanako" || ns[0].Text != "見てください" ||
		ns[0].TargetGID != "tx" || !ns[0].TaskCached || !ns[1].Read || !ns[1].TaskCached {
		t.Fatalf("notifications %+v", ns)
	}
	if n, _ := st.UnreadNotifications(ctx); n != 1 {
		t.Fatalf("unread = %d", n)
	}

	// Offline: marked read locally and queued; a sync's replace keeps the local state.
	if n, err := st.MarkNotificationsLocal(ctx, []string{"n2"}, true, time.Now()); err != nil || n != 1 {
		t.Fatalf("mark: %d %v", n, err)
	}
	ops, _ := st.PendingOps(ctx)
	if len(ops) != 1 || ops[0].Kind != store.OpMarkNotification || ops[0].TaskName != "Followed task" {
		t.Fatalf("ops %+v", ops)
	}
	if err := st.ReplaceNotifications(ctx, "w1", []asana.Notification{{GID: "n2", IsRead: false}}); err != nil {
		t.Fatal(err)
	}
	if ns, _ := st.ListNotifications(ctx, store.NotificationFilter{UnreadOnly: true}); len(ns) != 0 {
		t.Fatalf("unsent read mark lost by replace: %+v", ns)
	}

	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push %+v %v", res, err)
	}
	if len(f.marks) != 1 || f.marks[0]["read"] != true || f.marks[0]["workspace"] != "w1" ||
		fmt.Sprint(f.marks[0]["notification_gids"]) != "[n2]" {
		t.Fatalf("mark requests %+v", f.marks)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox not empty: %+v", ops)
	}
}

func TestMarkNotificationBackAndForthCancels(t *testing.T) {
	f := &inboxServer{}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n2"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n2"}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("toggling back should drop the queued change: %+v", ops)
	}
	// Discarding a queued change restores the state from before it.
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n1"}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	ops, _ := st.ListOps(ctx)
	if err := st.DiscardOp(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if ns, _ := st.ListNotifications(ctx, store.NotificationFilter{UnreadOnly: true}); len(ns) != 1 || ns[0].GID != "n2" {
		t.Fatalf("discard should restore read state: %+v", ns)
	}
}

func TestPushMarkNotificationGoneIsDiscarded(t *testing.T) {
	f := &inboxServer{failed: []string{"n2"}}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n2"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Discarded) != 1 || res.Discarded[0].Kind != store.OpMarkNotification {
		t.Fatalf("push %+v", res)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox not empty: %+v", ops)
	}
}

func TestInboxForbiddenFallsBackWithoutFailingSync(t *testing.T) {
	f := &inboxServer{listStatus: http.StatusForbidden}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatalf("sync should go on without the inbox: %v", err)
	}
	state, _ := st.InboxState(ctx)
	if state.Available() || state.Status != store.InboxForbidden {
		t.Fatalf("state %+v", state)
	}
	msg := InboxUnavailableMessage(state, nil)
	if !strings.Contains(msg, "auth login") || !strings.Contains(msg, "最近の更新") {
		t.Fatalf("message %q", msg)
	}
	if msg := InboxUnavailableMessage(state, []string{"tasks:read"}); !strings.Contains(msg, "auth.scopes") {
		t.Fatalf("explicit scopes message %q", msg)
	}
	// Recovered (e.g. after logging in again): the next sync makes it available.
	f.listStatus = 0
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.InboxState(ctx); !state.Available() {
		t.Fatalf("state after recovery %+v", state)
	}
	if ns, _ := st.ListNotifications(ctx, store.NotificationFilter{}); !slices.ContainsFunc(ns, func(n store.Notification) bool { return n.GID == "n1" }) {
		t.Fatalf("notifications %+v", ns)
	}
}

func TestMarkReadForbiddenDropsWithoutFailures(t *testing.T) {
	f := &inboxServer{markStatus: http.StatusForbidden}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n2"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || !res.InboxReadOnly || res.Failed != 0 {
		t.Fatalf("push %+v %v", res, err)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("a refused read change should not stay as a failure: %+v", ops)
	}
	state, _ := st.InboxState(ctx)
	if !state.Available() || !state.WriteForbidden {
		t.Fatalf("state %+v", state)
	}
	if ns, _ := st.ListNotifications(ctx, store.NotificationFilter{UnreadOnly: true}); len(ns) != 1 {
		t.Fatalf("the dropped change should be undone locally: %+v", ns)
	}
	if !strings.Contains(InboxReadOnlyMessage(nil), "auth login") {
		t.Fatal("read-only message should say what to do")
	}
	// Syncing never sends anything to check: Asana's data is not touched.
	requests := len(f.marks)
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(f.marks) != requests {
		t.Fatalf("sync sent %d mark requests", len(f.marks)-requests)
	}
	// The user marks it again once allowed: it is sent and the state is cleared.
	f.markStatus = 0
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n2"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.InboxReadOnly {
		t.Fatalf("push %+v %v", res, err)
	}
	if state, _ := st.InboxState(ctx); state.WriteForbidden {
		t.Fatal("an accepted change should clear WriteForbidden")
	}
}

func TestMarkAllReadIsOneRequestAndPartialFailure(t *testing.T) {
	f := &inboxServer{extra: `,
		{"gid":"n3","is_read":false,"created_at":"2026-11-10T10:00:00Z","work_objects":[{"gid":"t1","resource_type":"task","name":"My task"}]},
		{"gid":"n4","is_read":false,"created_at":"2026-11-09T10:00:00Z","work_objects":[{"gid":"t1","resource_type":"task","name":"My task"}]}`,
		failed: []string{"n3"}}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if n, err := st.MarkNotificationsLocal(ctx, []string{"n2", "n3", "n4"}, true, time.Now()); err != nil || n != 3 {
		t.Fatalf("mark: %d %v", n, err)
	}
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.marks) != 2 || fmt.Sprint(f.marks[0]["notification_gids"]) != "[n2 n3 n4]" ||
		fmt.Sprint(f.marks[1]["notification_gids"]) != "[n3]" {
		t.Fatalf("requests %+v: want one for all, then the failed one alone", f.marks)
	}
	if len(res.Discarded) != 1 || res.Discarded[0].TaskGID != "n3" {
		t.Fatalf("the failed notification should be dropped: %+v", res)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox %+v", ops)
	}
}

func TestInboxPagingAndUnauthorized(t *testing.T) {
	f := &inboxServer{paged: true}
	s, st := setupInbox(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if ns, _ := st.ListNotifications(ctx, store.NotificationFilter{}); len(ns) != 2 {
		t.Fatalf("both pages should be cached: %+v", ns)
	}
	// 401 is an invalid login, not a missing scope: the sync fails like any other fetch.
	f.paged, f.listStatus = false, http.StatusUnauthorized
	if _, err := s.Run(ctx, false); err == nil || !asana.IsUnauthorized(err) {
		t.Fatalf("401 should fail the sync: %v", err)
	}
	if state, _ := st.InboxState(ctx); !state.Available() {
		t.Fatalf("a 401 should not be taken as a missing scope: %+v", state)
	}
}
