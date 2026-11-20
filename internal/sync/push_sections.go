package sync

import (
	"context"
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending changes to sections themselves (#33).

func init() {
	registerOf(outbox.KindRenameSection, handlerOf[outbox.RenameSection]{send: (*Pusher).pushRenameSection})
	registerOf(outbox.KindCreateSection, handlerOf[outbox.CreateSection]{send: (*Pusher).pushCreateSection,
		planWorkspace: func(s *Pusher, ctx context.Context, _ outbox.Op, p outbox.CreateSection) string {
			return s.projectWorkspace(ctx, p.Project)
		},
		find: func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.CreateSection, _ finder) (outcome, match, error) {
			return s.findCreatedSection(ctx, op, p)
		}})
	registerOf(outbox.KindDeleteSection, handlerOf[outbox.DeleteSection]{send: sends((*Pusher).pushDeleteSection),
		planWorkspace: func(s *Pusher, ctx context.Context, _ outbox.Op, p outbox.DeleteSection) string {
			return s.projectWorkspace(ctx, p.Project)
		},
		waits: func(s *Pusher, ctx context.Context, op outbox.Op, _ outbox.DeleteSection) (bool, error) {
			return s.Store.TaskDeletesQueuedIn(ctx, op.TaskGID) // Asana deletes only an empty section
		}})
	registerOf(outbox.KindInsertSection, handlerOf[outbox.InsertSection]{send: sends((*Pusher).pushInsertSection),
		planWorkspace: func(s *Pusher, ctx context.Context, _ outbox.Op, p outbox.InsertSection) string {
			return s.projectWorkspace(ctx, p.Project)
		}})
}

// projectWorkspace is the workspace of a project ("" if not known), the one a section
// entry's 402 is recorded for.
func (s *Pusher) projectWorkspace(ctx context.Context, project string) string {
	ws, _ := s.Store.ProjectWorkspace(ctx, project)
	return ws
}

// waitsForSection reports whether op names a section created offline whose creation is
// still queued (pending, being verified or failed): it waits, and goes out once the
// section is created (its gid is then replaced in op) or is discarded with it.
func (s *Pusher) waitsForSection(ctx context.Context, op outbox.Op) (bool, error) {
	var refs []string
	if spec, _ := outbox.SpecOf(op.Kind); spec.Subject == outbox.SubjectSection && !spec.Creates {
		refs = append(refs, op.TaskGID)
	}
	refs = append(refs, outbox.SectionRefs(op)...)
	for _, g := range refs {
		if !localgid.IsLocal(g) {
			continue
		}
		real, err := s.Store.RealSectionGID(ctx, g)
		if err != nil {
			return false, err
		}
		if !localgid.IsLocal(real) {
			continue
		}
		if queued, err := s.Store.SectionCreateQueued(ctx, g); err != nil || queued {
			return queued, err
		}
	}
	return false, nil
}

// realSection returns the Asana gid of a section ("" stays ""). A section created offline
// whose creation is gone (discarded) is reported as not found.
func (s *Pusher) realSection(ctx context.Context, gid string) (string, error) {
	real, err := s.Store.RealSectionGID(ctx, gid)
	if err != nil {
		return "", err
	}
	if localgid.IsLocal(real) {
		return "", &model.Error{Status: 404} // its create failed
	}
	return real, nil
}

// pushCreateSection creates a section. Creating twice would make two sections, so before
// the request the gids of the sections that already have that name are recorded: if the
// answer is lost, reconcile takes a new section with the name as ours.
func (s *Pusher) pushCreateSection(ctx context.Context, op outbox.Op, p outbox.CreateSection) (outcome, error) {
	secs, err := s.API.Sections(ctx, p.Project)
	if err != nil {
		return sent, err
	}
	if p.Existing == nil {
		existing := []string{}
		for _, sec := range secs {
			if sec.Name == p.Name {
				existing = append(existing, sec.GID)
			}
		}
		if err := s.Store.SetOpPayload(ctx, op.ID, "existing", existing); err != nil {
			return sent, err
		}
	}
	var before, after string
	for _, a := range []struct {
		gid string
		dst *string
	}{{p.Before, &before}, {p.After, &after}} {
		if a.gid == "" {
			continue
		}
		if *a.dst, err = s.realSection(ctx, a.gid); isNotFound(err) {
			*a.dst = "" // the section next to it was never created: put it last
		} else if err != nil {
			return sent, err
		}
		if !slices.ContainsFunc(secs, func(sec model.Section) bool { return sec.GID == *a.dst }) {
			*a.dst = "" // deleted in Asana meanwhile: put it last rather than fail the create
		}
	}
	created, err := s.API.CreateSection(ctx, p.Project, p.Name, before, after)
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteCreateSection(ctx, op.ID, op.TaskGID, created.GID); err != nil {
		return sent, sentNotSaved(err)
	}
	return sent, nil
}

// findCreatedSection looks for the section a create_section entry may have created: a
// section of the project with its name that was not there before the request.
func (s *Pusher) findCreatedSection(ctx context.Context, op outbox.Op, p outbox.CreateSection) (outcome, match, error) {
	existing := p.Existing
	if existing == nil {
		return sent, notFound, nil // the request was never made
	}
	secs, err := s.API.Sections(ctx, p.Project)
	if err != nil {
		return sent, notFound, err
	}
	used, err := s.Store.RecordedResults(ctx, readmodel.ResultSection)
	if err != nil {
		return sent, notFound, err
	}
	sec, m := pick(secs, func(sec model.Section) bool {
		return sec.Name == p.Name && !slices.Contains(existing, sec.GID) && !used[sec.GID]
	})
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteCreateSection(ctx, op.ID, op.TaskGID, sec.GID)
}

// pushDeleteSection deletes a section. One that is already gone is done.
func (s *Pusher) pushDeleteSection(ctx context.Context, op outbox.Op, _ outbox.DeleteSection) error {
	gid, err := s.realSection(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if err := s.API.DeleteSection(ctx, gid); err != nil && !isNotFound(err) {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

// pushInsertSection moves a section before or after another one.
func (s *Pusher) pushInsertSection(ctx context.Context, op outbox.Op, p outbox.InsertSection) error {
	gid, err := s.realSection(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	var before, after string
	if p.Before != "" {
		if before, err = s.realSection(ctx, p.Before); err != nil {
			return err
		}
	} else if after, err = s.realSection(ctx, p.After); err != nil {
		return err
	}
	if err := s.API.InsertSection(ctx, p.Project, gid, before, after); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

func (s *Pusher) pushRenameSection(ctx context.Context, op outbox.Op, p outbox.RenameSection) (outcome, error) {
	gid, err := s.realSection(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	op.TaskGID = gid
	server, err := s.API.Section(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	name := p.Name
	if !same(server.Name, op.Base["name"]) && !same(server.Name, name) {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, map[string]any{"name": server.Name})
	}
	updated, err := s.API.UpdateSection(ctx, op.TaskGID, name)
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	_ = s.Store.ApplySectionName(ctx, op.TaskGID, updated.Name) // the next sync fixes a failure
	return sent, nil
}
