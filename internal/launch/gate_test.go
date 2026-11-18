package launch

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// A workspace whose checks fail on demand: a file named after a check at its root fails it.
var gateFixture = map[string]string{
	"go.mod": "module example.com/w\n\ngo 1.27\n",
	"cmd/pipelinegen/main.go": `package main

import "os"

func main() {
	if _, err := os.Stat("check-fails"); err == nil || os.Args[1] != "check" {
		os.Exit(1)
	}
}
`,
	"ci/proof/main.go": `package main

import (
	"fmt"
	"os"
)

func main() {
	work := os.Args[len(os.Args)-1]
	fmt.Printf("proof %s in %s\n", os.Args[1], work)
	if err := os.MkdirAll(work, 0o750); err != nil {
		os.Exit(2)
	}
	if _, err := os.Stat("proof-fails"); err == nil {
		os.Exit(1)
	}
}
`,
	"pkg/pkg_test.go": `package pkg

import (
	"os"
	"testing"
)

func TestIt(t *testing.T) {
	if _, err := os.Stat("../tests-fail"); err == nil {
		t.Fatal("asked to fail")
	}
}
`,
}

const fakeLint = `#!/bin/sh
case "$1" in
version) echo 2.14.0 ;;
run) [ ! -e lint-fails ] ;;
*) exit 2 ;;
esac
`

func gateWorkspace(t *testing.T) (string, *Launch, *bytes.Buffer) {
	t.Helper()

	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}

	goVersion, err := exec.CommandContext(t.Context(), goTool, "env", "GOVERSION").Output()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	workspace, runfiles := filepath.Join(dir, "src"), filepath.Join(dir, "gate.runfiles")

	files := map[string]string{".tool-versions": "golang " + strings.TrimPrefix(strings.TrimSpace(string(goVersion)), "go") + "\ngolangci-lint 2.14.0\n"}
	maps.Copy(files, gateFixture)

	for name, content := range files {
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

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(runfiles, "golangci-lint"), []byte(fakeLint)); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	return workspace, &Launch{
		Env:        append(os.Environ(), "BUILD_WORKSPACE_DIRECTORY="+workspace, "TMPDIR="+t.TempDir()),
		Executable: filepath.Join(dir, "gate"),
		Stdout:     &out, Stderr: &out,
	}, &out
}

func touch(t *testing.T, dir, name string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

var gateArgsFixed = []string{"--go=go_sdk/bin/go", "--lint=golangci-lint", "gate"}

func TestGatePassesAndRemovesTheProofsWorkDirectory(t *testing.T) {
	_, l, out := gateWorkspace(t)

	if code := l.Main(t.Context(), gateArgsFixed); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}

	for _, want := range []string{"== build ok", "== vet ok", "== lint ok", "== tests ok", "== packages ok: 1, failed: 0", "== proof ok", "proof --source in "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}

	work := regexp.MustCompile(`proof --source in (\S+)`).FindStringSubmatch(out.String())
	if len(work) < 2 {
		t.Fatalf("the proof named no work directory\n%s", out)
	}

	if _, err := os.Stat(work[1]); !os.IsNotExist(err) {
		t.Errorf("the proof's work directory %s stays after a pass (%v)", work[1], err)
	}
}

func TestGateStopsAtAFailedLint(t *testing.T) {
	for name, tc := range map[string]struct {
		file, toolVersions, says string
	}{
		"golangci-lint":       {file: "lint-fails", says: "golangci-lint: exit status 1"},
		"pipelinegen check":   {file: "check-fails", says: "pipelinegen check exited 1"},
		"another lint pinned": {toolVersions: "golangci-lint 2.13.0\n", says: `golangci-lint "2.13.0", MODULE.bazel pins 2.14.0`},
	} {
		workspace, l, out := gateWorkspace(t)

		if tc.file != "" {
			touch(t, workspace, tc.file)
		}

		if tc.toolVersions != "" {
			if err := os.WriteFile(filepath.Join(workspace, ".tool-versions"), []byte(tc.toolVersions), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		code := l.Main(t.Context(), gateArgsFixed)
		if code == 0 || !strings.Contains(out.String(), "== lint FAILED") || !strings.Contains(out.String(), tc.says) || strings.Contains(out.String(), "== tests") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

func TestGateRunsTheProofAfterFailedTestsAndKeepsItsWork(t *testing.T) {
	workspace, l, out := gateWorkspace(t)
	touch(t, workspace, "tests-fail")
	touch(t, workspace, "proof-fails")

	work := filepath.Join(t.TempDir(), "proof")

	code := l.Main(t.Context(), append(gateArgsFixed, "--proof-work", work, "./pkg/..."))
	if code == 0 {
		t.Fatalf("exit 0\n%s", out)
	}

	for _, want := range []string{"== tests FAILED", "== packages ok: 0, failed: 1", "--- FAIL: TestIt", "proof --source in " + work, "== proof FAILED", "gate: tests:", "gate: proof: exited 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}

	if _, err := os.Stat(work); err != nil {
		t.Errorf("the proof's work directory is gone: %v", err)
	}
}
