package releasetest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// releasesGH is a gh on PATH that keeps the files of the one release it creates in store, and
// downloads them again.
func releasesGH(t *testing.T, dir, store string) string {
	t.Helper()

	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}

	gh := `#!/bin/sh
store='` + store + `'
case "$1 $2" in
"release view") [ -n "$(ls -A "$store")" ] ;;
"release create") for a in "$@"; do if [ -f "$a" ]; then cp "$a" "$store/"; fi; done ;;
"release download") while [ $# -gt 0 ]; do if [ "$1" = -D ]; then d=$2; fi; shift; done; cp "$store"/* "$d/" ;;
*) exit 1 ;;
esac
`

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "gh"), []byte(gh)); err != nil {
		t.Fatal(err)
	}

	return bin
}

func TestPatchTagsBuildsPublishesAndVerifies(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, map[string]string{
		"probe/go.mod":   "module example.com/widget/probe\n\ngo 1.23\n",
		"probe/probe.go": "package probe\n",
		// a module of another path is not released with it
		"example/go.mod":  "module example.com/demo\n\ngo 1.27\n",
		"example/main.go": "package main\n\nfunc main() {}\n",
	})
	// the program takes its name from origin's
	repo, origin, store := filepath.Join(work, "widget"), filepath.Join(work, "remote", "widget.git"), filepath.Join(work, "store")

	run(t, repo, "git", "tag", "-d", "v1.2.3")
	run(t, work, "git", "clone", "-q", "--bare", repo, origin)
	run(t, repo, "git", "remote", "set-url", "origin", origin)

	if err := os.MkdirAll(store, 0o750); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(), "PATH="+releasesGH(t, work, store)+":"+os.Getenv("PATH"))
	patch := func() (string, error) {
		cmd := exec.CommandContext(t.Context(), releaseBin(t), "patch", "widget", "owner/widget", "v1.2.3", "out")
		cmd.Dir, cmd.Env = work, env

		out, err := cmd.CombinedOutput()

		return string(out), err
	}

	if err := os.WriteFile(filepath.Join(repo, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := patch(); err == nil || !strings.Contains(out, "changes that aren't committed") {
		t.Fatalf("a dirty clone: %v\n%s", err, out)
	}

	if err := os.Remove(filepath.Join(repo, "stray")); err != nil {
		t.Fatal(err)
	}

	out, err := patch()
	if err != nil || !strings.Contains(out, "owner/widget v1.2.3 (") || !strings.Contains(out, ") verified by download, SHA256SUMS ") {
		t.Fatalf("patch: %v\n%s", err, out)
	}

	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	if tags := run(t, origin, "git", "tag", "--points-at", head); tags != "probe/v1.2.3\nv1.2.3\n" {
		t.Errorf("origin's tags on main's head: %q, want v1.2.3 and probe/v1.2.3 alone", tags)
	}

	published, errA := os.ReadFile(filepath.Join(store, "SHA256SUMS"))
	built, errB := os.ReadFile(filepath.Join(work, "out", "SHA256SUMS"))

	if errA != nil || errB != nil || !bytes.Equal(published, built) {
		t.Errorf("the release's SHA256SUMS %q (%v), the build's %q (%v)", published, errA, built, errB)
	}

	// once more, as after a push that failed: the tags stay and are pushed, the build stays, and the
	// release is checked
	run(t, origin, "git", "tag", "-d", "v1.2.3", "probe/v1.2.3")

	out, err = patch()

	if tags := run(t, origin, "git", "tag", "--points-at", head); tags != "probe/v1.2.3\nv1.2.3\n" {
		t.Errorf("origin's tags on main's head after the second run: %q", tags)
	}

	if err != nil || !strings.Contains(out, "v1.2.3 is tagged already") || !strings.Contains(out, "keeping the build in") ||
		!strings.Contains(out, "released with exactly these files") || !strings.Contains(out, "verified by download") {
		t.Errorf("again: %v\n%s", err, out)
	}

	// a published release that differs from the build fails the check
	if err := os.WriteFile(filepath.Join(store, "widget-linux-amd64"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := patch(); err == nil || !strings.Contains(out, "don't match its SHA256SUMS") {
		t.Errorf("a changed release: %v\n%s", err, out)
	}
}

// A build that fails a check pushes no tag: a pushed tag never moves, so it could never be released.
func TestAPatchWhoseBuildFailsPushesNoTag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}

	work := widget(t, map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n"})
	repo, origin, store := filepath.Join(work, "widget"), filepath.Join(work, "remote", "widget.git"), filepath.Join(work, "store")

	run(t, repo, "git", "tag", "-d", "v1.2.3")
	run(t, work, "git", "clone", "-q", "--bare", repo, origin)
	run(t, repo, "git", "remote", "set-url", "origin", origin)

	if err := os.MkdirAll(store, 0o750); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "patch", "widget", "owner/widget", "v1.2.3", "out")
	cmd.Dir, cmd.Env = work, append(os.Environ(), "PATH="+releasesGH(t, work, store)+":"+os.Getenv("PATH"))

	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), `no entry "## [1.2.3]"`) {
		t.Fatalf("a changelog without the release: %v\n%s", err, out)
	}

	if tags := run(t, origin, "git", "tag"); tags != "" {
		t.Errorf("origin has tags %q after a build that failed", tags)
	}

	if tags := run(t, repo, "git", "tag", "-l", "v1.2.3"); tags != "" {
		t.Errorf("the clone keeps the tag %q it made for a build that failed", tags)
	}
}
