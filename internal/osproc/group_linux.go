package osproc

import (
	"os/exec"
	"syscall"
)

// OwnGroup gives cmd a process group of its own, killed with the kit.
func OwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
