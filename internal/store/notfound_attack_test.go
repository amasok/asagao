package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A DB failure inside a lookup must reach the caller as itself, not as ErrNotFound (#54).
// The failure is made by renaming the table the lookup reads ("no such table").

type lookupCase struct {
	name  string
	table string // table to break
	call  func(ctx context.Context, s *Store) error
}

func lookupCases() []lookupCase {
	now := time.Now()
	return []lookupCase{
		{"AddTagLocal/task", "tasks", func(ctx context.Context, s *Store) error {
			_, err := s.AddTagLocal(ctx, "t1", "g1", "", now)
			return err
		}},
		{"AddTagLocal/tag", "tags", func(ctx context.Context, s *Store) error {
			_, err := s.AddTagLocal(ctx, "t1", "g1", "", now)
			return err
		}},
		{"SetCustomFieldLocal/task", "tasks", func(ctx context.Context, s *Store) error {
			_, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", "x", now)
			return err
		}},
		{"CreateStatusLocal/project", "projects", func(ctx context.Context, s *Store) error {
			_, err := s.CreateStatusLocal(ctx, "p1", "on_track", "t", "body", "", UserRef{Name: "me"}, now)
			return err
		}},
		{"SetProjectLocal/project", "projects", func(ctx context.Context, s *Store) error {
			return s.SetProjectLocal(ctx, "t1", "p2", "", true, now)
		}},
		{"CreateTaskLocal/parent", "tasks", func(ctx context.Context, s *Store) error {
			_, err := s.CreateTaskLocal(ctx, NewTask{ParentGID: "t1", Name: "sub"}, now)
			return err
		}},
		{"CreateTaskLocal/project", "projects", func(ctx context.Context, s *Store) error {
			_, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "x"}, now)
			return err
		}},
		{"DuplicateTaskLocal/task", "tasks", func(ctx context.Context, s *Store) error {
			_, err := s.DuplicateTaskLocal(ctx, "t1", "", now)
			return err
		}},
	}
}

func TestAttackLookupDBErrorIsNotNotFound(t *testing.T) {
	for _, c := range lookupCases() {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			if _, err := s.db.ExecContext(ctx, "ALTER TABLE "+c.table+" RENAME TO "+c.table+"_broken"); err != nil {
				t.Skipf("cannot break %s: %v", c.table, err)
			}
			err := c.call(ctx, s)
			if err == nil {
				t.Fatal("want an error")
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("DB error turned into ErrNotFound: %v", err)
			}
		})
	}
}

func TestAttackLookupClosedDBIsNotNotFound(t *testing.T) {
	for _, c := range lookupCases() {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			s.Close()
			err := c.call(context.Background(), s)
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("closed DB: got %v", err)
			}
		})
	}
}

func TestAttackLookupCanceledCtxIsNotNotFound(t *testing.T) {
	for _, c := range lookupCases() {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := c.call(ctx, s)
			if err == nil || errors.Is(err, ErrNotFound) {
				t.Fatalf("canceled ctx: got %v", err)
			}
			if !errors.Is(err, context.Canceled) {
				t.Logf("note: error does not wrap context.Canceled: %v", err)
			}
		})
	}
}

// What is really missing is still ErrNotFound.
func TestAttackLookupMissingStillNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	checks := map[string]error{}
	_, checks["AddTag/task"] = s.AddTagLocal(ctx, "nope", "g1", "", now)
	_, checks["AddTag/tag"] = s.AddTagLocal(ctx, "t1", "nope", "", now)
	_, checks["SetCF/task"] = s.SetCustomFieldLocal(ctx, "nope", "cf1", "x", now)
	_, checks["Status/project"] = s.CreateStatusLocal(ctx, "nope", "on_track", "t", "body", "", UserRef{Name: "me"}, now)
	_, checks["Dup/task"] = s.DuplicateTaskLocal(ctx, "nope", "", now)
	checks["SetProject/project"] = s.SetProjectLocal(ctx, "t1", "nope", "", true, now)
	_, checks["NewTask/parent"] = s.CreateTaskLocal(ctx, NewTask{ParentGID: "nope", Name: "x"}, now)
	_, checks["NewTask/project"] = s.CreateTaskLocal(ctx, NewTask{ProjectGID: "nope", Name: "x"}, now)
	for name, err := range checks {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: want ErrNotFound, got %v", name, err)
		}
	}
}

// migrateRetry (#54 comment): when ctx ends, the error says so, and it returns promptly.
func TestAttackMigrateRetryCtx(t *testing.T) {
	path := t.TempDir() + "/store.db"
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// roll the schema back one step so the next Open has a migration to run
	rollbackTo(t, s.db, len(migrations)-1)
	s.Close()

	holder, err := Open(context.Background(), path) // re-migrates; then we hold the lock
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	rollbackTo(t, holder.db, len(migrations)-1)
	conn, err := holder.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	s2, err := Open(ctx, path)
	if err == nil {
		s2.Close()
		t.Skip("migration did not need the lock")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("want context.DeadlineExceeded in the error, got: %v", err)
	}
	if d := time.Since(start); d > busyTimeout+2*time.Second {
		t.Errorf("took %v after ctx ended", d)
	}
}
