package mcpserver

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asana-tui/internal/config"
)

// get_attachment shows an image that is not recorded under any task (copied from a task
// that is not in the cache) from the downloaded file. (#42)
func TestGetAttachmentOfImageCopiedFromAnotherTask(t *testing.T) {
	for _, k := range []string{"XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	st := seed(t)
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	path := filepath.Join(config.CacheDir(), "attachments", "77-copy.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, &Server{Store: st})
	var out AttachmentOutput
	res := call(t, cs, "get_attachment", map[string]any{"gid": "77", "task_gid": "t1"}, &out)
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("result = %s", toolError(res))
	}
	if ic, ok := res.Content[0].(*mcp.ImageContent); !ok || !bytes.Equal(ic.Data, buf.Bytes()) {
		t.Fatalf("first content should be the image: %T", res.Content[0])
	}
	if out.Name != "copy.png" || !out.IsImage || out.LocalPath != path {
		t.Fatalf("output = %+v", out)
	}
	if _, err := st.GetAttachment(context.Background(), "77"); err == nil {
		t.Fatal("should not be recorded under the task")
	}
}
