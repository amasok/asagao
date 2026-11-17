package tokenstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/amasok/asagao/internal/config"
)

// withLegacy is a store under the current name that falls back to the one under the former
// name (asana-tui): a login saved before the rename keeps working, with nothing to do by hand.
//
// Load reads the former store only when nothing is saved under the current name, and saves
// what it finds again under the current name. The former entry is not deleted there: an old
// binary still registered somewhere (an MCP server, say) may still use it. Save writes only
// the current store.
type withLegacy struct {
	Store
	legacy Store
}

func (s *withLegacy) Load(ctx context.Context) (*Credentials, error) {
	c, err := s.Store.Load(ctx)
	if !errors.Is(err, ErrNotFound) {
		return c, err
	}
	c, err = s.legacy.Load(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("旧名 %s で保存したログインを読めません: %w", config.LegacyAppName, err)
	}
	// Not saved now: used as it is, and copied again on the next run.
	_ = s.Store.Save(ctx, c)
	return c, nil
}

// Delete removes the login under both names: `auth logout` and `auth migrate` mean to forget
// it, and an entry left under the former name would be read again by the next Load.
func (s *withLegacy) Delete(ctx context.Context) error {
	return errors.Join(s.Store.Delete(ctx), s.legacy.Delete(ctx))
}
