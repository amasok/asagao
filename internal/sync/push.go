package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

type PushResult struct {
	Sent      int `json:"sent"`
	Conflicts int `json:"conflicts"`
	Failed    int `json:"failed"`
	// Remaining is the number of pending entries left (e.g. because we went offline).
	Remaining int `json:"remaining"`
}

// Push sends pending outbox entries to Asana in order.
//
// An update is checked against the server first: if a field we change was also changed
// on the server (server value differs from both our base and our new value), the entry is
// marked as a conflict and left for the user to resolve. API errors (e.g. the task was
// deleted) mark the entry failed and Push continues; network errors stop Push and are
// returned so the caller can treat it as offline.
func (s *Syncer) Push(ctx context.Context) (PushResult, error) {
	var res PushResult
	ops, err := s.Store.PendingOps(ctx)
	if err != nil {
		return res, err
	}
	for i, snapshot := range ops {
		// Re-read: sending an earlier entry may have rewritten this one (a create replaces
		// the local gid that later comments/updates refer to).
		op, err := s.Store.GetOp(ctx, snapshot.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return res, err
		}
		if op.State != store.OpPending {
			continue
		}
		switch op.Kind {
		case store.OpUpdateTask:
			var conflict bool
			conflict, err = s.pushUpdate(ctx, *op)
			if err == nil && conflict {
				res.Conflicts++
				continue
			}
		case store.OpCreateTask:
			err = s.pushCreate(ctx, *op)
		case store.OpAddComment:
			err = s.pushComment(ctx, *op)
		case store.OpMoveTask:
			err = s.pushMove(ctx, *op)
		case store.OpRenameSection:
			var conflict bool
			conflict, err = s.pushRenameSection(ctx, *op)
			if err == nil && conflict {
				res.Conflicts++
				continue
			}
		default:
			err = &asana.Error{Status: 400}
		}
		if err == nil {
			res.Sent++
			continue
		}
		var apiErr *asana.Error
		if !errors.As(err, &apiErr) {
			res.Remaining = len(ops) - i
			return res, err // offline or cancelled: keep the rest for later
		}
		if err := s.Store.MarkOpFailed(ctx, op.ID, err); err != nil {
			return res, err
		}
		res.Failed++
	}
	return res, nil
}

func (s *Syncer) pushUpdate(ctx context.Context, op store.Op) (conflict bool, err error) {
	if store.IsLocalGID(op.TaskGID) {
		return false, &asana.Error{Status: 404} // its create failed
	}
	server, err := s.API.Task(ctx, op.TaskGID)
	if err != nil {
		return false, err
	}
	current := store.TaskFieldValues(server)
	conflicts := map[string]any{}
	for k, local := range op.Payload {
		sv := current[k]
		if !same(sv, op.Base[k]) && !same(sv, local) {
			conflicts[k] = sv
		}
	}
	if len(conflicts) > 0 {
		return true, s.Store.MarkOpConflict(ctx, op.ID, conflicts)
	}
	t, err := s.API.UpdateTask(ctx, op.TaskGID, apiFields(op.Payload))
	if err != nil {
		return false, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return false, err
	}
	return false, s.Store.UpsertTasks(ctx, []asana.Task{*t})
}

func (s *Syncer) pushCreate(ctx context.Context, op store.Op) error {
	fields := apiFields(op.Payload)
	section, _ := fields["section"].(string)
	project, _ := fields["project"].(string)
	delete(fields, "section")
	delete(fields, "project")
	if project != "" {
		fields["projects"] = []string{project}
	}
	t, err := s.API.CreateTask(ctx, fields)
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceLocalTaskGID(ctx, op.TaskGID, t.GID); err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return err
	}
	if section != "" {
		if err := s.API.AddTaskToSection(ctx, section, t.GID); err != nil {
			return fmt.Errorf("作成したタスクをセクションに移せませんでした: %w", err)
		}
		if t, err = s.API.Task(ctx, t.GID); err != nil {
			return err
		}
	}
	return s.Store.UpsertTasks(ctx, []asana.Task{*t})
}

func (s *Syncer) pushComment(ctx context.Context, op store.Op) error {
	if store.IsLocalGID(op.TaskGID) {
		return &asana.Error{Status: 404} // its create failed
	}
	text, _ := op.Payload["text"].(string)
	localGID, _ := op.Payload["story_gid"].(string)
	st, err := s.API.AddComment(ctx, op.TaskGID, text)
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceLocalStory(ctx, localGID, op.TaskGID, *st); err != nil {
		return err
	}
	return s.Store.CompleteOp(ctx, op.ID)
}

func (s *Syncer) pushMove(ctx context.Context, op store.Op) error {
	if store.IsLocalGID(op.TaskGID) {
		return &asana.Error{Status: 404} // its create failed
	}
	section, _ := op.Payload["section"].(string)
	if err := s.API.AddTaskToSection(ctx, section, op.TaskGID); err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return err
	}
	t, err := s.API.Task(ctx, op.TaskGID)
	if err != nil {
		return nil // moved; the next sync refreshes the task
	}
	return s.Store.UpsertTasks(ctx, []asana.Task{*t})
}

func (s *Syncer) pushRenameSection(ctx context.Context, op store.Op) (conflict bool, err error) {
	server, err := s.API.Section(ctx, op.TaskGID)
	if err != nil {
		return false, err
	}
	name, _ := op.Payload["name"].(string)
	if !same(server.Name, op.Base["name"]) && !same(server.Name, name) {
		return true, s.Store.MarkOpConflict(ctx, op.ID, map[string]any{"name": server.Name})
	}
	updated, err := s.API.UpdateSection(ctx, op.TaskGID, name)
	if err != nil {
		return false, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return false, err
	}
	return false, s.Store.ApplySectionName(ctx, op.TaskGID, updated.Name)
}

// apiFields converts outbox values to Asana's: "" clears due_on / assignee.
func apiFields(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		if slices.Contains([]string{"due_on", "assignee"}, k) && v == "" {
			out[k] = nil
			continue
		}
		out[k] = v
	}
	return out
}

func same(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }
