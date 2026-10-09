// Package storetest opens stores for tests, on refs or on a branch, so a
// package's tests can run once on each and show the two hold the same
// records the same way.
package storetest

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// OnBranch opens the stores on a branch; Both sets it for the second run.
var OnBranch bool

// Both runs the package's tests on refs and, when they pass, again on a
// branch; a package's TestMain calls it. With a pattern, the second run
// takes only the tests it matches, and a -run given on the command line
// wins.
func Both(m *testing.M, pattern ...string) {
	code := m.Run()
	if code == 0 {
		OnBranch = true

		if f := flag.Lookup("test.run"); len(pattern) > 0 && f != nil && f.Value.String() == "" {
			if err := f.Value.Set(pattern[0]); err != nil {
				panic(err)
			}
		}

		code = m.Run()
	}

	os.Exit(code)
}

// Bare makes an empty bare repository that serves what a host serves (the
// partial fetches a branch store reads by) and returns its file:// URL.
func Bare(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "remote.git")
	run(t, "git", "init", "--quiet", "--bare", dir)

	for _, kv := range [][2]string{{"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}} {
		run(t, "git", "-C", dir, "config", kv[0], kv[1])
	}

	return "file://" + dir
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()

	if out, err := exec.CommandContext(t.Context(), name, args...).CombinedOutput(); err != nil { //nolint:gosec // git with the test's own arguments
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, out)
	}
}

// Open opens a store of remote for the test, on refs or on a branch as
// OnBranch says, and closes it when the test ends.
func Open(t *testing.T, remote string) *gitstore.Store {
	t.Helper()

	s := &gitstore.Store{Remote: remote, Dir: filepath.Join(t.TempDir(), "scratch")}
	if OnBranch {
		s.Branch = &branchstore.Store{Remote: remote, Branch: "pipelinegen", Dir: filepath.Join(t.TempDir(), "branch")}
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	return s
}
