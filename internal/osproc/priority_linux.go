package osproc

import "syscall"

// ioprioSet and its values are Linux's ioprio_set: the idle class for the
// whole process.
const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3
	ioprioClassShift = 13
)

// LowestPriority makes this process the machine's last in line for CPU
// (nice 19) and disk (the idle I/O class). Either may be refused, which
// leaves it at its priority.
func LowestPriority() {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, 0, 19) //nolint:errcheck // best effort

	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, 0, ioprioClassIdle<<ioprioClassShift) //nolint:errcheck,dogsled // best effort
}
