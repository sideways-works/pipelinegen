package testenv

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// BazelMark is set while a package's tests run under Bazel: its value is
// the output root of the test binary's own that Bazel uses.
const BazelMark = "PIPELINEGEN_TEST_BAZEL_ROOT"

// Bazel runs a package's tests, from its TestMain, so that no Bazel they
// start leaves anything behind: the test binary gets a directory of its own
// for its temporary files and Bazel's output root (TEST_TMPDIR, which every
// Bazel the tests start inherits), and when the tests end every server there
// is stopped and the directory removed. The tests fail if a workspace left
// an output base in the default root all the same.
func Bazel(m *testing.M) int {
	dir, err := os.MkdirTemp("", "pipelinegen-test-")
	if err != nil {
		panic("testenv: " + err.Error())
	}

	tmp, root := filepath.Join(dir, "tmp"), filepath.Join(dir, "bazel")
	for _, d := range []string{tmp, root} {
		if err := os.Mkdir(d, 0o700); err != nil {
			panic("testenv: " + err.Error())
		}
	}

	for k, v := range map[string]string{"TMPDIR": tmp, "TEST_TMPDIR": root, BazelMark: root} {
		if err := os.Setenv(k, v); err != nil {
			panic("testenv: " + err.Error())
		}
	}

	before := outputBases()

	code := m.Run()

	stopServers(root)

	leaked := leakedFrom(before, dir)

	if err := removeAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "testenv: removing %s: %v\n", dir, err)
	}

	if len(leaked) > 0 {
		fmt.Fprintf(os.Stderr, "testenv: tests left Bazel output in the default output root, outside the root testenv.Bazel gives them:\n  %s\n",
			strings.Join(leaked, "\n  "))

		if code == 0 {
			code = 1
		}
	}

	return code
}

func defaultRoots() []string {
	u, err := user.Current()
	if err != nil {
		return nil
	}

	name := "_bazel_" + u.Username

	var out []string

	if c := os.Getenv("XDG_CACHE_HOME"); c != "" {
		out = append(out, filepath.Join(c, "bazel", name))
	}

	if h, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(h, ".cache", "bazel", name))
	}

	return append(out, filepath.Join("/private/var/tmp", name))
}

func outputBases() []string {
	var out []string

	for _, r := range defaultRoots() {
		entries, err := os.ReadDir(r)
		if err != nil {
			continue
		}

		for _, e := range entries {
			out = append(out, filepath.Join(r, e.Name()))
		}
	}

	return out
}

// leakedFrom are the output bases in the default roots, new since before, of
// a workspace under dir: Bazel writes an output base's workspace into its
// DO_NOT_BUILD_HERE file.
func leakedFrom(before []string, dir string) []string {
	dirs := []string{dir}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		dirs = append(dirs, resolved)
	}

	var out []string

	for _, base := range outputBases() {
		if slices.Contains(before, base) {
			continue
		}

		ws, err := os.ReadFile(filepath.Join(base, "DO_NOT_BUILD_HERE")) //nolint:gosec // Bazel's own marker in its output root
		if err != nil {
			continue
		}

		for _, d := range dirs {
			if strings.HasPrefix(strings.TrimSpace(string(ws)), d+string(filepath.Separator)) {
				out = append(out, base+" (for "+strings.TrimSpace(string(ws))+")")

				break
			}
		}
	}

	return out
}

// stopServers stops every Bazel server whose output base is under root:
// each writes its process id into server/server.pid.txt there.
func stopServers(root string) {
	pids, err := filepath.Glob(filepath.Join(root, "_bazel_*", "*", "server", "server.pid.txt"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "testenv: finding Bazel servers: %v\n", err)

		return
	}

	var running []int

	for _, f := range pids {
		b, err := os.ReadFile(f) //nolint:gosec // a server's pid file in the test binary's own root
		if err != nil {
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 1 {
			continue
		}

		if syscall.Kill(pid, syscall.SIGTERM) == nil {
			running = append(running, pid)
		}
	}

	// a server writes its output down before it exits; one that doesn't
	// within the wait is killed
	deadline := time.Now().Add(10 * time.Second)

	for _, pid := range running {
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}

		if syscall.Kill(pid, 0) == nil {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
				fmt.Fprintf(os.Stderr, "testenv: stopping Bazel server %d: %v\n", pid, err)
			}
		}
	}
}

// removeAll removes a directory Bazel wrote into: its output trees hold
// directories without write permission, which have to get it back first.
func removeAll(dir string) error {
	writable(dir)

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("testenv: %w", err)
	}

	return nil
}

func writable(dir string) {
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory must be searchable to be emptied
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			writable(filepath.Join(dir, e.Name()))
		}
	}
}
