package releasetest

import (
	"debug/buildinfo"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/release"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// pluginsLicence is the plugins' LICENSE: a NOTICE names the licence by its title.
const pluginsLicence = "                                 Apache License\n\nThe plugins' licence.\n"

// plugins is a repository of plugins in directories: a root module that holds no package, the root's
// LICENSE, and the plugin driver/widget with a nested module, its own NOTICE and its tags
// driver/widget/v1.2.3 and driver/widget/probe/v1.2.3 on the head.
func plugins(t *testing.T) (work, repo, mod string) {
	t.Helper()

	work = t.TempDir()
	repo = filepath.Join(work, "plugins")
	mod = filepath.Join(repo, "driver", "widget")

	for name, content := range map[string]string{
		"go.mod":                               "module example.com/plugins\n\ngo 1.27\n",
		"go.work":                              "go 1.27\n\nuse ./driver/widget\n",
		"LICENSE":                              pluginsLicence,
		"driver/widget/go.mod":                 "module example.com/plugins/driver/widget\n\ngo 1.27\n",
		"driver/widget/cmd/widget-driver/x.go": program,
		"driver/widget/probe/go.mod":           "module example.com/plugins/driver/widget/probe\n\ngo 1.23\n",
		"driver/widget/probe/probe.go":         "package probe\n",
		// a file the module names for its release, by its path in the module's directory
		"driver/widget/.pipelinegen-release": "marks/marks.txt\n",
		"driver/widget/marks/marks.txt":      "marks\n",
	} {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// the module is one of a repository's directories once there is a repository
	run(t, repo, "git", "init", "-q", "-b", "main")

	text, err := release.Notice(t.Context(), release.IO{Stdout: io.Discard, Stderr: io.Discard}, mod)
	if err != nil {
		t.Fatalf("notice: %v", err)
	}

	if err := os.WriteFile(filepath.Join(mod, "NOTICE"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.email=test@example.com", "-c", "user.name=test", "-c", "commit.gpgsign=false",
		"commit", "-q", "-m", "plugins")
	run(t, repo, "git", "tag", "driver/widget/v1.2.3")
	run(t, repo, "git", "tag", "driver/widget/probe/v1.2.3")

	return work, repo, mod
}

func TestAModuleInADirectoryIsReleasedByItsOwnTag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program eight times")
	}

	work, repo, mod := plugins(t)

	if !strings.HasPrefix(run(t, mod, "cat", "NOTICE"), "widget-driver\n") {
		t.Errorf("notice names another program than cmd/widget-driver:\n%s", run(t, mod, "cat", "NOTICE"))
	}

	// the version alone names the directory's tag too
	run(t, work, releaseBin(t), "build", "plugins/driver/widget", "v1.2.3", "out")

	out := filepath.Join(work, "out")
	sums := run(t, out, "cat", "SHA256SUMS")

	for _, f := range []string{"widget-driver-linux-amd64", "widget-driver-darwin-arm64", "plugin.json", "LICENSE", "NOTICE", "marks.txt"} {
		if !strings.Contains(sums, "  "+f+"\n") {
			t.Errorf("SHA256SUMS lists no %s:\n%s", f, sums)
		}
	}

	if got := run(t, out, "cat", "LICENSE"); got != pluginsLicence {
		t.Errorf("LICENSE %q, want the repository's", got)
	}

	if got, want := run(t, out, "cat", "NOTICE"), run(t, mod, "cat", "NOTICE"); got != want {
		t.Errorf("NOTICE %q, want the module's own", got)
	}

	if got := run(t, out, "cat", "plugin.json"); !strings.Contains(got, `"version":"v1.2.3"`) {
		t.Errorf("plugin.json %q, want the version v1.2.3", got)
	}

	// the program records the repository's commit and the version its directory's tag names
	info, err := buildinfo.ReadFile(filepath.Join(out, "widget-driver-linux-amd64"))
	if err != nil {
		t.Fatal(err)
	}

	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))

	var revision string

	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			revision = s.Value
		}
	}

	if revision != head || info.Main.Version != "v1.2.3" || info.Main.Path != "example.com/plugins/driver/widget" {
		t.Errorf("build info: revision %s, module %s %s; want %s, example.com/plugins/driver/widget v1.2.3", revision, info.Main.Path, info.Main.Version, head)
	}

	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if got := run(t, work, filepath.Join(out, "widget-driver-linux-amd64"), "version"); got != "v1.2.3\n" {
			t.Errorf("version %q, want v1.2.3", got)
		}
	}

	// the whole tag gives the same files; a nested module needs its tag under the directory
	run(t, work, releaseBin(t), "build", "plugins/driver/widget", "driver/widget/v1.2.3", "again")

	if again := run(t, filepath.Join(work, "again"), "cat", "SHA256SUMS"); again != sums {
		t.Errorf("by its whole tag:\n%s\nby its version:\n%s", again, sums)
	}

	run(t, repo, "git", "tag", "-d", "driver/widget/probe/v1.2.3")

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "plugins/driver/widget", "v1.2.3", "out3")
	cmd.Dir = work

	if said, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(said), "needs the tag driver/widget/probe/v1.2.3") {
		t.Errorf("without driver/widget/probe/v1.2.3: %v\n%s", err, said)
	}
}

func TestPatchAndCheckReleaseAModuleInADirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program eight times")
	}

	work, repo, _ := plugins(t)
	origin, store := filepath.Join(work, "remote", "plugins.git"), filepath.Join(work, "store")

	run(t, repo, "git", "tag", "-d", "driver/widget/v1.2.3", "driver/widget/probe/v1.2.3")
	run(t, work, "git", "clone", "-q", "--bare", repo, origin)
	run(t, repo, "git", "remote", "add", "origin", origin)
	run(t, repo, "git", "fetch", "-q", "origin")
	run(t, repo, "git", "branch", "-q", "-u", "origin/main")

	if err := os.MkdirAll(store, 0o750); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(), "PATH="+releasesGH(t, work, store)+":"+os.Getenv("PATH"))
	release := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), releaseBin(t), args...)
		cmd.Dir, cmd.Env = work, env

		out, err := cmd.CombinedOutput()

		return string(out), err
	}

	out, err := release("patch", "plugins/driver/widget", "owner/plugins", "v1.2.3", "out")
	if err != nil || !strings.Contains(out, "owner/plugins driver/widget/v1.2.3 (") || !strings.Contains(out, "verified by download") {
		t.Fatalf("patch: %v\n%s", err, out)
	}

	head := strings.TrimSpace(run(t, repo, "git", "rev-parse", "HEAD"))
	if tags := run(t, origin, "git", "tag", "--points-at", head); tags != "driver/widget/probe/v1.2.3\ndriver/widget/v1.2.3\n" {
		t.Errorf("origin's tags on main's head: %q, want the directory's and its nested module's", tags)
	}

	if out, err := release("check", "plugins/driver/widget", "owner/plugins", "driver/widget/v1.2.3"); err != nil || !strings.Contains(out, "is byte for byte a fresh build of its tag") {
		t.Errorf("check: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(store, "widget-driver-linux-amd64"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := release("check", "plugins/driver/widget", "owner/plugins", "v1.2.3"); err == nil || !strings.Contains(out, "don't match its SHA256SUMS") {
		t.Errorf("check of a changed release: %v\n%s", err, out)
	}
}

func TestTaggedReleasesTheDirectorysTagOnTheHead(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program eight times")
	}

	work, repo, _ := plugins(t)

	// another plugin's tag on the same commit is not this one's
	run(t, repo, "git", "tag", "driver/other/v9.9.9")

	bin, calls := filepath.Join(work, "bin"), filepath.Join(work, "calls")
	gh := `#!/bin/sh
echo "$*" >> '` + calls + `'
case "$1 $2" in
"api repos/owner/plugins/commits/"*) echo "pipelinegen/nightly success" ;;
"release view") exit 1 ;;
esac
`

	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "gh"), []byte(gh)); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	cmd := exec.CommandContext(t.Context(), releaseBin(t), "tagged", "plugins/driver/widget", "owner/plugins", "out", "pipelinegen/nightly")
	cmd.Dir, cmd.Env = work, env

	out, err := cmd.CombinedOutput()
	got, _ := os.ReadFile(calls) //nolint:errcheck // no calls is an empty file

	if err != nil || !strings.Contains(string(got), "release create driver/widget/v1.2.3 -R owner/plugins") {
		t.Errorf("exit %v, gh called with\n%s\n%s", err, got, out)
	}
}
