package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

type notificationStore struct{ relationStore }

func (f *notificationStore) ResolveNotification(_ context.Context, arg string) (string, error) {
	f.log("ResolveNotification %s", arg)
	if g, ok := map[string]string{"0001": "1200001", "1300002": "1300002"}[arg]; ok {
		return g, nil
	}
	return "", fmt.Errorf("通知 %s: %w", arg, errNotFound)
}

func (f *notificationStore) MarkNotificationsLocal(_ context.Context, gids []string, read bool, now time.Time) (int, error) {
	return len(gids), f.write("MarkNotificationsLocal", "%v read=%v at=%s", gids, read, now.Format(time.TimeOnly))
}

func TestNotificationsMark(t *testing.T) {
	st := &notificationStore{}
	n := &Notifications{Store: st, Clock: ticking()}
	ctx := context.Background()
	if c, err := n.Mark(ctx, []string{"0001", "1300002"}, true); c != 2 || err != nil {
		t.Fatalf("Mark = %d, %v", c, err)
	}
	if c, err := n.MarkHere(ctx, []string{"1200001"}, false); c != 1 || err != nil {
		t.Fatalf("MarkHere = %d, %v", c, err)
	}
	want := []string{"ResolveNotification 0001", "ResolveNotification 1300002",
		"MarkNotificationsLocal [1200001 1300002] read=true at=10:00:01",
		"MarkNotificationsLocal [1200001] read=false at=10:00:02"}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}

	// one that does not resolve: the store's error, and nothing is marked
	st = &notificationStore{}
	n = &Notifications{Store: st, Clock: ticking()}
	if c, err := n.Mark(ctx, []string{"0001", "9999", "1300002"}, true); c != 0 || !errors.Is(err, errNotFound) {
		t.Errorf("Mark = %d, %v", c, err)
	}
	if w := st.writes(); len(w) != 0 {
		t.Errorf("wrote: %v", w)
	}
	if gids, err := n.Resolve(ctx, nil); err != nil || gids == nil || len(gids) != 0 {
		t.Errorf("Resolve(nil) = %#v, %v", gids, err)
	}
	st.failWrite = "MarkNotificationsLocal"
	if _, err := n.MarkHere(ctx, []string{"1200001"}, true); !errors.Is(err, errWrite) {
		t.Errorf("a failed write: %v", err)
	}
}
