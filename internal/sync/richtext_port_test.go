package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// A Syncer built without RichText does not send a rich comment unchecked: Push fails with
// store.ErrNoFit and the entry stays queued as it was. With RichText it is sent.
func TestPushWithoutRichTextFails(t *testing.T) {
	_, _, s, st := setupPush(t)
	ctx := context.Background()
	if _, err := st.AddRichCommentLocal(ctx, "t1", "見出し", "<body><h1>見出し</h1></body>", store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListOps(ctx)
	if err != nil || len(before) != 1 {
		t.Fatalf("ops = %+v, %v", before, err)
	}
	if _, err := s.Push(ctx); !errors.Is(err, store.ErrNoFit) {
		t.Fatalf("Push without RichText: %v, want store.ErrNoFit", err)
	}
	after, err := st.ListOps(ctx)
	if err != nil || len(after) != 1 || after[0].State != store.OpPending || after[0].Payload["html_text"] != before[0].Payload["html_text"] || after[0].Attempts != 0 {
		t.Fatalf("the entry changed: %+v, %v", after, err)
	}

	s.RichText = richtext.Converter{}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("Push with RichText = %+v, %v", res, err)
	}
}
