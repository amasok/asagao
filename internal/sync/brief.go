package sync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// A project's brief (#60) is created, changed and deleted through the outbox
// (update_project_brief). It is a preview API: a 402 or 403 for it is recorded per
// workspace (readmodel.PlanProjectBrief) and hides it for a while, as a 402 hides other
// features (plan.go).

// serverBrief returns the brief project p has in Asana (nil: none).
func (s *Syncer) serverBrief(ctx context.Context, p *model.Project) (*model.ProjectBrief, error) {
	if p.ProjectBrief == nil || p.ProjectBrief.GID == "" {
		return nil, nil
	}
	b, err := s.API.ProjectBrief(ctx, p.ProjectBrief.GID)
	if isNotFound(err) {
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
// Asana since it was made here (its base): Asana's title or text differing from both the
// base and this change, a brief made there while one was made here, or one deleted there
// while it was changed here, is a conflict. A change Asana already has (it was sent before
// and its answer lost, or someone made the same change) is done without sending it again.
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
	title, htmlText, text := str(op.Payload, "title"), str(op.Payload, "html_text"), str(op.Payload, "text")
	baseState := str(op.Base, "state")
	same := func(title, text string) bool {
		return server != nil && server.Title == title && sameBriefText(server.Text, text)
	}
	conflict := false
	switch {
	case server == nil && del: // deleted already (sent before, or deleted there too)
		return sent, sentNotSaved(s.Store.CompleteBriefOp(ctx, op.ID, op.TaskGID, nil))
	case server == nil:
		conflict = baseState == readmodel.BriefAvailable // deleted in Asana while changed here
	case baseState != readmodel.BriefAvailable:
		// made here, and Asana has one: this one (sent before, its answer lost), or one made
		// there (or, before the brief was ever fetched, one that was there)
		if same(title, text) {
			return sent, sentNotSaved(s.Store.CompleteBriefOp(ctx, op.ID, op.TaskGID, server))
		}
		conflict = true
	default:
		// changed there since the base, to something else than this change (a change that
		// only looks the same is sent anyway: its formatting may differ)
		conflict = !same(str(op.Base, "title"), str(op.Base, "text")) && (del || !same(title, text))
	}
	if conflict {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, briefValues(server))
	}
	var result *model.ProjectBrief
	switch {
	case del:
		err = s.API.DeleteProjectBrief(ctx, server.GID)
		if isNotFound(err) {
			err = nil // deleted meanwhile: done
		}
	case server == nil:
		result, err = s.API.CreateProjectBrief(ctx, op.TaskGID, title, htmlText)
	default:
		result, err = s.API.UpdateProjectBrief(ctx, server.GID, title, htmlText)
		if isNotFound(err) { // deleted meanwhile: a conflict, checked again next time
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

// briefRefused reports whether err is Asana refusing the brief API: 402 (the plan lacks
// it) or 403 (the preview API is not open to this workspace or user).
func briefRefused(err error) bool {
	return model.IsPaymentRequired(err) || isForbidden(err)
}

// briefRefusal records a refusal of the brief API (briefRefused) for the project's
// workspace, and says so in the error; other errors are returned as they are.
func (s *Syncer) briefRefusal(ctx context.Context, projectGID string, err error) error {
	if !briefRefused(err) {
		return err
	}
	ws, wsErr := s.Store.ProjectWorkspace(ctx, projectGID)
	if wsErr == nil && ws != "" {
		if serr := s.Store.SetSyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, ws), s.clock().UTC().Format(time.RFC3339)); serr != nil {
			return serr
		}
	}
	return fmt.Errorf("このワークスペースでは、プロジェクトブリーフ (概要のページ) を API で使えません (Asana が %d を返しました): %w",
		statusOf(err), err)
}

// clearBriefRefusal forgets an earlier refusal of the brief API in the project's
// workspace (Asana answered).
func (s *Syncer) clearBriefRefusal(ctx context.Context, projectGID string) error {
	ws, err := s.Store.ProjectWorkspace(ctx, projectGID)
	if err != nil || ws == "" {
		return nil
	}
	return s.clearPlanRefusal(ctx, readmodel.PlanProjectBrief, ws)
}

// briefHidden reports whether a recent refusal hides the brief in the project's workspace.
func (s *Syncer) briefHidden(ctx context.Context, projectGID string) (bool, error) {
	ws, err := s.Store.ProjectWorkspace(ctx, projectGID)
	if err != nil || ws == "" {
		return false, nil
	}
	return s.planHidden(ctx, readmodel.PlanProjectBrief, ws)
}

func statusOf(err error) int {
	var apiErr *model.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return 0
}
