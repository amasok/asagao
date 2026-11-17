// Package gid holds the rules for local gids: the temporary gids of tasks, comments, tags,
// projects and attachments created offline and not yet sent to Asana.
//
// A local gid is Prefix followed by a random id. Domain code does not read the random
// source (architecture.md, 依存の決まり 1), so NewLocal takes the id as an argument and
// the caller (the store) draws it.
package gid

import "strings"

// Prefix marks gids of tasks/comments created offline and not yet sent.
const Prefix = "local:"

// IsLocal reports whether gid belongs to something not yet sent to Asana.
func IsLocal(gid string) bool { return strings.HasPrefix(gid, Prefix) }

// NewLocal makes the local gid for a random id.
func NewLocal(id string) string { return Prefix + id }
