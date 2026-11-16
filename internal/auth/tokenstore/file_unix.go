//go:build unix

package tokenstore

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode"
)

// privateDir creates dir (0700) if needed and makes sure only the owner can read it: an
// existing folder that others can read or enter (e.g. 0755) is made 0700.
//
// A folder that is a symbolic link (dotfiles kept elsewhere) is not changed: what it points
// to may be shared for something else. It is used if it is a folder of ours that nobody else
// can write to (0755 is fine: the token file in it is 0600, and only someone who can write to
// the folder could put another one there); otherwise saving fails, saying what to run (#73).
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
			return fmt.Errorf("トークンの保存先 %s のリンク先がフォルダではありません", showPath(dir))
		}
		return ownedUnwritable(dir, target, true)
	}
	if !fi.IsDir() {
		return fmt.Errorf("トークンの保存先 %s がフォルダではありません", showPath(dir))
	}
	if fi.Mode().Perm() == 0o700 {
		return nil
	}
	// chmod what was checked: a link put there since Lstat is not followed
	d, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrPermission) {
		return os.Chmod(dir, 0o700) // a folder we cannot read (e.g. 0300): no way to open it
	}
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Chmod(0o700)
}

// readPrivate reads the token file at path: a regular file (opened without following a link
// put there in the meantime) or a symbolic link to one. Either must be ours and not writable
// by anyone else: in a folder others can write to (a link to /tmp/..., say), someone else
// could have put a token of theirs there, and we would act as them (#73). Anything else
// (a folder, a FIFO, ...) is refused, and a link to nothing is no file (fs.ErrNotExist).
func readPrivate(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	link := fi.Mode()&fs.ModeSymlink != 0
	if !link && !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("トークンのファイル %s が通常のファイルではありません (%v)", showPath(path), fi.Mode().Type())
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
		return nil, fmt.Errorf("トークンのファイル %s のリンク先が通常のファイルではありません (%v)", showPath(path), st.Mode().Type())
	}
	if err := ownedUnwritable(path, st, link); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// ownedUnwritable checks that fi (what is at path, or what the link at path points to) is
// ours and that nobody else can write to it. The error names it and, when it helps, the
// command that fixes it (chmod go-w): it is not changed here, since what a link points to
// may be shared for something else.
func ownedUnwritable(path string, fi fs.FileInfo, link bool) error {
	what := "ファイル"
	if fi.IsDir() {
		what = "フォルダ"
	}
	target := path
	if link {
		if t, err := filepath.EvalSymlinks(path); err == nil {
			target = t
		}
	}
	// paths are shown on the terminal (showPath)
	subject := "トークンの" + what + " " + showPath(path)
	if link {
		subject = "トークンの保存先 " + showPath(path) + " はシンボリックリンクで、リンク先の" + what + " " + showPath(target)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s の持ち主を確かめられません", subject)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s の持ち主が自分ではありません", subject)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		fix := "chmod go-w " + shellQuote(target)
		if !printable(target) { // no command to paste: the path shown is not the path itself
			fix = "chmod go-w で、グループとほかの人の書き込みを外す"
		}
		return fmt.Errorf("%s にほかの人も書き込めます (%v)。ほかの人が書き込めないようにしてください: %s",
			subject, fi.Mode().Perm(), fix)
	}
	return nil
}

// showPath is path as an error shows it on the terminal: as it is, or quoted (with escapes,
// as Go writes strings) if it has anything that does not print, such as a line break, which
// could make up a line of the message, or an escape sequence.
func showPath(path string) string {
	if printable(path) {
		return path
	}
	return strconv.Quote(path)
}

// printable reports whether every character of s prints (a space does; a tab, a line break,
// a control or format character does not).
func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
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
