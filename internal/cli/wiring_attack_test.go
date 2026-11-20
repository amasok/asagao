package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amasok/asagao/internal/wire"
	"path/filepath"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/store"
)

func TestNewRootZeroWiringHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{
		{"--version"},
		{"--help"},
		{"task", "--help"},
		{"searches", "--help"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := NewRoot(Wiring{})
			var out, errBuf bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errBuf)
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("NewRoot(Wiring{}) %v failed: %v", args, err)
			}
			if out.Len() == 0 {
				t.Fatalf("NewRoot(Wiring{}) %v produced empty output", args)
			}
		})
	}
}

func TestNewRootPersistentPreRunErrors(t *testing.T) {
	t.Run("migrate_warning_sanitized_load_settings_succeeds", func(t *testing.T) {
		w := Wiring{
			MigrateLegacyDirs: func() error {
				return errors.New("bad\x1b[31m-dir\x1b[0m\x07")
			},
			LoadSettings: func() (settings.Config, error) {
				return settings.Config{}, nil
			},
			Searches: stubSearchFile{items: []readmodel.SavedSearch{{Name: "safe-search"}}},
		}
		cmd := NewRoot(w)
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetArgs([]string{"searches", "list"})
		if err := cmd.ExecuteContext(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.ContainsAny(errBuf.String(), "\x1b\x07") {
			t.Fatalf("stderr leaked control chars: %q", errBuf.String())
		}
		if !strings.Contains(errBuf.String(), "注意: bad[31m-dir[0m") {
			t.Fatalf("stderr = %q, want sanitized migrate warning", errBuf.String())
		}
		if !strings.Contains(out.String(), "safe-search") {
			t.Fatalf("stdout = %q, want command output", out.String())
		}
	})

	t.Run("load_settings_error_stops_before_run", func(t *testing.T) {
		var openCalled atomic.Bool
		wantErr := errors.New("broken config.toml")
		w := Wiring{
			MigrateLegacyDirs: func() error { return errors.New("warn") },
			LoadSettings:      func() (settings.Config, error) { return settings.Config{}, wantErr },
			OpenStore: func(context.Context, string) (*Local, error) {
				openCalled.Store(true)
				return nil, errors.New("must not be called")
			},
		}
		cmd := NewRoot(w)
		var out, errBuf bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errBuf)
		cmd.SetArgs([]string{"task", "list"})
		err := cmd.ExecuteContext(context.Background())
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if openCalled.Load() {
			t.Fatal("OpenStore was called despite PersistentPreRunE LoadSettings error")
		}
	})
}

func TestNewRootDryRunAndNormalConcurrentIsolation(t *testing.T) {
	ctx := context.Background()
	dryDB := filepath.Join(t.TempDir(), "dry.db")
	realDB := filepath.Join(t.TempDir(), "real.db")
	for _, tc := range []struct {
		path string
		gid  string
		name string
	}{
		{dryDB, "t-dry", "ドライランタスク"},
		{realDB, "t-real", "本番タスク"},
	} {
		st, err := store.Open(ctx, tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertTasks(ctx, []asana.Task{{GID: tc.gid, Name: tc.name}}); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}

	makeWiring := func(dbPath string) Wiring {
		return Wiring{
			MigrateLegacyDirs: func() error { return nil },
			LoadSettings:      func() (settings.Config, error) { return settings.Config{}, nil },
			OpenStore: func(ctx context.Context, path string) (*Local, error) {
				if !strings.Contains(path, dryRunPrefix) {
					path = dbPath
				}
				return StoreOpener(store.Open, wire.UseCases, wire.Syncer)(ctx, path)
			},
		}
	}

	wDry := makeWiring(dryDB)
	wReal := makeWiring(realDB)

	const rounds = 6
	for range rounds {
		start := make(chan struct{})
		var dryOut, normalOut string
		var dryErr, normalErr error
		var wg gosync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			dryOut, dryErr = runWithWiring(wDry, "", "task", "complete", "t-dry", "--dry-run", "--json")
		}()
		go func() {
			defer wg.Done()
			<-start
			normalOut, normalErr = runWithWiring(wReal, "", "task", "list", "--all", "--assignee=")
		}()
		close(start)
		wg.Wait()

		if dryErr != nil {
			t.Fatalf("dry-run failed: %v", dryErr)
		}
		if normalErr != nil {
			t.Fatalf("normal task list failed: %v", normalErr)
		}
		var res dryRunResult
		if err := json.Unmarshal([]byte(dryOut), &res); err != nil {
			t.Fatalf("dry-run json invalid: %v (%q)", err, dryOut)
		}
		if !res.DryRun || len(res.Planned) != 1 {
			t.Fatalf("unexpected dry-run result: %+v", res)
		}
		if strings.Contains(normalOut, "dry_run") || strings.Contains(normalOut, "ドライランタスク") {
			t.Fatalf("normal output contaminated by dry-run: %q", normalOut)
		}
	}

	// Verify dry-run never queued anything in dryDB.
	st, err := store.Open(ctx, dryDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 {
		t.Fatalf("dryDB outbox has %d ops, want 0", len(ops))
	}
}

func TestNewRootJSONAndTermSafeConcurrentIsolation(t *testing.T) {
	ctx := context.Background()
	dbJSON := filepath.Join(t.TempDir(), "json.db")
	dbPlain := filepath.Join(t.TempDir(), "plain.db")
	for _, tc := range []struct {
		path, gid, name string
	}{
		{dbJSON, "t-json", "JSON専用タスク"},
		{dbPlain, "t-plain", "PLAIN専用タスク"},
	} {
		st, err := store.Open(ctx, tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertTasks(ctx, []asana.Task{{GID: tc.gid, Name: tc.name}}); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}

	makeWiring := func(dbPath string) Wiring {
		return Wiring{
			MigrateLegacyDirs: func() error { return nil },
			LoadSettings:      func() (settings.Config, error) { return settings.Config{}, nil },
			OpenStore: func(ctx context.Context, _ string) (*Local, error) {
				return StoreOpener(store.Open, wire.UseCases, wire.Syncer)(ctx, dbPath)
			},
		}
	}
	wJSON := makeWiring(dbJSON)
	wPlain := makeWiring(dbPlain)

	const workers = 6
	for range 5 {
		start := make(chan struct{})
		errs := make([]error, workers)
		var wg gosync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				if idx%2 == 0 {
					out, err := runWithWiring(wJSON, "", "task", "list", "--all", "--assignee=", "--json")
					if err != nil {
						errs[idx] = err
						return
					}
					var items []struct {
						GID  string `json:"gid"`
						Name string `json:"name"`
					}
					if err := json.Unmarshal([]byte(out), &items); err != nil {
						errs[idx] = fmt.Errorf("json unmarshal: %w (%q)", err, out)
						return
					}
					if len(items) != 1 || items[0].Name != "JSON専用タスク" {
						errs[idx] = fmt.Errorf("json items = %+v, want JSON専用タスク", items)
					}
					return
				}
				out, err := runWithWiring(wPlain, "", "task", "list", "--all", "--assignee=")
				if err != nil {
					errs[idx] = err
					return
				}
				if strings.Contains(out, `"gid"`) || strings.Contains(out, "JSON専用タスク") || !strings.Contains(out, "PLAIN専用タスク") {
					errs[idx] = fmt.Errorf("plain output contaminated: %q", out)
				}
			}(i)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("worker %d: %v", i, err)
			}
		}
	}
}
