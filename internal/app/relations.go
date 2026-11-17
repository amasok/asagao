package app

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// RelationStore is the part of the local cache that the use cases on a task's relations
// (tags, followers, projects, dependencies, custom fields) need. *store.Store satisfies it.
type RelationStore interface {
	ResolveTagEdit(ctx context.Context, add, remove []string) (task.TagEdit, error)
	ApplyTagEdit(ctx context.Context, taskGID string, e task.TagEdit, now time.Time) error
	AddTagLocal(ctx context.Context, taskGID, tagGID, tagName string, now time.Time) (string, error)
	RemoveTagLocal(ctx context.Context, taskGID, tagGID string, now time.Time) error
	ResolveUsers(ctx context.Context, args []string) ([]string, error)
	SetFollowerLocal(ctx context.Context, taskGID, userGID string, follow bool, now time.Time) error
	ResolveProjects(ctx context.Context, args []string) ([]string, error)
	SetProjectLocal(ctx context.Context, taskGID, projectGID, sectionGID string, add bool, now time.Time) error
	ResolveTaskGID(ctx context.Context, gid string) (string, error)
	SetDependencyLocal(ctx context.Context, taskGID, dependencyGID string, add bool, now time.Time) error
	ResolveCustomField(ctx context.Context, taskGID, arg string) (readmodel.CustomFieldView, error)
	ParseCustomFieldValue(ctx context.Context, fieldGID string, input any, now time.Time) (any, error)
	SetCustomFieldLocal(ctx context.Context, taskGID, fieldGID string, value any, now time.Time) (string, error)
}

// Relations is the use cases on a task's relations: its tags, followers, other projects,
// dependencies and custom fields. Each change is its own local write and outbox entry,
// as the store makes them; the clock is read once for each.
type Relations struct {
	Store RelationStore
	Clock Clock
}

// RelationsInput is what a user gives to change a task's relations: tags by name or gid
// (a tag that does not exist is created), followers as "me", a user's gid or name,
// projects by gid, name or a unique part of it, and tasks by gid. Dependencies are the
// tasks this one waits for; Dependents are the tasks that wait for it.
type RelationsInput struct {
	AddTags, RemoveTags                 []string
	AddFollowers, RemoveFollowers       []string
	AddProjects, RemoveProjects         []string
	AddDependencies, RemoveDependencies []string
	AddDependents, RemoveDependents     []string
}

// Link is a relation to add (On) or remove, to the gid of a user, a project or a tag.
type Link struct {
	GID string
	On  bool
}

// Dependency is a dependency to add (Add) or remove: Task waits for Dependency.
type Dependency struct {
	Task, Dependency string
	Add              bool
}

// RelationEdit is a change to a task's relations with every name resolved. Followers and
// Projects are in the order given, the ones to add first.
type RelationEdit struct {
	Tags         task.TagEdit
	Followers    []Link
	Projects     []Link
	Dependencies []Dependency
}

// Empty is true when the edit changes nothing.
func (e RelationEdit) Empty() bool {
	return e.Tags.Empty() && len(e.Followers)+len(e.Projects)+len(e.Dependencies) == 0
}

// FieldValue is a custom field and the value to set it to, in the stored form (nil clears).
type FieldValue struct {
	Field readmodel.CustomFieldView
	Value any
}

// Resolve resolves every name in the input without writing anything, in the order tags,
// add_followers, remove_followers, add_projects, remove_projects, add_dependencies,
// remove_dependencies, add_dependents, remove_dependents; the first that does not resolve
// is returned. A follower, project or task that does not resolve is a *FieldError whose
// Field is that name; a tag's error is the store's, as it is.
func (r *Relations) Resolve(ctx context.Context, taskGID string, in RelationsInput) (RelationEdit, error) {
	var e RelationEdit
	var err error
	if e.Tags, err = r.Store.ResolveTagEdit(ctx, in.AddTags, in.RemoveTags); err != nil {
		return RelationEdit{}, err
	}
	for _, l := range []struct {
		field string
		args  []string
		on    bool
		to    *[]Link
		read  func(context.Context, []string) ([]string, error)
	}{
		{"add_followers", in.AddFollowers, true, &e.Followers, r.Store.ResolveUsers},
		{"remove_followers", in.RemoveFollowers, false, &e.Followers, r.Store.ResolveUsers},
		{"add_projects", in.AddProjects, true, &e.Projects, r.Store.ResolveProjects},
		{"remove_projects", in.RemoveProjects, false, &e.Projects, r.Store.ResolveProjects},
	} {
		gids, err := l.read(ctx, l.args)
		if err != nil {
			return RelationEdit{}, &FieldError{Field: l.field, Err: err}
		}
		for _, g := range gids {
			*l.to = append(*l.to, Link{GID: g, On: l.on})
		}
	}
	for _, d := range []struct {
		field   string
		args    []string
		add, of bool // of: the given tasks wait for this one
	}{
		{"add_dependencies", in.AddDependencies, true, false}, {"remove_dependencies", in.RemoveDependencies, false, false},
		{"add_dependents", in.AddDependents, true, true}, {"remove_dependents", in.RemoveDependents, false, true},
	} {
		for _, g := range d.args {
			other, err := r.Store.ResolveTaskGID(ctx, g)
			if err != nil {
				return RelationEdit{}, &FieldError{Field: d.field, Err: err}
			}
			c := Dependency{Task: taskGID, Dependency: other, Add: d.add}
			if d.of {
				c.Task, c.Dependency = other, taskGID
			}
			e.Dependencies = append(e.Dependencies, c)
		}
	}
	return e, nil
}

