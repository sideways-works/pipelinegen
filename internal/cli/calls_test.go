package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCallsDirIsRemovedAndLeftoversSwept(t *testing.T) {
	cache := t.TempDir()

	mkdir := func(name string, held bool, age time.Duration) string {
		t.Helper()

		dir := filepath.Join(cache, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}

		if held {
			writeFile(t, filepath.Join(dir, heldFile), "")
		}

		at := time.Now().Add(-age)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}

		return dir
	}

	killed := mkdir(callsPrefix+"killed", true, time.Minute)
	older := mkdir(callsPrefix+"older", false, time.Hour)
	fresh := mkdir(callsPrefix+"fresh", false, 0)
	running := mkdir(callsPrefix+"running", true, time.Hour)

	f, err := os.Open(filepath.Join(running, heldFile))
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close() //nolint:errcheck // the test's own lock

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	work, done, err := callsDir(cache)
	if err != nil {
		t.Fatal(err)
	}

	for dir, want := range map[string]bool{killed: false, older: false, fresh: true, running: true, work: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s there: %v, want %v", filepath.Base(dir), err == nil, want)
		}
	}

	sweepCalls(cache)

	if _, err := os.Stat(work); err != nil {
		t.Fatalf("a sweep took a running plan's directory: %v", err)
	}

	done()

	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatalf("the calls directory outlived its plan: %v", err)
	}

	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.Contains(e.Name(), "held-") {
			t.Fatalf("a held file was left: %s", e.Name())
		}
	}
}

func noCalls(t *testing.T, cache, after string) {
	t.Helper()

	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), callsPrefix) {
			t.Fatalf("%s left %s in the cache", after, e.Name())
		}
	}
}
