package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

func init() {
	registerUndo(OpMarkNotification, func(ctx context.Context, tx *sql.Tx, op *Op, _ outbox.MarkNotification) error {
		read, ok := op.Base["read"].(bool)
		if !ok {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE notifications SET is_read = ? WHERE gid = ?", read, op.TaskGID)
		return err
	})
}

// The inbox: the user's Asana notifications, cached by sync (see sync.Run) so they can be
// read offline. Marking one read / unread is applied locally and queued in the outbox.

// OpMarkNotification marks a notification read or unread (outbox.KindMarkNotification).
const OpMarkNotification = outbox.KindMarkNotification

// Inbox availability, recorded by sync under KeyInboxStatus.
const (
	KeyInboxStatus = "inbox_status" // one of the Inbox* values below ("" before the first try)
	KeyInboxError  = "inbox_error"  // Asana's error message when not InboxOK
	// KeyInboxWrite is "forbidden" since Asana refused to mark notifications (no
	// notifications:write scope): front ends say why and do not mark them by themselves.
	KeyInboxWrite = "inbox_write"

	// The Inbox* values are readmodel's.
	InboxOK        = readmodel.InboxOK
	InboxForbidden = readmodel.InboxForbidden
	InboxPayment   = readmodel.InboxPayment
	InboxError     = readmodel.InboxError
)

// InboxState is readmodel.InboxState.
type InboxState = readmodel.InboxState

func (s *Store) InboxState(ctx context.Context) (InboxState, error) {
	status, err := s.SyncState(ctx, KeyInboxStatus)
	if err != nil {
		return InboxState{}, err
	}
	msg, err := s.SyncState(ctx, KeyInboxError)
	if err != nil {
		return InboxState{}, err
	}
	write, err := s.SyncState(ctx, KeyInboxWrite)
	return InboxState{Status: status, Error: msg, WriteForbidden: write == InboxForbidden}, err
}

// SetInboxWriteForbidden records whether Asana refuses to mark notifications.
func (s *Store) SetInboxWriteForbidden(ctx context.Context, forbidden bool) error {
	v := ""
	if forbidden {
		v = InboxForbidden
	}
	return s.SetSyncState(ctx, KeyInboxWrite, v)
}

// DropNotificationMarks removes every queued read / unread change that is not being sent
// (and entry also, which may be), restoring the read state from before each: Asana refuses
// them all (no notifications:write scope). It returns how many were dropped.
func (s *Store) DropNotificationMarks(ctx context.Context, also int64) (int, error) {
	n := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM outbox WHERE kind = ? AND (state <> 'sending' OR id = ?) ORDER BY id DESC", OpMarkNotification, also)
		if err != nil {
			return err
		}
		ids, err := scanIDs(rows)
		if err != nil {
			return err
		}
		for _, id := range ids { // newest first, so the oldest base is restored last
			op, err := getOpTx(ctx, tx, id)
			if err != nil {
				return err
			}
			if err := discardOpTx(ctx, tx, op); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func (s *Store) SetInboxState(ctx context.Context, st InboxState) error {
	if err := s.SetSyncState(ctx, KeyInboxStatus, st.Status); err != nil {
		return err
	}
	return s.SetSyncState(ctx, KeyInboxError, st.Error)
}

// Notification is readmodel.Notification.
type Notification = readmodel.Notification

// ReplaceNotifications replaces the cached notifications of a workspace with what Asana
// returned (newest first). Read / unread changes not sent yet are applied again on top.
func (s *Store) ReplaceNotifications(ctx context.Context, workspaceGID string, ns []model.Notification) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM notifications WHERE workspace_gid = ?", workspaceGID); err != nil {
			return err
		}
		for i, n := range ns {
			var actor model.Ref
			if n.CreatedBy != nil {
				actor = *n.CreatedBy
			}
			subtype, text := n.StoryResourceSubtype, ""
			if n.Story != nil {
				text = n.Story.Text
				if subtype == "" {
					subtype = n.Story.ResourceSubtype
				}
				if actor.GID == "" && n.Story.CreatedBy != nil {
					actor = *n.Story.CreatedBy
				}
			}
			target := notificationTarget(n.WorkObjects)
			if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO notifications(gid, workspace_gid, is_read, created_at,
				actor_gid, actor_name, subtype, text, target_gid, target_type, target_name, position)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				n.GID, workspaceGID, n.IsRead, formatTime(n.CreatedAt), actor.GID, actor.Name, subtype, text,
				target.GID, target.ResourceType, target.Name, i); err != nil {
				return err
			}
		}
		return reapplyNotificationMarks(ctx, tx)
	})
}

