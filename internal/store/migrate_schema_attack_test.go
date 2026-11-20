package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

func TestOpenNewerSchemaConcurrentWriteLockAndRace(t *testing.T) {
	ctx := context.Background()
	future := len(migrations) + 1

	t.Run("external_bump_under_write_lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t-keep", Name: "既存タスク"}}); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if err := st.SetSyncState(ctx, "marker", "untouched"); err != nil {
			st.Close()
			t.Fatal(err)
		}
		rollbackTo(t, st.db, len(migrations)-1)
		beforeSchema := readSchemaSQL(t, st.db)
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}

		holder := otherProcess(t, path, 0)
		conn, err := holder.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}

		const workers = 8
		errs := make([]error, workers)
		stores := make([]*Store, workers)
		var wg sync.WaitGroup
		ready := make(chan struct{})
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-ready
				stores[idx], errs[idx] = Open(ctx, path)
			}(i)
		}

		close(ready)
		// Give goroutines time to read version < len(migrations) and block on BeginTx.
		time.Sleep(80 * time.Millisecond)

		if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", future)); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			t.Fatal(err)
		}
		wg.Wait()

		for i := range workers {
			if stores[i] != nil {
				stores[i].Close()
				t.Errorf("worker %d: Open succeeded despite concurrent bump to %d", i, future)
			}
			if !errors.Is(errs[i], ErrNewerSchema) {
				t.Errorf("worker %d: err = %v, want ErrNewerSchema", i, errs[i])
			}
		}

		raw := openRawSQLite(t, path)
		var ver int
		if err := raw.QueryRowContext(ctx, "PRAGMA user_version").Scan(&ver); err != nil {
			t.Fatal(err)
		}
		if ver != future {
			t.Fatalf("PRAGMA user_version = %d, want %d", ver, future)
		}
		if afterSchema := readSchemaSQL(t, raw); afterSchema != beforeSchema {
			t.Fatalf("schema changed despite concurrent bump to %d:\nbefore:\n%s\nafter:\n%s", future, beforeSchema, afterSchema)
		}
		var taskName, marker string
		if err := raw.QueryRowContext(ctx, "SELECT name FROM tasks WHERE gid = 't-keep'").Scan(&taskName); err != nil || taskName != "既存タスク" {
			t.Fatalf("task = %q, %v", taskName, err)
		}
		if err := raw.QueryRowContext(ctx, "SELECT value FROM sync_state WHERE key = 'marker'").Scan(&marker); err != nil || marker != "untouched" {
			t.Fatalf("marker = %q, %v", marker, err)
		}
	})

	t.Run("already_newer_under_write_lock", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "store.db")
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t-lock", Name: "保護対象"}}); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if _, err := st.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", future)); err != nil {
			st.Close()
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}

		holder := otherProcess(t, path, 0)
		conn, err := holder.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = conn.ExecContext(ctx, "ROLLBACK") }()

		const workers = 8
		errs := make([]error, workers)
		stores := make([]*Store, workers)
		var wg sync.WaitGroup
		start := time.Now()
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				stores[idx], errs[idx] = Open(ctx, path)
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)

		if elapsed >= 2*time.Second {
			t.Fatalf("concurrent locked Open took %v, want fast failure without busyTimeout", elapsed)
		}
		for i := range workers {
			if stores[i] != nil {
				stores[i].Close()
				t.Errorf("worker %d: Open succeeded on newer schema under write lock", i)
			}
			if !errors.Is(errs[i], ErrNewerSchema) {
				t.Errorf("worker %d: err = %v, want ErrNewerSchema", i, errs[i])
			}
		}
	})
}

func TestOpenNewerSchemaNoLeakOrStaleLock(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t-reopen", Name: "復旧対象"}}); err != nil {
		st.Close()
		t.Fatal(err)
	}
	future := len(migrations) + 5
	if _, err := st.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", future)); err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Repeated failed Opens must stay deterministic and leave no lingering locks/handles.
	for i := range 30 {
		got, err := Open(ctx, path)
		if got != nil {
			got.Close()
			t.Fatalf("iteration %d: Open succeeded on newer schema", i)
		}
		if !errors.Is(err, ErrNewerSchema) {
			t.Fatalf("iteration %d: err = %v, want ErrNewerSchema", i, err)
		}
	}

	// Another connection must be able to take an immediate write lock with zero busy timeout.
	raw := otherProcess(t, path, 0)
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		conn.Close()
		t.Fatalf("write lock after failed Opens failed (stale lock?): %v", err)
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", len(migrations))); err != nil {
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	conn.Close()

	// Reverting user_version must immediately allow Open and writes again.
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after reverting user_version failed: %v", err)
	}
	defer reopened.Close()
	if err := reopened.SetSyncState(ctx, "recovered", "yes"); err != nil {
		t.Fatalf("write on reopened store failed: %v", err)
	}
	d, err := reopened.GetTask(ctx, "t-reopen")
	if err != nil || d.Name != "復旧対象" {
		t.Fatalf("GetTask = %+v, %v", d, err)
	}
}

func TestOpenExtremeSchemaVersions(t *testing.T) {
	ctx := context.Background()

	for _, ver := range []int{len(migrations) + 1, len(migrations) + 1000, math.MaxInt32} {
		t.Run(fmt.Sprintf("newer_%d", ver), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			raw := openRawSQLite(t, path)
			if _, err := raw.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", ver)); err != nil {
				t.Fatal(err)
			}

			got, err := Open(ctx, path)
			if got != nil {
				got.Close()
				t.Fatalf("Open succeeded on version %d", ver)
			}
			if !errors.Is(err, ErrNewerSchema) {
				t.Fatalf("err = %v, want ErrNewerSchema", err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(ver)) || !strings.Contains(err.Error(), strconv.Itoa(len(migrations))) {
				t.Fatalf("err = %q, want both db=%d and supported=%d", err.Error(), ver, len(migrations))
			}
		})
	}

	for _, ver := range []int{-1, math.MinInt32} {
		t.Run(fmt.Sprintf("negative_%d", ver), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			raw := openRawSQLite(t, path)
			if _, err := raw.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", ver)); err != nil {
				t.Fatal(err)
			}

			got, err := Open(ctx, path)
			if got != nil {
				got.Close()
				t.Fatalf("Open succeeded on negative version %d", ver)
			}
			if err == nil || errors.Is(err, ErrNewerSchema) {
				t.Fatalf("err = %v, want non-ErrNewerSchema error", err)
			}
			if !strings.Contains(err.Error(), strconv.Itoa(ver)) {
				t.Fatalf("err = %q, want version %d", err.Error(), ver)
			}
		})
	}
}
