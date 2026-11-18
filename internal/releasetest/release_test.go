// Package releasetest checks the release command (ci/release) on a small repository of
// its own.
package releasetest

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/release"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

const program = `package main

import (
	"fmt"
	"os"
)

// Version is the release's tag, set by the release build.
var Version = "dev"

func main() {
	switch os.Args[1] {
	case "version":
		fmt.Println(Version)
	case "describe":
		fmt.Printf("{\"protocol\":1,\"kind\":\"sbom\",\"name\":\"widget\",\"version\":%q,\"toolchains\":{\"go\":\"go1.0.0\"}}\n", Version)
	}
}
`

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir

	var out bytes.Buffer

	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}

	return out.String()
}

func widget(t *testing.T, extra map[string]string) string {
	t.Helper()

	work := t.TempDir()
	repo := filepath.Join(work, "widget")
	files := map[string]string{
		"go.mod":             "module example.com/widget\n\ngo 1.27\n",
		"cmd/widget/main.go": program,
		"LICENSE":            "The widget's licence.\n",
		// written once the repository is made, as the release needs it
		"NOTICE": "widget\nThe widget's notices.\n",
	}

	maps.Copy(files, extra)

	for name, content := range files {
		// an empty file is one the repository leaves out
		if content == "" {
			continue
		}

		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	run(t, repo, "git", "init", "-q", "-b", "main")
	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.email=test@example.com", "-c", "user.name=test", "-c", "commit.gpgsign=false",
		"commit", "-q", "-m", "widget")
	run(t, repo, "git", "remote", "add", "origin", "https://github.com/owner/widget.git")
	run(t, repo, "git", "tag", "v1.2.3")

	// a NOTICE the test gives is the one it means; otherwise the one the release needs
	if _, given := extra["NOTICE"]; !given && exists(filepath.Join(repo, "go.mod")) && exists(filepath.Join(repo, "cmd", "widget", "main.go")) {
		renotice(t, work)
	}

	return work
}

func exists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

var (
	built    sync.Once
	builtBin string
	errBuild error
)

func releaseBin(t *testing.T) string {
	t.Helper()

	built.Do(func() {
		dir, err := os.MkdirTemp("", "release-bin-") //nolint:usetesting // built once for every test of the package
		if err != nil {
			errBuild = err

			return
		}

		builtBin = filepath.Join(dir, "release")

		// without VCS stamping: the command under test must not depend on the checkout the tests
		// run in, which git may refuse to read (another user's, a tarball's, a shallow export's)
		out, err := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-o", builtBin, "../../ci/release").CombinedOutput()
		if err != nil {
			errBuild = fmt.Errorf("%w: %s", err, out)
		}
	})

	if errBuild != nil {
		t.Fatal(errBuild)
	}

	return builtBin
}

func renotice(t *testing.T, work string, tags ...string) {
	t.Helper()

	repo := filepath.Join(work, "widget")

	text, err := release.Notice(t.Context(), release.IO{Stdout: io.Discard, Stderr: io.Discard}, repo)
	if err != nil {
		t.Fatalf("notice: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo, "NOTICE"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	run(t, repo, "git", "add", "-A")
	run(t, repo, "git", "-c", "user.email=test@example.com", "-c", "user.name=test", "-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", "notice")

	for _, tag := range append([]string{"v1.2.3"}, tags...) {
		run(t, repo, "git", "tag", "-f", tag)
	}
}

func TestBuildWithRelativePaths(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, nil)

	run(t, work, releaseBin(t), "build", "widget", "v1.2.3", "out")

	out := filepath.Join(work, "out")

	sums, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"widget-linux-amd64", "widget-linux-arm64", "widget-darwin-amd64", "widget-darwin-arm64", "plugin.json", "LICENSE", "NOTICE"} {
		if !strings.Contains(string(sums), "  "+f+"\n") {
			t.Errorf("SHA256SUMS lists no %s:\n%s", f, sums)
		}
	}

	meta, err := os.ReadFile(filepath.Join(out, "plugin.json"))
	if err != nil || !strings.Contains(string(meta), `"version":"v1.2.3"`) {
		t.Errorf("plugin.json %q (%v), want version v1.2.3", meta, err)
	}

	// the versions one run resolved are no fact about the release
	if strings.Contains(string(meta), "toolchains") {
		t.Errorf("plugin.json %q holds the build machine's toolchains", meta)
	}

	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if got := run(t, work, filepath.Join(out, "widget-linux-amd64"), "version"); got != "v1.2.3\n" {
			t.Errorf("version %q, want v1.2.3", got)
		}
	}
}

