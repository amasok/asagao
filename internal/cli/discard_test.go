package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/ui/format"
)

func TestTakeDiscards(t *testing.T) {
	ctx := context.Background()
	st := createStore(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.Local)
	gid, err := createTask(ctx, st, createOpts{name: "牛乳を買う"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DiscardDeletedTask(ctx, gid, "deleted", now); err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	ds := takeDiscards(ctx, st, &b, true)
	if len(ds) != 1 || !strings.Contains(b.String(), format.DiscardNotice) || !strings.Contains(b.String(), "牛乳を買う") {
		t.Fatalf("discards = %+v, output %q", ds, b.String())
	}
	b.Reset()
	if ds := takeDiscards(ctx, st, &b, true); len(ds) != 0 || b.Len() != 0 {
		t.Fatalf("shown twice: %+v %q", ds, b.String())
	}

	// --json: not printed, but returned and marked
	gid, _ = createTask(ctx, st, createOpts{name: "パンを買う"}, now)
	_, _ = st.DiscardDeletedTask(ctx, gid, "deleted", now)
	if ds := takeDiscards(ctx, st, &b, false); len(ds) != 1 || b.Len() != 0 {
		t.Fatalf("json mode: %+v %q", ds, b.String())
	}
	if ds, _ := st.PendingDiscards(ctx); len(ds) != 0 {
		t.Fatalf("still pending: %+v", ds)
	}
}

func TestPrintDiscardShowsFullText(t *testing.T) {
	var b bytes.Buffer
	printDiscard(&b, store.Discard{Kind: store.OpAddComment, TaskName: "ソファ", Summary: "コメント「1行目 2行目」",
		DiscardedAt: "2026-11-13T01:00:00Z", Payload: map[string]any{"text": "1行目\n2行目\n"}})
	out := b.String()
	for _, want := range []string{"「ソファ」: コメント「1行目 2行目」", "コメント:", "        1行目\n        2行目\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
