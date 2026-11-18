package app

import (
	"context"
	"errors"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// TaskOpsStore is the part of the local cache that deleting a task and changing its parent
// need. *store.Store satisfies it.
type TaskOpsStore interface {
	PlanDeleteTask(ctx context.Context, gid string) (readmodel.DeletePlan, error)
	DeleteTaskLocal(ctx context.Context, gid string, now time.Time) (readmodel.DeletePlan, error)
	SetParentLocal(ctx context.Context, gid, parent string, pos model.Position, now time.Time) error
}

// TaskOps is the use cases that reorganize tasks (#32): deleting a task, and making it a
// subtask of another or a top-level task. Each is one local write, queued in the outbox.
type TaskOps struct {
	Store TaskOpsStore
	Clock Clock
}

// PlanDelete tells what Delete would do, writing nothing (to confirm first, or a dry run).
func (o *TaskOps) PlanDelete(ctx context.Context, gid string) (readmodel.DeletePlan, error) {
	return o.Store.PlanDeleteTask(ctx, gid)
}

// Delete deletes task gid with its subtasks. Until the delete is sent the task is hidden,
// and discarding the outbox entry brings it back; a task never sent is removed at once.
func (o *TaskOps) Delete(ctx context.Context, gid string) (readmodel.DeletePlan, error) {
	return o.Store.DeleteTaskLocal(ctx, gid, o.Clock())
}

// SetParent makes task gid a subtask of parent, or a top-level task (parent ""). pos puts
// it before or after another subtask of parent (zero: where Asana puts it).
func (o *TaskOps) SetParent(ctx context.Context, gid, parent string, pos model.Position) error {
	return o.Store.SetParentLocal(ctx, gid, parent, pos, o.Clock())
}

// errNoTarget is MoveAt's error when neither a section nor a place is given.
var errNoTarget = errors.New("移動先のセクションか、位置の目印 (前・後) を指定してください")

// MoveAt moves task taskGID as Move does, to pos in the section: just before or after
// another task of it. With no section, the task goes to the section of that other task.
func (s *Sections) MoveAt(ctx context.Context, taskGID, project, section string, pos model.Position) error {
	if pos.IsZero() {
		if section == "" {
			return errNoTarget
		}
		return s.Move(ctx, taskGID, project, section)
	}
	anchor := pos.Before
	if anchor == "" {
		anchor = pos.After
	}
	var projectGID, sectionGID string
	var err error
	if section == "" {
		projectGID, sectionGID, err = s.Store.ResolveMoveNear(ctx, taskGID, project, anchor)
	} else {
		projectGID, sectionGID, err = s.ResolveMove(ctx, taskGID, project, section)
	}
	if err != nil {
		return err
	}
	return s.Store.MoveTaskAtLocal(ctx, taskGID, projectGID, sectionGID, pos, s.Clock())
}