func TestARootMainIsNamedByItsModule(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, map[string]string{
		"go.mod":             "module example.com/pipelinegen-sbom-gadget\n\ngo 1.27\n",
		"cmd/widget/main.go": "",
		"main.go":            program,
	})
	renotice(t, work)

	run(t, work, releaseBin(t), "build", "widget", "v1.2.3", "out")

	out := filepath.Join(work, "out")

	sums, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"gadget-sbom-linux-amd64", "gadget-sbom-linux-arm64", "gadget-sbom-darwin-amd64", "gadget-sbom-darwin-arm64", "plugin.json"} {
		if !strings.Contains(string(sums), "  "+f+"\n") {
			t.Errorf("SHA256SUMS lists no %s:\n%s", f, sums)
		}
	}

	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		if got := run(t, work, filepath.Join(out, "gadget-sbom-linux-amd64"), "version"); got != "v1.2.3\n" {
			t.Errorf("version %q, want v1.2.3", got)
		}
	}
}

func TestAPluginThatDoesntDescribeFailsTheBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program twice")
	}

	broken := strings.Replace(program, `	case "describe":
		fmt.Printf(`, `	case "describe":
		fmt.Fprintln(os.Stderr, "out of memory")
		os.Exit(1)
		fmt.Printf(`, 1)
	work := widget(t, map[string]string{"cmd/widget/main.go": broken})

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", "v1.2.3", "out")
	cmd.Dir = work

	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "the plugin widget didn't answer describe (exit 1): out of memory") {
		t.Fatalf("a plugin without an answer: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(work, "out", "SHA256SUMS")); err == nil {
		t.Fatal("the failed build left a release behind")
	}

	tool := strings.Replace(program, `	case "describe":`, `	case "nothing":`, 1)
	work = widget(t, map[string]string{"cmd/widget/main.go": "", "cmd/pipelinegen/main.go": tool})
	run(t, filepath.Join(work, "widget"), "git", "remote", "set-url", "origin", "https://github.com/owner/pipelinegen.git")
	renotice(t, work)
	run(t, work, releaseBin(t), "build", "widget", "v1.2.3", "out")

	if _, err := os.Stat(filepath.Join(work, "out", "plugin.json")); err == nil {
		t.Fatal("pipelinegen got a plugin.json")
	}
}

func TestNestedModuleNeedsItsTag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, map[string]string{
		"probe/go.mod":            "module example.com/widget/probe\n\ngo 1.23\n",
		"probe/probe.go":          "package probe\n",
		"probe/testdata/x/go.mod": "module example.com/x\n",
		// a module of another path (an example) is not released with it
		"example/go.mod":  "module example.com/demo\n\ngo 1.27\n",
		"example/main.go": "package main\n\nfunc main() {}\n",
	})

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", "v1.2.3", "out")
	cmd.Dir = work

	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "example.com/widget/probe needs the tag probe/v1.2.3") {
		t.Fatalf("without probe/v1.2.3: %v\n%s", err, out)
	}

	run(t, filepath.Join(work, "widget"), "git", "tag", "probe/v1.2.3", "v1.2.3")

	run(t, work, releaseBin(t), "build", "widget", "v1.2.3", "out2")
}

