package auth

import (
	"testing"

	"github.com/amasok/asagao/internal/testguard"
)

// TestMain keeps the tests away from the user's own folders and keychain (Issue #53).
func TestMain(m *testing.M) { testguard.Main(m) }
