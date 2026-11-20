package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Sending the relations of a task: tags, followers, projects and dependencies.

func init() {
	registerOf(outbox.KindAddTag, handlerOf[outbox.AddTag]{send: sends((*Pusher).pushAddTag), find: findAddedTag,
		batch: func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.AddTag, e *batchEntry) (bool, error) {
			return batchTag(s, ctx, op, p.Tag, e)
		}})
	registerOf(outbox.KindRemoveTag, handlerOf[outbox.RemoveTag]{send: sends((*Pusher).pushRemoveTag),
		batch: func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.RemoveTag, e *batchEntry) (bool, error) {
			return batchTag(s, ctx, op, p.Tag, e)
		}})
	registerOf(outbox.KindAddFollower, handlerOf[outbox.Follower]{batch: batchFollower,
		send: relationSender(func(s *Pusher, ctx context.Context, task, _ string, p outbox.Follower) error {
			return s.API.AddFollowers(ctx, task, []string{p.User})
		}, nil)})
	registerOf(outbox.KindRemoveFollower, handlerOf[outbox.Follower]{batch: batchFollower,
		send: relationSender(func(s *Pusher, ctx context.Context, task, _ string, p outbox.Follower) error {
			return s.API.RemoveFollowers(ctx, task, []string{p.User})
		}, nil)})
	section := func(p outbox.ProjectMembership) string { return outbox.Str(p.Section) }
	registerOf(outbox.KindAddProject, handlerOf[outbox.ProjectMembership]{batch: batchAddProject,
		send: relationSender(func(s *Pusher, ctx context.Context, task, section string, p outbox.ProjectMembership) error {
			return s.API.AddProject(ctx, task, p.Project, section)
		}, section)})
	registerOf(outbox.KindRemoveProject, handlerOf[outbox.ProjectMembership]{batch: batchRemoveProject,
		send: relationSender(func(s *Pusher, ctx context.Context, task, _ string, p outbox.ProjectMembership) error {
			return s.API.RemoveProject(ctx, task, p.Project)
		}, section)})
	registerOf(outbox.KindAddDependency, handlerOf[outbox.Dependency]{send: sends((*Pusher).pushDependency), batch: batchDependency, goneReason: dependencyGone})
	registerOf(outbox.KindRemoveDependency, handlerOf[outbox.Dependency]{send: sends((*Pusher).pushDependency), batch: batchDependency, goneReason: dependencyGone})
}

// dependencyGone is why a dependency change is dropped when the task is there.
func dependencyGone(_ outbox.Op, _ outbox.Dependency, cause error) string {
	return "依存先のタスクが Asana で見つからないか、アクセスできません (" + cause.Error() + ")"
}

func (s *Pusher) pushRemoveTag(ctx context.Context, op outbox.Op, p outbox.RemoveTag) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	tag, err := s.Store.RealTagGID(ctx, p.Tag)
	if err != nil {
		return err
	}
	if err := s.API.RemoveTag(ctx, op.TaskGID, tag); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

