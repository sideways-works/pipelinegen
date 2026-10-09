//go:build !linux

package storetest

import "os/exec"

// dieWithParent does nothing where the kernel has no parent-death signal:
// the test's cleanup stops the daemon.
func dieWithParent(*exec.Cmd) {}
