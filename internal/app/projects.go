package app

import (
	"context"
	"time"
)

// ProjectStore is the part of the local cache that editing a project needs. *store.Store
// satisfies it.
type ProjectStore interface {
	ResolveProject(ctx context.Context, arg string) (string, error)
	ResolveUser(ctx context.Context, arg string) (string, error)
	UpdateProjectLocal(ctx context.Context, gid string, changes map[string]any, now time.Time) error
}

// Projects is the use case of editing a project's fields (description, owner, start and
// due dates, color): one local write, queued in the outbox as update_project.
type Projects struct {
	Store ProjectStore
	Text  RichText
	Clock Clock
}

// UpdateProjectInput is what a user gives to change a project. A nil field is left as it
// is. Notes is Markdown that replaces the description; Owner is "me", a user's gid or name
// ("" or "none" removes the owner); StartOn and DueOn are read by ParseDue ("" clears);
// Color is one of project.Colors.
type UpdateProjectInput struct {
	Notes, Owner, StartOn, DueOn, Color *string
}

// ProjectUpdate is a project change with every value resolved: Owner a user's gid or "",
// the dates YYYY-MM-DD or "". Notes is still Markdown.
type ProjectUpdate struct {
	Notes, Owner, StartOn, DueOn, Color *string
}

// Count is the number of fields the update changes.
func (u ProjectUpdate) Count() int {
	n := 0
	for _, s := range []*string{u.Notes, u.Owner, u.StartOn, u.DueOn, u.Color} {
		if s != nil {
			n++
		}
	}
	return n
}

// ResolveUpdate reads and checks the input without writing anything. An invalid value is
// a *FieldError ("start_on", "due_on", "owner"), the first one found in that order.
func (p *Projects) ResolveUpdate(ctx context.Context, in UpdateProjectInput) (ProjectUpdate, error) {
	u := ProjectUpdate{Notes: in.Notes, Color: in.Color}
	now := p.Clock()
	for _, f := range []struct {
		field string
		in    *string
		out   **string
	}{{"start_on", in.StartOn, &u.StartOn}, {"due_on", in.DueOn, &u.DueOn}} {
		if f.in == nil {
			continue
		}
		v, err := ParseDue(*f.in, now)
		if err != nil {
			return ProjectUpdate{}, &FieldError{Field: f.field, Err: err}
		}
		*f.out = &v
	}
	if in.Owner != nil {
		v, err := p.Store.ResolveUser(ctx, *in.Owner)
		if err != nil {
			return ProjectUpdate{}, &FieldError{Field: "owner", Err: err}
		}
		u.Owner = &v
	}
	return u, nil
}

// UpdateHere applies resolved changes to the project gid, locally and queued in the outbox
// (the store's UpdateProjectLocal). An update that changes nothing writes nothing. The
// errors are the store's, as they are (a color Asana does not have, a start date without a
// due date or after it, a project not in the cache).
func (p *Projects) UpdateHere(ctx context.Context, gid string, u ProjectUpdate) error {
	changes := map[string]any{}
	for key, v := range map[string]*string{"owner": u.Owner, "start_on": u.StartOn, "due_on": u.DueOn, "color": u.Color} {
		if v != nil {
			changes[key] = *v
		}
	}
	if u.Notes != nil {
		changes["html_notes"], changes["notes"] = p.Text.TaskNotes(*u.Notes)
	}
	if len(changes) == 0 {
		return nil
	}
	return p.Store.UpdateProjectLocal(ctx, gid, changes, p.Clock())
}

// Update resolves the project (by gid or name) and the input, and applies it. Nothing is
// written when either is invalid. It returns the project's gid and the resolved update.
func (p *Projects) Update(ctx context.Context, project string, in UpdateProjectInput) (string, ProjectUpdate, error) {
	gid, err := p.Store.ResolveProject(ctx, project)
	if err != nil {
		return "", ProjectUpdate{}, err
	}
	now := p.Clock()
	once := *p
	once.Clock = func() time.Time { return now }
	u, err := once.ResolveUpdate(ctx, in)
	if err != nil {
		return "", ProjectUpdate{}, err
	}
	return gid, u, once.UpdateHere(ctx, gid, u)
}
