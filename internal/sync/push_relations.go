package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
)

// Sending the relations of a task: tags, followers, projects and dependencies.

func init() {
	register(outbox.KindAddTag, opHandler{send: sends((*Syncer).pushTag), batch: batchTag, find: findAddedTag})
	register(outbox.KindRemoveTag, opHandler{send: sends((*Syncer).pushTag), batch: batchTag})
	register(outbox.KindAddFollower, opHandler{batch: batchFollower,
		send: relationSender(func(s *Syncer, ctx context.Context, task, _ string, op outbox.Op) error {
			return s.API.AddFollowers(ctx, task, []string{payloadString(op, "user")})
		})})
	register(outbox.KindRemoveFollower, opHandler{batch: batchFollower,
		send: relationSender(func(s *Syncer, ctx context.Context, task, _ string, op outbox.Op) error {
			return s.API.RemoveFollowers(ctx, task, []string{payloadString(op, "user")})
		})})
	register(outbox.KindAddProject, opHandler{batch: batchAddProject,
		send: relationSender(func(s *Syncer, ctx context.Context, task, section string, op outbox.Op) error {
			return s.API.AddProject(ctx, task, payloadString(op, "project"), section)
		})})
	register(outbox.KindRemoveProject, opHandler{batch: batchRemoveProject,
		send: relationSender(func(s *Syncer, ctx context.Context, task, _ string, op outbox.Op) error {
			return s.API.RemoveProject(ctx, task, payloadString(op, "project"))
		})})
	register(outbox.KindAddDependency, opHandler{send: sends((*Syncer).pushDependency), batch: batchDependency, goneReason: dependencyGone})
	register(outbox.KindRemoveDependency, opHandler{send: sends((*Syncer).pushDependency), batch: batchDependency, goneReason: dependencyGone})
}

// dependencyGone is why a dependency change is dropped when the task is there.
func dependencyGone(_ outbox.Op, cause error) string {
	return "依存先のタスクが Asana で見つからないか、アクセスできません (" + cause.Error() + ")"
}

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

// relationSender sends a follower or project entry: apply makes the request for the task
// (an Asana gid) and the section of the entry (an Asana gid, or "").
func relationSender(apply func(s *Syncer, ctx context.Context, task, section string, op outbox.Op) error) func(*Syncer, context.Context, outbox.Op) (outcome, error) {
	return sends(func(s *Syncer, ctx context.Context, op outbox.Op) error { return s.pushRelation(ctx, op, apply) })
}

func (s *Syncer) pushRelation(ctx context.Context, op outbox.Op, apply func(s *Syncer, ctx context.Context, task, section string, op outbox.Op) error) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if localgid.IsLocal(task) {
		return &model.Error{Status: 404} // its create failed
	}
	section, _ := op.Payload["section"].(string)
	if section != "" {
		if section, err = s.realSection(ctx, section); isNotFound(err) {
			section = "" // created offline and discarded: the project's first section
		} else if err != nil {
			return err
		}
	}
	if err := apply(s, ctx, task, section, op); err != nil {
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

// findAddedTag looks for the tag an add_tag of a tag created offline made (see reconcile).
func findAddedTag(s *Syncer, ctx context.Context, op outbox.Op, f finder) (outcome, match, error) {
	str := func(k string) string { return payloadString(op, k) }
	// A tag created offline is created before it is added. If a tag with that name
	// exists (whoever made it), use it; the add itself is harmless to repeat, so it is
	// sent either way.
	tag, err := s.Store.RealTagGID(ctx, str("tag"))
	if err != nil || !localgid.IsLocal(tag) {
		return sent, notFound, err
	}
	tags, err := s.API.Tags(ctx, str("workspace"))
	if err != nil {
		return sent, notFound, err
	}
	g, m := pick(tags, func(g model.Tag) bool { return g.Name == str("tag_name") })
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

// batchTag is the batch request for an add_tag or remove_tag of a tag Asana has.
func batchTag(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	str := func(k string) string { return payloadString(op, k) }
	task := e.task
	tag, err := s.Store.RealTagGID(ctx, str("tag"))
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
func batchFollower(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	str := func(k string) string { return payloadString(op, k) }
	task := e.task
	user := str("user")
	if !realGID(user) {
		return false, nil
	}
	if op.Kind == outbox.KindRemoveFollower {
		e.write = model.BatchRemoveFollower(task, user)
	} else {
		e.write = model.BatchAddFollower(task, user)
	}
	return true, nil
}

// batchAddProject is the batch request for an add_project.
func batchAddProject(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	str := func(k string) string { return payloadString(op, k) }
	task := e.task
	project, section := str("project"), str("section")
	if !realGID(project) || (section != "" && !realGID(section)) {
		return false, nil
	}
	e.write = model.BatchAddProject(task, project, section)
	return true, nil
}

// batchRemoveProject is the batch request for a remove_project.
func batchRemoveProject(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	str := func(k string) string { return payloadString(op, k) }
	task := e.task
	project := str("project")
	if !realGID(project) {
		return false, nil
	}
	e.write = model.BatchRemoveProject(task, project)
	return true, nil
}

// batchDependency is the batch request for an add_dependency or remove_dependency.
func batchDependency(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
	str := func(k string) string { return payloadString(op, k) }
	task := e.task
	dep, err := s.Store.RealGID(ctx, str("dependency"))
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
