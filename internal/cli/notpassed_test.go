package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestABatchsEndSaysWhyItsSlicesErrored(t *testing.T) {
	f := drivenRepo(t, "[{run: stub, batch: true, with: {log: LOG}}]", false)

	p := filepath.Join(f.root, config.FileName)
	writeFile(t, p, strings.Replace(readFile(t, p), "config: {log: ", "config: {fail: observe, log: ", 1))
	gitT(t, f.root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "-am", "observe fails")

	stdout, stderr, code := runIn(t, f.root, f.env, "record", "--suite", "e2e", "--root", f.root)
	if code == 0 {
		t.Fatalf("record passed: %s\n%s", stdout, stderr)
	}

	for _, want := range []string{
		"  errored, 2 slices: e2e/cart.spec.ts, e2e/pay.spec.ts\n",
		"    (recording)\n",
		"the fake was told to fail",
		"  the full text of each: its state.json in the store, refs/pipelinegen/runs/",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in:\n%s", want, stderr)
		}
	}

	if n := strings.Count(stderr, "    (recording)\n"); n != 1 {
		t.Errorf("the same failure said %d times:\n%s", n, stderr)
	}
}

func TestASuitesEndSaysWhyItDidNotPass(t *testing.T) {
	root, _, store := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), branchConfig)
	c := checkout(t, root, 1)

	t.Setenv(envRun, "main")
	t.Setenv(envChange, "main")
	t.Setenv(envPR, "0")
	t.Setenv(envCommit, c[1])

	id := report.ID{Run: "main", Change: "main", Commit: c[1]}
	plan := `{"suites": [{"suite": "e2e", "selected": [
		{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts"},
		{"name": "e2e/b.spec.ts", "label": "@pipelinegen_deps//slices:e2e/b.spec.ts"},
		{"name": "e2e/c.spec.ts", "label": "@pipelinegen_deps//slices:e2e/c.spec.ts"},
		{"name": "e2e/d.spec.ts", "label": "@pipelinegen_deps//slices:e2e/d.spec.ts"}]}]}`

	if err := report.WritePlan(t.Context(), store, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	state := func(slice, body string) {
		t.Helper()

		if _, err := store.Write(t.Context(), "slice", gitstore.Update{Ref: id.StateRef("e2e", slice), Any: true, Files: map[string][]byte{"state.json": []byte(body)}}); err != nil {
			t.Fatal(err)
		}
	}

	same := `{"state": "failed", "failures": [{"test": "checkout pays", "tail": "expected 200\ngot 500"}]}`
	state("e2e/a.spec.ts", same)
	state("e2e/b.spec.ts", same)
	state("e2e/c.spec.ts", `{"state": "errored", "failures": [{"test": "(recording)", "tail": "observe: plugin failed"}]}`)
	state("e2e/d.spec.ts", `{"state": "passed"}`)

	_, stderr, code := run(t, "report", "suite", "--root", root)
	if code == 0 {
		t.Fatalf("the suite passed: %s", stderr)
	}

	for _, want := range []string{
		"suite e2e: did not pass\n",
		"  failed, 2 slices: e2e/a.spec.ts, e2e/b.spec.ts\n    checkout pays\n      expected 200\n      got 500\n",
		"  errored, 1 slice: e2e/c.spec.ts\n    (recording)\n      observe: plugin failed\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("no %q in:\n%s", want, stderr)
		}
	}

	if strings.Contains(stderr, "e2e/d.spec.ts") {
		t.Errorf("a passed slice is named:\n%s", stderr)
	}
}

// In full, every group is said: the store the rest would be read from is gone.
func TestWhyInFullSaysEveryGroup(t *testing.T) {
	var groups []*whyGroup
	for i := range 10 {
		groups = append(groups, &whyGroup{State: report.Failed, Failures: []report.Failure{{Test: fmt.Sprintf("t%d", i), Tail: strings.Repeat("x", 1000)}}, slices: []string{fmt.Sprintf("s%d", i)}})
	}

	if text, cut := renderWhy(groups, false); !cut || strings.Contains(text, "t9\n") {
		t.Fatalf("capped: cut %t\n%s", cut, text)
	}

	if text, cut := renderWhy(groups, true); cut || !strings.Contains(text, "t9\n") {
		t.Fatalf("in full: cut %t\n%s", cut, text)
	}
}

func TestWhyIsCapped(t *testing.T) {
	var long []string
	for i := range 40 {
		long = append(long, fmt.Sprintf("line %d", i))
	}

	tail, cut := capTail(strings.Join(long, "\n"))
	if !cut || !strings.HasPrefix(tail, "line 25\n") || !strings.HasSuffix(tail, "line 39") {
		t.Fatalf("tail %q, cut %t", tail, cut)
	}

	if tail, cut := capTail(strings.Repeat("x", 5000)); !cut || len(tail) != whyTailBytes+3 {
		t.Fatalf("a long line: %d bytes, cut %t", len(tail), cut)
	}

	names := make([]string, 12)
	for i := range names {
		names[i] = fmt.Sprintf("s%d", i)
	}

	if got := sliceNames(names); got != "s0, s1, s2, s3, s4, s5, s6, s7 and 4 more" {
		t.Fatalf("names: %q", got)
	}
}
