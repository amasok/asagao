package sync

import (
	"context"
	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Sending the relations of a task: tags, followers, projects and dependencies.

func (s *Syncer) pushTag(ctx context.Context, op outbox.Op) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	tag, _ := op.Payload["tag"].(string)
	tag, err := s.Store.RealTagGID(ctx, tag)
	if err != nil {
		return err
	}
	if op.Kind == outbox.KindRemoveTag {
		if err := s.API.RemoveTag(ctx, op.TaskGID, tag); err != nil {
			return err
		}
		return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
	}
	if localgid.IsLocal(tag) { // created offline: create it in Asana first
		name, _ := op.Payload["tag_name"].(string)
		workspace, _ := op.Payload["workspace"].(string)
		created, err := s.API.CreateTag(ctx, workspace, name)
		if err != nil {
			return err
		}
		if err := s.Store.ReplaceLocalTagGID(ctx, tag, created.GID); err != nil {
			return sentNotSaved(err)
		}
		tag = created.GID
	}
	if err := s.API.AddTag(ctx, op.TaskGID, tag); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

func (s *Syncer) pushRelation(ctx context.Context, op outbox.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if localgid.IsLocal(task) {
		return &model.Error{Status: 404} // its create failed
	}
	user, _ := op.Payload["user"].(string)
	project, _ := op.Payload["project"].(string)
	section, _ := op.Payload["section"].(string)
	if section != "" {
		if section, err = s.realSection(ctx, section); isNotFound(err) {
			section = "" // created offline and discarded: the project's first section
		} else if err != nil {
			return err
		}
	}
	switch op.Kind {
	case outbox.KindAddFollower:
		err = s.API.AddFollowers(ctx, task, []string{user})
	case outbox.KindRemoveFollower:
		err = s.API.RemoveFollowers(ctx, task, []string{user})
	case outbox.KindAddProject:
		err = s.API.AddProject(ctx, task, project, section)
	case outbox.KindRemoveProject:
		err = s.API.RemoveProject(ctx, task, project)
	}
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

// pushDependency adds or removes a dependency. Both are harmless to repeat: Asana adds only
// tasks that are not dependencies yet.
func (s *Syncer) pushDependency(ctx context.Context, op outbox.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	dep, _ := op.Payload["dependency"].(string)
	if dep, err = s.Store.RealGID(ctx, dep); err != nil {
		return err
	}
	if localgid.IsLocal(task) || localgid.IsLocal(dep) {
		return &model.Error{Status: 404} // its create failed
	}
	if op.Kind == outbox.KindAddDependency {
		err = s.API.AddDependencies(ctx, task, []string{dep})
	} else {
		err = s.API.RemoveDependencies(ctx, task, []string{dep})
	}
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}
