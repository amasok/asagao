package store

import (
	"context"
	"database/sql/driver"
	"fmt"

	"modernc.org/sqlite"

	"github.com/amasok/asana-tui/internal/termsafe"
)

// Every TEXT value read from the store passes through termsafe.String, so text from Asana
// (or written locally, e.g. by an MCP client) cannot reach a terminal as control characters,
// whichever front end shows it (TUI, CLI, MCP) and whenever it was stored. Doing it here, at
// the one place all reads go through, means no query can forget it.
//
// JSON stored in TEXT columns (outbox payloads, custom field values, enum options) may hold
// control characters as \u escapes, which decoding turns back into raw characters; those
// escapes are removed too (see safeText).

type safeConnector struct {
	dsn string
	drv *sqlite.Driver
}

func (c safeConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.drv.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	sc, ok := conn.(sqliteConn)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("store: unexpected sqlite connection type %T", conn)
	}
	return safeConn{sc}, nil
}

func (c safeConnector) Driver() driver.Driver { return c.drv }

// sqliteConn is what modernc.org/sqlite's connection implements and database/sql uses.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
}

type safeConn struct{ sqliteConn }

func (c safeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.sqliteConn.QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return safeRows{rows}, nil
}

func (c safeConn) Prepare(query string) (driver.Stmt, error) {
	return wrapStmt(c.sqliteConn.Prepare(query))
}

func (c safeConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return wrapStmt(c.sqliteConn.PrepareContext(ctx, query))
}

type sqliteStmt interface {
	driver.Stmt
	driver.StmtExecContext
	driver.StmtQueryContext
}

func wrapStmt(s driver.Stmt, err error) (driver.Stmt, error) {
	if err != nil {
		return nil, err
	}
	ss, ok := s.(sqliteStmt)
	if !ok {
		s.Close()
		return nil, fmt.Errorf("store: unexpected sqlite statement type %T", s)
	}
	return safeStmt{ss}, nil
}

type safeStmt struct{ sqliteStmt }

func (s safeStmt) Query(args []driver.Value) (driver.Rows, error) {
	rows, err := s.sqliteStmt.Query(args)
	if err != nil {
		return nil, err
	}
	return safeRows{rows}, nil
}

func (s safeStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := s.sqliteStmt.QueryContext(ctx, args)
	if err != nil {
		return nil, err
	}
	return safeRows{rows}, nil
}

type safeRows struct{ driver.Rows }

func (r safeRows) Next(dest []driver.Value) error {
	if err := r.Rows.Next(dest); err != nil {
		return err
	}
	for i, v := range dest {
		if s, ok := v.(string); ok {
			dest[i] = safeText(s)
		}
	}
	return nil
}

// safeText is a TEXT value as the store hands it out. termsafe.JSON also covers JSON kept in
// TEXT columns (objects, arrays, and strings such as custom field values); it only parses
// values that contain a backslash and are valid JSON, and is termsafe.String otherwise.
func safeText(s string) string { return termsafe.JSON(s) }
