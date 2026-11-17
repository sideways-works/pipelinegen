package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// runAll runs every slice of the driven fixture: a known reason to run everything asks neither Bazel nor
// bazel-diff, which the flag names only so nothing is fetched.
func runAll(t *testing.T, f drivenFixture, more ...string) (string, string, int) {
	t.Helper()

	args := append([]string{"run", "--all", "--root", f.root, "--cache", t.TempDir(), "--bazel-diff", "/nonexistent/bazel-diff"}, more...)

	return runIn(t, f.root, f.env, args...)
}

func localStoreOf(t *testing.T, root string) string {
	t.Helper()

	return filepath.Join(gitT(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir"), "pipelinegen", "local.git")
}

func TestRunWithAllRunsEverySlice(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	stdout, stderr, code := runAll(t, f)
	if code != 0 {
		t.Fatalf("run: %d %s\n%s", code, stdout, stderr)
	}

	for _, want := range []string{
		"every slice: every slice runs in this run\ne2e: 2 selected, 0 untouched\n",
		"slice e2e/cart.spec.ts: passed\n",
		"slice e2e/pay.spec.ts: passed\n",
		"  e2e: 2 passed, 0 failed, 0 errored\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("no %q in:\n%s", want, stdout)
		}
	}

	if got := drivenLog(t, f.driverLog); len(got) != 1 || !strings.HasSuffix(got[0], " e2e/cart.spec.ts,e2e/pay.spec.ts") || !strings.Contains(got[0], "marks=false") {
		t.Fatalf("driver calls: %q", got)
	}

	if _, err := os.Stat(localStoreOf(t, f.root)); err == nil {
		t.Fatal("a run without --record made the local store")
	}

	// the plan's work tree in the cache stays, as affected's does; the build's goes.
	if got := gitT(t, f.root, "worktree", "list"); strings.Contains(got, filepath.Join("build", "source")) {
		t.Errorf("the build's checkout is left: %s", got)
	}
}

func TestRunSaysWhichSliceFailed(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	writeFile(t, filepath.Join(f.root, "e2e/bad.spec.ts"), "test('bad')\n")
	gitT(t, f.root, "add", "-A", ":!tools")
	gitT(t, f.root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qm", "a failing spec")

	stdout, stderr, code := runAll(t, f)
	if code != 1 {
		t.Fatalf("run: %d %s\n%s", code, stdout, stderr)
	}

	for _, want := range []string{"slice e2e/bad.spec.ts: failed\n", "  e2e: 2 passed, 1 failed, 0 errored\n    failed e2e/bad.spec.ts\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("no %q in:\n%s", want, stdout)
		}
	}

	if !strings.Contains(stderr, "broke") {
		t.Errorf("the failure's tail isn't said:\n%s", stderr)
	}
}

func TestRunRecordsOnlyWhenEverySlicePassed(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	stdout, stderr, code := runAll(t, f, "--record")
	if code != 0 || !strings.Contains(stdout, "  e2e: 2 passed, 0 failed, 0 errored; its lists are attached to it in the local store\n") {
		t.Fatalf("run --record: %d %s\n%s", code, stdout, stderr)
	}

	l := recordedLists(t, f.root)
	for _, s := range []string{"e2e/cart.spec.ts", "e2e/pay.spec.ts"} {
		if l["e2e/"+s] != "unit:"+s {
			t.Errorf("%s's list: %q (all: %v)", s, l["e2e/"+s], l)
		}
	}

	writeFile(t, filepath.Join(f.root, "e2e/bad.spec.ts"), "test('bad')\n")
	gitT(t, f.root, "add", "-A", ":!tools")
	gitT(t, f.root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qm", "a failing spec")

	stdout, stderr, code = runAll(t, f, "--record")
	if code != 1 || !strings.Contains(stdout, "  e2e: 2 passed, 1 failed, 0 errored; no lists attached, as not every slice passed\n") {
		t.Fatalf("run --record with a failing slice: %d %s\n%s", code, stdout, stderr)
	}

	head := gitT(t, f.root, "rev-parse", "HEAD")
	if got := gitT(t, localStoreOf(t, f.root), "for-each-ref", lists.Ref(head)); got != "" {
		t.Fatalf("lists attached to a run with a failed slice: %s", got)
	}
}

func TestRunNamesTheChangesItLeavesOut(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	writeFile(t, filepath.Join(f.root, "e2e/cart.spec.ts"), "test('cart, changed')\n")

	stdout, stderr, code := runAll(t, f)
	if code != 0 || !strings.Contains(stderr, "the work tree has changes that are neither planned nor run") || !strings.Contains(stderr, "e2e/cart.spec.ts") ||
		!strings.HasSuffix(stdout, "the work tree's uncommitted changes were neither planned nor run\n") {
		t.Fatalf("run: %d %s\n%s", code, stdout, stderr)
	}

	if _, stderr, code := runAll(t, f, "--record"); code != 1 || !strings.Contains(stderr, "commit these first") {
		t.Fatalf("run --record on a changed tree: %d %s", code, stderr)
	}

	if _, stderr, code := runIn(t, f.root, f.env, "run", "--root", f.root); code != 2 || !strings.Contains(stderr, "--base is needed, or --all") {
		t.Fatalf("no base: %d %s", code, stderr)
	}
}

// exampleRun is the example's repository with its unit suite run by the stub driver, which fails a
// slice whose name holds "bad".
type exampleRun struct {
	r                         *testrepo.Repo
	plugins, log, cache, diff string
	env                       map[string]string
}

func newExampleRun(t *testing.T) *exampleRun {
	t.Helper()

	dir, err := filepath.Abs(filepath.Join("..", "..", "example"))
	if err != nil {
		t.Fatal(err)
	}

	r := testrepo.NewFrom(t, dir)
	e := &exampleRun{r: r, plugins: t.TempDir(), log: filepath.Join(t.TempDir(), "driver"), cache: t.TempDir(), diff: bazeldifftest.Tool(t).Path}

	flags, err := json.Marshal(testrepo.Bazel(t, r.Dir).StartupFlags)
	if err != nil {
		t.Fatal(err)
	}

	testrepo.Bazel(t, filepath.Join(e.cache, "tree"))

	data, err := os.ReadFile(filepath.Join(r.Dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	shell := regexp.MustCompile(`(?m)^  - name: shell\n(    .*\n)+`)
	if !shell.Match(data) {
		t.Fatal("the example's config has no shell driver")
	}

	cfg := shell.ReplaceAllString(string(data), "  - {name: stub, kind: driver, repository: example/stub, commit: 0123456789abcdef0123456789abcdef01234567}\n")
	cfg = strings.NewReplacer(
		"  workspace: .\n", "  workspace: .\n  startup_flags: "+string(flags)+"\n",
		"      - run: shell\n        with: {functions: test_}\n", "      - run: stub\n        with: {log: "+e.log+"}\n",
	).Replace(cfg)
	r.Write(config.FileName, cfg)
	r.Commit("the stub driver")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(e.plugins, "stub"), "#!/bin/sh\nFAKE_DRIVER=1 FAKE_MARKS=\"${"+plugin.EnvMarks+":-}\" exec '"+exe+"' \"$@\"\n")

	if err := os.Chmod(filepath.Join(e.plugins, "stub"), 0o700); err != nil { //nolint:gosec // the test's own plugin
		t.Fatal(err)
	}

	e.env = map[string]string{envGitURL: "", envToken: "", envJobURL: "", envRun: "", envConfig: ""}

	return e
}

func (e *exampleRun) run(t *testing.T, base string) (string, string, int) {
	t.Helper()

	return runIn(t, e.r.Dir, e.env, "run", "--base", base, "--suite", "unit", "--root", e.r.Dir,
		"--cache", e.cache, "--bazel-diff", e.diff, "--plugins", e.plugins)
}

func (e *exampleRun) calls(t *testing.T) []string {
	t.Helper()

	if _, err := os.Stat(e.log); err != nil {
		return nil
	}

	return drivenLog(t, e.log)
}

func TestRunRunsWhatTheChangeSelects(t *testing.T) {
	testrepo.Parallel(t)

	e := newExampleRun(t)
	base := e.r.Git("rev-parse", "HEAD")

	data, err := os.ReadFile(filepath.Join(e.r.Dir, "calc/add_test.sh"))
	if err != nil {
		t.Fatal(err)
	}

	e.r.Write("calc/add_test.sh", string(data)+"\ntest_add_zero() {\n\t[ \"$(add 0 0)\" = 0 ] || fail \"add 0 0\"\n}\n")
	one := e.r.Commit("a test added")

	stdout, stderr, code := e.run(t, base)
	if code != 0 || !strings.HasPrefix(stdout, "unit: 1 selected, 3 untouched\n  run calc/add_test.sh\n") ||
		!strings.Contains(stdout, "slice calc/add_test.sh: passed\n") || !strings.Contains(stdout, "  unit: 1 passed, 0 failed, 0 errored\n") {
		t.Fatalf("one slice: %d\n%s\n%s", code, stdout, stderr)
	}

	if got := e.calls(t); len(got) != 1 || !strings.HasSuffix(got[0], " calc/add_test.sh") {
		t.Fatalf("driver calls: %q", got)
	}

	data, err = os.ReadFile(filepath.Join(e.r.Dir, "words/words.go"))
	if err != nil {
		t.Fatal(err)
	}

	e.r.Write("words/words.go", strings.Replace(string(data), "return len(strings.Fields(s))", "n := len(strings.Fields(s))\n\n\treturn n", 1))
	two := e.r.Commit("the server, which no unit test uses")

	stdout, stderr, code = e.run(t, one)
	if code != 0 || stdout != "unit: 0 selected, 4 untouched\nnothing selected: no slice to run\n" {
		t.Fatalf("nothing selected: %d\n%s\n%s", code, stdout, stderr)
	}

	if got := e.calls(t); len(got) != 1 {
		t.Fatalf("the driver ran for a change that selects nothing: %q", got)
	}

	e.r.Write("calc/bad_test.sh", ". ./lib.sh\n\ntest_bad() {\n\tfail bad\n}\n")
	data, err = os.ReadFile(filepath.Join(e.r.Dir, "calc/BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}

	e.r.Write("calc/BUILD.bazel", string(data)+"\nfilegroup(\n    name = \"bad_test\",\n    srcs = [\n        \"bad_test.sh\",\n        \":lib\",\n    ],\n)\n")
	e.r.Commit("a failing test file")

	stdout, stderr, code = e.run(t, two)
	if code != 1 || !strings.Contains(stdout, "slice calc/bad_test.sh: failed\n") ||
		!strings.HasSuffix(stdout, "  unit: 0 passed, 1 failed, 0 errored\n    failed calc/bad_test.sh\n") {
		t.Fatalf("a failing slice: %d\n%s\n%s", code, stdout, stderr)
	}
}
