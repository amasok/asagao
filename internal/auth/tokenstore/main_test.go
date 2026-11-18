package tokenstore

import (
	"testing"

	"github.com/amasok/asagao/internal/testguard"
)

// The stores of New keep a lock file in the config folder, and the keychain is the mock of
// init (keychain.go): HOME and the XDG folders are a private temporary directory, so the
// tests never write to the user's ~/.config or keychain (Issue #53).
func TestMain(m *testing.M) { testguard.Main(m) }
