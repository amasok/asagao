package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// The sections of My Tasks (#35).

// syncMySections fetches the sections of my My Tasks list in a workspace (with the poll:
// they change rarely). Asana answering 402 hides the feature; any other failure keeps
// what is cached (the sections tasks are in are still known from the tasks) and is tried
// again with the next poll. Only an error of the store is returned.
func (s *Syncer) syncMySections(ctx context.Context, workspaceGID string) error {
	key := readmodel.KeyPlanUnavailable + readmodel.PlanMySection
	if off, err := s.Store.SyncState(ctx, key); err != nil || off != "" {
		return err
	}
	unavailable := func() error {
		return s.Store.SetSyncState(ctx, key, s.clock().UTC().Format("2006-01-02T15:04:05Z07:00"))
	}
	list, err := s.API.UserTaskList(ctx, "me", workspaceGID)
	switch {
	case model.IsPaymentRequired(err):
		return unavailable()
	case err != nil || list.GID == "":
		return nil
	}
	secs, err := s.API.Sections(ctx, list.GID)
	switch {
	case model.IsPaymentRequired(err):
		return unavailable()
	case err != nil:
		return nil
	}
	if err := s.Store.SetSyncState(ctx, readmodel.KeyUserTaskList+workspaceGID, list.GID); err != nil {
		return err
	}
	return s.Store.ReplaceMyTaskSections(ctx, workspaceGID, secs)
}

// pushMoveMySection moves a task to another section of My Tasks (harmless to repeat).
func (s *Syncer) pushMoveMySection(ctx context.Context, op outbox.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if localgid.IsLocal(task) {
		return &model.Error{Status: 404} // its create failed
	}
	section, _ := op.Payload["section"].(string)
	if err := s.API.AddTaskToSection(ctx, section, task); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}
