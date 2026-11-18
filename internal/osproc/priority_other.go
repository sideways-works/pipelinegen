//go:build !linux

package osproc

import "syscall"

// LowestPriority makes this process the machine's last in line for CPU (nice 19); it may be
// refused.
func LowestPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19) //nolint:errcheck // best effort
}
