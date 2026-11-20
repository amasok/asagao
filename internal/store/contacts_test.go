package store

import (
	"context"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// ListUserContacts gives each known user's name and email (for quick add's "@name"), me
// first, without users that have neither (u4; "me" comes from newTestStore), and with Asana's text made terminal-safe.
func TestListUserContacts(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.UpsertUsers(ctx, []asana.User{
		{GID: "u1", Name: "Bob", Email: "bob@example.com"},
		{GID: "u2", Name: "Alice\x1b]0;x\x07", Email: "alice\x1b[2J@example.com"},
		{GID: "u3", Email: "ghost@example.com"},
		{GID: "u4"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSyncState(ctx, keyMeGID, "u1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListUserContacts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var gids []string
	for _, u := range got {
		gids = append(gids, u.GID)
		assertSafe(t, "user name", u.Name)
		assertSafe(t, "user email", u.Email)
	}
	if !slices.Equal(gids, []string{"u1", "u3", "u2", "me"}) {
		t.Fatalf("users %+v", got)
	}
	if got[0] != (UserContact{GID: "u1", Name: "Bob", Email: "bob@example.com"}) || got[1] != (UserContact{GID: "u3", Email: "ghost@example.com"}) {
		t.Errorf("users %+v", got)
	}
}
