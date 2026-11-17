package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/amasok/asagao/internal/xdg"
)

// LegacyAppName is the tool's former name. Its folders are moved to the new ones on startup
// (MigrateLegacyDirs), and its keychain entry and 1Password item are read when nothing is
// saved under the new name yet (tokenstore).
const LegacyAppName = xdg.LegacyAppName

// dirPair is one of the tool's folders under the former name and under the current one.
type dirPair struct{ old, new string }

func dirPairs() []dirPair {
	bases := []string{xdg.ConfigHome(), xdg.DataHome(), xdg.CacheHome()}
	pairs := make([]dirPair, 0, len(bases))
	for _, base := range bases {
		pairs = append(pairs, dirPair{old: filepath.Join(base, LegacyAppName), new: filepath.Join(base, appName)})
	}
	return pairs
}

// OwnDirs is xdg.OwnDirs.
func OwnDirs() []string { return xdg.OwnDirs() }

// MigrateLegacyDirs moves the config, data and cache folders of the former name (asana-tui)
// to the current ones, so nothing is lost and nothing has to be done by hand. For each:
//
//   - If the new folder exists, it is used and the old one is left as it is.
//   - If only the old folder exists, it is moved (os.Rename) and a link to the new folder is
//     left at the old path. An old binary still registered somewhere (an MCP server, say)
//     then keeps using the same store and outbox, instead of starting a second outbox.
//   - If the old path is a link to a folder (dotfiles kept elsewhere), the new path is made a
//     link to the same folder.
//
// Two processes may start at once (the TUI and an MCP server): the one whose move fails
// because the new folder now exists just uses it. Nothing is ever deleted; what could not be
// moved is left in place, and the error says so (the caller warns and goes on).
func MigrateLegacyDirs() error {
	var errs []error
	for _, p := range dirPairs() {
		if err := migrateDir(p.old, p.new); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func migrateDir(oldDir, newDir string) error {
	if _, err := os.Lstat(newDir); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s を確かめられないため、旧フォルダ %s は移していません: %w", newDir, oldDir, err)
	}
	fi, err := os.Lstat(oldDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("旧フォルダ %s を確かめられないため、%s に移していません: %w", oldDir, newDir, err)
	}
	// newExists reports whether another process made the new folder meanwhile.
	newExists := func() bool { _, err := os.Lstat(newDir); return err == nil }
	switch {
	case fi.IsDir():
		if err := os.Rename(oldDir, newDir); err != nil {
			if newExists() {
				return nil
			}
			return fmt.Errorf("旧フォルダ %s を %s に移せませんでした（そのまま残しています）: %w", oldDir, newDir, err)
		}
		// A relative link: both are in the same folder, so it stays right if that one moves.
		if err := os.Symlink(filepath.Base(newDir), oldDir); err != nil {
			return fmt.Errorf("旧フォルダ %s を %s に移しましたが、元の場所にリンクを作れませんでした"+
				"（古い %s を動かすと、別のデータを使います）: %w", oldDir, newDir, LegacyAppName, err)
		}
		return nil
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := filepath.EvalSymlinks(oldDir)
		if err != nil {
			return nil // a link to nothing (e.g. to the new folder, removed since): nothing to keep
		}
		if st, err := os.Stat(target); err != nil || !st.IsDir() {
			return fmt.Errorf("旧フォルダ %s のリンク先がフォルダではないため、%s に移していません", oldDir, newDir)
		}
		if err := os.Symlink(target, newDir); err != nil {
			if newExists() {
				return nil
			}
			return fmt.Errorf("旧フォルダ %s はリンクです。%s から同じ場所 (%s) へのリンクを作れませんでした: %w", oldDir, newDir, target, err)
		}
		return nil
	}
	return fmt.Errorf("旧フォルダの場所 %s がフォルダではないため、%s に移していません（そのまま残しています）", oldDir, newDir)
}
