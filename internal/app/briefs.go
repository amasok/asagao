package app

import (
	"context"
	"errors"
	"html"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// BriefStore is the part of the local cache that changing a project's brief needs.
// *store.Store satisfies it.
type BriefStore interface {
	ResolveProject(ctx context.Context, arg string) (string, error)
	ProjectWorkspace(ctx context.Context, projectGID string) (string, error)
	SyncState(ctx context.Context, key string) (string, error)
	ProjectOverview(ctx context.Context, gid string) (*readmodel.ProjectOverview, error)
	UpdateProjectBriefLocal(ctx context.Context, projectGID string, e readmodel.BriefEdit, now time.Time) error
}

// BriefText converts a brief's rich text to and from editable Markdown (richtext.Converter):
// its tables and embeds are kept as they are (asana-html blocks). A block Asana would
// refuse is an error.
type BriefText interface {
	BriefMarkdown(htmlText string) string
	ProjectBrief(md string) (htmlText, plain string, err error)
}

// Briefs is the use case of creating, changing and deleting a project's brief (#60): one
// local write, queued in the outbox as update_project_brief. Asana's brief is compared
// with the one it was made from when it is sent (a change there is a conflict).
type Briefs struct {
	Store BriefStore
	Text  BriefText
	Clock Clock
}

// BriefInput is a change to a brief. A nil field is left as it is; Markdown is the whole
// body, as Markdown (BriefText).
type BriefInput struct {
	Title, Markdown *string
}

// ErrEmptyBrief: a new brief needs a title or a body.
var ErrEmptyBrief = errors.New("ブリーフのタイトルも本文も空です")

// ErrNoBrief: the project has no brief (to delete).
var ErrNoBrief = errors.New("このプロジェクトにはブリーフがありません")

// Available returns nil when the project's brief can be changed here, and
// ErrPlanUnavailable while Asana refuses the brief API in its workspace (402 or 403, for
// readmodel.PlanRecheckAfter) or the cache says it does not give this project's brief.
func (b *Briefs) Available(ctx context.Context, projectGID string) error {
	ws, err := b.Store.ProjectWorkspace(ctx, projectGID)
	if err != nil {
		return err
	}
	if err := planOffered(ctx, b.Store, readmodel.PlanProjectBrief, ws, b.Clock()); err != nil {
		return err
	}
	o, err := b.Store.ProjectOverview(ctx, projectGID)
	if err != nil {
		return err
	}
	if o.BriefState == readmodel.BriefUnavailable {
		return &ErrPlanUnavailable{Feature: readmodel.PlanProjectBrief}
	}
	return nil
}

// SaveHere creates or changes the brief of project gid. It reports whether anything
// changed: the brief's title and body as they are (the body as BriefMarkdown reads it)
// write nothing.
func (b *Briefs) SaveHere(ctx context.Context, gid string, in BriefInput) (bool, error) {
	if err := b.Available(ctx, gid); err != nil {
		return false, err
	}
	o, err := b.Store.ProjectOverview(ctx, gid)
	if err != nil {
		return false, err
	}
	var cur readmodel.ProjectBrief
	if o.Brief != nil {
		cur = *o.Brief
	}
	e := readmodel.BriefEdit{Title: cur.Title, HTMLText: cur.HTMLText, Text: cur.Text}
	changed := false
	if in.Title != nil && *in.Title != cur.Title {
		e.Title, changed = *in.Title, true
	}
	if in.Markdown != nil && (o.Brief == nil || strings.TrimSpace(*in.Markdown) != strings.TrimSpace(b.Text.BriefMarkdown(cur.HTMLText))) {
		if e.HTMLText, e.Text, err = b.Text.ProjectBrief(*in.Markdown); err != nil {
			return false, &FieldError{Field: "brief", Err: err}
		}
		changed = true
	}
	if o.Brief == nil {
		if e.Title == "" && (in.Markdown == nil || isBlank(*in.Markdown)) {
			return false, ErrEmptyBrief
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	if e.HTMLText == "" { // a new brief with a title only (or a cached one without rich text)
		e.HTMLText = "<body>" + html.EscapeString(e.Text) + "</body>"
	}
	return true, b.Store.UpdateProjectBriefLocal(ctx, gid, e, b.Clock())
}

// Save resolves the project (by gid or name) and is SaveHere. It returns its gid.
func (b *Briefs) Save(ctx context.Context, project string, in BriefInput) (string, bool, error) {
	gid, err := b.Store.ResolveProject(ctx, project)
	if err != nil {
		return "", false, err
	}
	changed, err := b.SaveHere(ctx, gid, in)
	return gid, changed, err
}

// DeleteHere deletes the brief of project gid (ErrNoBrief when it has none).
func (b *Briefs) DeleteHere(ctx context.Context, gid string) error {
	if err := b.Available(ctx, gid); err != nil {
		return err
	}
	o, err := b.Store.ProjectOverview(ctx, gid)
	if err != nil {
		return err
	}
	if o.Brief == nil {
		return ErrNoBrief
	}
	return b.Store.UpdateProjectBriefLocal(ctx, gid, readmodel.BriefEdit{Delete: true}, b.Clock())
}

// Delete resolves the project (by gid or name) and is DeleteHere. It returns its gid.
func (b *Briefs) Delete(ctx context.Context, project string) (string, error) {
	gid, err := b.Store.ResolveProject(ctx, project)
	if err != nil {
		return "", err
	}
	return gid, b.DeleteHere(ctx, gid)
}

func isBlank(s string) bool { return strings.TrimSpace(s) == "" }
