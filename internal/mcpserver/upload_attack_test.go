//go:build unix

package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Ways around the upload policy that the basic cases do not try.
func TestUploadPolicyBypasses(t *testing.T) {
	allowed := t.TempDir()
	appData := filepath.Join(allowed, "appdata")
	token := write(t, filepath.Join(appData, "token.json"))
	p := UploadPolicy{Allowed: []string{allowed}, Denied: []string{appData}}

	hidden := write(t, filepath.Join(allowed, ".secret", "key"))
	visibleLink := filepath.Join(allowed, "visible")
	if err := os.Symlink(filepath.Join(allowed, ".secret"), visibleLink); err != nil {
		t.Fatal(err)
	}
	hiddenFileLink := filepath.Join(allowed, "notes.txt")
	if err := os.Symlink(filepath.Join(allowed, ".env"), hiddenFileLink); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(allowed, ".env"))
	devLink := filepath.Join(allowed, "zero.bin")
	if err := os.Symlink("/dev/zero", devLink); err != nil {
		t.Fatal(err)
	}
	// a link to the parent: allowed/up/<outside name>/...
	upLink := filepath.Join(allowed, "up")
	if err := os.Symlink("..", upLink); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Dir(allowed)

	for _, tc := range []struct{ name, path string }{
		{"symlinked folder whose target is hidden", filepath.Join(visibleLink, "key")},
		{"visible name linking to a hidden file", hiddenFileLink},
		{"hidden file through a path with ./ and //", allowed + "/./sub/..//" + ".secret/key"},
		{"symlink to /dev/zero", devLink},
		{"symlink to the parent folder", filepath.Join(upLink, filepath.Base(outside))},
		{"asagao's files via .. from an allowed folder", allowed + "/x/../appdata/token.json"},
		{"tilde is not expanded", "~/Downloads/" + filepath.Base(hidden)},
		{"empty path", ""},
	} {
		if got, err := p.Check(tc.path); err == nil {
			t.Errorf("%s: %s was allowed (as %s)", tc.name, tc.path, got)
		}
	}
	_ = token

	if runtime.GOOS == "darwin" { // case-insensitive file names
		for _, path := range []string{
			filepath.Join(allowed, "APPDATA", "token.json"),
			filepath.Join(allowed, "AppData", "TOKEN.JSON"),
		} {
			if _, err := os.Stat(path); err != nil {
				continue // case-sensitive volume
			}
			if got, err := p.Check(path); err == nil {
				t.Errorf("%s was allowed (as %s)", path, got)
			}
		}
	}
}

// A hard link has no symlink to resolve: a link in an allowed folder to asagao's token
// (or to any file elsewhere) looks like an ordinary file there.
func TestUploadPolicyHardLinkToDeniedFile(t *testing.T) {
	allowed := t.TempDir()
	appData := filepath.Join(allowed, "appdata")
	token := write(t, filepath.Join(appData, "token.json"))
	hard := filepath.Join(allowed, "receipt.pdf")
	if err := os.Link(token, hard); err != nil {
		t.Skip("hard links not supported:", err)
	}
	p := UploadPolicy{Allowed: []string{allowed}, Denied: []string{appData}}
	if got, err := p.Check(hard); err == nil {
		t.Errorf("hard link to asagao's token was allowed (as %s)", got)
	}
}

// Starting the server in a symlink to the home folder (or to a hidden folder of it) must
// not make the whole home folder uploadable.
func TestUploadPolicyCwdSymlinkToHome(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, "Library", "Keychains", "login.keychain-db"))
	write(t, filepath.Join(home, ".ssh", "id_ed25519"))
	links := t.TempDir()
	toHome := filepath.Join(links, "h")
	toSSH := filepath.Join(links, "s")
	if err := os.Symlink(home, toHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".ssh"), toSSH); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ cwd, file string }{
		{toHome, filepath.Join(home, "Library", "Keychains", "login.keychain-db")},
		{toSSH, filepath.Join(home, ".ssh", "id_ed25519")},
	} {
		p := NewUploadPolicy(tc.cwd, home, nil, nil)
		if got, err := p.Check(tc.file); err == nil {
			t.Errorf("cwd %s (-> %s): %s was allowed (as %s); allowed = %v", tc.cwd, mustEval(tc.cwd), tc.file, got, p.Allowed)
		}
	}
}

func mustEval(p string) string {
	r, _ := filepath.EvalSymlinks(p)
	return r
}

// Through the tool: a FIFO, a folder and an over-limit sparse file in an allowed folder
// are refused quickly and nothing is queued.
func TestUploadToolRefusesSpecialFiles(t *testing.T) {
	st := seed(t)
	allowed := t.TempDir()
	cache := t.TempDir()
	cs := connect(t, &Server{Store: st, CacheDir: cache, Upload: UploadPolicy{Allowed: []string{allowed}}})
	fifo := filepath.Join(allowed, "pipe.pdf")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	big := filepath.Join(allowed, "big.pdf")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(100<<20 + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Mkdir(filepath.Join(allowed, "folder.pdf"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, big, filepath.Join(allowed, "folder.pdf"), allowed} {
		done := make(chan string, 1)
		go func() {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "upload_attachment",
				Arguments: map[string]any{"task_gid": "t1", "path": path}})
			if err != nil {
				done <- "call error: " + err.Error()
				return
			}
			if !res.IsError {
				done <- "accepted"
				return
			}
			done <- ""
		}()
		select {
		case msg := <-done:
			if msg != "" {
				t.Errorf("%s: %s", path, msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: upload_attachment did not return", path)
		}
	}
	d, err := st.GetTask(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Attachments) != 0 {
		t.Errorf("nothing should be attached: %+v", d.Attachments)
	}
	if entries, _ := os.ReadDir(cache); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("nothing should be copied to the cache: %s", strings.Join(names, ", "))
	}
}
