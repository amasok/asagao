package store

import (
	"context"
	"database/sql"
	"errors"
)

// The workspace a section or a tag is in, so that a 402 for it is recorded (and read) for
// that workspace only (readmodel.PlanKey, #50).

// SectionWorkspace returns the workspace of a cached section: its project's ("" if the
// section or its project is not cached).
func (s *Store) SectionWorkspace(ctx context.Context, sectionGID string) (string, error) {
	var ws string
	err := s.db.QueryRowContext(ctx, `SELECT p.workspace_gid FROM sections s JOIN projects p ON p.gid = s.project_gid
		WHERE s.gid = ?`, sectionGID).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ws, err
}

// TagWorkspace returns the workspace of a tag as the cache knows it: tags.workspace_gid,
// falling back to a task that carries it ("" when neither is known). A local gid whose
// creation already reached Asana is resolved to its real gid first.
func (s *Store) TagWorkspace(ctx context.Context, tagGID string) (string, error) {
	if IsLocalGID(tagGID) {
		real, err := s.RealTagGID(ctx, tagGID)
		if err != nil {
			return "", err
		}
		tagGID = real
	}
	var ws string
	err := s.db.QueryRowContext(ctx, "SELECT workspace_gid FROM tags WHERE gid = ?", tagGID).Scan(&ws)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if ws != "" {
		return ws, nil
	}
	return tagWorkspaceFromTasks(ctx, s.db, tagGID)
}

func tagWorkspaceFromTasks(ctx context.Context, q dbQuerier, tagGID string) (string, error) {
	var ws string
	err := q.QueryRowContext(ctx, `SELECT ws FROM (
		SELECT COALESCE(NULLIF(t.workspace_gid, ''),
			(SELECT p.workspace_gid FROM task_memberships m JOIN projects p ON p.gid = m.project_gid
				WHERE m.task_gid = t.gid ORDER BY m.rowid LIMIT 1), '') AS ws
		FROM task_tags tt JOIN tasks t ON t.gid = tt.task_gid WHERE tt.tag_gid = ? ORDER BY tt.task_gid)
		WHERE ws <> '' LIMIT 1`, tagGID).Scan(&ws)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ws, err
}
