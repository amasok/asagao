package store

import (
	"context"
	"database/sql"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// The entries of the outbox to work on are picked in SQL by their columns (kind, task,
// state) and in Go by what their payload holds, read as their kind's struct as far as it
// reads (outbox.BestEffortPayload): never with json_extract on the stored form (archtest:
// TestStoreSQLDoesNotReadPayloads), so that a key name or a value type mistyped is caught
// by the struct. An entry this version cannot read entirely (held) is still found by what
// it holds that reads, as json_extract found it; a value of another type reads as its
// field's zero value, and matches no gid. The outbox is small (the changes not sent yet),
// so reading the entries of a kind to pick some of them costs little.

// opColumns are the columns of the outbox scanOp reads, in its order.
const opColumns = `id, kind, task_gid, payload, base, base_modified_at, state, conflict, last_error, attempts, created_at,
	sent_at, last_sent_at, verify`

// scanOp reads an entry (opColumns) with scan (a row's or rows' Scan).
func scanOp(scan func(dest ...any) error) (*Op, error) {
	var op Op
	var payload, base, conflict string
	if err := scan(&op.ID, &op.Kind, &op.TaskGID, &payload, &base, &op.BaseModifiedAt,
		&op.State, &conflict, &op.LastError, &op.Attempts, &op.CreatedAt, &op.SentAt, &op.LastSentAt, &op.Verify); err != nil {
		return nil, err
	}
	return &op, unmarshalOp(&op, payload, base, conflict)
}

// opsWhere returns the entries where (a condition on the columns of the outbox, with
// args) selects whose payload match says yes to (nil: every one), in the order they were
// queued. A row whose payload, base or conflict is not a JSON object (damaged) is left
// out, as json_extract read nothing from it: it is neither changed nor removed here, and
// does not stop the reads and pulls that look among the entries.
func opsWhere(ctx context.Context, q dbQuerier, match func(Op) bool, where string, args ...any) ([]Op, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+opColumns+` FROM outbox WHERE `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []Op
	for rows.Next() {
		op, err := scanOp(rows.Scan)
		if op == nil {
			return nil, err
		}
		if err != nil {
			continue // damaged (see above)
		}
		if match == nil || match(*op) {
			ops = append(ops, *op)
		}
	}
	return ops, rows.Err()
}

// lastOpWhere returns the newest entry opsWhere selects (nil when none).
func lastOpWhere(ctx context.Context, q dbQuerier, match func(Op) bool, where string, args ...any) (*Op, error) {
	ops, err := opsWhere(ctx, q, match, where, args...)
	if err != nil || len(ops) == 0 {
		return nil, err
	}
	return &ops[len(ops)-1], nil
}

// anyOpWhere reports whether opsWhere selects an entry.
func anyOpWhere(ctx context.Context, q dbQuerier, match func(Op) bool, where string, args ...any) (bool, error) {
	ops, err := opsWhere(ctx, q, match, where, args...)
	return len(ops) > 0, err
}

// payloadIs is the match of the entries whose payload, read as P (their kind's struct) as
// far as it reads, f says yes to. An entry of a kind whose struct is not P reads as a zero P.
func payloadIs[P any](f func(P) bool) func(Op) bool {
	return func(op Op) bool {
		p, _ := outbox.BestEffortPayload[P](op)
		return f(p)
	}
}

// deleteOps removes the entries ops from the outbox, and returns how many it removed: all
// of them, read in the same transaction (not RowsAffected, which a driver may fail to
// report: #67).
func deleteOps(ctx context.Context, tx *sql.Tx, ops []Op) (int, error) {
	for _, op := range ops {
		if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id = ?", op.ID); err != nil {
			return 0, err
		}
	}
	return len(ops), nil
}

// refOf returns the comment, attachment or time entry the payload of op names, as far as
// it reads: the one it creates, changes or deletes ("" for another kind).
func refOf(op Op) string {
	switch op.Kind {
	case OpAddComment:
		p, _ := outbox.BestEffortPayload[outbox.AddComment](op)
		return p.StoryGID
	case OpUpdateStory:
		p, _ := outbox.BestEffortPayload[outbox.UpdateStory](op)
		return p.Story
	case OpDeleteStory:
		p, _ := outbox.BestEffortPayload[outbox.DeleteStory](op)
		return p.Story
	case OpUploadAttachment:
		p, _ := outbox.BestEffortPayload[outbox.UploadAttachment](op)
		return p.Attachment
	case OpDeleteAttachment:
		p, _ := outbox.BestEffortPayload[outbox.DeleteAttachment](op)
		return p.Attachment
	case OpAddTimeEntry, OpUpdateTimeEntry, OpDeleteTimeEntry:
		p, _ := outbox.BestEffortPayload[outbox.TimeEntry](op)
		return p.Entry
	}
	return ""
}

// refIs is the match of the entries whose payload names gid (refOf).
func refIs(gid string) func(Op) bool {
	return func(op Op) bool { return gid != "" && refOf(op) == gid }
}

// resolvedRefs returns the comments, attachments or time entries the entries where selects
// name (refOf), as Asana knows them: a local gid is replaced by what Asana created for it
// (recorded under prefix), if recorded.
func resolvedRefs(ctx context.Context, q dbQuerier, prefix, where string, args ...any) (map[string]bool, error) {
	ops, err := opsWhere(ctx, q, nil, where, args...)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, op := range ops {
		ref := refOf(op)
		if ref == "" {
			continue
		}
		real, err := resolveResult(ctx, q, prefix, ref)
		if err != nil {
			return nil, err
		}
		out[real] = true
	}
	return out, nil
}
