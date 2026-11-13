package store

import (
	"context"
	"testing"
	"time"
)

func TestRecentDiscards(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for _, gid := range []string{"t1", "t2", "t3"} {
		if _, err := s.AddCommentLocal(ctx, gid, "メモ "+gid, UserRef{GID: "me"}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DiscardDeletedTask(ctx, gid, "deleted", now); err != nil {
			t.Fatal(err)
		}
	}
	pending, _ := s.PendingDiscards(ctx)
	if err := s.MarkDiscardsNotified(ctx, []int64{pending[0].ID}); err != nil {
		t.Fatal(err)
	}
	got, err := s.RecentDiscards(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Payload["text"] != "メモ t3" || got[1].Payload["text"] != "メモ t2" {
		t.Fatalf("newest first, limited: %+v", got)
	}
	all, _ := s.RecentDiscards(ctx, 10)
	if len(all) != 3 || all[2].Payload["text"] != "メモ t1" {
		t.Fatalf("notified ones are listed too: %+v", all)
	}
}