func (s *Pusher) pushAddTag(ctx context.Context, op outbox.Op, p outbox.AddTag) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its create failed
	}
	tag, err := s.Store.RealTagGID(ctx, p.Tag)
	if err != nil {
		return err
	}
	if localgid.IsLocal(tag) { // created offline: create it in Asana first
		created, err := s.API.CreateTag(ctx, p.Workspace, p.TagName)
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

// relationSender sends a follower or project entry: apply makes the request for the task
// (an Asana gid) and the section of the entry (an Asana gid, or ""), which sectionOf
// reads from the payload (nil: none).
func relationSender[P any](apply func(s *Pusher, ctx context.Context, task, section string, p P) error, sectionOf func(P) string) func(*Pusher, context.Context, outbox.Op, P) (outcome, error) {
	return sends(func(s *Pusher, ctx context.Context, op outbox.Op, p P) error {
		var section string
		if sectionOf != nil {
			section = sectionOf(p)
		}
		return s.pushRelation(ctx, op, section, func(task, section string) error { return apply(s, ctx, task, section, p) })
	})
}

func (s *Pusher) pushRelation(ctx context.Context, op outbox.Op, section string, apply func(task, section string) error) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if localgid.IsLocal(task) {
		return &model.Error{Status: 404} // its create failed
	}
	if section != "" {
		if section, err = s.realSection(ctx, section); isNotFound(err) {
			section = "" // created offline and discarded: the project's first section
		} else if err != nil {
			return err
		}
	}
	if err := apply(task, section); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

// pushDependency adds or removes a dependency. Both are harmless to repeat: Asana adds only
// tasks that are not dependencies yet.
func (s *Pusher) pushDependency(ctx context.Context, op outbox.Op, p outbox.Dependency) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	dep, err := s.Store.RealGID(ctx, p.Dependency)
	if err != nil {
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

// findAddedTag looks for the tag an add_tag of a tag created offline made (see reconcile).
func findAddedTag(s *Pusher, ctx context.Context, _ outbox.Op, p outbox.AddTag, _ finder) (outcome, match, error) {
	// A tag created offline is created before it is added. If a tag with that name
	// exists (whoever made it), use it; the add itself is harmless to repeat, so it is
	// sent either way.
	tag, err := s.Store.RealTagGID(ctx, p.Tag)
	if err != nil || !localgid.IsLocal(tag) {
		return sent, notFound, err
	}
	tags, err := s.API.Tags(ctx, p.Workspace)
	if err != nil {
		return sent, notFound, err
	}
	g, m := pick(tags, func(g model.Tag) bool { return g.Name == p.TagName })
	switch m {
	case ambiguous:
		return sent, ambiguous, nil
	case matched:
		if err := s.Store.ReplaceLocalTagGID(ctx, tag, g.GID); err != nil {
			return sent, notFound, err
		}
	}
	return sent, notFound, nil
}

// batchTag is the batch request for an add_tag or remove_tag of tag, if Asana has it.
func batchTag(s *Pusher, ctx context.Context, op outbox.Op, tag string, e *batchEntry) (bool, error) {
	task := e.task
	tag, err := s.Store.RealTagGID(ctx, tag)
	if err != nil {
		return false, err
	}
	if !realGID(tag) {
		return false, nil // a tag created offline is created in Asana first
	}
	if op.Kind == outbox.KindRemoveTag {
		e.write = model.BatchRemoveTag(task, tag)
	} else {
		e.write = model.BatchAddTag(task, tag)
	}
	return true, nil
}

// batchFollower is the batch request for an add_follower or remove_follower.
func batchFollower(s *Pusher, ctx context.Context, op outbox.Op, p outbox.Follower, e *batchEntry) (bool, error) {
	task := e.task
	if !realGID(p.User) {
		return false, nil
	}
	if op.Kind == outbox.KindRemoveFollower {
		e.write = model.BatchRemoveFollower(task, p.User)
	} else {
		e.write = model.BatchAddFollower(task, p.User)
	}
	return true, nil
}

// batchAddProject is the batch request for an add_project.
func batchAddProject(s *Pusher, ctx context.Context, op outbox.Op, p outbox.ProjectMembership, e *batchEntry) (bool, error) {
	task := e.task
	project, section := p.Project, outbox.Str(p.Section)
	if !realGID(project) || (section != "" && !realGID(section)) {
		return false, nil
	}
	e.write = model.BatchAddProject(task, project, section)
	return true, nil
}

// batchRemoveProject is the batch request for a remove_project.
func batchRemoveProject(s *Pusher, ctx context.Context, op outbox.Op, p outbox.ProjectMembership, e *batchEntry) (bool, error) {
	task := e.task
	if !realGID(p.Project) {
		return false, nil
	}
	e.write = model.BatchRemoveProject(task, p.Project)
	return true, nil
}

// batchDependency is the batch request for an add_dependency or remove_dependency.
func batchDependency(s *Pusher, ctx context.Context, op outbox.Op, p outbox.Dependency, e *batchEntry) (bool, error) {
	task := e.task
	dep, err := s.Store.RealGID(ctx, p.Dependency)
	if err != nil {
		return false, err
	}
	if !realGID(dep) {
		return false, nil
	}
	if op.Kind == outbox.KindRemoveDependency {
		e.write = model.BatchRemoveDependency(task, dep)
	} else {
		e.write = model.BatchAddDependency(task, dep)
	}
	return true, nil
}
