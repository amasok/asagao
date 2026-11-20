package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/wire"
)

type stubSearchFile struct {
	items []readmodel.SavedSearch
}

func (s stubSearchFile) LoadSearches() ([]readmodel.SavedSearch, error) {
	return append([]readmodel.SavedSearch(nil), s.items...), nil
}

func (s stubSearchFile) UpdateSearches(func([]readmodel.SavedSearch) ([]readmodel.SavedSearch, error)) error {
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type isolatedHarness struct {
	id            string
	completedDays int
	taskGID       string
	taskName      string
	searchName    string
	patErr        string
	apiErr        string
	dbPath        string

	migrateCalls      atomic.Int32
	loadSettingsCalls atomic.Int32
	openStoreCalls    atomic.Int32
	apiClientCalls    atomic.Int32
	syncerCalls       atomic.Int32
	checkPATCalls     atomic.Int32

	mismatchMu gosync.Mutex
	mismatches []string
}

func newIsolatedHarness(t *testing.T, id string, completedDays int) *isolatedHarness {
	t.Helper()
	h := &isolatedHarness{
		id:            id,
		completedDays: completedDays,
		taskGID:       "task-" + id,
		taskName:      "タスク-" + id,
		searchName:    "search-" + id,
		patErr:        "pat-err-" + id,
		apiErr:        "api-err-" + id,
		dbPath:        filepath.Join(t.TempDir(), "store.db"),
	}
	st, err := store.Open(context.Background(), h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: h.taskGID, Name: h.taskName}}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *isolatedHarness) recordMismatch(msg string) {
	h.mismatchMu.Lock()
	defer h.mismatchMu.Unlock()
	h.mismatches = append(h.mismatches, msg)
}

func (h *isolatedHarness) wiring() Wiring {
	return Wiring{
		MigrateLegacyDirs: func() error {
			h.migrateCalls.Add(1)
			return nil
		},
		LoadSettings: func() (settings.Config, error) {
			h.loadSettingsCalls.Add(1)
			return settings.Config{
				Auth: settings.AuthConfig{TokenStore: "file"},
				Sync: settings.SyncConfig{CompletedDays: h.completedDays},
			}, nil
		},
		APIClient: func(_ context.Context, cfg settings.Config, _ string) (sync.AsanaAPI, error) {
			h.apiClientCalls.Add(1)
			if cfg.Sync.CompletedDays != h.completedDays {
				h.recordMismatch(fmt.Sprintf("APIClient got CompletedDays=%d, want %d", cfg.Sync.CompletedDays, h.completedDays))
			}
			client := &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					body := fmt.Sprintf(`{"errors":[{"message":%q}]}`, h.apiErr)
					return &http.Response{
						StatusCode: http.StatusBadRequest,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(body)),
						Request:    r,
					}, nil
				}),
			}
			c := asana.NewClient(client)
			c.BaseURL = "http://localhost/api/1.0"
			return c, nil
		},
		Syncer: func(api sync.AsanaAPI, st sync.LocalStore, cfg settings.Config) *sync.Syncer {
			h.syncerCalls.Add(1)
			if cfg.Sync.CompletedDays != h.completedDays {
				h.recordMismatch(fmt.Sprintf("Syncer got CompletedDays=%d, want %d", cfg.Sync.CompletedDays, h.completedDays))
			}
			return wire.Syncer(api, st, cfg)
		},
		OpenStore: func(ctx context.Context, path string) (Store, error) {
			h.openStoreCalls.Add(1)
			// Redirect store.db to this harness's isolated DB, while letting --dry-run copy paths pass through.
			if !strings.Contains(path, dryRunPrefix) {
				path = h.dbPath
			}
			return StoreOpener(store.Open)(ctx, path)
		},
		CheckPAT: func(_ context.Context, token, _ string, _ time.Duration, _ func(int, time.Duration)) (*model.User, error) {
			h.checkPATCalls.Add(1)
			if token != "tok-"+h.id {
				h.recordMismatch(fmt.Sprintf("CheckPAT got token=%q, want %q", token, "tok-"+h.id))
			}
			return nil, errors.New(h.patErr)
		},
		Searches: stubSearchFile{items: []readmodel.SavedSearch{{Name: h.searchName}}},
	}
}

