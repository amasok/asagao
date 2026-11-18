package main

import (
	"testing"

	"github.com/amasok/asagao/internal/testguard"
)

// TestMain keeps the tests away from the user's own folders and keychain (Issue #53). The
// tests build a fake ~/asagao-test in t.TempDir(); only the program (go run) uses the real
// one, behind CheckEnv.
func TestMain(m *testing.M) { testguard.Main(m) }
