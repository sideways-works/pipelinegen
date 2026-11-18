package launch

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckVersions(t *testing.T) {
	pinned := map[string]string{"golang": "1.27.1", "golangci-lint": "2.14.0"}

	if err := CheckVersions("# tools\ngolang 1.27.1\ngolangci-lint 2.14.0\nnodejs 26.10.0\n", pinned); err != nil {
		t.Errorf("the same versions: %v", err)
	}

	err := CheckVersions("golang 1.27.0\n", pinned)
	if err == nil || !strings.Contains(err.Error(), `golang "1.27.0", MODULE.bazel pins 1.27.1`) || !strings.Contains(err.Error(), `golangci-lint "", MODULE.bazel pins 2.14.0`) {
		t.Errorf("other versions: %v", err)
	}
}

func TestChildEnvRunsThePinnedGoAndHidesBazelRun(t *testing.T) {
	env := ChildEnv([]string{
		"HOME=/home/x", "PATH=/usr/bin", "GOROOT=/other/go", "GOTOOLCHAIN=auto", "GOFLAGS=-trimpath",
		"BUILD_WORKSPACE_DIRECTORY=/src", "BUILD_WORKING_DIRECTORY=/src/a", "RUNFILES_DIR=/r", "JAVA_RUNFILES=/r",
	}, "/sdk/bin/go")

	want := []string{"HOME=/home/x", "GOFLAGS=-trimpath", "PATH=/sdk/bin:/usr/bin", "GOTOOLCHAIN=local"}
	if !slices.Equal(env, want) {
		t.Errorf("got %q, want %q", env, want)
	}
}

func TestRlocationResolvesLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sdk", "bin", "go")
	runfiles := filepath.Join(dir, "x.runfiles")

	for _, d := range []string{filepath.Dir(target), filepath.Join(runfiles, "go_sdk", "bin")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(target, filepath.Join(runfiles, "go_sdk", "bin", "go")); err != nil {
		t.Fatal(err)
	}

	manifest := filepath.Join(dir, "MANIFEST")
	if err := os.WriteFile(manifest, []byte("other/x /nowhere\ngo_sdk/bin/go "+target+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}

	for name, env := range map[string][]string{
		"next to the executable": nil,
		"RUNFILES_DIR":           {"RUNFILES_DIR=" + runfiles},
		"a manifest alone":       {"RUNFILES_MANIFEST_FILE=" + manifest},
	} {
		got, err := Rlocation(env, filepath.Join(dir, "x"), "go_sdk/bin/go")
		if err != nil || got != want {
			t.Errorf("%s: %q (%v), want %q", name, got, err, want)
		}
	}

	if _, err := Rlocation(nil, filepath.Join(dir, "x"), "go_sdk/bin/gofmt"); err == nil {
		t.Error("a file the runfiles don't hold was found")
	}
}

func TestTallyCountsPackagesAndKeepsFailures(t *testing.T) {
	var out bytes.Buffer

	tally := &Tally{Out: &out}
	text := "ok  \tx/a\t0.1s\n--- FAIL: TestB (0.00s)\nFAIL\n" + "FAIL\tx/b\t0.2s\nok  \tx/c\t(cached)\npanic: boom\n"

	// go test's output arrives in pieces that split lines
	for chunk := range slices.Chunk([]byte(text), 7) {
		if _, err := tally.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"--- FAIL: TestB (0.00s)", "FAIL", "FAIL\tx/b\t0.2s", "panic: boom"}
	if tally.OK != 2 || tally.Failed != 1 || !slices.Equal(tally.Failures, want) || out.String() != text {
		t.Errorf("ok %d, failed %d, failures %q, passed on %q", tally.OK, tally.Failed, tally.Failures, out.String())
	}
}

const program = `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	wd, _ := os.Getwd()
	fmt.Printf("in %s with %s; workspace %q\n", wd, strings.Join(os.Args[1:], " "), os.Getenv("BUILD_WORKSPACE_DIRECTORY"))
	os.Exit(3)
}
`

func TestRunBuildsTheProgramAndRunsItWhereBazelRunStarted(t *testing.T) {
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	workspace, working, runfiles := filepath.Join(dir, "src"), filepath.Join(dir, "src", "sub"), filepath.Join(dir, "launch.runfiles")

	for name, content := range map[string]string{"go.mod": "module example.com/x\n\ngo 1.27\n", "ci/x/main.go": program, "sub/.keep": ""} {
		p := filepath.Join(workspace, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.MkdirAll(filepath.Join(runfiles, "go_sdk", "bin"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(goTool, filepath.Join(runfiles, "go_sdk", "bin", "go")); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	l := &Launch{
		Env:        append(os.Environ(), "BUILD_WORKSPACE_DIRECTORY="+workspace, "BUILD_WORKING_DIRECTORY="+working),
		Executable: filepath.Join(dir, "launch"),
		Stdout:     &stdout, Stderr: &stderr,
	}

	code := l.Main(t.Context(), []string{"--go=go_sdk/bin/go", "run", "./ci/x", "a", "b"})
	if want := "in " + working + " with a b; workspace \"\"\n"; code != 3 || stdout.String() != want {
		t.Errorf("exit %d, printed %q, want exit 3 and %q\n%s", code, stdout.String(), want, stderr.String())
	}

	l.Env = os.Environ()
	if code := l.Main(t.Context(), []string{"--go=go_sdk/bin/go", "run", "./ci/x"}); code != 2 || !strings.Contains(stderr.String(), "run it with bazel run") {
		t.Errorf("outside bazel run: exit %d\n%s", code, stderr.String())
	}
}
