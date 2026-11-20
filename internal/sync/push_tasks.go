package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	domaintask "github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending changes to tasks themselves: updates, creates, duplicates, moves to a section,
// deletes and parent changes.

func init() {
	register(outbox.KindUpdateTask, opHandler{send: (*Syncer).pushUpdate, batch: batchUpdateTask, startOn: true})
	register(outbox.KindCreateTask, opHandler{send: (*Syncer).pushCreate, find: findCreatedTask, startOn: true,
		fieldValues: func(op outbox.Op) map[string]any {
			cfs, _ := op.Payload["custom_fields"].(map[string]any)
			return cfs
		}})
	// A duplicate is copied by a background job, so its result takes longer to show.
	register(outbox.KindDuplicateTask, opHandler{send: sends((*Syncer).pushDuplicate), find: findDuplicate, settle: 5 * time.Minute})
	register(outbox.KindMoveTask, opHandler{send: sends((*Syncer).pushMove), batch: batchMoveTask,
		goneReason: func(op outbox.Op, cause error) string {
			if op.Payload["insert_before"] != nil || op.Payload["insert_after"] != nil {
				return "セクション (または位置の目印のタスク) が Asana で見つからないか、アクセスできません (" + cause.Error() + ")"
			}
			return ""
		}})
	register(outbox.KindDeleteTask, opHandler{send: sends((*Syncer).pushDelete)})
	register(outbox.KindSetParent, opHandler{send: sends((*Syncer).pushSetParent),
		goneReason: func(_ outbox.Op, cause error) string {
			return "親タスク (または位置の目印のタスク) が Asana で見つからないか、アクセスできません (" + cause.Error() + ")"
		}})
}

func (s *Syncer) pushUpdate(ctx context.Context, op outbox.Op) (outcome, error) {
	if localgid.IsLocal(op.TaskGID) {
		return sent, &model.Error{Status: 404} // its create failed
	}
	server, err := s.API.Task(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	if conflicts := updateConflicts(op, server); conflicts != nil {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, conflicts)
	}
	if reason := startOnBlocked(op.Payload, server); reason != "" {
		return sent, &startOnWithdrawn{reason: reason}
	}
	fields := withDue(taskUpdateFields(op), server)
	if len(fields) == 0 { // only cleared a start date Asana does not have
		if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
			return sent, sentNotSaved(err)
		}
		return sent, nil
	}
	t, err := s.API.UpdateTask(ctx, op.TaskGID, fields)
	if err != nil {
		return sent, s.customTypeRefused(ctx, op, err)
	}
	if err := customTypeApplied(op, t); err != nil {
		s.refresh(ctx, *t) // the rest was applied: show Asana's task
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	s.refresh(ctx, *t)
	return sent, nil
}

// serverDueDay is the day a task is due in Asana: due_on, or the local day of due_at.
func serverDueDay(t *model.Task) string {
	if t.DueOn != "" {
		return t.DueOn
	}
	if t.DueAt != nil && !t.DueAt.IsZero() {
		return t.DueAt.Local().Format(time.DateOnly)
	}
	return ""
}

// startOnBlocked says why an update's start date cannot be sent when it keeps the due
// date and the due date in Asana no longer allows it (removed, or moved before the start
// date since the edit): Asana would refuse it with a 400. "" when it can be sent.
func startOnBlocked(payload map[string]any, server *model.Task) string {
	start, _ := payload["start_on"].(string)
	if _, due := payload["due_on"]; start == "" || due {
		return ""
	}
	switch due := serverDueDay(server); {
	case due == "":
		return fmt.Sprintf("Asana でこのタスクの期日が外されたため、開始日 (%s) を付けられませんでした", start)
	case start > due:
		return fmt.Sprintf("Asana でこのタスクの期日が %s になり、開始日 (%s) が期日より後になるため付けられませんでした", due, start)
	}
	return ""
}

