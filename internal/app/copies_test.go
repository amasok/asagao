package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type copyStore struct{ relationStore }

func (f *copyStore) DuplicateTaskLocal(_ context.Context, gid, name string, now time.Time) (string, error) {
	return "local:copy", f.write("DuplicateTaskLocal", "%s %q at=%s", gid, name, now.Format(time.TimeOnly))
}

func (f *copyStore) UploadAttachmentLocal(_ context.Context, taskGID, srcPath, cacheDir string, now time.Time) (string, error) {
	return "local:att", f.write("UploadAttachmentLocal", "%s %s into %s at=%s", taskGID, srcPath, cacheDir, now.Format(time.TimeOnly))
}

func TestCopies(t *testing.T) {
	st := &copyStore{}
	c := &Copies{Store: st, CacheDir: "/cache/attachments", Clock: ticking()}
	ctx := context.Background()
	if gid, err := c.Duplicate(ctx, "t1", ""); gid != "local:copy" || err != nil {
		t.Errorf("Duplicate = %q, %v", gid, err)
	}
	if gid, err := c.Attach(ctx, "t1", "/home/me/図面.png"); gid != "local:att" || err != nil {
		t.Errorf("Attach = %q, %v", gid, err)
	}
	want := []string{`DuplicateTaskLocal t1 "" at=10:00:01`, "UploadAttachmentLocal t1 /home/me/図面.png into /cache/attachments at=10:00:02"}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls\n%s\nwant\n%s", strings.Join(st.calls, "\n"), strings.Join(want, "\n"))
	}
	st = &copyStore{}
	c = &Copies{Store: st, Clock: ticking()}
	st.failWrite = "DuplicateTaskLocal"
	if _, err := c.Duplicate(ctx, "t1", "x"); !errors.Is(err, errWrite) {
		t.Errorf("Duplicate: err = %v, want the store's", err)
	}
	st.failWrite = "UploadAttachmentLocal"
	if _, err := c.Attach(ctx, "t1", "x"); !errors.Is(err, errWrite) {
		t.Errorf("Attach: err = %v, want the store's", err)
	}
}
