package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/xdg"
)

// SearchFile is searches.toml, the saved searches (Issue #10). It is a file of its own so
// that saving a search does not rewrite config.toml (and lose its comments). It satisfies
// app.SearchFile.
type SearchFile struct {
	// Path is the file; "" is SearchesFilePath() at the time of each call.
	Path string
}

// SearchesFilePath is searches.toml in ConfigDir.
func SearchesFilePath() string { return filepath.Join(xdg.ConfigDir(), "searches.toml") }

func (f SearchFile) path() string {
	if f.Path != "" {
		return f.Path
	}
	return SearchesFilePath()
}

// searchesDoc is the file's shape: one [[search]] table per saved search.
type searchesDoc struct {
	Search []readmodel.SavedSearch `toml:"search"`
}

const searchesHeader = `# asagao の保存した検索 (asagao searches save / rm が書き換えます。コメントは残りません)
# 条件の名前は asagao search / asagao task list のフラグと同じです (due_before = "+7d" など)。

`

// LoadSearches reads the saved searches in the file's order; no file means none. A file
// that is not TOML, or has a key that is not a saved search's, is an error: it is not
// read in part, and so never rewritten without what it had.
func (f SearchFile) LoadSearches() ([]readmodel.SavedSearch, error) {
	path := f.path()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc searchesDoc
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return nil, fmt.Errorf("parse %s: unknown keys:\n%s", path, strict.String())
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc.Search, nil
}

// UpdateSearches changes the saved searches: change gets them as LoadSearches reads them
// and returns what to save (an error saves nothing). Changes run one at a time, also
// across processes (a lock file next to searches.toml), so two saves at once keep both.
func (f SearchFile) UpdateSearches(change func([]readmodel.SavedSearch) ([]readmodel.SavedSearch, error)) error {
	path := f.path()
	unlock, err := lockSearches(filepath.Join(filepath.Dir(path), ".searches.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	ss, err := f.LoadSearches()
	if err != nil {
		return err
	}
	ss, err = change(ss)
	if err != nil {
		return err
	}
	return f.SaveSearches(ss)
}

// maxLinkHops is how many symbolic links SaveSearches follows to the file.
const maxLinkHops = 40

// linkTarget is the file path names, following symbolic links (also one that points to a
// file not made yet).
func linkTarget(path string) (string, error) {
	for range maxLinkHops {
		fi, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			return path, nil
		}
		to, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(to) {
			to = filepath.Join(filepath.Dir(path), to)
		}
		path = to
	}
	return "", fmt.Errorf("%s: too many symbolic links", path)
}

// SaveSearches replaces the file with ss: it writes a temporary file (0600) in the same
// folder, syncs it, and renames it over the file, so a crash leaves the old file or the
// new one. A symbolic link is followed: the file it points to is replaced (or made). It
// does not take the lock: changes go through UpdateSearches.
func (f SearchFile) SaveSearches(ss []readmodel.SavedSearch) error {
	path, err := linkTarget(f.path())
	if err != nil {
		return err
	}
	body, err := toml.Marshal(searchesDoc{Search: ss})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".searches-*.toml")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(searchesHeader); err != nil {
		return err
	}
	if len(ss) > 0 {
		if _, err := tmp.Write(body); err != nil {
			return err
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	done = true
	syncDir(dir)
	return nil
}

// syncDir makes the rename in dir durable where the OS allows it (not on Windows).
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
