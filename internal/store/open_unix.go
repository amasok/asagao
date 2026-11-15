//go:build unix

package store

import (
	"os"
	"syscall"
)

// openNoBlock opens a file for reading without waiting: if a FIFO was swapped in after
// the file was checked, opening it would otherwise wait for a writer. On a regular file
// O_NONBLOCK changes nothing.
func openNoBlock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
