package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
)

// The sections of My Tasks (#35).

// syncMySections fetches the sections of my My Tasks list in a workspace (with the poll:
// they change rarely). Asana answering 402 hides the feature in that workspace for
// plan.RecheckAfter (it is then asked again); an answer forgets an earlier 402.
// Any other failure keeps what is cached (the sections tasks are in are still known from
// the tasks) and is tried again with the next poll. Only an error of the store is returned.
func (s *Syncer) syncMySections(ctx context.Context, workspaceGID string) error {
	const feature = plan.MySection
	if hidden, err := s.planHidden(ctx, feature, workspaceGID); err != nil || hidden {
		return err
	}
	list, err := s.API.UserTaskList(ctx, "me", workspaceGID)
	if refused, rerr := s.recordPlanRefusal(ctx, feature, workspaceGID, err); refused || rerr != nil {
		return rerr
	}
	if err != nil || list.GID == "" {
		return nil
	}
	secs, err := s.API.Sections(ctx, list.GID)
	if refused, rerr := s.recordPlanRefusal(ctx, feature, workspaceGID, err); refused || rerr != nil {
		return rerr
	}
	if err != nil {
		return nil
	}
	if err := s.clearPlanRefusal(ctx, feature, workspaceGID); err != nil {
		return err
	}
	if err := s.Store.SetUserTaskListGID(ctx, workspaceGID, list.GID); err != nil {
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
