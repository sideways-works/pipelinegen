package storetest

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestHelperDaemon is the child of TestADaemonDiesWithItsTest: it starts a
// daemon, says its URL and waits to be killed.
func TestHelperDaemon(t *testing.T) {
	if os.Getenv("STORETEST_DAEMON_CHILD") == "" {
		t.Skip("run by TestADaemonDiesWithItsTest")
	}

	_, _ = os.Stdout.WriteString(Daemon(t) + "\n") //nolint:errcheck // the parent reads it

	select {}
}

func TestADaemonDiesWithItsTest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the parent-death signal is Linux's")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	child := exec.CommandContext(t.Context(), exe, "-test.run=^TestHelperDaemon$", "-test.v")

	child.Env = append(os.Environ(), "STORETEST_DAEMON_CHILD=1")

	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	url := ""
	for sc := bufio.NewScanner(out); url == "" && sc.Scan(); {
		if line := sc.Text(); strings.HasPrefix(line, "git://") {
			url = line
		}
	}

	port := strings.TrimPrefix(url, "git://127.0.0.1:")
	port, _, _ = strings.Cut(port, "/")

	pids := daemonsOn(t, port)
	if url == "" || len(pids) == 0 {
		t.Fatalf("no daemon: %q %v", url, pids)
	}

	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	_ = child.Wait() //nolint:errcheck // killed

	for deadline := time.Now().Add(10 * time.Second); len(daemonsOn(t, port)) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			for _, pid := range daemonsOn(t, port) {
				_ = syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck // the test's own leftover
			}

			t.Fatal("the daemon outlived its test binary")
		}
	}
}

func daemonsOn(t *testing.T, port string) []int {
	t.Helper()

	var pids []int

	dirs, _ := filepath.Glob("/proc/[0-9]*") //nolint:errcheck // a bad pattern only
	for _, d := range dirs {
		data, err := os.ReadFile(filepath.Join(d, "cmdline"))
		if err != nil || !strings.Contains(string(data), "git-daemon") || !strings.Contains(string(data), "--port="+port+"\x00") {
			continue
		}

		if pid, err := strconv.Atoi(filepath.Base(d)); err == nil {
			pids = append(pids, pid)
		}
	}

	return pids
}
