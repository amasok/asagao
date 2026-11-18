package sync

import (
	"context"
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending changes to sections and tags themselves (#33).

// sectionKinds are the kinds whose task_gid is a section gid.
var sectionKinds = []string{outbox.KindRenameSection, outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection}

// waitsForSection reports whether op names a section created offline whose creation is
// still queued (pending, being verified or failed): it waits, and goes out once the
// section is created (its gid is then replaced in op) or is discarded with it.
func (s *Syncer) waitsForSection(ctx context.Context, op outbox.Op) (bool, error) {
	var refs []string
	if slices.Contains(sectionKinds, op.Kind) && op.Kind != outbox.KindCreateSection {
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

// pushUpdateTag renames and/or recolors a tag. A value changed in Asana since the local
// edit (to something else) is a conflict.
func (s *Syncer) pushUpdateTag(ctx context.Context, op outbox.Op) (outcome, error) {
	gid, err := s.Store.RealTagGID(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	if localgid.IsLocal(gid) {
		return sent, &model.Error{Status: 404} // the tag was never created
	}
	server, err := s.API.Tag(ctx, gid)
	if err != nil {
		return sent, err
	}
	current := map[string]string{"name": server.Name, "color": server.Color}
	changes := map[string]any{}
	conflict := map[string]any{}
	for k, v := range op.Payload {
		want, _ := v.(string)
		cur, base := any(current[k]), op.Base[k]
		if k == "color" { // no color is "" here and "none" in Asana's answers, like a project's
			cur, base = noColor(cur), noColor(base)
			if noColor(want) == "" {
				want = ""
			}
		}
		if !same(cur, base) && cur != any(want) {
			conflict[k] = current[k]
		}
		if k == "color" && want == "" {
			changes[k] = nil // no color
		} else {
			changes[k] = want
		}
	}
	if len(conflict) > 0 {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, conflict)
	}
	updated, err := s.API.UpdateTag(ctx, gid, changes)
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	if updated.GID == "" {
		updated.GID = gid
	}
	_ = s.Store.ApplyTag(ctx, *updated) // the next sync fixes a failure
	return sent, nil
}

// pushDeleteTag deletes a tag. One that is already gone (or was never created) is done.
func (s *Syncer) pushDeleteTag(ctx context.Context, op outbox.Op) error {
	gid, err := s.Store.RealTagGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if !localgid.IsLocal(gid) {
		if err := s.API.DeleteTag(ctx, gid); err != nil && !isNotFound(err) {
			return err
		}
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

// waitsForTag reports whether a change to a tag created offline waits for the tag to be
// created (an add_tag that creates it is still queued).
func (s *Syncer) waitsForTag(ctx context.Context, tag string) (bool, error) {
	if !localgid.IsLocal(tag) {
		return false, nil
	}
	real, err := s.Store.RealTagGID(ctx, tag)
	if err != nil || !localgid.IsLocal(real) {
		return false, err
	}
	return s.Store.TagCreateQueued(ctx, tag)
}
