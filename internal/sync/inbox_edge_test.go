package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// edgeInbox is a stateful fake of the notification endpoints: it pages like Asana (limit /
// offset / next_page), remembers read state, and can be told to fail in several ways.
type edgeInbox struct {
	fakeAsana
	mu         gosync.Mutex
	notes      []edgeNote
	pageCap    int // the server returns at most this many per page (0: no cap)
	listStatus int // != 0: GET notifications answers this status
	listDrop   bool
	listCalls  []string // the limit of each GET notifications
	markStatus int      // != 0: markRead / markUnread answer this status
	markApply  bool     // with markStatus: apply the change anyway (a 5xx after the fact)
	markFails  int      // > 0: the next markFails mark requests answer markStatus
	failed     []string // gids reported in failed_gids
	markCalls  int
	tasks      map[string]string // task gid -> name, served by GET /tasks/{gid}
	taskCalls  []string
	twoWS      bool           // /workspaces lists w1 and w2
	wsStatus   map[string]int // workspace -> status GET notifications answers
}

type edgeNote struct {
	gid, target, targetType string
	read                    bool
	created                 time.Time
}

func (f *edgeInbox) handler() http.Handler {
	if f.wsStatus == nil {
		f.wsStatus = map[string]int{}
	}
	mux := f.fakeAsana.handler().(*http.ServeMux)
	mux.HandleFunc("GET /users/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		q := r.URL.Query()
		f.listCalls = append(f.listCalls, q.Get("limit"))
		if f.listDrop {
			dropConnection(w)
			return
		}
		status := f.listStatus
		if st := f.wsStatus[q.Get("workspace")]; st != 0 {
			status = st
		}
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"errors":[{"message":"status %d"}]}`, status)
			return
		}
		prefix := ""
		if ws := q.Get("workspace"); ws != "w1" {
			prefix = ws + "-"
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 || limit > 100 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"errors":[{"message":"bad limit %q"}]}`, q.Get("limit"))
			return
		}
		if f.pageCap > 0 {
			limit = min(limit, f.pageCap)
		}
		off, _ := strconv.Atoi(q.Get("offset"))
		end := min(off+limit, len(f.notes))
		var items []string
		for _, n := range f.notes[off:end] {
			wo := "[]"
			if n.target != "" {
				typ := n.targetType
				if typ == "" {
					typ = "task"
				}
				wo = fmt.Sprintf(`[{"gid":%q,"resource_type":%q,"name":"name of %s"}]`, n.target, typ, n.target)
			}
			items = append(items, fmt.Sprintf(`{"gid":%q,"is_read":%t,"created_at":%q,"work_objects":%s}`,
				prefix+n.gid, n.read, n.created.UTC().Format(time.RFC3339), wo))
		}
		next := "null"
		if end < len(f.notes) {
			next = fmt.Sprintf(`{"offset":"%d"}`, end)
		}
		fmt.Fprintf(w, `{"data":[%s],"next_page":%s}`, strings.Join(items, ","), next)
	})
	mark := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.markCalls++
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Data struct {
				GIDs []string `json:"notification_gids"`
			}
		}
		_ = json.Unmarshal(b, &body)
		read := strings.HasSuffix(r.URL.Path, "/markRead")
		failing := f.markStatus != 0 && (f.markFails == 0 || f.markCalls <= f.markFails)
		if !failing || f.markApply {
			for i := range f.notes {
				for _, g := range body.Data.GIDs {
					if f.notes[i].gid == g {
						f.notes[i].read = read
					}
				}
			}
		}
		if failing {
			w.WriteHeader(f.markStatus)
			fmt.Fprintf(w, `{"errors":[{"message":"status %d"}]}`, f.markStatus)
			return
		}
		failed, _ := json.Marshal(f.failed)
		fmt.Fprintf(w, `{"data":{"failed_gids":%s}}`, nonNull(failed))
	}
	mux.HandleFunc("POST /users/me/notifications/markRead", mark)
	mux.HandleFunc("POST /users/me/notifications/markUnread", mark)
	mux.HandleFunc("/tasks/", func(w http.ResponseWriter, r *http.Request) {
		gid := strings.TrimPrefix(r.URL.Path, "/tasks/")
		f.mu.Lock()
		f.taskCalls = append(f.taskCalls, gid)
		name, ok := f.tasks[gid]
		f.mu.Unlock()
		if !ok || strings.Contains(gid, "/") {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"Not Found"}]}`)
			return
		}
		fmt.Fprintf(w, `{"data":{"gid":%q,"name":%q,"modified_at":"2026-11-12T00:00:00Z"}}`, gid, name)
	})
	if !f.twoWS {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/workspaces" {
			fmt.Fprint(w, `{"data":[{"gid":"w1","name":"WS"},{"gid":"w2","name":"WS2"}]}`)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (f *edgeInbox) serverRead(gid string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, n := range f.notes {
		if n.gid == gid {
			return n.read
		}
	}
	panic("no notification " + gid)
}

// notesN makes n notifications, newest first, each about its own followed task.
func notesN(n int) ([]edgeNote, map[string]string) {
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	notes := make([]edgeNote, n)
	tasks := map[string]string{}
	for i := range notes {
		gid := fmt.Sprintf("n%03d", i)
		target := fmt.Sprintf("x%03d", i)
		notes[i] = edgeNote{gid: gid, target: target, read: i%2 == 1, created: base.Add(-time.Duration(i) * time.Minute)}
		tasks[target] = "Followed " + target
	}
	return notes, tasks
}

func setupEdge(t *testing.T, f *edgeInbox) (*Syncer, *store.Store, *httptest.Server) {
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
	api.MaxRetries = 0 // 5xx answers come back at once
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	return &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}, now: func() time.Time { return now }}, st, srv
}

func listAll(t *testing.T, st *store.Store) []store.Notification {
	t.Helper()
	ns, err := st.ListNotifications(context.Background(), store.NotificationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func noteByGID(t *testing.T, st *store.Store, gid string) store.Notification {
	t.Helper()
	for _, n := range listAll(t, st) {
		if n.GID == gid {
			return n
		}
	}
	t.Fatalf("notification %s not cached", gid)
	return store.Notification{}
}

// Asana pages notifications: every page is followed, the limit never exceeds 100, and the
// sync stops at MaxNotifications (newest first) without asking for more.
func TestInboxPagingFollowsPagesUpToMax(t *testing.T) {
	notes, tasks := notesN(130)
	f := &edgeInbox{notes: notes, tasks: tasks, pageCap: 40}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	ns := listAll(t, st)
	if len(ns) != MaxNotifications {
		t.Fatalf("cached %d notifications, want %d", len(ns), MaxNotifications)
	}
	if ns[0].GID != "n000" || ns[len(ns)-1].GID != fmt.Sprintf("n%03d", MaxNotifications-1) {
		t.Fatalf("order: first %s last %s", ns[0].GID, ns[len(ns)-1].GID)
	}
	if got := strings.Join(f.listCalls, ","); got != "100,60,20" {
		t.Fatalf("limits asked = %s, want 100,60,20", got)
	}
	if n, _ := st.UnreadNotifications(ctx); n != MaxNotifications/2 {
		t.Fatalf("unread = %d", n)
	}

	// Fewer than the maximum over several pages: every one, and no request past the last page.
	f.notes, f.listCalls = notes[:45], nil
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if ns := listAll(t, st); len(ns) != 45 {
		t.Fatalf("cached %d notifications after the second sync, want 45 (old ones must be replaced)", len(ns))
	}
	if len(f.listCalls) != 2 {
		t.Fatalf("requests %v, want 2 pages", f.listCalls)
	}
}

// An inbox Asana will not give (no scope, plan, bad request) does not fail the sync: the
// tasks are still synced, and the state says why, with a hint in the message.
func TestInboxRefusedKeepsSyncGoing(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
		hint   string
	}{
		{http.StatusForbidden, store.InboxForbidden, "auth login"},
		{http.StatusPaymentRequired, store.InboxPayment, "プラン"},
		{http.StatusBadRequest, store.InboxError, "status 400"},
		{http.StatusNotFound, store.InboxError, "status 404"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			f := &edgeInbox{listStatus: tc.status}
			s, st, _ := setupEdge(t, f)
			ctx := context.Background()
			if _, err := s.Run(ctx, false); err != nil {
				t.Fatalf("sync failed: %v", err)
			}
			if _, err := st.GetTask(ctx, "t1"); err != nil {
				t.Fatalf("tasks not synced: %v", err)
			}
			state, _ := st.InboxState(ctx)
			if state.Available() || state.Status != tc.want {
				t.Fatalf("state %+v, want %s", state, tc.want)
			}
			msg := InboxUnavailableMessage(state, nil)
			if !strings.Contains(msg, tc.hint) || !strings.Contains(msg, "最近の更新") {
				t.Fatalf("message %q lacks %q", msg, tc.hint)
			}
			if len(f.listCalls) != 1 {
				t.Fatalf("notifications asked %d times", len(f.listCalls))
			}
		})
	}
}

// A 401 means the login itself is no longer valid: the sync stops (to ask for a new login)
// and the inbox state is left as it was.
func TestInboxUnauthorizedStopsSync(t *testing.T) {
	notes, tasks := notesN(2)
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.listStatus = http.StatusUnauthorized
	_, err := s.Run(ctx, false)
	if !asana.IsUnauthorized(err) {
		t.Fatalf("sync with a 401 on the inbox: %v", err)
	}
	if state, _ := st.InboxState(ctx); !state.Available() {
		t.Fatalf("state changed by a 401: %+v", state)
	}
	if len(listAll(t, st)) != 2 {
		t.Fatal("cached notifications lost")
	}
}

// After the inbox worked, a refusal (e.g. the login lost the scope) switches to the
// fallback; Asana having trouble (5xx) keeps showing the cached notifications.
func TestInboxTemporaryErrorKeepsCache(t *testing.T) {
	notes, tasks := notesN(3)
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()

	// Asana down on the very first sync: nothing cached, "next sync" message.
	f.listStatus = http.StatusServiceUnavailable
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatalf("5xx on the inbox failed the sync: %v", err)
	}
	if state, _ := st.InboxState(ctx); state.Available() || !strings.Contains(InboxUnavailableMessage(state, nil), "次の同期") {
		t.Fatalf("state %+v: %q", state, InboxUnavailableMessage(state, nil))
	}

	f.listStatus = 0
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if len(listAll(t, st)) != 3 {
		t.Fatal("not cached")
	}

	f.listStatus = http.StatusInternalServerError
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatalf("5xx on the inbox failed the sync: %v", err)
	}
	if state, _ := st.InboxState(ctx); !state.Available() {
		t.Fatalf("a 5xx should keep the inbox available: %+v", state)
	}
	if len(listAll(t, st)) != 3 {
		t.Fatal("a 5xx dropped the cached notifications")
	}

	f.listStatus = http.StatusForbidden
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if state, _ := st.InboxState(ctx); state.Available() {
		t.Fatalf("403 after a working inbox should switch to the fallback: %+v", state)
	}
}

// The connection dropping while the notifications are fetched must not lose the cache.
func TestInboxNetworkErrorKeepsCache(t *testing.T) {
	notes, tasks := notesN(2)
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.listDrop = true
	_, err := s.Run(ctx, false)
	t.Logf("sync with the connection dropped: %v", err)
	if len(listAll(t, st)) != 2 {
		t.Fatal("cached notifications lost")
	}
	if state, _ := st.InboxState(ctx); !state.Available() {
		t.Fatalf("state %+v", state)
	}
}

// Offline, a read mark is kept locally and queued; it is sent once Asana is reachable.
func TestMarkReadOfflineQueuedThenSent(t *testing.T) {
	notes, tasks := notesN(2)
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, srv := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if n, err := st.MarkNotificationsLocal(ctx, []string{"n000"}, true, time.Now()); err != nil || n != 1 {
		t.Fatalf("mark %d %v", n, err)
	}
	s.API.BaseURL = "http://127.0.0.1:1" // nothing listens: offline
	if _, err := s.Push(ctx); err == nil {
		t.Fatal("push offline should report the network error")
	}
	if ops, _ := st.PendingOps(ctx); len(ops) != 1 || ops[0].Attempts > 1 {
		t.Fatalf("queued ops %+v", ops)
	}
	if n := noteByGID(t, st, "n000"); !n.Read || !n.Pending {
		t.Fatalf("local state %+v", n)
	}
	s.API.BaseURL = srv.URL
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 {
		t.Fatalf("push %+v %v", res, err)
	}
	if !f.serverRead("n000") {
		t.Fatal("Asana not updated")
	}
	if n := noteByGID(t, st, "n000"); !n.Read || n.Pending {
		t.Fatalf("after push %+v", n)
	}
}

// markRead answering 403 (no notifications:write): the queued read / unread changes are
// dropped (the local state goes back to Asana's), the sync still runs and never sends a
// mark by itself (Asana's data is not touched), and a mark the user makes once Asana
// accepts marks again (logged in with the scope) goes through and clears the state.
func TestMarkReadForbiddenDropsAndRecovers(t *testing.T) {
	notes, tasks := notesN(4)
	f := &edgeInbox{notes: notes, tasks: tasks, markStatus: http.StatusForbidden}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n000", "n002"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n001"}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("refused marks should be dropped, not left as failures: %+v", ops)
	}
	for gid, want := range map[string]bool{"n000": false, "n001": true, "n002": false} {
		if n := noteByGID(t, st, gid); n.Read != want || n.Pending {
			t.Fatalf("%s should be back to Asana's state (read=%t): %+v", gid, want, n)
		}
	}
	if state, _ := st.InboxState(ctx); !state.WriteForbidden || !state.Available() {
		t.Fatalf("state %+v", state)
	}
	if msg := InboxReadOnlyMessage(nil); !strings.Contains(msg, "notifications:write") {
		t.Fatalf("message %q", msg)
	}
	f.mu.Lock()
	calls := f.markCalls
	f.mu.Unlock()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatalf("a refused mark should not fail the sync: %v", err)
	}
	f.mu.Lock()
	if f.markCalls != calls {
		t.Fatalf("the sync sent %d marks by itself", f.markCalls-calls)
	}
	f.markStatus = 0
	f.mu.Unlock()
	if state, _ := st.InboxState(ctx); !state.WriteForbidden {
		t.Fatalf("state %+v", state)
	}
	for _, n := range notes {
		if f.serverRead(n.gid) != n.read {
			t.Fatalf("%s changed in Asana", n.gid)
		}
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n000"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || !f.serverRead("n000") {
		t.Fatalf("push %+v %v", res, err)
	}
	if state, _ := st.InboxState(ctx); state.WriteForbidden {
		t.Fatalf("Asana accepted the mark, but the state stays read-only: %+v", state)
	}
}

// "Mark all read" over more than one request's worth (50) is sent in few requests, and
// every one arrives.
func TestMarkManyIsBatched(t *testing.T) {
	notes, tasks := notesN(120)
	for i := range notes {
		notes[i].read = false
	}
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	var gids []string
	for _, n := range listAll(t, st) {
		gids = append(gids, n.GID)
	}
	if n, err := st.MarkNotificationsLocal(ctx, gids, true, time.Now()); err != nil || n != MaxNotifications {
		t.Fatalf("mark %d %v", n, err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != MaxNotifications || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("push %+v %v", res, err)
	}
	if f.markCalls != 2 {
		t.Fatalf("%d mark requests for %d notifications, want 2", f.markCalls, MaxNotifications)
	}
	for _, g := range gids {
		if !f.serverRead(g) {
			t.Fatalf("%s not marked in Asana", g)
		}
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox not empty: %d", len(ops))
	}
}

// failed_gids naming other notifications does not fail ours.
func TestMarkFailedGIDsOfOthersIgnored(t *testing.T) {
	notes, tasks := notesN(2)
	f := &edgeInbox{notes: notes, tasks: tasks, failed: []string{"n999"}}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n000"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || len(res.Discarded) != 0 {
		t.Fatalf("push %+v %v", res, err)
	}
}

// A 5xx to markRead may have been applied. If the user then marks the notification unread
// again, Asana must end up unread: the queued change cannot just be dropped.
func TestToggleBackAfterUncertainSend(t *testing.T) {
	notes, tasks := notesN(2)
	f := &edgeInbox{notes: notes, tasks: tasks, markStatus: http.StatusInternalServerError, markApply: true, markFails: 2}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n000"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Push(ctx)
	if !f.serverRead("n000") {
		t.Fatal("setup: the fake should have applied the mark")
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n000"}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if f.serverRead("n000") {
		t.Fatal("the user marked it unread again, but Asana still has it read")
	}
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if n := noteByGID(t, st, "n000"); n.Read {
		t.Fatalf("after sync the notification came back read: %+v", n)
	}
}

// Tasks only reachable through a notification (followed, not in a synced project nor
// assigned) survive full syncs, even when the inbox fetch has a temporary error.
func TestFullSyncKeepsNotificationTargets(t *testing.T) {
	notes, tasks := notesN(3)
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := s.Run(ctx, true); err != nil {
			t.Fatal(err)
		}
		for _, n := range listAll(t, st) {
			if !n.TaskCached {
				t.Fatalf("full sync %d pruned %s's task %s", i, n.GID, n.TargetGID)
			}
		}
	}
	f.listStatus = http.StatusServiceUnavailable
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, n := range listAll(t, st) {
		if !n.TaskCached {
			t.Fatalf("full sync with the inbox down pruned %s's task %s", n.GID, n.TargetGID)
		}
	}
}

// More followed tasks than one sync fetches on their own: a full sync must still not
// prune the cached ones it did not get to.
func TestFullSyncKeepsNotificationTargetsBeyondFetchLimit(t *testing.T) {
	notes, tasks := notesN(maxTargetFetches + 10)
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	for i := 0; i < 3; i++ { // incremental syncs fetch the missing ones, 50 at a time
		if _, err := s.Run(ctx, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range listAll(t, st) {
		if !n.TaskCached {
			t.Fatalf("setup: %s not cached", n.TargetGID)
		}
	}
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	var pruned []string
	for _, n := range listAll(t, st) {
		if !n.TaskCached {
			pruned = append(pruned, n.TargetGID)
		}
	}
	if len(pruned) > 0 {
		t.Fatalf("full sync pruned %d notification tasks: %v", len(pruned), pruned)
	}
}

// A notification about something other than a task (a project, or nothing) is listed,
// but no task is fetched for it.
func TestNonTaskNotificationNotFetchedAsTask(t *testing.T) {
	f := &edgeInbox{
		notes: []edgeNote{
			{gid: "np", target: "p9", targetType: "project", created: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)},
			{gid: "nn", created: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)},
		},
		tasks: map[string]string{},
	}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	ns := listAll(t, st)
	if len(ns) != 2 || ns[0].IsTask() || ns[1].IsTask() || ns[0].TargetType != "project" {
		t.Fatalf("notifications %+v", ns)
	}
	for _, g := range f.taskCalls {
		if g == "p9" || g == "" {
			t.Fatalf("fetched %q as a task", g)
		}
	}
}

// With several workspaces, the inbox is usable when one of them answers: a workspace on a
// plan without the API (402) or refusing (403) does not hide the others' notifications,
// nor does it fail the sync.
func TestInboxPerWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    map[string]int
		available bool
		want      int // cached notifications
		state     string
	}{
		{"w1 refused", map[string]int{"w1": http.StatusForbidden}, true, 2, store.InboxOK},
		{"w2 plan", map[string]int{"w2": http.StatusPaymentRequired}, true, 2, store.InboxOK},
		{"both refused", map[string]int{"w1": http.StatusPaymentRequired, "w2": http.StatusForbidden}, false, 0, store.InboxPayment},
		{"w1 down", map[string]int{"w1": http.StatusServiceUnavailable}, true, 2, store.InboxOK},
		{"both fine", map[string]int{}, true, 4, store.InboxOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notes, tasks := notesN(2)
			f := &edgeInbox{notes: notes, tasks: tasks, twoWS: true, wsStatus: tc.status}
			s, st, _ := setupEdge(t, f)
			ctx := context.Background()
			if _, err := s.Run(ctx, false); err != nil {
				t.Fatalf("sync: %v", err)
			}
			state, _ := st.InboxState(ctx)
			if state.Available() != tc.available || state.Status != tc.state {
				t.Fatalf("state %+v", state)
			}
			if ns := listAll(t, st); len(ns) != tc.want {
				t.Fatalf("cached %d notifications, want %d", len(ns), tc.want)
			}
		})
	}
}

// In a batch, a notification Asana reports as failed is dropped on its own; the others in
// the batch are marked.
func TestMarkBatchWithFailedGID(t *testing.T) {
	notes, tasks := notesN(4)
	for i := range notes {
		notes[i].read = false
	}
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkNotificationsLocal(ctx, []string{"n000", "n001", "n002"}, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.failed = []string{"n001"} // e.g. archived in Asana meanwhile
	f.mu.Unlock()
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || len(res.Discarded) != 1 || res.Discarded[0].TaskGID != "n001" {
		t.Fatalf("push %+v %v", res, err)
	}
	if !f.serverRead("n000") || !f.serverRead("n002") {
		t.Fatal("the other notifications of the batch were not marked")
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("outbox %+v", ops)
	}
}

// An incremental sync fetches again the tasks that got a notification since the previous
// sync (they changed), but not the others already cached.
func TestIncrementalSyncRefetchesTasksWithNewNotifications(t *testing.T) {
	notes, tasks := notesN(3)
	for i := range notes {
		notes[i].created = time.Date(2026, 11, 1, 0, 0, i, 0, time.UTC) // well before the syncs
	}
	f := &edgeInbox{notes: notes, tasks: tasks}
	s, st, _ := setupEdge(t, f)
	ctx := context.Background()
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.taskCalls = nil
	f.notes = append([]edgeNote{{gid: "n100", target: "x001", created: s.clock().Add(time.Minute)}}, f.notes...)
	f.tasks["x001"] = "Renamed"
	f.mu.Unlock()
	s.now = func() time.Time { return time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC) }
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.taskCalls) != "[x001]" {
		t.Fatalf("tasks fetched %v, want only x001", f.taskCalls)
	}
	if task, err := st.GetTask(ctx, "x001"); err != nil || task.Name != "Renamed" {
		t.Fatalf("x001 %+v %v", task, err)
	}
}
