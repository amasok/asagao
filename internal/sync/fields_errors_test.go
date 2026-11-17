package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// hooked wraps a fake server; hook may answer a request itself (returning true).
type hooked struct {
	mu   stdsync.Mutex
	next http.Handler
	hook func(rw http.ResponseWriter, r *http.Request) bool
}

func (h *hooked) set(f func(rw http.ResponseWriter, r *http.Request) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hook = f
}

func (h *hooked) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	f := h.hook
	h.mu.Unlock()
	if f != nil && f(rw, r) {
		return
	}
	h.next.ServeHTTP(rw, r)
}

// hookAPI points s at a server that forwards to next unless the hook answers.
func hookAPI(t *testing.T, s *Syncer, next http.Handler) *hooked {
	h := &hooked{next: next}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s.API.BaseURL = srv.URL
	return h
}

func answer(status int) func(rw http.ResponseWriter, r *http.Request) {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(status)
		fmt.Fprintf(rw, `{"errors":[{"message":"status %d"}]}`, status)
	}
}

// Setting a custom field follows the outbox rules for each kind of failure.
func TestPushCustomFieldErrors(t *testing.T) {
	ctx := context.Background()
	isPut := func(r *http.Request) bool { return r.Method == http.MethodPut }

	t.Run("5xx after it was applied: done, not a conflict, not failed", func(t *testing.T) {
		fs, s, st := setupFieldPush(t)
		h := hookAPI(t, s, fs)
		_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now)
		h.set(func(rw http.ResponseWriter, r *http.Request) bool {
			if !isPut(r) {
				return false
			}
			fs.ServeHTTP(httptest.NewRecorder(), r) // applied, but the answer is lost
			answer(http.StatusBadGateway)(rw, r)
			return true
		})
		res, err := s.Push(ctx)
		h.set(nil)
		if err == nil { // the first push may stop on the 5xx; the next one settles it
			res, err = s.Push(ctx)
		}
		if err != nil || res.Failed != 0 || res.Conflicts != 0 || fs.value != "o2" {
			t.Fatalf("push = %+v %v, server value %q", res, err, fs.value)
		}
		if p, c, f := pendingCount(t, st); p+c+f != 0 {
			t.Fatalf("outbox = %d %d %d", p, c, f)
		}
	})

	t.Run("5xx not applied: kept, sent on the next push", func(t *testing.T) {
		fs, s, st := setupFieldPush(t)
		h := hookAPI(t, s, fs)
		_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now)
		h.set(func(rw http.ResponseWriter, r *http.Request) bool {
			if isPut(r) {
				answer(http.StatusServiceUnavailable)(rw, r)
				return true
			}
			return false
		})
		if res, err := s.Push(ctx); res.Failed != 0 || res.Conflicts != 0 || fs.value != "o1" {
			t.Fatalf("push = %+v %v", res, err)
		}
		h.set(nil)
		if res, err := s.Push(ctx); err != nil || res.Failed != 0 || fs.value != "o2" {
			t.Fatalf("second push = %+v %v, value %q", res, err, fs.value)
		}
	})

	t.Run("429: kept", func(t *testing.T) {
		fs, s, st := setupFieldPush(t)
		h := hookAPI(t, s, fs)
		_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now)
		h.set(func(rw http.ResponseWriter, r *http.Request) bool {
			if isPut(r) {
				answer(http.StatusTooManyRequests)(rw, r)
				return true
			}
			return false
		})
		res, err := s.Push(ctx)
		if err == nil || res.Failed != 0 || res.Remaining != 1 {
			t.Fatalf("push = %+v %v", res, err)
		}
	})

	t.Run("400 (invalid value): failed with Asana's message, not retried", func(t *testing.T) {
		fs, s, st := setupFieldPush(t)
		h := hookAPI(t, s, fs)
		_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now)
		puts := 0
		h.set(func(rw http.ResponseWriter, r *http.Request) bool {
			if isPut(r) {
				puts++
				answer(http.StatusBadRequest)(rw, r)
				return true
			}
			return false
		})
		res, err := s.Push(ctx)
		if err != nil || res.Failed != 1 {
			t.Fatalf("push = %+v %v", res, err)
		}
		_, _ = s.Push(ctx)
		op := opByKind(t, st, store.OpSetCustomField)
		if puts != 1 || op == nil || op.State != store.OpFailed || !strings.Contains(op.LastError, "400") {
			t.Fatalf("puts %d, entry %+v", puts, op)
		}
	})
}

