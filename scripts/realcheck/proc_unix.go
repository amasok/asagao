//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detach puts a child in its own process group, so Ctrl-C reaches only this program and
// the asagao it is running finishes (and cleanup can still run children).
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