// notificationTarget picks what a notification is about: its task if it has one.
func notificationTarget(objs []model.WorkRef) model.WorkRef {
	for _, o := range objs {
		if o.ResourceType == "task" {
			return o
		}
	}
	if len(objs) > 0 {
		return objs[0]
	}
	return model.WorkRef{}
}

// reapplyNotificationMarks keeps unsent read / unread changes after a sync replaced the
// notifications.
func reapplyNotificationMarks(ctx context.Context, tx *sql.Tx) error {
	ops, err := opsTx(ctx, tx, "SELECT id FROM outbox WHERE kind = ? AND state <> 'failed' ORDER BY id", OpMarkNotification)
	if err != nil {
		return err
	}
	marks := map[string]bool{}
	for _, op := range ops {
		p, _ := outbox.BestEffortPayload[outbox.MarkNotification](op) // (held: as far as it reads)
		marks[op.TaskGID] = p.Read
	}
	for gid, read := range marks {
		if _, err := tx.ExecContext(ctx, "UPDATE notifications SET is_read = ? WHERE gid = ?", read, gid); err != nil {
			return err
		}
	}
	return nil
}

// NotificationFilter is readmodel.NotificationFilter.
type NotificationFilter = readmodel.NotificationFilter

// ListNotifications returns cached notifications, newest first.
func (s *Store) ListNotifications(ctx context.Context, f NotificationFilter) ([]Notification, error) {
	q := `SELECT n.gid, n.workspace_gid, n.is_read, n.created_at, n.actor_gid, n.actor_name, n.subtype, n.text,
		n.target_gid, n.target_type, COALESCE(t.name, n.target_name), t.gid IS NOT NULL,
		EXISTS (SELECT 1 FROM outbox o WHERE o.kind = ? AND o.task_gid = n.gid AND o.state <> 'failed')
		FROM notifications n LEFT JOIN tasks t ON n.target_type = 'task' AND t.gid = n.target_gid`
	args := []any{OpMarkNotification}
	if f.UnreadOnly {
		q += " WHERE n.is_read = 0"
	}
	q += " ORDER BY n.created_at DESC, n.position"
	if f.Limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.GID, &n.WorkspaceGID, &n.Read, &n.CreatedAt, &n.ActorGID, &n.ActorName, &n.Subtype, &n.Text,
			&n.TargetGID, &n.TargetType, &n.TargetName, &n.TaskCached, &n.Pending); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotifications counts the cached unread notifications.
func (s *Store) UnreadNotifications(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE is_read = 0").Scan(&n)
	return n, err
}

// NotificationTargets returns the gids of the tasks the cached notifications are about,
// those of the newest notifications first. recent are the ones with a notification
// created at or after since (RFC 3339; "" for none).
func (s *Store) NotificationTargets(ctx context.Context, since string) (gids []string, recent map[string]bool, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target_gid, MAX(created_at) AS latest FROM notifications
		WHERE target_type = 'task' AND target_gid <> '' GROUP BY target_gid ORDER BY latest DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	recent = map[string]bool{}
	for rows.Next() {
		var g, latest string
		if err := rows.Scan(&g, &latest); err != nil {
			return nil, nil, err
		}
		gids = append(gids, g)
		if since != "" && latest >= since {
			recent[g] = true
		}
	}
	return gids, recent, rows.Err()
}

// ResolveNotification finds a cached notification by gid or by a unique gid suffix
// (the CLI shows short ids).
func (s *Store) ResolveNotification(ctx context.Context, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", errors.New("通知の ID が空です")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT gid FROM notifications ORDER BY gid = ? DESC", arg)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var gids []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return "", err
		}
		if g == arg {
			return g, nil
		}
		if strings.HasSuffix(g, arg) {
			gids = append(gids, g)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(gids) {
	case 0:
		return "", fmt.Errorf("通知 %s: %w", arg, ErrNotFound)
	case 1:
		return gids[0], nil
	}
	return "", fmt.Errorf("通知 %s に当てはまるものが %d 件あります。もっと長く指定してください", arg, len(gids))
}

