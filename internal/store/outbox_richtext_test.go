package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/richtext"
)

// A comment or a status update queued before #63 (converted like task notes) is rewritten as
// it is converted now; one left failed by Asana's 400 goes back in the queue as a fresh
// start; one being sent is left alone (#71).
func TestFitQueuedRichText(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	md := "# 予定\n\n---\n\n本文"
	oldHTML, oldPlain := richtext.TaskNotes.FromMarkdown(md), richtext.TaskNotes.PlainFromMarkdown(md)
	me := UserRef{GID: "me"}
	_, err := s.AddRichCommentLocal(ctx, "t1", oldPlain, oldHTML, me, now) // pending
	must(t, err)
	_, err = s.AddRichCommentLocal(ctx, "t2", oldPlain, oldHTML, me, now) // failed
	must(t, err)
	_, err = s.CreateStatusLocal(ctx, "p1", "on_track", "週報", oldPlain, oldHTML, me, now) // failed
	must(t, err)
	_, err = s.AddRichCommentLocal(ctx, "t3", oldPlain, oldHTML, me, now) // sending
	must(t, err)
	ops, err := s.ListOps(ctx)
	must(t, err)
	if len(ops) != 4 {
		t.Fatalf("ops = %+v", ops)
	}
	id := map[string]int64{}
	for _, op := range ops {
		id[op.TaskGID] = op.ID
	}
	for _, gid := range []string{"t2", "p1"} {
		ok, err := s.BeginSend(ctx, id[gid], now)
		must(t, err)
		if !ok {
			t.Fatal("not begun")
		}
		must(t, s.CountAttempt(ctx, id[gid], now))
		must(t, s.MarkOpVerify(ctx, id[gid], ""))
		must(t, s.MarkOpFailed(ctx, id[gid], errors.New("400 Bad Request")))
	}
	if ok, err := s.BeginSend(ctx, id["t3"], now); err != nil || !ok {
		t.Fatalf("BeginSend = %v %v", ok, err)
	}

	n, err := s.FitQueuedRichText(ctx, richtext.Converter{}.Fit)
	must(t, err)
	if n != 3 {
		t.Fatalf("fitted %d, want 3", n)
	}
	wantHTML, wantPlain := richtext.Comment.FromMarkdown(md), richtext.Comment.PlainFromMarkdown(md)
	for _, gid := range []string{"t1", "t2", "p1"} {
		op, err := s.GetOp(ctx, id[gid])
		must(t, err)
		if op.Payload["html_text"] != wantHTML || op.Payload["text"] != wantPlain {
			t.Errorf("%s: payload = %q", gid, op.Payload)
		}
		if op.State != OpPending || op.LastError != "" || op.Attempts != 0 || op.Verify {
			t.Errorf("%s: not a fresh pending entry: %+v", gid, op)
		}
	}
	op, err := s.GetOp(ctx, id["t3"])
	must(t, err)
	if op.State != "sending" || op.Payload["html_text"] != oldHTML {
		t.Errorf("an entry being sent was changed: %+v", op)
	}
	// once fitted, nothing changes any more: a failure for another reason stays failed
	must(t, s.MarkOpFailed(ctx, id["t2"], errors.New("403 Forbidden")))
	if n, err := s.FitQueuedRichText(ctx, richtext.Converter{}.Fit); err != nil || n != 0 {
		t.Fatalf("second FitQueuedRichText = %d, %v", n, err)
	}
	if op, _ := s.GetOp(ctx, id["t2"]); op.State != OpFailed || !strings.Contains(op.LastError, "403") {
		t.Fatalf("put back again: %+v", op)
	}
}
