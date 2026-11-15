package tokenstore

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/amasok/asana-tui/internal/config"
)

// File stores credentials as JSON in a 0600 file. Fallback for environments without a keychain.
type File struct {
	Path string
}

func defaultFilePath() string { return filepath.Join(config.ConfigDir(), "token.json") }

func (f *File) Name() string            { return "file" }
func (f *File) CachesAccessToken() bool { return true }

func (f *File) Load(context.Context) (*Credentials, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the credentials to a new temporary file in the same folder (a random name,
// created exclusively, so neither a leftover file nor a symbolic link is reused), makes it
// 0600, syncs it and renames it over the token file. The folder is made 0700 as well.
func (f *File) Save(_ context.Context, c *Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(f.Path)
	if err := privateDir(dir); err != nil {
		return err
	}
	// an old version wrote "token.json.tmp": do not leave a copy of a token lying around
	// (Remove does not follow a symbolic link)
	_ = os.Remove(f.Path + ".tmp")

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.Path)+".*.tmp")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Rename replaces the token file itself, even if it is a symbolic link.
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		return err
	}
	done = true
	return nil
}

func (f *File) Delete(context.Context) error {
	err := os.Remove(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// privateDir creates dir (0700) if needed and makes sure only the owner can read it: an
// existing folder that others can read or enter (e.g. 0755) is made 0700.
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