// withDue adds the task's due date in Asana to an update that sets or clears the start
// date without changing the due date: Asana requires one of due_on / due_at in such a
// request. The server's value is sent as it is, so the due date does not change.
func withDue(fields map[string]any, server *model.Task) map[string]any {
	if _, ok := fields["start_on"]; !ok {
		return fields
	}
	if _, ok := fields["due_on"]; ok {
		return fields
	}
	if _, ok := fields["due_at"]; ok {
		return fields
	}
	switch {
	case server.DueAt != nil && !server.DueAt.IsZero():
		fields["due_at"] = server.DueAt.UTC().Format(time.RFC3339)
	case server.DueOn != "":
		fields["due_on"] = server.DueOn
	case fields["start_on"] == nil && server.StartOn == "":
		delete(fields, "start_on") // clearing a start date Asana does not have: nothing to send
	default:
		fields["due_on"] = nil
	}
	return fields
}

// updateConflicts compares an update with the task in Asana: the server's values of the
// fields that were changed there too (nil if none).
func updateConflicts(op outbox.Op, server *model.Task) map[string]any {
	current := model.TaskFieldValues(server)
	conflicts := map[string]any{}
	for k, local := range op.Payload {
		if k == "html_notes" {
			continue // the server normalizes HTML; "notes" (plain) is compared instead
		}
		sv, known := current[k]
		if !known {
			continue // not fetched (Task.Partial): nothing to compare with
		}
		if !same(sv, op.Base[k]) && !same(sv, local) {
			conflicts[k] = sv
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return conflicts
}

// refresh stores a task as Asana returned it after a change. The change is already
// recorded, so a failure here only leaves the cache stale until the next sync.
func (s *Syncer) refresh(ctx context.Context, t model.Task) {
	_ = s.Store.UpsertTasks(ctx, []model.Task{t})
}

func (s *Syncer) pushCreate(ctx context.Context, op outbox.Op) (outcome, error) {
	fields := apiFields(op.Payload)
	section, _ := fields["section"].(string)
	project, _ := fields["project"].(string)
	parent, _ := fields["parent"].(string)
	delete(fields, "section")
	delete(fields, "project")
	delete(fields, "parent")
	if fields["start_on"] == nil {
		delete(fields, "start_on") // cleared before it was sent: nothing to unset, and Asana wants a due date with it
	}
	if project != "" {
		fields["projects"] = []string{project}
	}
	var t *model.Task
	var err error
	if parent != "" {
		// The parent may have been created offline too; it has been sent before us.
		if parent, err = s.Store.RealGID(ctx, parent); err != nil {
			return sent, err
		}
		if localgid.IsLocal(parent) {
			return sent, &model.Error{Status: 404} // the parent's create failed
		}
		delete(fields, "workspace")
		t, err = s.API.CreateSubtask(ctx, parent, fields)
	} else {
		t, err = s.API.CreateTask(ctx, fields)
	}
	if err != nil {
		return sent, err
	}
	// Moving the task into its section is a separate request: the entry becomes a move,
	// sent right after (and retried later if it fails), so the create is never repeated.
	var move *outbox.MoveTarget
	if section != "" {
		move = &outbox.MoveTarget{Project: project, Section: section, From: sectionIn(t, project)}
	}
	if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, move); err != nil {
		return sent, sentNotSaved(err)
	}
	s.refresh(ctx, *t)
	if move != nil {
		return followUp, nil
	}
	return sent, nil
}

// sectionIn returns the section t is in within project ("" if unknown).
func sectionIn(t *model.Task, project string) string {
	for _, m := range t.Memberships {
		if m.Project != nil && m.Project.GID == project && m.Section != nil {
			return m.Section.GID
		}
	}
	return ""
}

func (s *Syncer) pushMove(ctx context.Context, op outbox.Op) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	section, _ := op.Payload["section"].(string)
	section, err := s.realSection(ctx, section)
	if err != nil {
		return err
	}
	pos, err := s.position(ctx, op)
	if err != nil {
		return err
	}
	if err := s.API.InsertTaskInSection(ctx, section, op.TaskGID, pos); err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	if t, err := s.API.Task(ctx, op.TaskGID); err == nil { // else the next sync refreshes it
		s.refresh(ctx, *t)
	}
	project, _ := op.Payload["project"].(string)
	s.refreshSectionOrderAfterMove(ctx, project, section)
	return nil
}

