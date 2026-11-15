//go:build !unix

package mcpserver

import "os"

func links(os.FileInfo) uint64 { return 1 }