// fakeGH is a gh on PATH that knows no release and writes the arguments of each call to a file in
// dir, one call a line.
func fakeGH(t *testing.T, dir string) string {
	t.Helper()

	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}

	calls := filepath.Join(dir, "calls")
	gh := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n[ \"$1 $2\" = \"release view\" ] && exit 1\nexit 0\n"

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "gh"), []byte(gh)); err != nil {
		t.Fatal(err)
	}

	return calls
}

func TestACandidateIsAPrereleaseNeverTheLatest(t *testing.T) {
	out := t.TempDir()
	if err := os.WriteFile(filepath.Join(out, "widget-linux-amd64"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	run(t, out, "sh", "-c", "sha256sum widget-linux-amd64 > SHA256SUMS")

	for tag, prerelease := range map[string]bool{"v1.2.3-rc.1": true, "v1.2.3": false} {
		dir := t.TempDir()
		calls := fakeGH(t, dir)

		cmd := exec.CommandContext(t.Context(), releaseBin(t), "publish", "owner/widget", tag, out)

		cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "bin")+":"+os.Getenv("PATH"))

		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", tag, err, b)
		}

		got, err := os.ReadFile(calls)
		if err != nil {
			t.Fatal(err)
		}

		create := strings.Contains(string(got), "release create "+tag+" ")
		marked := strings.Contains(string(got), "--prerelease --latest=false")

		if !create || marked != prerelease {
			t.Errorf("%s: gh was called with\n%s", tag, got)
		}
	}

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", t.TempDir(), "v1.2.3-beta", filepath.Join(t.TempDir(), "out"))
	if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), "is no version tag") {
		t.Errorf("v1.2.3-beta: %v\n%s", err, b)
	}
}

func TestACandidateBuildsWithItsTag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, map[string]string{"CHANGELOG.md": "# Changelog\n\n## [Unreleased]\n\n## [1.2.3] - 2026-01-02\n"})
	run(t, filepath.Join(work, "widget"), "git", "tag", "v1.2.4-rc.1")

	run(t, work, releaseBin(t), "build", "widget", "v1.2.4-rc.1", "out")

	meta, err := os.ReadFile(filepath.Join(work, "out", "plugin.json"))
	if err != nil || !strings.Contains(string(meta), `"version":"v1.2.4-rc.1"`) {
		t.Errorf("plugin.json %q (%v), want version v1.2.4-rc.1", meta, err)
	}
}

func TestOnlyACommitEveryTestPassedOnIsReleased(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"

	for name, tc := range map[string]struct {
		statuses string
		broken   bool
		passes   bool
	}{
		"the nightly passed":          {statuses: "pipelinegen/main success\npipelinegen/nightly success\n", passes: true},
		"the nightly failed":          {statuses: "pipelinegen/main success\npipelinegen/nightly failure\n"},
		"the nightly is still going":  {statuses: "pipelinegen/nightly pending\n"},
		"only an everyday run passed": {statuses: "pipelinegen/main success\n"},
		"a prefix of the context":     {statuses: "pipelinegen/nightly-old success\n"},
		"the statuses can't be read":  {broken: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			calls := filepath.Join(dir, "calls")

			gh := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n"
			if tc.broken {
				gh += "exit 1\n"
			} else {
				gh += "printf '" + strings.ReplaceAll(tc.statuses, "\n", `\n`) + "'\n"
			}

			if err := os.MkdirAll(bin, 0o750); err != nil {
				t.Fatal(err)
			}

			if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "gh"), []byte(gh)); err != nil {
				t.Fatal(err)
			}

			cmd := exec.CommandContext(t.Context(), releaseBin(t), "tested", "owner/widget", commit, "pipelinegen/nightly")

			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))

			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.passes {
				t.Fatalf("passed %v, want %v:\n%s", err == nil, tc.passes, out)
			}

			got, err := os.ReadFile(calls)
			if err != nil || !strings.Contains(string(got), "api repos/owner/widget/commits/"+commit+"/status") {
				t.Errorf("gh was called with %q (%v)", got, err)
			}
		})
	}
}

