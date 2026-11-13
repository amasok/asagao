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

func (f *File) Save(_ context.Context, c *Credentials) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

func (f *File) Delete(context.Context) error {
	err := os.Remove(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
