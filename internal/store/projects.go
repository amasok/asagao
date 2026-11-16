package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

// OpCreateStatus posts a project status update. task_gid holds the project gid;
// payload: status (local gid), status_type, title, text, html_text.
const OpCreateStatus = "create_status"

// StatusTypes are the status values of a project status update, with Japanese labels.
var StatusTypes = []struct{ Value, Label string }{
	{"on_track", "順調"}, {"at_risk", "要注意"}, {"off_track", "遅れ"}, {"on_hold", "保留"}, {"complete", "完了"},
}

// StatusLabel returns the Japanese label of a status type.
func StatusLabel(t string) string {
	for _, s := range StatusTypes {
		if s.Value == t {
			return s.Label
		}
	}
	return t
}

type StatusUpdateView struct {
	GID        string `json:"gid"`
	StatusType string `json:"status_type"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	HTMLText   string `json:"html_text,omitempty"`
	Author     string `json:"author"`
	CreatedAt  string `json:"created_at"`
	Pending    bool   `json:"pending"`
}

// ReplaceStatusUpdates stores a project's status updates (keeping unsent local ones).
func (s *Store) ReplaceStatusUpdates(ctx context.Context, projectGID string, updates []asana.StatusUpdate) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM status_updates WHERE project_gid = ? AND gid NOT LIKE 'local:%'", projectGID); err != nil {
			return err
		}
		for _, u := range updates {
			author := ""
			if u.Author != nil {
				author = u.Author.Name
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO status_updates
				(gid, project_gid, status_type, title, text, html_text, author_name, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
				u.GID, projectGID, u.StatusType, u.Title, u.Text, u.HTMLText, author, formatTime(u.CreatedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// StatusUpdates returns a project's status updates, newest first.
func (s *Store) StatusUpdates(ctx context.Context, projectGID string) ([]StatusUpdateView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT gid, status_type, title, text, html_text, author_name, created_at
		FROM status_updates WHERE project_gid = ? ORDER BY created_at DESC`, projectGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatusUpdateView{}
	for rows.Next() {
		var v StatusUpdateView
		if err := rows.Scan(&v.GID, &v.StatusType, &v.Title, &v.Text, &v.HTMLText, &v.Author, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.Pending = IsLocalGID(v.GID)
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateStatusLocal adds a status update to a project and queues it (offline too).
func (s *Store) CreateStatusLocal(ctx context.Context, projectGID, statusType, title, text, htmlText string, author UserRef, now time.Time) (string, error) {
	valid := false
	for _, t := range StatusTypes {
		valid = valid || t.Value == statusType
	}
	if !valid {
		return "", fmt.Errorf("status は on_track / at_risk / off_track / on_hold / complete のどれかです（%q）", statusType)
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("本文が空です")
	}
	gid := LocalPrefix + randomID()
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM projects WHERE gid = ?", projectGID).Scan(&n); err != nil {
			return fmt.Errorf("project %s: %w", projectGID, err)
		}
		if n == 0 {
			return fmt.Errorf("project %s: %w", projectGID, ErrNotFound)
		}
		ts := formatTime(now)
		if _, err := tx.ExecContext(ctx, `INSERT INTO status_updates(gid, project_gid, status_type, title, text, html_text, author_name, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, gid, projectGID, statusType, title, text, htmlText, author.Name, ts); err != nil {
			return err
		}
		return saveOp(ctx, tx, &Op{Kind: OpCreateStatus, TaskGID: projectGID, State: OpPending, CreatedAt: ts,
			Payload: map[string]any{"status": gid, "status_type": statusType, "title": title, "text": text, "html_text": htmlText}})
	})
	return gid, err
}

// ReplaceLocalStatus records the Asana gid of a posted status update.
func (s *Store) ReplaceLocalStatus(ctx context.Context, local string, u asana.StatusUpdate) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return replaceLocalStatus(ctx, tx, local, u) })
}

func replaceLocalStatus(ctx context.Context, tx *sql.Tx, local string, u asana.StatusUpdate) error {
	if _, err := tx.ExecContext(ctx, "UPDATE status_updates SET gid = ?, created_at = ? WHERE gid = ?", u.GID, formatTime(u.CreatedAt), local); err != nil {
		return err
	}
	return recordResult(ctx, tx, ResultStatus, local, u.GID)
}

// ProjectMembers returns a project's members.
func (s *Store) ProjectMembers(ctx context.Context, projectGID string) ([]UserRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.user_gid, COALESCE(u.name, '') FROM project_members m
		LEFT JOIN users u ON u.gid = m.user_gid WHERE m.project_gid = ? ORDER BY u.name`, projectGID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRef{}
	for rows.Next() {
		var u UserRef
		if err := rows.Scan(&u.GID, &u.Name); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ArchiveProjectLocal marks a project archived (after Asana accepted it); it disappears
// from the project list.
func (s *Store) SetProjectArchivedLocal(ctx context.Context, projectGID string, archived bool) error {
	_, err := s.db.ExecContext(ctx, "UPDATE projects SET archived = ? WHERE gid = ?", archived, projectGID)
	return err
}
