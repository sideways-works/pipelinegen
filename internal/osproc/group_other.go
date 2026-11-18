//go:build !linux

package osproc

import (
	"os/exec"
	"syscall"
)

func OwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
