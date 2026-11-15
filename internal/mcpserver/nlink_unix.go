//go:build unix

package mcpserver

import (
	"os"
	"syscall"
)

// links returns the number of hard links to a file (1 if unknown).
func links(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
