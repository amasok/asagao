package tokenstore

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The stores of New keep a lock file in the config folder: point the XDG folders at a
// private temporary directory, so the tests never write to the user's ~/.config.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "asagao-tokenstore-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		os.Setenv(k, filepath.Join(tmp, k))
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
