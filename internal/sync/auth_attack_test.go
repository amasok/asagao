package sync

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// A token error that is not "no token" (the keychain cannot be read, ...) does not prove the
// request was not sent: a create waits to be verified against Asana, as before auth.TokenError.
// Wrapped no-token errors still count as not sent.
func TestPushOtherTokenErrorsMaySendAndWrappedNoTokenDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantVerify bool
	}{
		{"keychain unreadable", errors.New("keychain: access denied"), true},
		{"login changed under us", fmt.Errorf("%w", errors.New("login changed")), true},
		{"logged out, wrapped twice", fmt.Errorf("a: %w", fmt.Errorf("b: %w", auth.ErrNotLoggedIn)), false},
		{"replaced, joined with another error", errors.Join(errors.New("x"), auth.ErrLoginReplaced), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, srv, s, st := setupPush(t)
			ctx := context.Background()
			_, _ = st.CreateTaskLocal(ctx, store.NewTask{ProjectGID: "p1", Name: "新しいタスク"}, now)
			bad := asana.NewClient(oauth2.NewClient(ctx, &auth.Source{TokenSource: failingTokens{tc.err}}))
			bad.BaseURL, bad.MaxRetries = srv.URL, 0
			s.API = bad
			s.accountOK.Store(true)
			_, _ = s.Push(ctx)
			ops, _ := st.ListOps(ctx)
			if len(ops) != 1 {
				t.Fatalf("ops = %+v", ops)
			}
			if ops[0].Verify != tc.wantVerify {
				t.Errorf("Verify = %v, want %v (state %s)", ops[0].Verify, tc.wantVerify, ops[0].State)
			}
			if len(ws.calls) != 0 {
				t.Errorf("nothing can reach Asana without a token: %v", ws.calls)
			}
		})
	}
}
