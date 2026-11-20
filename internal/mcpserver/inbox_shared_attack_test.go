package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/wire"
)

// #71 (tester): list_inbox answers as before Query.Inbox took over, in each state.
func TestListInboxByState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     store.InboxState
		forbidden bool
		available bool
		notice    string
		notes     int
		unread    int
	}{
		{"never synced", store.InboxState{}, false, false, "同期", 0, 0},
		{"ok", store.InboxState{Status: store.InboxOK}, false, true, "", 3, 2},
		{"ok but read only", store.InboxState{Status: store.InboxOK}, true, true, "notifications:write", 3, 2},
		{"forbidden", store.InboxState{Status: store.InboxForbidden}, false, false, "auth login", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := seedInbox(t, tc.state)
			if tc.forbidden {
				if err := st.SetInboxWriteForbidden(context.Background(), true); err != nil {
					t.Fatal(err)
				}
			}
			cs := connect(t, &Server{App: wire.UseCases(st)})
			var out InboxOutput
			call(t, cs, "list_inbox", map[string]any{}, &out)
			if out.Available != tc.available || len(out.Notifications) != tc.notes || out.Notifications == nil || out.Unread != tc.unread {
				t.Fatalf("list_inbox %+v", out)
			}
			if (tc.notice == "") != (out.Notice == "") || !strings.Contains(out.Notice, tc.notice) {
				t.Fatalf("notice %q, want %q", out.Notice, tc.notice)
			}
			if tc.available && out.Recent != nil {
				t.Fatalf("recent shown while the inbox is available: %+v", out.Recent)
			}
		})
	}
}
