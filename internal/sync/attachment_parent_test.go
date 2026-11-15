package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// An attachment fetched by gid is recorded under the task Asana says it belongs to, not
// under whatever task it was asked for with. An image whose task is not in the store (e.g.
// copied from another task) is still downloaded and shown, but not recorded. (#42)
func TestDownloadAttachmentChecksParent(t *testing.T) {
	ctx := context.Background()
	parents := map[string]string{"11": `{"gid":"t-unknown"}`, "12": `null`, "13": `{"gid":"t2"}`, "14": `{"gid":"t1"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gid := filepath.Base(r.URL.Path)
		fmt.Fprintf(w, `{"data":{"gid":%q,"name":"x %s.png","host":"asana","download_url":"https://files.example/%s","parent":%s}}`, gid, gid, gid, parents[gid])
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T1"}, {GID: "t2", Name: "T2"}}); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	cache := t.TempDir()
	var downloads []string
	s := &Syncer{API: api, Store: st, CacheDir: cache, Download: func(_ context.Context, url string) ([]byte, error) {
		downloads = append(downloads, url)
		return []byte("img " + url), nil
	}}
	offline := &Syncer{Store: st, CacheDir: cache}

	// task not in the store, or no parent: shown, not recorded; offline the cached file is used
	for _, gid := range []string{"11", "12"} {
		path, err := s.DownloadAttachment(ctx, "t1", gid)
		if err != nil {
			t.Fatalf("%s: an image copied from another task should be shown: %v", gid, err)
		}
		if b, _ := os.ReadFile(path); string(b) != "img https://files.example/"+gid || filepath.Base(path) != gid+"-x_"+gid+".png" {
			t.Fatalf("%s: path %q content %q", gid, path, b)
		}
		if _, err := st.GetAttachment(ctx, gid); err == nil {
			t.Fatalf("%s: should not be recorded under a task", gid)
		}
		n := len(downloads)
		if p2, err := s.DownloadAttachment(ctx, "t1", gid); err != nil || p2 != path || len(downloads) != n {
			t.Fatalf("%s: second call should reuse the file: %q %v (%d downloads)", gid, p2, err, len(downloads))
		}
		if p3, err := offline.DownloadAttachment(ctx, "t1", gid); err != nil || p3 != path {
			t.Fatalf("%s: offline should use the cached file: %q %v", gid, p3, err)
		}
	}
	// the real parent is in the store: recorded under it, not under the task asked with
	for _, gid := range []string{"13", "14"} {
		if _, err := s.DownloadAttachment(ctx, "t1", gid); err != nil {
			t.Fatal(err)
		}
	}
	for task, want := range map[string]string{"t1": "14", "t2": "13"} {
		d, err := st.GetTask(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Attachments) != 1 || d.Attachments[0].GID != want || d.Attachments[0].LocalPath == "" {
			t.Fatalf("%s attachments = %+v, want only %s (downloaded)", task, d.Attachments, want)
		}
	}
	// a gid that is not a number is refused before it is used in a path
	if _, err := offline.DownloadAttachment(ctx, "t1", "../11"); err == nil {
		t.Fatal("a gid with a path should be refused")
	}
}
