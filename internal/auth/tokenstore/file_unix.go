//go:build unix

package tokenstore

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/amasok/asana-tui/internal/termsafe"
)

// privateDir creates dir (0700) if needed and makes sure only the owner can read it: an
// existing folder that others can read or enter (e.g. 0755) is made 0700.
//
// A folder that is a symbolic link (dotfiles kept elsewhere) is not changed: what it points
// to may be someone else's. It is used only if it is a folder of ours that nobody else can
// read, write or enter; otherwise saving fails (#73).
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if !target.IsDir() {
			return fmt.Errorf("トークンの保存先 %s のリンク先がフォルダではありません", dir)
		}
		return ownedPrivate(dir, target, "フォルダ")
	}
	if !fi.IsDir() {
		return fmt.Errorf("トークンの保存先 %s がフォルダではありません", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// readPrivate reads the token file at path. It must be a regular file (opened without
// following a link put there in the meantime), or a symbolic link to a regular file of ours
// that nobody else can read or write: a link to a file in a folder others can write to could
// hand us a token someone else put there (#73). A link to nothing is no file
// (fs.ErrNotExist).
func readPrivate(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	link := fi.Mode()&fs.ModeSymlink != 0
	if !link && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("トークンのファイル %s が通常のファイルではありません (%v)", path, fi.Mode().Type())
	}
	// O_NONBLOCK: opening a FIFO a link points to does not wait for a writer
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if !link {
		flags |= syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("トークンのファイル %s のリンク先が通常のファイルではありません (%v)", path, st.Mode().Type())
	}
	if link {
		if err := ownedPrivate(path, st, "ファイル"); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// ownedPrivate checks that what the link at path points to (fi) is ours and that nobody
// else has any access to it. The error names what the link points to and the command that
// makes it private (chmod 700 for a folder, 600 for a file): it is not changed here, since
// it may be shared for something else.
func ownedPrivate(path string, fi fs.FileInfo, what string) error {
	target := path
	if t, err := filepath.EvalSymlinks(path); err == nil {
		target = t
	}
	// paths are shown on the terminal
	path, target = termsafe.String(path), termsafe.String(target)
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("トークンの保存先 %s のリンク先 %s の持ち主を確かめられません", path, target)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("トークンの保存先 %s はシンボリックリンクで、リンク先の%s %s の持ち主が自分ではありません", path, what, target)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		mode := "600"
		if fi.IsDir() {
			mode = "700"
		}
		return fmt.Errorf("トークンの保存先 %s はシンボリックリンクで、リンク先の%s %s をほかの人も使えます (%v)。自分だけが使えるようにしてください: chmod %s %s",
			path, what, target, fi.Mode().Perm(), mode, shellQuote(target))
	}
	return nil
}

// shellQuote quotes s for a POSIX shell if it has anything but plain path characters.
func shellQuote(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%,", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
