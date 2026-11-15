//go:build windows

package store

import "os"

func openNoBlock(path string) (*os.File, error) { return os.Open(path) }
