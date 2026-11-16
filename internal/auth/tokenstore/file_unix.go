//go:build unix

package tokenstore

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
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
// else has any access to it.
func ownedPrivate(path string, fi fs.FileInfo, what string) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("トークンの保存先 %s の持ち主を確かめられません", path)
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("トークンの保存先 %s はシンボリックリンクで、リンク先の%sの持ち主が自分ではありません", path, what)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("トークンの保存先 %s はシンボリックリンクで、リンク先の%sをほかの人も使えます (%v)。リンク先を自分だけが使えるようにしてください (chmod go= <リンク先>)", path, what, fi.Mode().Perm())
	}
	return nil
}