// ResolveFields resolves custom fields of task taskGID by name or gid, and reads their
// values (ParseFieldValue), without writing anything. They are read in the order of their
// names; the first that fails is a *FieldError with Field "custom_fields".
func (r *Relations) ResolveFields(ctx context.Context, taskGID string, in map[string]any) ([]FieldValue, error) {
	var out []FieldValue
	for _, name := range slices.Sorted(maps.Keys(in)) {
		f, err := r.Store.ResolveCustomField(ctx, taskGID, name)
		if err != nil {
			return nil, &FieldError{Field: "custom_fields", Err: err}
		}
		v, err := r.ParseFieldValue(ctx, f.GID, in[name])
		if err != nil {
			return nil, &FieldError{Field: "custom_fields", Err: err}
		}
		out = append(out, FieldValue{Field: f, Value: v})
	}
	return out, nil
}

// Apply writes a resolved edit and custom field values to task taskGID, in the order
// tags, custom fields, dependencies, followers, projects. It stops at the first error;
// the writes before it stay (each is its own local write).
func (r *Relations) Apply(ctx context.Context, taskGID string, e RelationEdit, fields []FieldValue) error {
	if err := r.Store.ApplyTagEdit(ctx, taskGID, e.Tags, r.Clock()); err != nil {
		return err
	}
	for _, f := range fields {
		if _, err := r.SetFieldHere(ctx, taskGID, f.Field.GID, f.Value); err != nil {
			return err
		}
	}
	for _, d := range e.Dependencies {
		if err := r.SetDependency(ctx, d.Task, d.Dependency, d.Add); err != nil {
			return err
		}
	}
	for _, l := range e.Followers {
		if err := r.SetFollower(ctx, taskGID, l.GID, l.On); err != nil {
			return err
		}
	}
	for _, l := range e.Projects {
		if err := r.SetProject(ctx, taskGID, l.GID, l.On); err != nil {
			return err
		}
	}
	return nil
}

// SetTag adds (on) or removes the tag tagGID, which exists, on task taskGID.
func (r *Relations) SetTag(ctx context.Context, taskGID, tagGID string, on bool) error {
	if on {
		_, err := r.Store.AddTagLocal(ctx, taskGID, tagGID, "", r.Clock())
		return err
	}
	return r.Store.RemoveTagLocal(ctx, taskGID, tagGID, r.Clock())
}

// AddNewTag creates a tag named name and adds it to task taskGID.
func (r *Relations) AddNewTag(ctx context.Context, taskGID, name string) error {
	_, err := r.Store.AddTagLocal(ctx, taskGID, "", name, r.Clock())
	return err
}

// SetFollower adds (follow) or removes the user userGID as a follower of task taskGID.
func (r *Relations) SetFollower(ctx context.Context, taskGID, userGID string, follow bool) error {
	return r.Store.SetFollowerLocal(ctx, taskGID, userGID, follow, r.Clock())
}

// SetProject adds task taskGID to the project projectGID (in its first section, as Asana
// does), or removes it.
func (r *Relations) SetProject(ctx context.Context, taskGID, projectGID string, add bool) error {
	return r.Store.SetProjectLocal(ctx, taskGID, projectGID, "", add, r.Clock())
}

// SetDependency adds (add) or removes the dependency: task waitingGID waits for blockerGID.
func (r *Relations) SetDependency(ctx context.Context, waitingGID, blockerGID string, add bool) error {
	return r.Store.SetDependencyLocal(ctx, waitingGID, blockerGID, add, r.Clock())
}

// ParseFieldValue reads a custom field's value as a user types it into the stored form
// (an option name, a number, a date as ParseDue reads it, user names, …; nil or "" clears).
// The errors are the store's, as they are.
func (r *Relations) ParseFieldValue(ctx context.Context, fieldGID string, input any) (any, error) {
	return r.Store.ParseCustomFieldValue(ctx, fieldGID, input, r.Clock())
}

// SetFieldHere sets the custom field fieldGID of task taskGID to value, already in the
// stored form (nil clears). It returns the value as text ("" when empty).
func (r *Relations) SetFieldHere(ctx context.Context, taskGID, fieldGID string, value any) (string, error) {
	return r.Store.SetCustomFieldLocal(ctx, taskGID, fieldGID, value, r.Clock())
}
