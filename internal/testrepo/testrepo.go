// Package testrepo gives tests a git repository holding the fixture Bazel
// workspace from testdata, and a Bazel client for it.
package testrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// Repo is a temporary git repository with the fixture workspace at its root.
type Repo struct {
	t   *testing.T
	Dir string
}

// FixtureDir is testdata/workspace at the module's root, found from the
// test's working directory (its package's directory).
func FixtureDir(t *testing.T) string {
	t.Helper()

	return fixtureDir(t)
}

func fixtureDir(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", "workspace")
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}

		dir = parent
	}
}

// New copies the fixture into a new repository and commits it.
func New(t *testing.T) *Repo {
	t.Helper()

	return NewFrom(t, fixtureDir(t))
}

// NewFrom copies another fixture directory into a new repository and
// commits it.
func NewFrom(t *testing.T, dir string) *Repo {
	t.Helper()

	r := &Repo{t: t, Dir: t.TempDir()}

	if err := os.CopyFS(r.Dir, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}

	// what an earlier Bazel run left in the fixture is no part of it
	if err := os.Remove(filepath.Join(r.Dir, "MODULE.bazel.lock")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	r.Git("init", "-q", "-b", "main")
	r.Git("config", "user.email", "test@example.com")
	r.Git("config", "user.name", "test")
	r.Git("config", "commit.gpgsign", "false")
	r.Git("config", "core.hooksPath", "/dev/null")
	r.Commit("fixture")

	return r
}

// Git runs git in the repository and returns its trimmed output.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()

	return r.GitIn(r.t, args...)
}

// GitIn runs git in the repository for test t, which may be another test
// than the one that made it.
func (r *Repo) GitIn(t *testing.T, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = r.Dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}

	return strings.TrimSpace(string(out))
}

// Write writes a file, relative to the repository's root.
func (r *Repo) Write(rel, content string) {
	r.t.Helper()

	p := filepath.Join(r.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		r.t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// Commit commits everything and returns the new commit.
func (r *Repo) Commit(msg string) string {
	r.t.Helper()

	r.Git("add", "-A")
	r.Git("commit", "-q", "--allow-empty", "-m", msg)

	return r.Git("rev-parse", "HEAD")
}

// heavySlots bounds the tests that run Bazel side by side: each starts
// its own server, and the machine running the suite may be shared.
var heavySlots = make(chan struct{}, 4)

// Parallel runs a test that starts Bazel beside the package's other
// parallel tests, at most a few at a time, so a package of many such
// tests takes the time of its longest rather than of all of them.
func Parallel(t *testing.T) {
	t.Helper()
	NeedBazel(t)
	t.Parallel()
	Slot(t)
}

// Slot holds one of the slots for a parallel test that starts Bazel, until
// the test ends; Parallel takes it for the tests it starts.
func Slot(t *testing.T) {
	t.Helper()

	heavySlots <- struct{}{}

	t.Cleanup(func() { <-heavySlots })
}

// NeedBazel skips the test in -short mode, where nothing starts Bazel,
// and fails it when its package's TestMain doesn't run the tests through
// testenv.Bazel, which gives Bazel an output root of the test binary's
// own and removes it with every server in it when the tests end.
func NeedBazel(t *testing.T) {
	t.Helper()

	if testing.Short() {
		t.Skip("starts Bazel")
	}

	if os.Getenv(testenv.BazelMark) == "" {
		t.Fatal("this test starts Bazel: its package's TestMain must run the tests through testenv.Bazel")
	}
}

// Bazel returns a client for a workspace directory whose server is removed
// with its output when the test ends.
func Bazel(t *testing.T, dir string) *bazel.Client {
	t.Helper()
	NeedBazel(t)

	// the output root is the test binary's own (testenv.Bazel)
	c := &bazel.Client{Bazelisk: "bazelisk", Dir: dir, StartupFlags: []string{"--max_idle_secs=30"}}

	t.Cleanup(func() {
		if _, err := c.Run(context.Background(), "clean", "--expunge"); err != nil {
			t.Logf("bazel clean: %v", err)
		}
	})

	return c
}
