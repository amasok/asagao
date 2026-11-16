package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// breakTable makes every query on table fail with a database error ("no such table"),
// standing in for a real one (busy past busy_timeout, I/O): it is not a missing row.
func breakTable(t *testing.T, s *Store, table string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), "ALTER TABLE "+table+" RENAME TO "+table+"_broken"); err != nil {
		t.Fatal(err)
	}
}

// A lookup that fails for a reason other than a missing row returns that error, not
// ErrNotFound (#54): "the task is not there" would hide the real cause from the user.
func TestLookupDBErrorIsNotNotFound(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		table string
		call  func(s *Store) error
	}{
		{"AddTagLocal task", "tasks", func(s *Store) error {
			_, err := s.AddTagLocal(ctx, "t1", "", "新タグ", testNow)
			return err
		}},
		{"AddTagLocal tag", "tags", func(s *Store) error {
			_, err := s.AddTagLocal(ctx, "t1", "g1", "", testNow)
			return err
		}},
		{"SetCustomFieldLocal", "tasks", func(s *Store) error {
			_, err := s.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", testNow)
			return err
		}},
		{"CreateStatusLocal", "projects", func(s *Store) error {
			_, err := s.CreateStatusLocal(ctx, "p1", "on_track", "t", "x", "", UserRef{Name: "Me"}, testNow)
			return err
		}},
		{"DuplicateTaskLocal", "tasks", func(s *Store) error {
			_, err := s.DuplicateTaskLocal(ctx, "t1", "", testNow)
			return err
		}},
		{"SetProjectLocal", "projects", func(s *Store) error {
			return s.SetProjectLocal(ctx, "t1", "p2", "", true, testNow)
		}},
		{"CreateTaskLocal parent", "tasks", func(s *Store) error {
			_, err := s.CreateTaskLocal(ctx, NewTask{Name: "x", ParentGID: "t1"}, testNow)
			return err
		}},
		{"CreateTaskLocal project", "projects", func(s *Store) error {
			_, err := s.CreateTaskLocal(ctx, NewTask{Name: "x", ProjectGID: "p1"}, testNow)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := setupFields(t)
			breakTable(t, s, c.table)
			err := c.call(s)
			if err == nil {
				t.Fatal("succeeded with a broken table")
			}
			if errors.Is(err, ErrNotFound) {
				t.Fatalf("database error reported as not found: %v", err)
			}
			if !strings.Contains(err.Error(), "no such table") {
				t.Fatalf("the cause is lost: %v", err)
			}
		})
	}
}

// A missing row is still ErrNotFound.
func TestLookupMissingRowIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := setupFields(t)
	calls := map[string]func() error{
		"AddTagLocal task": func() error { _, err := s.AddTagLocal(ctx, "nope", "", "新タグ", testNow); return err },
		"AddTagLocal tag":  func() error { _, err := s.AddTagLocal(ctx, "t1", "nope", "", testNow); return err },
		"SetCustomFieldLocal": func() error {
			_, err := s.SetCustomFieldLocal(ctx, "nope", "cf1", "o2", testNow)
			return err
		},
		"CreateStatusLocal": func() error {
			_, err := s.CreateStatusLocal(ctx, "nope", "on_track", "t", "x", "", UserRef{Name: "Me"}, testNow)
			return err
		},
		"DuplicateTaskLocal": func() error { _, err := s.DuplicateTaskLocal(ctx, "nope", "", testNow); return err },
		"SetProjectLocal":    func() error { return s.SetProjectLocal(ctx, "t1", "nope", "", true, testNow) },
		"CreateTaskLocal parent": func() error {
			_, err := s.CreateTaskLocal(ctx, NewTask{Name: "x", ParentGID: "nope"}, testNow)
			return err
		},
		"CreateTaskLocal project": func() error {
			_, err := s.CreateTaskLocal(ctx, NewTask{Name: "x", ProjectGID: "nope"}, testNow)
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v, want ErrNotFound", name, err)
		}
	}
}
