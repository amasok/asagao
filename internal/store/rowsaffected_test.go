package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"testing"

	"modernc.org/sqlite"
)

// errRowsAffected is what RowsAffected reports in rowsAffectedFails. modernc.org/sqlite never
// fails there, but another driver may (#67).
var errRowsAffected = errors.New("rows affected: test failure")

type badRowsConnector struct{ safeConnector }

func (c badRowsConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.safeConnector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return badRowsConn{conn.(sqliteConn)}, nil
}

type badRowsConn struct{ sqliteConn }

func (c badRowsConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	res, err := c.sqliteConn.ExecContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return badRowsResult{res}, nil
}

type badRowsResult struct{ driver.Result }

func (badRowsResult) RowsAffected() (int64, error) { return 0, errRowsAffected }

// rowsAffectedFails opens the database of s again through a driver whose RowsAffected fails.
func rowsAffectedFails(t *testing.T, s *Store) *Store {
	t.Helper()
	dsn := "file:" + (&url.URL{Path: s.path}).EscapedPath() + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	db := sql.OpenDB(badRowsConnector{safeConnector{dsn: dsn, drv: &sqlite.Driver{}}})
	t.Cleanup(func() { db.Close() })
	return &Store{db: db, path: s.path}
}

// A failing RowsAffected is an error, not "nothing was deleted" (#67): treated as 0, a
// prune reports nothing pruned. (Cancelling an unsent change, a tag, a dependency, a
// follower, does not use it: it counts the entries it picks in the same transaction,
// deleteOps.)
func TestRowsAffectedErrorIsReturned(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(s *Store) error
	}{
		{"PruneTasks", func(s *Store) error { _, err := s.PruneTasks(ctx, map[string]bool{}); return err }},
		{"DeleteGoneTasks", func(s *Store) error { _, err := s.DeleteGoneTasks(ctx, []string{"t1"}); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			bad := rowsAffectedFails(t, s)
			if err := c.run(bad); !errors.Is(err, errRowsAffected) {
				t.Fatalf("err = %v, want %v", err, errRowsAffected)
			}
			// the transaction is rolled back: nothing is queued
			var n int
			must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM outbox").Scan(&n))
			if n != 0 {
				t.Fatalf("outbox has %d entries after the failure", n)
			}
		})
	}
}
