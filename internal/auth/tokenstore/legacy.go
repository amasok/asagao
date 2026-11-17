package tokenstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/amasok/asagao/internal/config"
)

// withLegacy is a store under the current name that falls back to the one under the former
// name (asana-tui): a login saved before the rename keeps working, with nothing to do by hand.
//
// Load reads the former store only when nothing is saved under the current name, and saves
// what it finds again under the current name. The former entry is not deleted there: an old
// binary still registered somewhere (an MCP server, say) may still use it. Save writes only
// the current store.
//
// The copy and Save hold a lock file (lockPath), so two processes starting together (the TUI
// and an MCP server) copy only once: 1Password makes a second item of the same name on a
// second create, and then finds neither. A login saved meanwhile (auth login in another
// terminal) is not overwritten by the copy either.
type withLegacy struct {
	Store
	legacy   Store
	lockPath string
}

func newWithLegacy(cur, legacy Store) *withLegacy {
	return &withLegacy{Store: cur, legacy: legacy, lockPath: filepath.Join(config.ConfigDir(), ".login.lock")}
}

func (s *withLegacy) Load(ctx context.Context) (*Credentials, error) {
	c, err := s.Store.Load(ctx)
	if !errors.Is(err, ErrNotFound) {
		return c, err
	}
	old, err := s.legacy.Load(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("旧名 %s で保存したログインを読めません: %w", config.LegacyAppName, err)
	}
	unlock, err := lockCopy(ctx, s.lockPath)
	if err != nil {
		return old, nil // not copied now (no lock: it might make a second copy); next run
	}
	defer unlock()
	// Another process may have copied it, saved a new login or logged out while we waited.
	c, err = s.Store.Load(ctx)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return old, nil
	}
	if old, err = s.legacy.Load(ctx); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotFound // logged out meanwhile: do not bring it back
		}
		return nil, fmt.Errorf("旧名 %s で保存したログインを読めません: %w", config.LegacyAppName, err)
	}
	_ = s.Store.Save(ctx, old) // if this fails, the login is still used, and copied next run
	return old, nil
}

// Save holds the lock too, so a copy running in another process does not overwrite it. If
// the lock cannot be taken, it saves anyway: a new login matters more.
func (s *withLegacy) Save(ctx context.Context, c *Credentials) error {
	if unlock, err := lockCopy(ctx, s.lockPath); err == nil {
		defer unlock()
	}
	return s.Store.Save(ctx, c)
}

// Delete removes the login under both names: `auth logout` and `auth migrate` mean to forget
// it, and an entry left under the former name would be read again by the next Load.
//
// It holds the lock too, so a copy running in another process does not save the login
// again after it is deleted. If the lock cannot be taken, it deletes anyway.
func (s *withLegacy) Delete(ctx context.Context) error {
	if unlock, err := lockCopy(ctx, s.lockPath); err == nil {
		defer unlock()
	}
	return errors.Join(s.Store.Delete(ctx), s.legacy.Delete(ctx))
}
