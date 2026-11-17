package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
)

// #21: the PC clock is far ahead of Asana's. A create whose answer was a 5xx was in fact
// created; Asana's created_at is earlier than the PC's send time minus the 2-minute
// margin. The Date header corrects the send time, so it is found and not sent again.
func TestPushCreate5xxWithPCClockFarAhead(t *testing.T) {
	for _, ahead := range []time.Duration{10 * time.Minute, 3 * time.Hour} {
		t.Run(ahead.String(), func(t *testing.T) {
			ws, _, s, st := setupPush(t)
			ws.shift = -ahead // Asana's clock (created_at, Date) is behind this PC's
			ctx := context.Background()
			gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "時計がずれている"}, now)
			ws.setFailAfter("POST /tasks", 1) // created, but answered 500
			res, err := s.Push(ctx)
			if err != nil || res.Sent != 1 || res.Failed != 0 || res.Remaining != 0 {
				t.Fatalf("push = %+v %v\n%v", res, err, ws.calls)
			}
			if n := ws.countCalls("POST /tasks "); n != 1 {
				t.Fatalf("created task sent again (%d times): duplicate", n)
			}
			if real, _ := st.RealGID(ctx, gid); store.IsLocalGID(real) {
				t.Fatal("local task should become the one Asana created")
			}
		})
	}
}

// A create that really failed is still sent again after settleTime when the PC clock
// is behind (the correction must not make it wait forever or look found).
func TestPushCreate5xxNotCreatedWithPCClockBehind(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ws.shift = time.Hour // this PC's clock is an hour behind Asana's
	ctx := context.Background()
	_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "届いていない"}, now)
	failOnce(ws, http.MethodPost, "/tasks", false)
	if res, err := s.Push(ctx); err != nil || res.Sent != 0 || res.Remaining != 1 {
		t.Fatalf("first push = %+v %v", res, err)
	}
	later(s, 2*time.Minute)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || ws.countCalls("POST /tasks ") != 2 {
		t.Fatalf("after settleTime it is sent once more: %+v %v\n%v", res, err, ws.calls)
	}
	assertOutboxEmpty(t, st)
}

// The incremental sync's modified_since is by Asana's clock, not the PC's.
func TestRunModifiedSinceUsesAsanaClock(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: `[]`}
	shift := -time.Hour // Asana's clock is an hour behind this PC's
	h := fake.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().Add(shift).UTC().Format(http.TimeFormat))
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	pc := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}, now: func() time.Time { return pc }}
	if _, err := s.Run(ctx, false); err != nil {
		t.Fatal(err)
	}
	pc = pc.Add(5 * time.Minute)
	if res, err := s.Run(ctx, false); err != nil || res.Full {
		t.Fatalf("second run = %+v %v", res, err)
	}
	got, err := time.Parse(time.RFC3339, fake.modifiedSince[1])
	if err != nil {
		t.Fatalf("modified_since = %q", fake.modifiedSince[1])
	}
	// first run at 12:00 by the PC = 11:00 by Asana; minus the 2-minute margin
	want := time.Date(2026, 11, 13, 10, 58, 0, 0, time.UTC)
	if d := got.Sub(want); d < -3*time.Second || d > 3*time.Second {
		t.Fatalf("modified_since = %v, want about %v", got, want)
	}
}

// Review of #21: before the first sync (no stored account, so Push does not ask /users/me)
// a create's connection drops after Asana created it. No answer has told the clock offset
// yet; reconcile asks before comparing, so the task is found and not created twice.
func TestPushCreateDroppedBeforeFirstSyncPCClockAhead(t *testing.T) {
	ws, _, s, st := setupPush(t)
	ctx := context.Background()
	if err := st.SetSyncState(ctx, store.KeyMeGID, ""); err != nil {
		t.Fatal(err)
	}
	ws.shift = -3 * time.Hour // Asana's clock is 3 hours behind this PC's
	gid, _ := st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "接続が切れた"}, now)
	dropped := false
	ws.setHook(func(rw http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks" && !dropped {
			dropped = true
			ws.tasks["created"] = &serverTask{Name: "接続が切れた", Project: "p1", Section: "s1", Created: ws.serverTime()}
			conn, _, _ := rw.(http.Hijacker).Hijack()
			conn.Close()
			return true
		}
		return false
	})
	_, _ = s.Push(ctx) // the dropped connection ends this push (as offline)
	if s.API.ClockKnown() {
		t.Fatal("test assumes no answer was received before reconcile")
	}
	later(s, 2*time.Minute)
	pushAndSettle(t, s)
	if n := ws.countCalls("POST /tasks "); n != 1 {
		t.Fatalf("task created %d times", n)
	}
	if real, _ := st.RealGID(ctx, gid); real != "created" {
		t.Fatalf("local task became %q, want the one Asana created", real)
	}
}

// Review of #21: last_sync / last_full_sync written by an older version with the PC's clock
// 3 hours ahead are in the future by Asana's clock. modified_since from them would miss
// changes, so the first sync after the update is a full one.
func TestRunStampsInFutureForceFullSync(t *testing.T) {
	for _, key := range []string{store.KeyLastSync, keyLastFullSync} {
		t.Run(key, func(t *testing.T) {
			fake := &fakeAsana{t: t, projectTasks: `[]`}
			srv := httptest.NewServer(fake.handler())
			defer srv.Close()
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			api := asana.NewClient(srv.Client())
			api.BaseURL = srv.URL
			pc := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
			s := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}, now: func() time.Time { return pc }}
			if _, err := s.Run(ctx, false); err != nil {
				t.Fatal(err)
			}
			pc = pc.Add(5 * time.Minute)
			if res, err := s.Run(ctx, false); err != nil || res.Full {
				t.Fatalf("normal second run = %+v %v", res, err)
			}
			_ = st.SetSyncState(ctx, key, pc.Add(5*time.Minute+30*time.Second).Format(time.RFC3339)) // 30s ahead at the next run
			pc = pc.Add(5 * time.Minute)
			if res, err := s.Run(ctx, false); err != nil || res.Full {
				t.Fatalf("a few seconds in the future is noise, not a reason for a full sync: %+v %v", res, err)
			}
			_ = st.SetSyncState(ctx, key, pc.Add(3*time.Hour).Format(time.RFC3339))
			pc = pc.Add(5 * time.Minute)
			if res, err := s.Run(ctx, false); err != nil || !res.Full {
				t.Fatalf("run after a stamp in the future = %+v %v, want full", res, err)
			}
			pc = pc.Add(5 * time.Minute)
			if res, err := s.Run(ctx, false); err != nil || res.Full {
				t.Fatalf("back to normal = %+v %v", res, err)
			}
		})
	}
}
