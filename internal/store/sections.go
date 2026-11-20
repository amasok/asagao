package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/termsafe"
)

func init() {
	registerUndo(OpRenameSection, func(ctx context.Context, tx *sql.Tx, op *Op, _ outbox.RenameSection) error {
		name, ok := op.Base["name"].(string)
		if !ok {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE sections SET name = ? WHERE gid = ?", name, op.TaskGID)
		return err
	})
}

// OpRenameSection renames a section (outbox.KindRenameSection).
const OpRenameSection = outbox.KindRenameSection

// RenameSectionLocal renames a section locally and queues the change for Asana.
func (s *Store) RenameSectionLocal(ctx context.Context, sectionGID, name string, now time.Time) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("セクション名が空です")
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		var current string
		err := tx.QueryRowContext(ctx, "SELECT name FROM sections WHERE gid = ?", sectionGID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("section %s: %w", sectionGID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if current == name {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "UPDATE sections SET name = ? WHERE gid = ?", name, sectionGID); err != nil {
			return err
		}
		op, err := findOp(ctx, tx, OpRenameSection, sectionGID)
		if err != nil {
			return err
		}
		if op == nil {
			op = &Op{Kind: OpRenameSection, TaskGID: sectionGID, Payload: map[string]any{},
				Base: map[string]any{"name": current}, State: OpPending, CreatedAt: formatTime(now)}
		}
		if op.Base["name"] == name { // renamed back
			if op.ID == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID)
			return err
		}
		op.Payload["name"] = name
		return saveOp(ctx, tx, op)
	})
}

// reapplySectionRenames keeps unsent renames after a sync replaced the sections.
func reapplySectionRenames(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT task_gid, payload FROM outbox WHERE kind = ? AND state <> 'failed' ORDER BY id", OpRenameSection)
	if err != nil {
		return err
	}
	renames := map[string]string{}
	for rows.Next() {
		var gid, payload string
		if err := rows.Scan(&gid, &payload); err != nil {
			rows.Close()
			return err
		}
		var p map[string]any
		if err := termsafe.Unmarshal([]byte(payload), &p); err != nil {
			rows.Close()
			return err
		}
		if name, ok := p["name"].(string); ok {
			renames[gid] = name
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for gid, name := range renames {
		if _, err := tx.ExecContext(ctx, "UPDATE sections SET name = ? WHERE gid = ?", name, gid); err != nil {
			return err
		}
	}
	return nil
}

// SectionName returns a section's (local) name.
func (s *Store) SectionName(ctx context.Context, gid string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM sections WHERE gid = ?", gid).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("section %s: %w", gid, ErrNotFound)
	}
	return name, err
}

// ApplySectionName records the server's name for a section (after a push).
func (s *Store) ApplySectionName(ctx context.Context, gid, name string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE sections SET name = ? WHERE gid = ?", name, gid)
	return err
}
