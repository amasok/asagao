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
	register(outbox.KindRenameSection, opHandler{send: (*Syncer).pushRenameSection})
	register(outbox.KindCreateSection, opHandler{send: (*Syncer).pushCreateSection, planWorkspace: sectionWorkspace,
		find: func(s *Syncer, ctx context.Context, op outbox.Op, _ finder) (outcome, match, error) {
			return s.findCreatedSection(ctx, op)
		}})
	register(outbox.KindDeleteSection, opHandler{send: sends((*Syncer).pushDeleteSection), planWorkspace: sectionWorkspace,
		waits: func(s *Syncer, ctx context.Context, op outbox.Op) (bool, error) {
			return s.Store.TaskDeletesQueuedIn(ctx, op.TaskGID) // Asana deletes only an empty section
		}})
	register(outbox.KindInsertSection, opHandler{send: sends((*Syncer).pushInsertSection), planWorkspace: sectionWorkspace})
}

// sectionWorkspace is the workspace of the project of a section entry.
func sectionWorkspace(s *Syncer, ctx context.Context, op outbox.Op) string {
	ws, _ := s.Store.ProjectWorkspace(ctx, payloadString(op, "project"))
	return ws
}

// waitsForSection reports whether op names a section created offline whose creation is
// still queued (pending, being verified or failed): it waits, and goes out once the
// section is created (its gid is then replaced in op) or is discarded with it.
func (s *Syncer) waitsForSection(ctx context.Context, op outbox.Op) (bool, error) {
	var refs []string
	if spec, _ := outbox.SpecOf(op.Kind); spec.Subject == outbox.SubjectSection && !spec.Creates {
		refs = append(refs, op.TaskGID)
	}
	for _, k := range outbox.SectionRefKeys {
		if g, _ := op.Payload[k].(string); g != "" {
			refs = append(refs, g)
		}
	}
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
func (s *Syncer) realSection(ctx context.Context, gid string) (string, error) {
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
func (s *Syncer) pushCreateSection(ctx context.Context, op outbox.Op) (outcome, error) {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	secs, err := s.API.Sections(ctx, str("project"))
	if err != nil {
		return sent, err
	}
	if _, ok := op.Payload["existing"]; !ok {
		existing := []string{}
		for _, sec := range secs {
			if sec.Name == str("name") {
				existing = append(existing, sec.GID)
			}
		}
		if err := s.Store.SetOpPayload(ctx, op.ID, "existing", existing); err != nil {
			return sent, err
		}
	}
	var before, after string
	for _, a := range []struct {
		key string
		dst *string
	}{{"before", &before}, {"after", &after}} {
		if str(a.key) == "" {
			continue
		}
		if *a.dst, err = s.realSection(ctx, str(a.key)); isNotFound(err) {
			*a.dst = "" // the section next to it was never created: put it last
		} else if err != nil {
			return sent, err
		}
		if !slices.ContainsFunc(secs, func(sec model.Section) bool { return sec.GID == *a.dst }) {
			*a.dst = "" // deleted in Asana meanwhile: put it last rather than fail the create
		}
	}
	created, err := s.API.CreateSection(ctx, str("project"), str("name"), before, after)
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
func (s *Syncer) findCreatedSection(ctx context.Context, op outbox.Op) (outcome, match, error) {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	raw, ok := op.Payload["existing"].([]any)
	if !ok {
		return sent, notFound, nil // the request was never made
	}
	var existing []string
	for _, v := range raw {
		if g, ok := v.(string); ok {
			existing = append(existing, g)
		}
	}
	secs, err := s.API.Sections(ctx, str("project"))
	if err != nil {
		return sent, notFound, err
	}
	used, err := s.Store.RecordedResults(ctx, readmodel.ResultSection)
	if err != nil {
		return sent, notFound, err
	}
	sec, m := pick(secs, func(sec model.Section) bool {
		return sec.Name == str("name") && !slices.Contains(existing, sec.GID) && !used[sec.GID]
	})
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteCreateSection(ctx, op.ID, op.TaskGID, sec.GID)
}

// pushDeleteSection deletes a section. One that is already gone is done.
func (s *Syncer) pushDeleteSection(ctx context.Context, op outbox.Op) error {
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
func (s *Syncer) pushInsertSection(ctx context.Context, op outbox.Op) error {
	gid, err := s.realSection(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	var before, after string
	if str("before") != "" {
		if before, err = s.realSection(ctx, str("before")); err != nil {
			return err
		}
	} else if after, err = s.realSection(ctx, str("after")); err != nil {
		return err
	}
	if err := s.API.InsertSection(ctx, str("project"), gid, before, after); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

func (s *Syncer) pushRenameSection(ctx context.Context, op outbox.Op) (outcome, error) {
	gid, err := s.realSection(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	op.TaskGID = gid
	server, err := s.API.Section(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	name, _ := op.Payload["name"].(string)
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
