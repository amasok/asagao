package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/domain/tag"
	"github.com/amasok/asagao/internal/readmodel"
)

// TagStore is the part of the local cache that changing tags themselves needs.
// *store.Store satisfies it.
type TagStore interface {
	ResolveTag(ctx context.Context, arg string) (string, error)
	GetTag(ctx context.Context, gid string) (readmodel.TagCount, error)
	UpdateTagLocal(ctx context.Context, tagGID string, changes map[string]any, now time.Time) error
	DeleteTagLocal(ctx context.Context, tagGID string, now time.Time) error
	SyncState(ctx context.Context, key string) (string, error)
	// The workspaces a 402 is recorded for (readmodel.PlanKey).
	TagWorkspace(ctx context.Context, tagGID string) (string, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	WorkspaceGIDs(ctx context.Context) ([]string, error)
}

// Tags is the use cases on tags themselves: renaming, recoloring and deleting one. Each
// is one local write, queued in the outbox. (Adding tags to tasks is Relations'.)
type Tags struct {
	Store TagStore
	Clock Clock
}

// Available reports whether renaming, recoloring and deleting tags can be offered: unless
// a recent 402 hides them in every workspace. Each change checks its tag's workspace (in).
func (t *Tags) Available(ctx context.Context) bool {
	return planAnywhere(ctx, t.Store, readmodel.PlanTags, planNow(t.Clock))
}

// in returns ErrPlanUnavailable while a recent 402 hides changing tags in the workspace of
// tag tagGID.
func (t *Tags) in(ctx context.Context, tagGID string) error {
	ws, err := t.Store.TagWorkspace(ctx, tagGID)
	return planIn(ctx, t.Store, readmodel.PlanTags, ws, err, planNow(t.Clock))
}

// Resolve finds the tag named (or with the gid) arg, with how many tasks carry it.
func (t *Tags) Resolve(ctx context.Context, arg string) (readmodel.TagCount, error) {
	gid, err := t.Store.ResolveTag(ctx, arg)
	if err != nil {
		return readmodel.TagCount{}, err
	}
	return t.Store.GetTag(ctx, gid)
}

// Rename renames the tag named (or with the gid) arg.
func (t *Tags) Rename(ctx context.Context, arg, name string) error {
	return t.update(ctx, arg, map[string]any{"name": name})
}

// SetColor sets the color of the tag named (or with the gid) arg: one of tag.Colors, or
// "" / "none" for none.
func (t *Tags) SetColor(ctx context.Context, arg, color string) error {
	c, err := tag.NormalizeColor(color)
	if err != nil {
		return err
	}
	return t.update(ctx, arg, map[string]any{"color": c})
}

func (t *Tags) update(ctx context.Context, arg string, changes map[string]any) error {
	gid, err := t.Store.ResolveTag(ctx, arg)
	if err != nil {
		return err
	}
	return t.UpdateHere(ctx, gid, changes)
}

// UpdateHere changes the tag tagGID ("name" and/or "color").
func (t *Tags) UpdateHere(ctx context.Context, tagGID string, changes map[string]any) error {
	if err := t.in(ctx, tagGID); err != nil {
		return err
	}
	if c, ok := changes["color"].(string); ok {
		n, err := tag.NormalizeColor(c)
		if err != nil {
			return err
		}
		changes["color"] = n
	}
	return t.Store.UpdateTagLocal(ctx, tagGID, changes, t.Clock())
}

// Delete deletes the tag named (or with the gid) arg (from every task).
func (t *Tags) Delete(ctx context.Context, arg string) error {
	gid, err := t.Store.ResolveTag(ctx, arg)
	if err != nil {
		return err
	}
	return t.DeleteHere(ctx, gid)
}

// DeleteHere deletes the tag tagGID (from every task).
func (t *Tags) DeleteHere(ctx context.Context, tagGID string) error {
	if err := t.in(ctx, tagGID); err != nil {
		return err
	}
	return t.Store.DeleteTagLocal(ctx, tagGID, t.Clock())
}
