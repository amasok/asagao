//go:build !unix

package repoctx

import "os"

// ownedByMe is true where files have no Unix owner.
func ownedByMe(os.FileInfo) bool { return true }
