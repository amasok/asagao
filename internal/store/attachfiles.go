package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/amasok/asagao/internal/xdg"
)

// SetAttachmentDir sets the folder of the attachment files the store may delete (files
// downloaded, or copied to be uploaded). By default it is the cache's attachments folder;
// a dry run's copy of the store uses its own, so it never deletes the real files.
func (s *Store) SetAttachmentDir(dir string) { s.attachmentDir = dir }

func (s *Store) attachmentRoot() string {
	if s.attachmentDir != "" {
		return s.attachmentDir
	}
	return filepath.Join(xdg.CacheDir(), "attachments")
}

// RemoveAttachmentFile deletes a local attachment file recorded in the store, only when
// it is inside the attachments folder once symbolic links are resolved: a path recorded
// wrongly (or planted) must not delete the user's other files, and a dry run's copy of
// the store must not delete the real ones. A file outside is left alone (it is not ours
// to delete); a file that is gone already is fine.
func (s *Store) RemoveAttachmentFile(path string) error {
	if path == "" {
		return nil
	}
	inside, err := within(s.attachmentRoot(), path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !inside {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// within reports whether path's folder is root or below it, both with symbolic links
// resolved. The file itself is not resolved: removing a link removes only the link.
func within(root, path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return false, err
	}
	r, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // no attachments folder: nothing of ours to delete
	}
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(r, dir)
	if err != nil {
		return false, nil
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}
