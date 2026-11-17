package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/richtext"
)

var fitNow = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

// A failed entry that holds nothing Asana rejects (a 403, say) is not touched: not put back,
// not counted, whatever else is in its html (#71).
func TestFitQueuedRichTextLeavesOtherFailures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	html := "<body>ふつう <strong>太字</strong> a &lt;h1&gt;</body>"
	_, err := s.AddRichCommentLocal(ctx, "t1", "ふつう", html, UserRef{GID: "me"}, fitNow)
	must(t, err)
	ops, _ := s.ListOps(ctx)
	id := ops[0].ID
	ok, err := s.BeginSend(ctx, id, fitNow)
	must(t, err)
	if !ok {
		t.Fatal("not begun")
	}
	must(t, s.CountAttempt(ctx, id, fitNow))
	must(t, s.MarkOpFailed(ctx, id, errors.New("403 Forbidden")))
	for i := 0; i < 3; i++ {
		if n, err := s.FitQueuedRichText(ctx); err != nil || n != 0 {
			t.Fatalf("Fit = %d, %v", n, err)
		}
	}
	op, _ := s.GetOp(ctx, id)
	if op.State != OpFailed || op.Attempts != 1 || !strings.Contains(op.LastError, "403") || op.Payload["html_text"] != html {
		t.Fatalf("touched: %+v", op)
	}
}

// Other kinds are never rewritten, even when their payload has a layout tag in html_text,
// and an entry with no or non-string html_text does not break the pass.
func TestFitQueuedRichTextIgnoresOtherEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	must(t, s.UpdateTaskLocal(ctx, "t1", map[string]any{"html_notes": "<body><h1>説明</h1></body>"}, fitNow))
	_, err := s.AddCommentLocal(ctx, "t2", "plain", UserRef{GID: "me"}, fitNow)
	must(t, err)
	ops, _ := s.ListOps(ctx)
	before := map[int64]Op{}
	for _, op := range ops {
		before[op.ID] = op
	}
	// html_text of the wrong type
	if _, err := s.db.ExecContext(ctx, `UPDATE outbox SET payload = json_set(payload, '$.html_text', 42) WHERE kind = ?`, OpAddComment); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FitQueuedRichText(ctx); err != nil || n != 0 {
		t.Fatalf("Fit = %d, %v", n, err)
	}
	for _, op := range func() []Op { o, _ := s.ListOps(ctx); return o }() {
		if op.Kind == OpUpdateTask && op.Payload["html_notes"] != before[op.ID].Payload["html_notes"] {
			t.Fatalf("task notes rewritten: %+v", op.Payload)
		}
	}
}

// Fitting while entries are being sent and sent again from several goroutines: every
// pending entry is fitted exactly once, and one that began sending is never rewritten.
func TestFitQueuedRichTextConcurrent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	md := "# 見出し\n\n本文"
	oldHTML, oldPlain := richtext.TaskNotes.FromMarkdown(md), richtext.TaskNotes.PlainFromMarkdown(md)
	const n = 12
	for i := 0; i < n; i++ {
		_, err := s.AddRichCommentLocal(ctx, "t1", oldPlain, oldHTML, UserRef{GID: "me"}, fitNow.Add(time.Duration(i)*time.Second))
		must(t, err)
	}
	ops, _ := s.ListOps(ctx)
	sending := map[int64]bool{}
	for i, op := range ops {
		if i%4 == 0 {
			if ok, err := s.BeginSend(ctx, op.ID, fitNow); err != nil || !ok {
				t.Fatalf("BeginSend: %v %v", ok, err)
			}
			sending[op.ID] = true
		}
	}
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := s.FitQueuedRichText(ctx)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total += c
			mu.Unlock()
		}()
	}
	wg.Wait()
	if want := n - len(sending); total != want {
		t.Fatalf("fitted %d in total, want %d", total, want)
	}
	ops, _ = s.ListOps(ctx)
	for _, op := range ops {
		isOld := op.Payload["html_text"] == oldHTML
		if sending[op.ID] != isOld {
			t.Errorf("op %d (sending=%v): old html kept = %v", op.ID, sending[op.ID], isOld)
		}
	}
}