// Field definitions that cannot be fetched never stop the sync: no plan (402/403) is
// remembered as "no fields"; a 5xx is tried again on the next sync.
func TestCustomFieldSettingsFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		status   int
		recorded bool
	}{
		{http.StatusPaymentRequired, true},
		{http.StatusForbidden, true},
		{http.StatusInternalServerError, false},
		{http.StatusServiceUnavailable, false},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			fs, s, st := setupFieldPush(t)
			_ = st.SetSyncState(ctx, store.KeyCustomFields+"p1", "")
			fs.settings = tc.status
			if err := s.syncCustomFields(ctx, "p1", false); err != nil {
				t.Fatalf("a %d must not stop the sync: %v", tc.status, err)
			}
			v, _ := st.SyncState(ctx, store.KeyCustomFields+"p1")
			if (v != "") != tc.recorded {
				t.Fatalf("recorded = %q, want recorded %v", v, tc.recorded)
			}
			if !tc.recorded {
				fs.settings = 0
				if err := s.syncCustomFields(ctx, "p1", false); err != nil {
					t.Fatal(err)
				}
				if v, _ := st.SyncState(ctx, store.KeyCustomFields+"p1"); v == "" {
					t.Fatal("the next sync should fetch the definitions again")
				}
			}
		})
	}
}

// Dependencies: no plan (402) and a refused link (400, e.g. a cycle or over 30) fail
// that entry with Asana's reason and do not hold back the other changes; a 5xx keeps it
// and it is sent once later.
func TestPushDependencyErrors(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{http.StatusPaymentRequired, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ds, s, st := setupDeps(t)
			h := hookAPI(t, s, ds)
			_ = st.SetDependencyLocal(ctx, "t1", "t2", true, now)
			_ = st.UpdateTaskLocal(ctx, "t2", map[string]any{"name": "B2"}, now) // unrelated change after it
			h.set(func(rw http.ResponseWriter, r *http.Request) bool {
				if strings.HasSuffix(r.URL.Path, "/addDependencies") {
					answer(status)(rw, r)
					return true
				}
				return false
			})
			res, err := s.Push(ctx)
			if err != nil || res.Failed != 1 || res.Sent+res.Conflicts != 1 { // the fake answers name "x": a conflict, but it was handled
				t.Fatalf("push = %+v %v (the unrelated change should still be sent)", res, err)
			}
			ops, _ := st.ListOps(ctx)
			failed := 0
			for _, op := range ops {
				if op.State == store.OpFailed {
					failed++
					if !strings.Contains(op.LastError, fmt.Sprint(status)) {
						t.Fatalf("the failure should keep Asana's reason: %+v", op)
					}
				}
			}
			if failed != 1 {
				t.Fatalf("only the refused link should fail: %+v", ops)
			}
		})
	}

	t.Run("5xx", func(t *testing.T) {
		ds, s, st := setupDeps(t)
		h := hookAPI(t, s, ds)
		_ = st.SetDependencyLocal(ctx, "t1", "t2", true, now)
		h.set(func(rw http.ResponseWriter, r *http.Request) bool {
			if r.Method == http.MethodPost {
				answer(http.StatusBadGateway)(rw, r)
				return true
			}
			return false
		})
		if res, _ := s.Push(ctx); res.Failed != 0 || res.Remaining != 1 {
			t.Fatalf("push = %+v", res)
		}
		h.set(nil)
		if res, err := s.Push(ctx); err != nil || res.Failed != 0 || res.Remaining != 0 {
			t.Fatalf("second push = %+v %v", res, err)
		}
		if len(ds.calls) != 1 || !strings.Contains(ds.calls[0], "addDependencies") {
			t.Fatalf("the link should be added once: %v", ds.calls)
		}
	})
}
