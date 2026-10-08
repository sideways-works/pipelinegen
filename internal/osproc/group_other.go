//go:build !linux

package osproc

import (
	"os/exec"
	"syscall"
)

// OwnGroup gives cmd a process group of its own.
func OwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
