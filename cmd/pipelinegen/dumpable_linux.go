package main

import "syscall"

// notDumpable keeps the process's /proc entries, its environment among them, from other processes
// of the same user, such as a repository rule Bazel runs. Hardening only.
func notDumpable() {
	// hardening: a kernel that refuses leaves nothing to do
	syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0) //nolint:errcheck // see above
}
