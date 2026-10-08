package scratch

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestMain lets the test binary hold a scratch directory until a signal
// ends it.
func TestMain(m *testing.M) {
	if os.Getenv("SCRATCH_HOLD") != "" {
		var d Dirs

		dir, err := d.Make("held")
		if err != nil {
			os.Exit(1)
		}

		println(dir) //nolint:forbidigo // the test reads the directory from stderr
		time.Sleep(time.Minute)
		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestDirsAreRemoved(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	var d Dirs

	dir, err := d.Make("change")
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 || !strings.HasPrefix(dir, tmp) {
		t.Fatalf("%s: %v %v", dir, info, err)
	}

	ro := filepath.Join(dir, "ro")
	if err := os.MkdirAll(ro, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(ro, "f"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(ro, 0o500); err != nil { //nolint:gosec // a read-only directory, as a module cache leaves one
		t.Fatal(err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("left %s: %v", dir, err)
	}
}

func TestDirsGoWithASignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}

		cmd := exec.CommandContext(t.Context(), self, "-test.run=^$")

		cmd.Env = append(os.Environ(), "SCRATCH_HOLD=1")

		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		line, err := bufio.NewReader(stderr).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}

		dir := strings.TrimSpace(line)

		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}

		if err := cmd.Wait(); err == nil {
			t.Errorf("%v: the process ended as if nothing happened", sig)
		}

		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%v left %s: %v", sig, dir, err)
		}
	}
}
