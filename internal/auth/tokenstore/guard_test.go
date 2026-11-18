package tokenstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// In a test binary the keychain is the in-memory mock from the start (init, before
// TestMain): the tests of every package that reaches a Keychain never read or write the
// user's entries under asagao or asana-tui (Issue #53).
func TestKeychainIsTheMockInTests(t *testing.T) {
	if !keychainMocked {
		t.Fatal("init did not put go-keyring's mock in place: tests would use the real keychain")
	}
	ctx := context.Background()
	for _, svc := range []string{"asagao", "asana-tui"} {
		kc := &Keychain{Service: svc, User: "testguard-probe"}
		if _, err := kc.Load(ctx); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: the mock should start empty: %v", svc, err)
		}
	}
}

// Without a run of its own, the 1Password store does not start the real op in a test: it
// would read or write the user's vault (Issue #53).
func TestOnePasswordDoesNotRunTheRealOpInTests(t *testing.T) {
	// No op on PATH either, so that even without the guard this test cannot reach the
	// user's vault: it fails on "not found" instead.
	t.Setenv("PATH", "")
	o := &OnePassword{Vault: "Private", Item: "asagao"}
	for name, call := range map[string]func() error{
		"load":   func() error { _, err := o.Load(context.Background()); return err },
		"save":   func() error { return o.Save(context.Background(), &Credentials{PersonalAccessToken: "x"}) },
		"delete": func() error { return o.Delete(context.Background()) },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "not run in tests") {
			t.Errorf("%s: %v, want the refusal to run op in tests", name, err)
		}
	}
}