// refreshSectionOrderAfterMove fetches Asana's order for section right after pushing a
// move into it (#69: Asana emits no Events on intra-section reorders, so an external
// reorder before our push would otherwise leave the cache out of order). Transient
// failures queue section in PendingSectionOrders so the next sync retries it.
func (s *Syncer) refreshSectionOrderAfterMove(ctx context.Context, project, section string) {
	if section == "" || localgid.IsLocal(section) {
		return
	}
	completedSince := s.newMode(s.serverNow()).window
	gids, err := s.API.SectionTaskOrder(ctx, section, completedSince)
	if err == nil {
		if gids != nil {
			_ = s.Store.SetSectionTaskOrder(ctx, section, gids)
		}
		return
	}
	if isNotFound(err) || isForbidden(err) || project == "" || localgid.IsLocal(project) {
		return
	}
	pending, perr := s.Store.PendingSectionOrders(ctx, project)
	if perr != nil {
		return
	}
	_ = setPendingOrders(pending, addNew(strings.Fields(pending), section), func(v string) error {
		return s.Store.SetPendingSectionOrders(ctx, project, v)
	})
}

// position is where op puts its task (insert_before / insert_after), with the real gid of
// a task created offline. One whose create has not gone out (it failed) is left out:
// the task still goes to its section or parent.
func (s *Syncer) position(ctx context.Context, op outbox.Op) (model.Position, error) {
	var pos model.Position
	for _, p := range []struct {
		key string
		to  *string
	}{{"insert_before", &pos.Before}, {"insert_after", &pos.After}} {
		gid, _ := op.Payload[p.key].(string)
		if gid == "" {
			continue
		}
		real, err := s.Store.RealGID(ctx, gid)
		if err != nil {
			return pos, err
		}
		if !localgid.IsLocal(real) {
			*p.to = real
		}
	}
	return pos, nil
}

// pushDelete deletes a task in Asana. A 404 means it is gone already (deleted by this
// entry sent before, or by someone else): done either way. Push has checked that the
// account is the one the store belongs to (checkPushAccount), so the 404 is not another
// account's view.
func (s *Syncer) pushDelete(ctx context.Context, op outbox.Op) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	if err := s.API.DeleteTask(ctx, op.TaskGID); err != nil && !isNotFound(err) {
		return err
	}
	return sentNotSaved(s.Store.CompleteDelete(ctx, op.ID, op.TaskGID))
}

// pushSetParent sends a parent change. Repeating it is harmless.
func (s *Syncer) pushSetParent(ctx context.Context, op outbox.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	parent, _ := op.Payload["parent"].(string)
	if parent, err = s.Store.RealGID(ctx, parent); err != nil {
		return err
	}
	if localgid.IsLocal(task) || localgid.IsLocal(parent) {
		return &model.Error{Status: 404} // its create failed
	}
	pos, err := s.position(ctx, op)
	if err != nil {
		return err
	}
	if err := s.API.SetParent(ctx, task, parent, pos); err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	if t, err := s.API.Task(ctx, task); err == nil { // else the next sync refreshes it
		s.refresh(ctx, *t)
	}
	return nil
}

func (s *Syncer) pushDuplicate(ctx context.Context, op outbox.Op) error {
	source, _ := op.Payload["source"].(string)
	name, _ := op.Payload["name"].(string)
	created, err := s.API.DuplicateTask(ctx, source, name)
	if err != nil {
		return err
	}
	if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, created.GID, nil); err != nil {
		return sentNotSaved(err)
	}
	if t, err := s.API.Task(ctx, created.GID); err == nil { // subtasks etc. follow on the next sync
		s.refresh(ctx, *t)
	}
	return nil
}

