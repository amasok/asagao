package sync

import (
	"context"
	"fmt"
	"strings"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
	"github.com/amasok/asagao/internal/ui/format"
)

// A project's brief (#60) is created, changed and deleted through the outbox
// (update_project_brief). It is a preview API: a 402 for it is recorded per workspace
// (plan.ProjectBrief) and hides it for a while, as a 402 hides other features
// (plan.go); a 403 makes this one project's brief unavailable (briefRefusal).

// serverBrief returns the brief project p has in Asana (nil: none).
func (s *Syncer) serverBrief(ctx context.Context, p *model.Project) (*model.ProjectBrief, error) {
	if p.ProjectBrief == nil || p.ProjectBrief.GID == "" {
		return nil, nil
	}
	b, err := s.API.ProjectBrief(ctx, p.ProjectBrief.GID)
	if s.briefGone(ctx, p.GID, p.ProjectBrief.GID, err) {
		return nil, nil // deleted meanwhile
	}
	if err != nil {
		return nil, err
	}
	if b.GID == "" {
		b.GID = p.ProjectBrief.GID
	}
	return b, nil
}

// briefValues are a brief's values as an entry's base and conflict keep them.
func briefValues(b *model.ProjectBrief) map[string]any {
	if b == nil {
		return map[string]any{"state": readmodel.BriefNone, "title": "", "text": "", "html_text": ""}
	}
	return map[string]any{"state": readmodel.BriefAvailable, "title": b.Title, "text": b.Text, "html_text": b.HTMLText}
}

