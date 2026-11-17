package tui

import "github.com/amasok/asagao/internal/store"

// The store satisfies the TUI's port as it is.
var _ Store = (*store.Store)(nil)
