package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
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

// An attachment downloaded before without a record is taken from the cache without asking
// Asana, so it shows when the network is down even though the syncer is logged in. (#42)
func TestUnrecordedAttachmentUsesCacheWhenAPIFails(t *testing.T) {
	ctx := context.Background()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	srv.Close() // the network is down: requests fail to connect
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T1"}})
	cache := t.TempDir()
	path := filepath.Join(cache, "21-copy.png")
	if err := os.WriteFile(path, []byte("img"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st, CacheDir: cache,
		Download: func(context.Context, string) ([]byte, error) { t.Fatal("no download expected"); return nil, nil }}
	if p, err := s.DownloadAttachment(ctx, "t1", "21"); err != nil || p != path {
		t.Fatalf("path %q %v", p, err)
	}
	if calls != 0 {
		t.Fatalf("Asana was asked %d times for a cached attachment", calls)
	}
	if _, err := s.DownloadAttachment(ctx, "t1", "22"); err == nil {
		t.Fatal("an attachment that is neither cached nor reachable should fail")
	}
}

// Names from Asana that reach error messages (shown in the TUI) carry no terminal control
// characters. (#42)
func TestUnrecordedAttachmentErrorIsTerminalSafe(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gid := filepath.Base(r.URL.Path)
		url := `"https://files.example/x"`
		if gid == "31" {
			url = `""` // not downloadable: the name and host are in the error
		}
		fmt.Fprintf(w, `{"data":{"gid":%q,"name":"a\u001b]0;evil\u0007\u001b[2Jb.png","host":"h\u001b[31mx","download_url":%s,"parent":null}}`, gid, url)
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	s := &Syncer{API: api, Store: st, CacheDir: t.TempDir(),
		Download: func(context.Context, string) ([]byte, error) { return nil, fmt.Errorf("HTTP 500") }}
	for _, gid := range []string{"31", "32"} {
		_, err := s.DownloadAttachment(ctx, "t1", gid)
		if err == nil {
			t.Fatalf("%s: expected an error", gid)
		}
		if strings.ContainsAny(err.Error(), "\x1b\x07") {
			t.Fatalf("%s: error has control characters: %q", gid, err.Error())
		}
	}
}
