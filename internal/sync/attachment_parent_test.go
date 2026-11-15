package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// An attachment fetched by gid is recorded under the task Asana says it belongs to, not
// under whatever task it was asked for with. (#42)
func TestDownloadAttachmentChecksParent(t *testing.T) {
	ctx := context.Background()
	parents := map[string]string{"a-other": `{"gid":"t-unknown"}`, "a-none": `null`, "a-t2": `{"gid":"t2"}`, "a-t1": `{"gid":"t1"}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gid := filepath.Base(r.URL.Path)
		fmt.Fprintf(w, `{"data":{"gid":%q,"name":"x.png","host":"asana","download_url":"https://files.example/%s","parent":%s}}`, gid, gid, parents[gid])
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
	var downloads int
	s := &Syncer{API: api, Store: st, CacheDir: t.TempDir(), Download: func(context.Context, string) ([]byte, error) {
		downloads++
		return []byte("img"), nil
	}}

	for _, gid := range []string{"a-other", "a-none"} {
		if _, err := s.DownloadAttachment(ctx, "t1", gid); err == nil {
			t.Fatalf("%s: attachment of another task should be refused", gid)
		}
		if _, err := st.GetAttachment(ctx, gid); err == nil {
			t.Fatalf("%s: should not be recorded", gid)
		}
	}
	if downloads != 0 {
		t.Fatalf("refused attachments were downloaded %d times", downloads)
	}
	// an image copied from another cached task is shown, but recorded under that task
	if _, err := s.DownloadAttachment(ctx, "t1", "a-t2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DownloadAttachment(ctx, "t1", "a-t1"); err != nil {
		t.Fatal(err)
	}
	for task, want := range map[string]string{"t1": "a-t1", "t2": "a-t2"} {
		d, err := st.GetTask(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Attachments) != 1 || d.Attachments[0].GID != want {
			t.Fatalf("%s attachments = %+v, want only %s", task, d.Attachments, want)
		}
	}
}
