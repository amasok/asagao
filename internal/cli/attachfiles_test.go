package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/xdg"
)

// A dry run of deleting an attachment works on a copy of the store, whose recorded file
// is the real one: it must not delete it.
func TestDetachDryRunKeepsTheDownloadedFile(t *testing.T) {
	path, _, run := dryRunEnv(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	withComments(t, path)
	dir := filepath.Join(xdg.CacheDir(), "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a1-図面.pdf")
	if err := os.WriteFile(file, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAttachmentPath(context.Background(), "a1", file); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if out, errOut, err := run("task", "detach", "a1", "--dry-run"); err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a dry run deleted the downloaded file: %v", err)
	}
}