// findCreatedTask looks for the task a create_task made (see reconcile).
func findCreatedTask(s *Syncer, ctx context.Context, op outbox.Op, f finder) (outcome, match, error) {
	str := func(k string) string { return payloadString(op, k) }
	tasks, ok, err := s.tasksCreatedUnder(ctx, str("parent"), str("project"), str("assignee"), str("workspace"), f.since)
	if err != nil || !ok {
		return sent, ambiguousUnless(ok), err
	}
	used, err := s.taken(ctx, readmodel.ResultTask)
	if err != nil {
		return sent, notFound, err
	}
	t, m := pick(tasks, func(t model.Task) bool {
		return !used[t.GID] && t.Name == str("name") && f.recent(t.CreatedAt) && f.byMe(t.CreatedBy) && sameTaskFields(t, op.Payload)
	})
	if m != matched {
		return sent, m, nil
	}
	var move *outbox.MoveTarget
	if section := str("section"); section != "" {
		move = &outbox.MoveTarget{Project: str("project"), Section: section, From: sectionIn(&t, str("project"))}
	}
	if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, move); err != nil {
		return sent, notFound, err
	}
	s.refresh(ctx, t)
	if move != nil {
		return followUp, matched, nil
	}
	return sent, matched, nil
}

// findDuplicate looks for the copy a duplicate_task made (see reconcile).
func findDuplicate(s *Syncer, ctx context.Context, op outbox.Op, f finder) (outcome, match, error) {
	str := func(k string) string { return payloadString(op, k) }
	// The copy goes where the source is (same parent, or the source's first project).
	src, err := s.API.Task(ctx, str("source"))
	if err != nil {
		return sent, notFound, err
	}
	parent, project, assignee, ws := "", "", "", ""
	if src.Parent != nil {
		parent = src.Parent.GID
	}
	if len(src.Memberships) > 0 && src.Memberships[0].Project != nil {
		project = src.Memberships[0].Project.GID
	}
	if src.Assignee != nil {
		assignee = src.Assignee.GID
	}
	if src.Workspace != nil {
		ws = src.Workspace.GID
	}
	tasks, ok, err := s.tasksCreatedUnder(ctx, parent, project, assignee, ws, f.since)
	if err != nil || !ok {
		return sent, ambiguousUnless(ok), err
	}
	used, err := s.taken(ctx, readmodel.ResultTask)
	if err != nil {
		return sent, notFound, err
	}
	t, m := pick(tasks, func(t model.Task) bool {
		return !used[t.GID] && t.Name == str("name") && t.GID != src.GID && f.recent(t.CreatedAt)
	})
	if m != matched {
		return sent, m, nil
	}
	if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, nil); err != nil {
		return sent, notFound, err
	}
	s.refresh(ctx, t)
	return sent, matched, nil
}

// batchUpdateTask is the batch request for an update_task: its fields, checked against the task in Asana first.
func batchUpdateTask(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	task := e.task
	if domaintask.TouchesCustomType(op.Payload) {
		return false, nil // sent alone: a 402 there records that the plan lacks custom types
	}
	e.write = model.BatchUpdateTask(task, taskUpdateFields(op))
	e.check = func(op outbox.Op, server *model.Task) (map[string]any, error) {
		return updateConflicts(op, server), nil
	}
	return true, nil
}

// batchMoveTask is the batch request for a move_task to a section without a place in it.
func batchMoveTask(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	str := func(k string) string { return payloadString(op, k) }
	task := e.task
	section := str("section")
	if !realGID(section) {
		return false, nil
	}
	if str("insert_before") != "" || str("insert_after") != "" {
		// A place is next to another task, which an action of the same batch may move:
		// the batch keeps the order of one task's changes only. Sent alone, in order.
		return false, nil
	}
	e.write = model.BatchMoveTaskToSection(section, task)
	e.project, e.section = str("project"), section
	e.refresh = true
	return true, nil
}
