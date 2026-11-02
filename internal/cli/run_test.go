package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const branchConfig = `
pipeline: shop
suites:
  e2e:
    tests: ["e2e/*.spec.ts"]
    run_slice: ci/e2e.yml
    parallel: 2
    whole_suite: [{name: reconcile, task: ci/reconcile.yml}]
github: {repository: o/r, token: gh}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  main:
    trigger: {branch: main}
    compare: none
    select: all
    suites: {e2e: {}}
`

func TestTreeBatchesAndSliceList(t *testing.T) {
	_, _, store := runEnv(t)

	r := testrepo.New(t)
	r.Write("pipelinegen.yaml", branchConfig)
	commit := r.Commit("config")

	id := report.ID{Run: "main", Change: "main", Commit: commit}
	plan := `{"head": "` + commit + `", "suites": [{"suite": "e2e", "selected": [
		{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts", "sources": ["e2e/a.spec.ts"]},
		{"name": "e2e/b.spec.ts", "label": "@pipelinegen_deps//slices:e2e/b.spec.ts", "sources": ["e2e/b.spec.ts"]},
		{"name": "e2e/c.spec.ts", "label": "@pipelinegen_deps//slices:e2e/c.spec.ts", "sources": ["e2e/c.spec.ts"]}],
		"untouched": [], "whole_suite": ["reconcile"]}]}`

	work := t.TempDir()
	gate, source, batches := filepath.Join(work, "gate"), filepath.Join(work, "source"), filepath.Join(work, "batches")

	if _, stderr, code := run(t, "tree", "-run", "main", "-suite", "e2e", "-trigger", r.Dir, "-out", source, "-gate", gate); code != 0 {
		t.Fatalf("tree without a plan: %d %s", code, stderr)
	}

	if got := readText(t, filepath.Join(gate, "gate.json")); got != "[]\n" {
		t.Fatalf("gate %q", got)
	}

	if err := concourseTakes(filepath.Join(gate, "gate.json")); err != nil {
		t.Fatal(err)
	}

	if err := report.WritePlan(t.Context(), store, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "tree", "-run", "main", "-suite", "e2e", "-trigger", r.Dir, "-out", source, "-gate", gate); code != 0 {
		t.Fatalf("tree: %d %s", code, stderr)
	}

	for file, want := range map[string]string{"gate.json": "[\"1\"]\n", "whole-reconcile.json": "[\"1\"]\n"} {
		if got := readText(t, filepath.Join(gate, file)); got != want {
			t.Fatalf("%s %q", file, got)
		}

		if err := concourseTakes(filepath.Join(gate, file)); err != nil {
			t.Fatal(err)
		}
	}

	head := (&testrepo.Repo{Dir: source}).GitIn(t, "rev-parse", "HEAD")
	if head != commit {
		t.Fatalf("source at %s, want %s", head, commit)
	}

	if _, stderr, code := run(t, "batches", "-run", "main", "-suite", "e2e", "-trigger", r.Dir, "-out", batches, "-root", source); code != 0 {
		t.Fatalf("batches: %d %s", code, stderr)
	}

	var index []string
	if err := json.Unmarshal([]byte(readText(t, filepath.Join(batches, "batches.json"))), &index); err != nil || len(index) != 2 {
		t.Fatalf("batches.json: %v %v", index, err)
	}

	if err := concourseTakes(filepath.Join(batches, "batches.json")); err != nil {
		t.Fatal(err)
	}

	if at := readStarted(batches); time.Since(at) > time.Minute {
		t.Fatalf("the batches' stamp: %s", at)
	}

	for _, b := range index {
		if _, err := os.Stat(filepath.Join(batches, b, "slices.json")); err != nil {
			t.Errorf("batch %s: %v", b, err)
		}
	}

	t.Setenv(envBatch, "0")

	stdout, stderr, code := run(t, "slice", "list", "-batches", batches)
	if code != 0 {
		t.Fatalf("slice list: %d %s", code, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || lines[0] != "e2e/a.spec.ts\t-\t-\te2e/a.spec.ts\t-" {
		t.Fatalf("slice list:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(batches, "run.json")); err != nil {
		t.Fatal("no run file for the batch task")
	}
}

func readText(t *testing.T, p string) string {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestTheBaseIsTheLastPassedElseOneStillGoing(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name     string
		states   map[string]string // by commit, newest first: c1, c2, c3
		base     string
		inFlight bool
	}{
		{"a passed run", map[string]string{"c1": "pending", "c2": "success", "c3": "pending"}, "c2", false},
		{"one still going", map[string]string{"c1": "failure", "c2": "pending", "c3": "pending"}, "c2", true},
		{"a failed run", map[string]string{"c1": "failure", "c2": "error"}, "", false},
		{"no run yet", map[string]string{}, "", false},
	} {
		base, inFlight, err := baseAmong([]string{"c1", "c2", "c3"}, func(commit string) (string, error) { return c.states[commit], nil })
		if err != nil || base != c.base || inFlight != c.inFlight {
			t.Errorf("%s: base %q (going %v), %v; want %q (going %v)", c.name, base, inFlight, err, c.base, c.inFlight)
		}
	}
}

func TestASuiteJobReadsItsCommitsConfigAsHistory(t *testing.T) {
	runEnv(t)

	for _, c := range []struct {
		name, config, gate string
	}{
		{"older keys", strings.Replace(branchConfig, "concourse: {", "concourse: {vars: [gh], from_the_future: 1, ", 1), "[]\n"},
		{"no suites it can say", strings.Replace(branchConfig, `    tests: ["e2e/*.spec.ts"]`+"\n", "", 1), "[]\n"},
	} {
		r := testrepo.New(t)
		r.Write("pipelinegen.yaml", c.config)
		r.Commit("config")

		gate := filepath.Join(t.TempDir(), "gate")

		_, stderr, code := run(t, "tree", "-run", "main", "-suite", "e2e", "-trigger", r.Dir, "-out", filepath.Join(t.TempDir(), "source"), "-gate", gate)
		if code != 0 {
			t.Fatalf("%s: tree: %d %s", c.name, code, stderr)
		}

		if got := readText(t, filepath.Join(gate, "gate.json")); got != c.gate {
			t.Errorf("%s: gate %q", c.name, got)
		}

		if _, stderr, code := run(t, "report", "suite", "-root", r.Dir); code != 0 && !strings.Contains(stderr, "did not pass") {
			t.Errorf("%s: report suite: %d %s", c.name, code, stderr)
		}
	}
}