// MarkNotificationsLocal marks notifications read (or unread) locally and queues the change
// for Asana. Notifications already in that state are left alone. It returns how many changed.
// Toggling one back before the change is sent removes the queued change.
func (s *Store) MarkNotificationsLocal(ctx context.Context, gids []string, read bool, now time.Time) (int, error) {
	changed := 0
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, gid := range gids {
			var ws, name string
			var cur bool
			err := tx.QueryRowContext(ctx, `SELECT n.workspace_gid, n.is_read, COALESCE(t.name, n.target_name) FROM notifications n
				LEFT JOIN tasks t ON n.target_type = 'task' AND t.gid = n.target_gid WHERE n.gid = ?`, gid).Scan(&ws, &cur, &name)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("通知 %s: %w", gid, ErrNotFound)
			}
			if err != nil {
				return err
			}
			if cur == read {
				continue
			}
			changed++
			if _, err := tx.ExecContext(ctx, "UPDATE notifications SET is_read = ? WHERE gid = ?", read, gid); err != nil {
				return err
			}
			op, err := findOp(ctx, tx, OpMarkNotification, gid)
			if err != nil {
				return err
			}
			if op != nil && (op.Verify || op.SentAt != "") {
				op = nil // it may have reached Asana: the change back must be sent too
			}
			fresh := op == nil
			if fresh {
				op = &Op{Kind: OpMarkNotification, TaskGID: gid, Base: map[string]any{"read": cur},
					State: OpPending, CreatedAt: formatTime(now)}
			}
			if base, ok := op.Base["read"].(bool); ok && base == read && op.ID != 0 { // back to the original
				if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID); err != nil {
					return err
				}
				continue
			}
			if err := outbox.EditPayload(op, func(p *outbox.MarkNotification) {
				if fresh {
					p.Workspace, p.Name = ws, name
				}
				p.Read = read
			}); err != nil {
				return err
			}
			if err := saveOp(ctx, tx, op); err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// PendingNotificationMarks returns queued read / unread changes that can be sent together
// with entry op: same workspace and direction, pending and not being verified, and the
// oldest queued change of their notification (so two changes to one notification keep
// their order). At most limit, oldest first.
func (s *Store) PendingNotificationMarks(ctx context.Context, op Op, limit int) ([]Op, error) {
	ops, err := s.queryOps(ctx, "WHERE o.kind = ?", OpMarkNotification)
	if err != nil {
		return nil, err
	}
	first := map[string]int64{} // the oldest entry of each notification goes first
	for _, o := range ops {
		if _, ok := first[o.TaskGID]; !ok && o.State != OpFailed {
			first[o.TaskGID] = o.ID
		}
	}
	mark, _ := outbox.BestEffortPayload[outbox.MarkNotification](op)
	var out []Op
	for _, o := range ops {
		if o.ID == op.ID || o.State != OpPending || o.Verify || first[o.TaskGID] != o.ID || o.TaskGID == op.TaskGID {
			continue
		}
		// One whose payload does not read is not sent along (it is held when its turn comes).
		if p, err := outbox.PayloadOf[outbox.MarkNotification](o); err == nil && len(out) < limit &&
			p.Workspace == mark.Workspace && p.Read == mark.Read {
			out = append(out, o)
		}
	}
	return out, nil
}

// NotificationLabel is readmodel.NotificationLabel.
func NotificationLabel(subtype string) string { return readmodel.NotificationLabel(subtype) }

// TaskSummaries returns the cached tasks among gids, by gid.
func (s *Store) TaskSummaries(ctx context.Context, gids []string) (map[string]TaskSummary, error) {
	out := map[string]TaskSummary{}
	for len(gids) > 0 {
		chunk := gids[:min(len(gids), 500)]
		gids = gids[len(chunk):]
		args := make([]any, len(chunk))
		for i, g := range chunk {
			args[i] = g
		}
		tasks, err := s.queryTasks(ctx, "SELECT "+taskColumns+" FROM tasks t LEFT JOIN users u ON u.gid = t.assignee_gid"+customStatusJoin+" WHERE t.gid IN (?"+
			strings.Repeat(", ?", len(chunk)-1)+")", args...)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			out[t.GID] = t
		}
	}
	return out, nil
}
