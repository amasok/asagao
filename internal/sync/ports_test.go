package sync

import (
	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// The Asana client and the store satisfy the ports as they are.
var (
	_ AsanaAPI   = (*asana.Client)(nil)
	_ LocalStore = (*store.Store)(nil)
)
