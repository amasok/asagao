package store_test

import (
	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/store"
)

// *store.Store is what the entry points give app.Query.
var _ app.QueryStore = (*store.Store)(nil)
