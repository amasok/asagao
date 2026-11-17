package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/rich"
	"github.com/amasok/asagao/internal/richtext"
)

// Without a fit function, FitQueuedRichText changes nothing. It fails with ErrNoFit only
// when there is an entry to check; with none (no rich comment or status update queued),
// there is nothing it would have done.
func TestFitQueuedRichTextWithoutFit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	me := UserRef{GID: "me"}

	_, err := s.AddCommentLocal(ctx, "t1", "ただの文字", me, now) // plain: nothing to check
	must(t, err)
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "新しい名前"}, now))
	if n, err := s.FitQueuedRichText(ctx, nil); n != 0 || err != nil {
		t.Fatalf("no entry to check: %d, %v", n, err)
	}

	md := "# 予定\n\n本文"
	_, err = s.AddRichCommentLocal(ctx, "t2", richtext.TaskNotes.PlainFromMarkdown(md), richtext.TaskNotes.FromMarkdown(md), me, now)
	must(t, err)
	_, err = s.CreateStatusLocal(ctx, "p1", "on_track", "週報", "本文", "<body>本文</body>", me, now)
	must(t, err)
	before, err := s.ListOps(ctx)
	must(t, err)
	if n, err := s.FitQueuedRichText(ctx, nil); n != 0 || !errors.Is(err, ErrNoFit) {
		t.Fatalf("an entry to check: %d, %v; want ErrNoFit", n, err)
	}
	after, err := s.ListOps(ctx)
	must(t, err)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the outbox changed:\n%+v\n%+v", before, after)
	}

	// With the fit function the store asks it for each entry's target.
	var asked []rich.Target
	fit := func(target rich.Target, html string) (string, string, bool) {
		asked = append(asked, target)
		return richtext.Converter{}.Fit(target, html)
	}
	n, err := s.FitQueuedRichText(ctx, fit)
	if err != nil || n != 1 { // the comment had a heading; the status update had none
		t.Fatalf("FitQueuedRichText = %d, %v", n, err)
	}
	if !reflect.DeepEqual(asked, []rich.Target{rich.Comment, rich.StatusUpdate}) {
		t.Fatalf("asked for %v", asked)
	}
}
