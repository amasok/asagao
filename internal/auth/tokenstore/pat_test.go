package tokenstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// A PAT survives a save and load in the file and keychain stores, and switching between
// OAuth and PAT leaves no token of the other method behind.
func TestPATRoundTripAndSwitch(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	for _, s := range []Store{
		&File{Path: filepath.Join(t.TempDir(), "token.json")},
		&Keychain{Service: "asagao-test", User: "pat"},
	} {
		t.Run(s.Name(), func(t *testing.T) {
			oauth := &Credentials{ClientSecret: "cs", RefreshToken: "rt", AccessToken: "at", UserGID: "1"}
			if err := s.Save(ctx, oauth); err != nil {
				t.Fatal(err)
			}
			pat := &Credentials{PersonalAccessToken: "2/123:abc", ClientSecret: "cs", UserGID: "2", UserName: "P", UserEmail: "p@example.com"}
			if err := s.Save(ctx, pat); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load(ctx)
			if err != nil || *got != *pat || got.Method() != MethodPAT {
				t.Fatalf("Load = %+v, %v; want %+v", got, err, pat)
			}
			if got.RefreshToken != "" || got.AccessToken != "" {
				t.Fatalf("the OAuth tokens should be gone after switching to a PAT: %+v", got)
			}
			if err := s.Save(ctx, oauth); err != nil {
				t.Fatal(err)
			}
			if got, err := s.Load(ctx); err != nil || got.Method() != MethodOAuth || got.PersonalAccessToken != "" {
				t.Fatalf("after switching back to OAuth: %+v, %v", got, err)
			}
			if err := s.Delete(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(ctx); err != ErrNotFound {
				t.Fatalf("Load after Delete: %v", err)
			}
		})
	}
}

// A token file written before PAT support (no personal_access_token key) is an OAuth login.
func TestFileFromOlderVersionIsOAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte(`{"client_secret":"cs","refresh_token":"rt","user_gid":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := (&File{Path: path}).Load(context.Background())
	if err != nil || c.Method() != MethodOAuth || c.RefreshToken != "rt" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}
