package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func handedHead(t *testing.T) handed {
	t.Helper()

	root, s, trigger, ch, _ := gatedChange(t)

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), loadConfig(t, trigger), ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	plan := fmt.Sprintf(`{"base": %q, "head": %q, "suites": [{"suite": "e2e", "selected": [
		{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts", "sources": ["e2e/a.spec.ts"]},
		{"name": "e2e/b.spec.ts", "label": "@pipelinegen_deps//slices:e2e/b.spec.ts", "sources": ["e2e/b.spec.ts"]},
		{"name": "e2e/c.spec.ts", "label": "@pipelinegen_deps//slices:e2e/c.spec.ts", "sources": ["e2e/c.spec.ts"]}],
		"untouched": [], "whole_suite": []}]}`, base, head)

	if err := report.WritePlan(t.Context(), pastStore(t, s), reportID(ch), []byte(plan)); err != nil {
		t.Fatal(err)
	}

	return handed{root: root, store: s, trigger: trigger, ch: ch}
}

type handed struct {
	root    string
	store   *gitstore.Store
	trigger string
	ch      runs.Change
}

func treeStep(t *testing.T, trigger string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")

	stdout, stderr, code := run(t, "tree", "-run", "pr", "-suite", "e2e", "-trigger", trigger, "-out", filepath.Join(dir, "source"), "-gate", gate)
	if code != 0 {
		t.Fatalf("tree: %d %s %s", code, stdout, stderr)
	}

	return stdout, gate
}

func suiteStart(t *testing.T, trigger string) string {
	t.Helper()

	stdout, gate := treeStep(t, trigger)
	if !strings.Contains(readFile(t, filepath.Join(gate, "gate.json")), `"1"`) {
		return stdout
	}

	more, stderr, code := run(t, "batches", "-run", "pr", "-suite", "e2e", "-trigger", trigger, "-out", filepath.Join(t.TempDir(), "batches"), "-root", trigger)
	if code != 0 {
		t.Fatalf("batches: %d %s %s", code, more, stderr)
	}

	return stdout + more
}

func TestABuildInItsFirstBatchIsNotHandedOnAgain(t *testing.T) {
	h := handedHead(t)
	root, trigger, ch := h.root, h.trigger, h.ch

	if out := suiteStart(t, trigger); !strings.Contains(out, "pr pr-7: e2e began\n") || !strings.Contains(out, "3 slices of e2e in ") {
		t.Fatalf("the build's start:\n%s", out)
	}

	stdout, out := sweepOnce(t, root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+ch.Commit[:12]+": planned, a suite began\n") {
		t.Fatalf("the sweep:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "handoff", "7")); err == nil {
		t.Fatal("the sweep handed on a head whose suite build is running")
	}

	stdout, code, gate := secondStep(t, trigger, t.TempDir())
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: planned, a suite began: nothing handed on\n") {
		t.Fatalf("the planning build: %d %s %s", code, gate, stdout)
	}
}

func TestABuildStartedASecondAgoIsBegunForTheSweep(t *testing.T) { begunForTheSweep(t) }

func TestABuildStartedASecondAgoIsBegunForTheSweepOnABranch(t *testing.T) {
	onBranch(t, begunForTheSweep)
}

func begunForTheSweep(t *testing.T) {
	t.Helper()

	h := handedHead(t)
	root, trigger, ch := h.root, h.trigger, h.ch

	if out, _ := treeStep(t, trigger); !strings.Contains(out, "pr pr-7: e2e began\n") {
		t.Fatalf("the start step:\n%s", out)
	}

	stdout, out := sweepOnce(t, root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+ch.Commit[:12]+": planned, a suite began\n") {
		t.Fatalf("a second after the build's start:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "handoff", "7")); err == nil {
		t.Fatal("handed on again")
	}
}

func TestABuildStartedASecondAgoIsBegunForThePlanningGate(t *testing.T) {
	trigger := handedHead(t).trigger

	if out, _ := treeStep(t, trigger); !strings.Contains(out, "pr pr-7: e2e began\n") {
		t.Fatalf("the start step:\n%s", out)
	}

	stdout, code, gate := secondStep(t, trigger, t.TempDir())
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: planned, a suite began: nothing handed on\n") {
		t.Fatalf("a second after the build's start: %d %s %s", code, gate, stdout)
	}
}

func TestAStartStepWithNothingToRunRecordsNoStart(t *testing.T) {
	h := handedHead(t)
	s, trigger, ch := h.store, h.trigger, h.ch
	id := reportID(ch)

	noStart := func(what string) {
		t.Helper()

		if out, gate := treeStep(t, trigger); strings.Contains(out, "began") || strings.TrimSpace(readFile(t, filepath.Join(gate, "gate.json"))) != gateShut {
			t.Fatalf("%s:\n%s", what, out)
		}

		if _, ok, err := s.First(t.Context(), []string{id.BeganRef("e2e")}); err != nil || ok {
			t.Fatalf("%s: a start record: %v %v", what, ok, err)
		}
	}

	if _, err := s.Write(t.Context(), "end", gitstore.Update{Ref: endedRef(id), Any: true, Files: map[string][]byte{endedFile: []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}

	noStart("ended by hand")

	nothing := `{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`
	if _, err := s.Write(t.Context(), "plan",
		gitstore.Update{Ref: endedRef(id), Any: true, Delete: true},
		gitstore.Update{Ref: id.PlanRef(), Any: true, Files: map[string][]byte{"plan.json": []byte(nothing)}}); err != nil {
		t.Fatal(err)
	}

	noStart("nothing selected")
}

func TestTheStartRecordIsNoSlice(t *testing.T) {
	h := handedHead(t)
	root, s, trigger, ch := h.root, h.store, h.trigger, h.ch
	id := reportID(ch)

	treeStep(t, trigger)

	states, err := report.SliceStates(t.Context(), s, id)
	if err != nil || len(states) != 0 {
		t.Fatalf("states: %v %v", states, err)
	}

	recs, err := report.Recordings(t.Context(), s, id)
	if err != nil || len(recs) != 0 {
		t.Fatalf("recordings: %v %v", recs, err)
	}

	cfg := loadConfig(t, root)
	rep := &report.Reporter{Config: cfg, ID: id, Store: s, GitHub: host(cfg), Warn: io.Discard}

	if _, err := rep.Suite(t.Context(), "e2e"); err != nil {
		t.Fatal(err)
	}

	ends, err := report.Ends(t.Context(), s, cfg, id)
	if _, ok := ends["e2e"]; err != nil || !ok || len(ends) != 1 {
		t.Fatalf("ends: %v %v", ends, err)
	}
}

func TestTwoBuildsOfAHeadStartedTogetherBothRunEverySlice(t *testing.T) {
	trigger := handedHead(t).trigger

	stdout, code, gate := secondStep(t, trigger, t.TempDir())
	if code != 0 || gate != gateOpen || !strings.Contains(stdout, "planned, no suite began: hand it off again") {
		t.Fatalf("the re-hand: %d %s %s", code, gate, stdout)
	}

	first := suiteStart(t, trigger)
	second := suiteStart(t, trigger)

	for _, out := range []string{first, second} {
		if !strings.Contains(out, "pr pr-7: 3 slices of e2e in ") {
			t.Fatalf("a build's start:\n%s", out)
		}
	}
}
