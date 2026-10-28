package main

import "syscall"

// notDumpable makes the process undumpable, so its /proc entries, its
// environment among them, are not readable by another process of the same
// user, such as a repository rule Bazel runs. It is hardening only.
func notDumpable() {
	// hardening: a kernel that refuses leaves nothing to do
	syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0) //nolint:errcheck // see above
}
