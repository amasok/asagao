package mcpserver

import "github.com/amasok/asagao/internal/store"

// The store satisfies the MCP server's port as it is.
var _ Store = (*store.Store)(nil)