func TestAReleaseNeedsItsLicenceAndNotice(t *testing.T) {
	for _, missing := range []string{"LICENSE", "NOTICE"} {
		work := widget(t, map[string]string{missing: ""})

		cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", "v1.2.3", "out")
		cmd.Dir = work

		if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), "v1.2.3 has no "+missing) {
			t.Errorf("a release without %s: %v\n%s", missing, err, b)
		}

		if _, err := os.Stat(filepath.Join(work, "out")); err == nil {
			t.Errorf("without %s: built", missing)
		}
	}
}

func TestAReleaseNeedsItsChangelogEntry(t *testing.T) {
	for _, c := range []struct {
		name, changelog, tag, want string
	}{
		{"no entry", "# Changelog\n\n## [1.2.2] - 2026-01-01\n", "v1.2.3", `no entry "## [1.2.3]"`},
		{"no entry for a candidate", "# Changelog\n\n## [1.2.2] - 2026-01-01\n", "v1.2.3-rc.1", `"## [Unreleased]"`},
		{"a release under unreleased", "# Changelog\n\n## [Unreleased]\n", "v1.2.3", `no entry "## [1.2.3]"`},
		{"a lookalike", "# Changelog\n\n## [1.2.30] - 2026-01-01\n### [1.2.3]\n", "v1.2.3", `no entry "## [1.2.3]"`},
	} {
		work := widget(t, map[string]string{"CHANGELOG.md": c.changelog})
		run(t, filepath.Join(work, "widget"), "git", "tag", "-f", c.tag)

		cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", c.tag, "out")
		cmd.Dir = work

		b, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(b), c.want) {
			t.Errorf("%s: %v\n%s\nwant %q", c.name, err, b, c.want)
		}

		if _, err := os.Stat(filepath.Join(work, "out")); err == nil {
			t.Errorf("%s: built although the changelog has no entry", c.name)
		}
	}
}

func TestANoticeThatIsNotTheReleasesIsRefused(t *testing.T) {
	work := widget(t, map[string]string{"NOTICE": "widget\nThe widget's notices.\n"})

	cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", "v1.2.3", "out")
	cmd.Dir = work

	if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), "release: NOTICE is not the one its release files need") {
		t.Fatalf("a NOTICE of the widget's own making: %v\n%s", err, b)
	}

	run(t, work, releaseBin(t), "notice", "widget")

	text, err := os.ReadFile(filepath.Join(work, "widget", "NOTICE"))
	if err != nil || !strings.HasPrefix(string(text), "widget\nCopyright 2026 Sideways Works UG (haftungsbeschränkt)\n") ||
		!strings.Contains(string(text), "\nThe Go standard library (go") {
		t.Errorf("the written NOTICE: %v\n%s", err, text)
	}
}

func TestAGoModuleNamesTheFilesItsReleaseCarries(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program four times")
	}

	work := widget(t, map[string]string{
		".pipelinegen-release": "# taken by suites without building\nweb/marks.mjs\n",
		"web/marks.mjs":        "export const mark = 1;\n",
	})
	run(t, work, releaseBin(t), "build", "widget", "v1.2.3", "out")

	sums, err := os.ReadFile(filepath.Join(work, "out", "SHA256SUMS"))
	if err != nil || !strings.Contains(string(sums), "  marks.mjs\n") {
		t.Errorf("SHA256SUMS: %v\n%s", err, sums)
	}

	for list, want := range map[string]string{
		"NOTICE\n":      "NOTICE would replace the release's own NOTICE",
		"web/gone.js\n": "web/gone.js is named for the release but not committed",
	} {
		work := widget(t, map[string]string{".pipelinegen-release": list})

		cmd := exec.CommandContext(t.Context(), releaseBin(t), "build", "widget", "v1.2.3", "out")
		cmd.Dir = work

		if b, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(b), want) {
			t.Errorf("%q: %v\n%s\nwant %q", list, err, b, want)
		}
	}
}
