package storetest

import (
	"os/exec"
	"syscall"
)

// dieWithParent has the kernel kill cmd when the test binary dies, however
// it dies, and gives it a process group of its own.
func dieWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
}
