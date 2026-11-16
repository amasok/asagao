//go:build windows

package tokenstore

import "os"

// privateDir creates dir (0700) if needed and makes it 0700 (see file_unix.go; on Windows
// the mode only sets whether it is read-only).
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if fi.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// readPrivate reads the token file at path.
func readPrivate(path string) ([]byte, error) { return os.ReadFile(path) }
