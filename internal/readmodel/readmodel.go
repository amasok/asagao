// Package readmodel holds the types that the store returns to be shown, and that the
// application layer also passes back to the store. It has no dependencies, so the ports in
// internal/app can use these types without importing the store
// (docs/design/adr/0006-application-use-cases.md, decision 4).
//
// The store keeps type aliases for them (store.UserRef), so its methods and their callers
// do not change. The rest of the read types move here in stage c2 (migration-plan.md).
package readmodel

// UserRef is a user as the CLI and MCP show it (keep the JSON stable), and the author of
// a comment or a status update written locally.
type UserRef struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}
