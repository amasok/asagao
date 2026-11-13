package tokenstore

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeOP emulates the subset of `op item get/create/edit/delete` the store uses.
type fakeOP struct {
	item  map[string]any // nil when the item does not exist
	calls [][]string
}

func (f *fakeOP) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	for _, a := range args {
		if strings.Contains(a, "secret-") {
			panic("secret passed as a command-line argument: " + a)
		}
	}
	switch args[1] {
	case "get":
		if f.item == nil {
			return nil, &opError{msg: `[ERROR] "asana-tui" isn't an item in the "Private" vault.`}
		}
		return json.Marshal(f.item)
	case "create":
		if err := json.Unmarshal(stdin, &f.item); err != nil {
			return nil, err
		}
		f.item["id"] = "abc123"
		return nil, nil
	case "edit":
		if args[2] != "abc123" {
			panic("edit by unexpected id " + args[2])
		}
		f.item = nil
		return nil, json.Unmarshal(stdin, &f.item)
	case "delete":
		if f.item == nil {
			return nil, &opError{msg: `[ERROR] "asana-tui" isn't an item in the "Private" vault.`}
		}
		f.item = nil
		return nil, nil
	}
	panic("unexpected op call " + strings.Join(args, " "))
}

func TestOnePasswordRoundTrip(t *testing.T) {
	ctx := context.Background()
	fake := &fakeOP{}
	s := &OnePassword{Vault: "Private", Item: "asana-tui", run: fake.run}

	if _, err := s.Load(ctx); err != ErrNotFound {
		t.Fatalf("Load before save: %v", err)
	}
	in := &Credentials{ClientSecret: "secret-cs", RefreshToken: "secret-rt1", AccessToken: "secret-at", UserGID: "42", UserName: "me"}
	if err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.calls[len(fake.calls)-1], "create") {
		t.Fatalf("first save should create: %v", fake.calls)
	}

	// An unrelated field added by the user in 1Password must survive an edit.
	fake.item["notes"] = "keep me"
	in.RefreshToken = "secret-rt2"
	if err := s.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(fake.calls[len(fake.calls)-1], "edit") {
		t.Fatalf("second save should edit: %v", fake.calls)
	}
	if fake.item["notes"] != "keep me" {
		t.Fatalf("extra item data lost: %v", fake.item)
	}

	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshToken != "secret-rt2" || got.ClientSecret != "secret-cs" || got.UserGID != "42" {
		t.Fatalf("got %+v", got)
	}
	if got.AccessToken != "" {
		t.Fatalf("access token must not be stored in 1Password")
	}

	if err := s.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx); err != nil {
		t.Fatalf("delete of missing item should succeed: %v", err)
	}
}

func TestFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := &File{Path: filepath.Join(t.TempDir(), "sub", "token.json")}
	if _, err := s.Load(ctx); err != ErrNotFound {
		t.Fatalf("Load before save: %v", err)
	}
	if err := s.Save(ctx, &Credentials{RefreshToken: "rt", AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil || got.RefreshToken != "rt" || got.AccessToken != "at" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if err := s.Delete(ctx); err != nil {
		t.Fatal(err)
	}
}