func runWithWiring(w Wiring, stdin string, args ...string) (string, error) {
	cmd := NewRoot(w)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if stdin != "" {
		cmd.SetIn(strings.NewReader(stdin))
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

// Two NewRoot(w) instances running concurrently with distinct Wiring callbacks must not
// race or leak callbacks/settings/stores across instances.
func TestNewRootWiringParallelIsolation(t *testing.T) {
	const rounds = 6
	h1 := newIsolatedHarness(t, "one", 11)
	h2 := newIsolatedHarness(t, "two", 22)
	w1 := h1.wiring()
	w2 := h2.wiring()

	runPair := func(name string, fn1, fn2 func() error) {
		t.Helper()
		for range rounds {
			start := make(chan struct{})
			var err1, err2 error
			var wg gosync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				err1 = fn1()
			}()
			go func() {
				defer wg.Done()
				<-start
				err2 = fn2()
			}()
			close(start)
			wg.Wait()
			if err1 != nil {
				t.Fatalf("%s (h1): %v", name, err1)
			}
			if err2 != nil {
				t.Fatalf("%s (h2): %v", name, err2)
			}
		}
	}

	runPair("searches list",
		func() error {
			out, err := runWithWiring(w1, "", "searches", "list")
			if err != nil {
				return err
			}
			if !strings.Contains(out, h1.searchName) || strings.Contains(out, h2.searchName) {
				return fmt.Errorf("unexpected output: %q", out)
			}
			return nil
		},
		func() error {
			out, err := runWithWiring(w2, "", "searches", "list")
			if err != nil {
				return err
			}
			if !strings.Contains(out, h2.searchName) || strings.Contains(out, h1.searchName) {
				return fmt.Errorf("unexpected output: %q", out)
			}
			return nil
		},
	)

	runPair("sync",
		func() error {
			_, err := runWithWiring(w1, "", "sync")
			if err == nil || !strings.Contains(err.Error(), h1.apiErr) || strings.Contains(err.Error(), h2.apiErr) {
				return fmt.Errorf("err = %v, want %q", err, h1.apiErr)
			}
			return nil
		},
		func() error {
			_, err := runWithWiring(w2, "", "sync")
			if err == nil || !strings.Contains(err.Error(), h2.apiErr) || strings.Contains(err.Error(), h1.apiErr) {
				return fmt.Errorf("err = %v, want %q", err, h2.apiErr)
			}
			return nil
		},
	)

	runPair("auth login --pat",
		func() error {
			_, err := runWithWiring(w1, "tok-"+h1.id+"\n", "auth", "login", "--pat")
			if err == nil || !strings.Contains(err.Error(), h1.patErr) || strings.Contains(err.Error(), h2.patErr) {
				return fmt.Errorf("err = %v, want %q", err, h1.patErr)
			}
			return nil
		},
		func() error {
			_, err := runWithWiring(w2, "tok-"+h2.id+"\n", "auth", "login", "--pat")
			if err == nil || !strings.Contains(err.Error(), h2.patErr) || strings.Contains(err.Error(), h1.patErr) {
				return fmt.Errorf("err = %v, want %q", err, h2.patErr)
			}
			return nil
		},
	)

	runPair("dry-run OpenStore",
		func() error {
			out, err := runWithWiring(w1, "", "task", "complete", h1.taskGID, "--dry-run")
			if err != nil {
				return err
			}
			if !strings.Contains(out, h1.taskName) || strings.Contains(out, h2.taskName) {
				return fmt.Errorf("unexpected dry-run output: %q", out)
			}
			return nil
		},
		func() error {
			out, err := runWithWiring(w2, "", "task", "complete", h2.taskGID, "--dry-run")
			if err != nil {
				return err
			}
			if !strings.Contains(out, h2.taskName) || strings.Contains(out, h1.taskName) {
				return fmt.Errorf("unexpected dry-run output: %q", out)
			}
			return nil
		},
	)

	for _, h := range []*isolatedHarness{h1, h2} {
		if len(h.mismatches) > 0 {
			t.Errorf("harness %s mismatches: %v", h.id, h.mismatches)
		}
		if got := h.migrateCalls.Load(); got != 4*rounds {
			t.Errorf("harness %s MigrateLegacyDirs calls = %d, want %d", h.id, got, 4*rounds)
		}
		if got := h.loadSettingsCalls.Load(); got != 4*rounds {
			t.Errorf("harness %s LoadSettings calls = %d, want %d", h.id, got, 4*rounds)
		}
		if got := h.apiClientCalls.Load(); got != rounds {
			t.Errorf("harness %s APIClient calls = %d, want %d", h.id, got, rounds)
		}
		if got := h.syncerCalls.Load(); got != rounds {
			t.Errorf("harness %s Syncer calls = %d, want %d", h.id, got, rounds)
		}
		if got := h.checkPATCalls.Load(); got != rounds {
			t.Errorf("harness %s CheckPAT calls = %d, want %d", h.id, got, rounds)
		}
		// 1 openStore in sync + 2 openStore in dry-run (original + copy) per round.
		if got := h.openStoreCalls.Load(); got != 3*rounds {
			t.Errorf("harness %s OpenStore calls = %d, want %d", h.id, got, 3*rounds)
		}
	}
}
