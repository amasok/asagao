package sync

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
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
		case store.OpAddTag, store.OpRemoveTag:
			err = s.pushTag(ctx, *op)
		case store.OpAddFollower, store.OpRemoveFollower, store.OpAddProject, store.OpRemoveProject:
			err = s.pushRelation(ctx, *op)
		case store.OpDuplicateTask:
			err = s.pushDuplicate(ctx, *op)
		case store.OpUploadAttachment:
			err = s.pushUpload(ctx, *op)
		case store.OpCreateStatus:
			err = s.pushStatus(ctx, *op)
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
		if k == "html_notes" {
			continue // the server normalizes HTML; "notes" (plain) is compared instead
		}
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
	parent, _ := fields["parent"].(string)
	delete(fields, "section")
	delete(fields, "project")
	delete(fields, "parent")
	if project != "" {
		fields["projects"] = []string{project}
	}
	var t *asana.Task
	var err error
	if parent != "" {
		// The parent may have been created offline too; it has been sent before us.
		if parent, err = s.Store.RealGID(ctx, parent); err != nil {
			return err
		}
		if store.IsLocalGID(parent) {
			return &asana.Error{Status: 404} // the parent's create failed
		}
		delete(fields, "workspace")
		t, err = s.API.CreateSubtask(ctx, parent, fields)
	} else {
		t, err = s.API.CreateTask(ctx, fields)
	}
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
	htmlText, _ := op.Payload["html_text"].(string)
	localGID, _ := op.Payload["story_gid"].(string)
	st, err := s.API.AddComment(ctx, op.TaskGID, text, htmlText)
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

func (s *Syncer) pushTag(ctx context.Context, op store.Op) error {
	if store.IsLocalGID(op.TaskGID) {
		return &asana.Error{Status: 404} // its create failed
	}
	tag, _ := op.Payload["tag"].(string)
	tag, err := s.Store.RealTagGID(ctx, tag)
	if err != nil {
		return err
	}
	if op.Kind == store.OpRemoveTag {
		if err := s.API.RemoveTag(ctx, op.TaskGID, tag); err != nil {
			return err
		}
		return s.Store.CompleteOp(ctx, op.ID)
	}
	if store.IsLocalGID(tag) { // created offline: create it in Asana first
		name, _ := op.Payload["tag_name"].(string)
		workspace, _ := op.Payload["workspace"].(string)
		created, err := s.API.CreateTag(ctx, workspace, name)
		if err != nil {
			return err
		}
		if err := s.Store.ReplaceLocalTagGID(ctx, tag, created.GID); err != nil {
			return err
		}
		tag = created.GID
	}
	if err := s.API.AddTag(ctx, op.TaskGID, tag); err != nil {
		return err
	}
	return s.Store.CompleteOp(ctx, op.ID)
}

func (s *Syncer) pushRelation(ctx context.Context, op store.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if store.IsLocalGID(task) {
		return &asana.Error{Status: 404} // its create failed
	}
	user, _ := op.Payload["user"].(string)
	project, _ := op.Payload["project"].(string)
	section, _ := op.Payload["section"].(string)
	switch op.Kind {
	case store.OpAddFollower:
		err = s.API.AddFollowers(ctx, task, []string{user})
	case store.OpRemoveFollower:
		err = s.API.RemoveFollowers(ctx, task, []string{user})
	case store.OpAddProject:
		err = s.API.AddProject(ctx, task, project, section)
	case store.OpRemoveProject:
		err = s.API.RemoveProject(ctx, task, project)
	}
	if err != nil {
		return err
	}
	return s.Store.CompleteOp(ctx, op.ID)
}

func (s *Syncer) pushDuplicate(ctx context.Context, op store.Op) error {
	source, _ := op.Payload["source"].(string)
	name, _ := op.Payload["name"].(string)
	created, err := s.API.DuplicateTask(ctx, source, name)
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceLocalTaskGID(ctx, op.TaskGID, created.GID); err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return err
	}
	if t, err := s.API.Task(ctx, created.GID); err == nil { // subtasks etc. follow on the next sync
		return s.Store.UpsertTasks(ctx, []asana.Task{*t})
	}
	return nil
}

func (s *Syncer) pushUpload(ctx context.Context, op store.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if store.IsLocalGID(task) {
		return &asana.Error{Status: 404} // its create failed
	}
	local, _ := op.Payload["attachment"].(string)
	name, _ := op.Payload["name"].(string)
	path, _ := op.Payload["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		return &asana.Error{Status: 400} // the copied file is gone: cannot be retried
	}
	att, err := s.API.UploadAttachment(ctx, task, name, data)
	if err != nil {
		return err
	}
	if err := s.Store.ReplaceLocalAttachment(ctx, local, att.GID, att.ViewURL); err != nil {
		return err
	}
	return s.Store.CompleteOp(ctx, op.ID)
}

// apiFields converts outbox values to Asana's: "" clears due_on / assignee.
func apiFields(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	if _, rich := payload["html_notes"]; rich {
		payload = maps.Clone(payload)
		delete(payload, "notes") // Asana derives the plain notes from html_notes
	}
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