// sameBriefText compares two plain texts of a brief as Asana and this side write them: the
// server may end lines or the text differently.
func sameBriefText(a, b string) bool {
	norm := func(s string) string {
		s = termsafe.String(strings.ReplaceAll(s, "\r\n", "\n"))
		lines := strings.Split(s, "\n")
		for i, l := range lines {
			lines[i] = strings.TrimRight(l, " \t ")
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return norm(a) == norm(b)
}

// pushUpdateBrief sends a brief change, after checking that nobody changed the brief in
// Asana since it was made here (its base). A change sends only what it changes (the title,
// the body, or both), and only that is compared: Asana's value of it differing from both
// the base and this change is a conflict, and so are a brief made there while one was made
// here and one deleted there while it was changed here (a delete conflicts with any change
// there). A change Asana already has (it was sent before and its answer lost, or someone
// made the same change) is done without sending it again.
func (s *Syncer) pushUpdateBrief(ctx context.Context, op outbox.Op) (outcome, error) {
	p, err := s.API.Project(ctx, op.TaskGID)
	if err != nil {
		return sent, err // a 404: the project was deleted (dropGone)
	}
	server, err := s.serverBrief(ctx, p)
	if err != nil {
		return sent, s.briefRefusal(ctx, op.TaskGID, err)
	}
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	del := op.Payload["delete"] == true
	title, hasTitle := op.Payload["title"].(string)
	htmlText, hasBody := op.Payload["html_text"].(string)
	text := str(op.Payload, "text")
	baseState := str(op.Base, "state")
	baseTitle, baseText := str(op.Base, "title"), str(op.Base, "text")
	conflict := false
	switch {
	case server == nil && del: // deleted already (sent before, or deleted there too)
		return sent, sentNotSaved(s.Store.CompleteBriefOp(ctx, op.ID, op.TaskGID, nil))
	case server == nil:
		conflict = baseState == readmodel.BriefAvailable // deleted in Asana while changed here
	case baseState != readmodel.BriefAvailable:
		// made here, and Asana has one: this one (sent before, its answer lost), or one made
		// there (or, before the brief was ever fetched, one that was there). Sent before and
		// maybe made (op.Verify), the title is enough: Asana may write the text of tables
		// and lists otherwise than here.
		if server.Title == title && (sameBriefText(server.Text, text) || op.Verify) {
			return sent, sentNotSaved(s.Store.CompleteBriefOp(ctx, op.ID, op.TaskGID, server))
		}
		conflict = true
	case del:
		conflict = server.Title != baseTitle || !sameBriefText(server.Text, baseText)
	default:
		// a part changed there since the base, to something else than this change (a body
		// that only looks the same is sent anyway: its formatting may differ)
		conflict = hasTitle && server.Title != baseTitle && server.Title != title ||
			hasBody && !sameBriefText(server.Text, baseText) && !sameBriefText(server.Text, text)
	}
	if conflict {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, briefValues(server))
	}
	var result *model.ProjectBrief
	switch {
	case del:
		err = s.API.DeleteProjectBrief(ctx, server.GID)
		if s.briefGone(ctx, op.TaskGID, server.GID, err) {
			err = nil // deleted meanwhile: done
		}
	case server == nil:
		result, err = s.API.CreateProjectBrief(ctx, op.TaskGID, title, htmlText)
	default:
		changes := map[string]any{}
		if hasTitle {
			changes["title"] = title
		}
		if hasBody {
			changes["html_text"] = htmlText
		}
		result, err = s.API.UpdateProjectBrief(ctx, server.GID, changes)
		if s.briefGone(ctx, op.TaskGID, server.GID, err) { // deleted meanwhile: a conflict
			return conflicted, s.Store.MarkOpConflict(ctx, op.ID, briefValues(nil))
		}
	}
	if err != nil {
		return sent, s.briefRefusal(ctx, op.TaskGID, err)
	}
	if err := s.clearBriefRefusal(ctx, op.TaskGID); err != nil {
		return sent, sentNotSaved(err)
	}
	return sent, sentNotSaved(s.Store.CompleteBriefOp(ctx, op.ID, op.TaskGID, result))
}

// briefGone reports whether a request for brief gid of a project failed because the brief
// is no more: a 404, or the 403 Asana answers for a brief that does not exist (checked on
// the real Asana: "You do not have access to this project_brief."), when the project no
// longer has that brief.
func (s *Syncer) briefGone(ctx context.Context, projectGID, gid string, err error) bool {
	if isNotFound(err) {
		return true
	}
	if !isForbidden(err) {
		return false
	}
	p, perr := s.API.Project(ctx, projectGID)
	return perr == nil && (p.ProjectBrief == nil || p.ProjectBrief.GID != gid)
}

// briefRefused reports whether err is Asana refusing the brief API: 402 (the workspace's
// plan lacks it) or 403 (this project's brief is not ours to read or change, or the
// preview API is not open to us).
func briefRefused(err error) bool {
	return model.IsPaymentRequired(err) || isForbidden(err)
}

// briefRefusal records a refusal of the brief API (briefRefused) and says so in the error;
// other errors are returned as they are. A 402 is the workspace's plan: it is recorded for
// the workspace (readmodel.PlanKey), which hides the brief there for a while. A 403 is
// recorded for this project only (its brief is BriefUnavailable until the overview gets it
// again): a project we may not change must not hide the briefs of the others. (Whether the
// preview API answers 403 for a whole workspace is to be seen on the real Asana.)
func (s *Syncer) briefRefusal(ctx context.Context, projectGID string, err error) error {
	switch {
	case model.IsPaymentRequired(err):
		ws, wsErr := s.Store.ProjectWorkspace(ctx, projectGID)
		if wsErr == nil && ws != "" {
			if serr := s.Store.RecordPlanRefusal(ctx, plan.ProjectBrief, ws, s.clock()); serr != nil {
				return serr
			}
		}
		return fmt.Errorf("%s: %w", plan.UnavailableMessage(plan.ProjectBrief), err)
	case isForbidden(err):
		if serr := s.Store.SaveProjectOverview(ctx, projectGID, nil, readmodel.BriefUnavailable, nil, s.clock()); serr != nil {
			return serr
		}
		return fmt.Errorf("%s: %w", format.BriefForbiddenMessage, err)
	}
	return err
}

// clearBriefRefusal forgets an earlier refusal of the brief API in the project's
// workspace (Asana answered).
func (s *Syncer) clearBriefRefusal(ctx context.Context, projectGID string) error {
	ws, err := s.Store.ProjectWorkspace(ctx, projectGID)
	if err != nil || ws == "" {
		return nil
	}
	return s.clearPlanRefusal(ctx, plan.ProjectBrief, ws)
}

// briefHidden reports whether a recent refusal hides the brief in the project's workspace.
func (s *Syncer) briefHidden(ctx context.Context, projectGID string) (bool, error) {
	ws, err := s.Store.ProjectWorkspace(ctx, projectGID)
	if err != nil || ws == "" {
		return false, nil
	}
	return s.planHidden(ctx, plan.ProjectBrief, ws)
}
