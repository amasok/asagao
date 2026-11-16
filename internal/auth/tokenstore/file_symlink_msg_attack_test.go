//go:build unix

package tokenstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The refusal names the real target of a chain of links and a chmod command that can be
// pasted into a shell as it is; nothing in it can run a command or move the cursor (#73).
func TestSymlinkRefusalMessage(t *testing.T) {
	ctx := context.Background()
	t.Run("chain: the real target, not the first link", func(t *testing.T) {
		base := t.TempDir()
		real := filepath.Join(base, "real")
		os.Mkdir(real, 0o700)
		os.Chmod(real, 0o777)
		symlink(t, real, filepath.Join(base, "l1"))
		symlink(t, filepath.Join(base, "l1"), filepath.Join(base, "cfg"))
		err := (&File{Path: filepath.Join(base, "cfg", "token.json")}).Save(ctx, &Credentials{RefreshToken: "x"})
		if err == nil {
			t.Fatal("want error")
		}
		real, _ = filepath.EvalSymlinks(real)
		if !strings.Contains(err.Error(), "chmod go-w "+real) {
			t.Fatalf("no chmod for %s in %q", real, err)
		}
	})
	t.Run("file link: chmod go-w", func(t *testing.T) {
		base := t.TempDir()
		writeMode(t, filepath.Join(base, "real"), `{"refresh_token":"x"}`, 0o666)
		symlink(t, filepath.Join(base, "real"), filepath.Join(base, "token.json"))
		_, err := (&File{Path: filepath.Join(base, "token.json")}).Load(ctx)
		if err == nil || !strings.Contains(err.Error(), "chmod go-w ") {
			t.Fatalf("%v", err)
		}
	})
	for _, name := range []string{"with space", "it's", "semi;colon", "$(touch pwned)", "`x`", "a\"b", "日本語", "x\ny", "x\x1b[2Jy", "x\ry", "x\x00y"} {
		t.Run(strings.NewReplacer("\n", "NL", "\x1b", "ESC", "\r", "CR", "\x00", "NUL").Replace(name), func(t *testing.T) {
			base := t.TempDir()
			real := filepath.Join(base, name)
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Skip("this file system refuses the name:", err)
			}
			os.Chmod(real, 0o777)
			symlink(t, real, filepath.Join(base, "cfg"))
			err := (&File{Path: filepath.Join(base, "cfg", "token.json")}).Save(ctx, &Credentials{RefreshToken: "x"})
			if err == nil {
				t.Fatal("want error")
			}
			msg := err.Error()
			for _, r := range msg {
				if r < 0x20 && r != '\t' || r == 0x7f || (r >= 0x80 && r < 0xa0) {
					t.Fatalf("control character %q in %q", r, msg)
				}
			}
			// whatever is shown after "chmod 700 " is one shell word that is the real path,
			// or the message does not pretend to give a command for it
			i := strings.Index(msg, "chmod go-w ")
			if strings.ContainsAny(name, "\n\r\x00\x1b") {
				if i >= 0 && !strings.HasPrefix(msg[i:], "chmod go-w で") { // the shown path would not be the real one: no command to paste
					t.Fatalf("a command for an unprintable path: %q", msg)
				}
				return
			}
			if i < 0 {
				t.Fatalf("no command in %q", msg)
			}
			cmd := msg[i+len("chmod go-w "):]
			word := cmd
			if strings.HasPrefix(word, "'") {
				// unquote: '...'\''...'
				var b strings.Builder
				rest := word
				for strings.HasPrefix(rest, "'") {
					end := strings.Index(rest[1:], "'")
					b.WriteString(rest[1 : 1+end])
					rest = rest[2+end:]
					if strings.HasPrefix(rest, `\'`) {
						b.WriteString("'")
						rest = rest[2:]
					}
				}
				word = b.String()
			}
			want, _ := filepath.EvalSymlinks(real)
			if word != want {
				t.Fatalf("command argument %q, want %q (message %q)", word, want, msg)
			}
		})
	}
}
