package testenv

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func outputBase(t *testing.T, cache, name, workspace string) string {
	t.Helper()

	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(cache, "bazel", "_bazel_"+u.Username, name)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(base, "DO_NOT_BUILD_HERE"), []byte(workspace), 0o600); err != nil {
		t.Fatal(err)
	}

	return base
}

func TestAnOutputBaseLeftInTheDefaultRootIsFound(t *testing.T) {
	cache, dir := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)

	old := outputBase(t, cache, "old", filepath.Join(dir, "ws0"))
	before := outputBases()

	outputBase(t, cache, "other", "/somewhere/else")
	leak := outputBase(t, cache, "leak", filepath.Join(dir, "ws1"))

	got := leakedFrom(before, dir)
	if len(got) != 1 || got[0] != leak+" (for "+filepath.Join(dir, "ws1")+")" {
		t.Fatalf("leaked %v, want %s only (not %s)", got, leak, old)
	}
}

func TestTheServersInTheRootAreStopped(t *testing.T) {
	root := t.TempDir()

	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)

	go func() { done <- cmd.Wait() }()

	server := filepath.Join(root, "_bazel_x", "0123", "server")
	if err := os.MkdirAll(server, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(server, "server.pid.txt"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}

	stopServers(root)

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("the server is still running")
	}
}

func TestADirectoryWithReadOnlyTreesIsRemoved(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	sub := filepath.Join(dir, "external", "repo")

	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sub, "BUILD"), nil, 0o400); err != nil {
		t.Fatal(err)
	}

	for _, d := range []string{sub, filepath.Dir(sub)} {
		if err := os.Chmod(d, 0o500); err != nil { //nolint:gosec // read-only, as Bazel leaves it
			t.Fatal(err)
		}
	}

	if err := removeAll(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("%s is still there: %v", dir, err)
	}
}
