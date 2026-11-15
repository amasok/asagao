package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUploadPolicy(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	appData := filepath.Join(allowed, "appdata") // e.g. XDG_DATA_HOME inside an allowed folder
	p := UploadPolicy{Allowed: []string{allowed}, Denied: []string{appData}}

	ok := write(t, filepath.Join(allowed, "sub", "見積.pdf"))
	secret := write(t, filepath.Join(outside, "id_ed25519"))
	link := filepath.Join(allowed, "innocent.pdf")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(allowed, "docs")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(allowed, "latest.pdf")
	if err := os.Symlink(ok, inside); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, refused string
	}{
		{"file in an allowed folder", ok, ""},
		{"symlink to a file in an allowed folder", inside, ""},
		{"file outside", secret, "not in a folder that allows uploads"},
		{"symlink pointing outside", link, "not in a folder that allows uploads"},
		{"symlinked folder pointing outside", filepath.Join(dirLink, "id_ed25519"), "not in a folder that allows uploads"},
		{"relative path", "sub/見積.pdf", "absolute path"},
		{"dot-dot escaping the folder", allowed + "/sub/../../" + filepath.Base(outside) + "/id_ed25519", "not in a folder that allows uploads"},
		{"hidden file", write(t, filepath.Join(allowed, ".env")), "hidden"},
		{"file in a hidden folder", write(t, filepath.Join(allowed, ".ssh", "id_rsa")), "hidden"},
		{"asana-tui's own files", write(t, filepath.Join(appData, "store.db")), "asana-tui itself"},
		{"missing file", filepath.Join(allowed, "nope.pdf"), "no such file"},
	} {
		got, err := p.Check(tc.path)
		switch {
		case tc.refused == "" && err != nil:
			t.Errorf("%s: refused: %v", tc.name, err)
		case tc.refused == "" && filepath.Base(got) != "見積.pdf":
			t.Errorf("%s: resolved to %s", tc.name, got)
		case tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)):
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.refused)
		}
	}
	if runtime.GOOS == "darwin" { // case-insensitive file names must not slip past the deny list
		upper := filepath.Join(allowed, "APPDATA", "store.db")
		if _, err := p.Check(upper); err == nil || !strings.Contains(err.Error(), "asana-tui itself") {
			t.Errorf("%s: err = %v", upper, err)
		}
	}
	if _, err := (UploadPolicy{}).Check(ok); err == nil {
		t.Error("an empty policy must refuse everything")
	}
}

func TestDefaultUploadFolders(t *testing.T) {
	home := "/Users/me"
	for _, tc := range []struct {
		cwd    string
		usable bool
	}{
		{"/", false},
		{"/Users", false},
		{home, false},
		{home + "/.ssh", false},
		{home + "/.config/asana-tui", false},
		{home + "/src/project", true},
		{home + "/src/project/.worktrees/x", true},
		{"/opt/work", true},
	} {
		p := NewUploadPolicy(tc.cwd, home, []string{"/data/receipts"}, nil)
		want := []string{home + "/Downloads", home + "/Desktop", "/data/receipts"}
		if tc.usable {
			want = append([]string{tc.cwd}, want...)
		}
		if strings.Join(p.Allowed, ",") != strings.Join(want, ",") {
			t.Errorf("cwd %s: allowed %v, want %v", tc.cwd, p.Allowed, want)
		}
	}
}

// The MCP tool applies the policy and is marked destructive (its data leaves the machine).
func TestUploadAttachmentTool(t *testing.T) {
	st := seed(t)
	allowed, outside := t.TempDir(), t.TempDir()
	cs := connect(t, &Server{Store: st, CacheDir: t.TempDir(), Upload: UploadPolicy{Allowed: []string{allowed}}})
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "upload_attachment" {
			if a := tool.Annotations; a == nil || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("upload_attachment should have DestructiveHint: %+v", a)
			}
			if !strings.Contains(tool.Description, allowed) {
				t.Errorf("description should list the allowed folders: %q", tool.Description)
			}
		}
	}
	secret := write(t, filepath.Join(outside, "token.json"))
	res := call(t, cs, "upload_attachment", map[string]any{"task_gid": "t1", "path": secret}, nil)
	if !res.IsError || !strings.Contains(toolError(res), "not in a folder that allows uploads") {
		t.Fatalf("upload outside the allowed folders: %q", toolError(res))
	}
	if d, _ := st.GetTask(context.Background(), "t1"); len(d.Attachments) != 0 {
		t.Errorf("nothing should be attached: %+v", d.Attachments)
	}
	var w WriteOutput
	res = call(t, cs, "upload_attachment", map[string]any{"task_gid": "t1", "path": write(t, filepath.Join(allowed, "a.pdf"))}, &w)
	if res.IsError {
		t.Fatalf("upload in an allowed folder: %s", toolError(res))
	}
}
