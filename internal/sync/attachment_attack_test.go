package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

func attachmentAttackSetup(t *testing.T, h http.HandlerFunc) (*Syncer, *store.Store, string, *[]string) {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T1"}}); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	cache := t.TempDir()
	var downloads []string
	s := &Syncer{API: api, Store: st, CacheDir: cache, Download: func(_ context.Context, url string) ([]byte, error) {
		downloads = append(downloads, url)
		return []byte("img"), nil
	}}
	return s, st, cache, &downloads
}

// #42 (tester): Asana (or something answering as it) returns an attachment whose gid is not
// the one asked for, with a cached task as parent. Recording it does not make the asked
// gid known, so this must end with an error, not recurse until the stack overflows.
func TestDownloadAttachmentAnswerWithOtherGIDDoesNotLoop(t *testing.T) {
	var calls atomic.Int64
	s, st, _, _ := attachmentAttackSetup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"data":{"gid":"77","name":"x.png","host":"asana","download_url":"https://files.example/77","parent":{"gid":"t1"}}}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.DownloadAttachment(ctx, "t1", "11")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			if _, gerr := st.GetAttachment(context.Background(), "11"); gerr != nil {
				t.Fatalf("no error, but attachment 11 was not recorded either")
			}
		}
	case <-ctx.Done():
		t.Fatalf("DownloadAttachment did not return (%d API calls): it loops", calls.Load())
	}
	if n := calls.Load(); n > 3 {
		t.Fatalf("%d API calls for one attachment: it loops", n)
	}
}

// A name from Asana cannot place the cached file outside the cache folder.
func TestDownloadUnknownAttachmentNameCannotEscapeCache(t *testing.T) {
	for _, name := range []string{"../../evil.png", `..\..\evil.png`, "/etc/evil.png", "..", "a/../../b.png"} {
		t.Run(name, func(t *testing.T) {
			s, _, cache, _ := attachmentAttackSetup(t, func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"data":{"gid":"12","name":%q,"host":"asana","download_url":"https://files.example/12","parent":null}}`, name)
			})
			path, err := s.DownloadAttachment(context.Background(), "t1", "12")
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(path) != filepath.Clean(cache) || !strings.HasPrefix(filepath.Base(path), "12-") {
				t.Fatalf("path %q is not a file directly in %q", path, cache)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// #42 (tester): an image pasted into t1's description but copied from a task that is not
// cached is shown (downloaded), shown again offline from the cache without asking Asana,
// and never listed among t1's attachments.
func TestCopiedImageFromUncachedTaskStillShows(t *testing.T) {
	var calls atomic.Int64
	s, st, cache, downloads := attachmentAttackSetup(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"data":{"gid":"55","name":"copied.png","host":"asana","download_url":"https://files.example/55","parent":{"gid":"t-elsewhere"}}}`)
	})
	ctx := context.Background()
	path, err := s.DownloadAttachment(ctx, "t1", "55")
	if err != nil {
		t.Fatalf("copied image not shown: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "img" || filepath.Dir(path) != filepath.Clean(cache) {
		t.Fatalf("path %q content %q %v", path, b, err)
	}
	if d, err := st.GetTask(ctx, "t1"); err != nil || len(d.Attachments) != 0 {
		t.Fatalf("t1 attachments = %+v %v: a copied image must not be listed", d.Attachments, err)
	}
	if _, err := st.GetAttachment(ctx, "55"); err == nil {
		t.Fatal("copied image recorded")
	}
	before, n := calls.Load(), len(*downloads)
	if p, err := s.DownloadAttachment(ctx, "t1", "55"); err != nil || p != path || len(*downloads) != n || calls.Load() != before {
		t.Fatalf("second view should use the cached file: %q %v (api %d->%d, downloads %d->%d)", p, err, before, calls.Load(), n, len(*downloads))
	}
	offline := &Syncer{Store: st, CacheDir: cache}
	if p, err := offline.DownloadAttachment(ctx, "t1", "55"); err != nil || p != path {
		t.Fatalf("offline: %q %v", p, err)
	}
	if _, err := offline.DownloadAttachment(ctx, "t1", "56"); err == nil {
		t.Fatal("offline, an image never downloaded cannot be shown")
	}
	for _, bad := range []string{"", "55/../x", "-1", "５５"} {
		if _, err := s.DownloadAttachment(ctx, "t1", bad); err == nil {
			t.Errorf("gid %q accepted", bad)
		}
	}
}
