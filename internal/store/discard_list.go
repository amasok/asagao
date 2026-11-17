package store

import (
	"context"
	"fmt"

	"github.com/amasok/asagao/internal/termsafe"
)

// RecentDiscards returns the last limit dropped changes, notified or not, newest first,
// so that what the user wrote can still be found after the one-time notice.
func (s *Store) RecentDiscards(ctx context.Context, limit int) ([]Discard, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, task_gid, task_name, summary, reason, discarded_at, payload
		FROM discarded_changes ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Discard{}
	for rows.Next() {
		var d Discard
		var payload string
		if err := rows.Scan(&d.ID, &d.Kind, &d.TaskGID, &d.TaskName, &d.Summary, &d.Reason, &d.DiscardedAt, &payload); err != nil {
			return nil, err
		}
		if err := termsafe.Unmarshal([]byte(payload), &d.Payload); err != nil {
			return nil, fmt.Errorf("discarded change %d: %w", d.ID, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
