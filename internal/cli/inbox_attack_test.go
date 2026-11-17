package cli

import (
	"context"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// --all marks only the notifications that are not in the wanted state yet, one change
// for each, and marking again queues nothing more.
func TestInboxMarkAllTakesOnlyTheOnesInTheOtherState(t *testing.T) {
	st, _ := inboxStore(t, store.InboxState{Status: store.InboxOK})
	ctx := context.Background()
	marks := func() []string {
		ops, _ := st.ListOps(ctx)
		var out []string
		for _, op := range ops {
			if op.Kind == store.OpMarkNotification {
				out = append(out, op.TaskGID)
			}
		}
		slices.Sort(out)
		return out
	}
	if out, err := runCLI(t, "inbox", "read", "--all"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := marks(); !slices.Equal(got, []string{"1200001", "1300002"}) {
		t.Errorf("read --all: marks = %v", got)
	}
	if out, err := runCLI(t, "inbox", "read", "--all"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := marks(); !slices.Equal(got, []string{"1200001", "1300002"}) {
		t.Errorf("read --all again: marks = %v", got)
	}
	if out, err := runCLI(t, "inbox", "unread", "--all"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n, _ := st.UnreadNotifications(ctx); n != 3 {
		t.Errorf("unread = %d, want all 3", n)
	}
}
