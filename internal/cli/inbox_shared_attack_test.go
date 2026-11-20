package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// #71 (tester): the inbox command prints what it did before Query.Inbox took over, in every
// state of the inbox: never synced (Status ""), readable but not writable, and unavailable.
func TestInboxJSONShapeByState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     store.InboxState
		available bool
		notice    string // a part of it, "" for none
		wantNotes int
		recent    bool
	}{
		{"never synced", store.InboxState{}, false, "同期", 0, true},
		{"ok", store.InboxState{Status: store.InboxOK}, true, "", 3, false},
		{"ok but read only", store.InboxState{Status: store.InboxOK, WriteForbidden: true}, true, "notifications:write", 3, false},
		{"payment", store.InboxState{Status: store.InboxPayment}, false, "プラン", 0, true},
		{"error", store.InboxState{Status: store.InboxError, Error: "boom"}, false, "boom", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := inboxStore(t, tc.state)
			if tc.state.WriteForbidden {
				if err := st.SetInboxWriteForbidden(context.Background(), true); err != nil {
					t.Fatal(err)
				}
			}
			raw, _, err := runCLISplit(t, "--json", "inbox")
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(raw), &m); err != nil {
				t.Fatalf("json %q: %v", raw, err)
			}
			ns, isList := m["notifications"].([]any)
			if !isList { // must be [] (never null)
				t.Fatalf("notifications = %v, want a list: %s", m["notifications"], raw)
			}
			if len(ns) != tc.wantNotes {
				t.Errorf("notifications = %d, want %d: %s", len(ns), tc.wantNotes, raw)
			}
			if m["available"] != tc.available {
				t.Errorf("available = %v: %s", m["available"], raw)
			}
			notice, _ := m["notice"].(string)
			if (tc.notice == "") != (notice == "") || !strings.Contains(notice, tc.notice) {
				t.Errorf("notice = %q, want it to contain %q", notice, tc.notice)
			}
			_, hasRecent := m["recent"]
			if hasRecent != tc.recent {
				t.Errorf("recent present = %v, want %v: %s", hasRecent, tc.recent, raw)
			}
			if !tc.available {
				if u, _ := m["unread"].(float64); u != 0 {
					t.Errorf("unread = %v while unavailable", m["unread"])
				}
			}
		})
	}
}
