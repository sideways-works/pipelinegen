package cli

import (
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func gatedChange(t *testing.T) (string, *gitstore.Store, string, runs.Change, *githubAPI) {
	t.Helper()

	root, s, trigger, ch, api := proposedChange(t)

	writeFile(t, filepath.Join(trigger, "pipelinegen.yaml"), readFile(t, filepath.Join(root, "pipelinegen.yaml")))
	t.Setenv(envTrigger, trigger)
	t.Setenv(envChange, "")
	t.Setenv(envCommit, "")

	return root, s, trigger, ch, api
}

func ownPlanDir(t *testing.T, s *gitstore.Store, trigger string, ch runs.Change) string {
	t.Helper()

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), loadConfig(t, trigger), ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, planOutFile), fmt.Sprintf(`{"base": %q, "head": %q, "all": false, "reason": "", "suites": [{"suite": "e2e", "all": false, "reason": "", "selected": [{"label": "//e2e:a", "sources": []}], "untouched": [], "whole_suite": []}]}`, base, head))

	return dir
}

func secondStep(t *testing.T, trigger, from string) (string, int, string) {
	t.Helper()

	gate := filepath.Join(t.TempDir(), "handoff")

	stdout, stderr, code := run(t, "plan", "-run", "pr", "-trigger", trigger, "-from", from, "-gate", gate, "-cache", t.TempDir())
	if code != 0 {
		stdout += stderr
	}

	return stdout, code, strings.TrimSpace(readFile(t, filepath.Join(gate, "gate.json")))
}

const (
	gateOpen = `["1"]`
	gateShut = `[]`
)

func TestTheSecondStepHandsOnOnlyAPlanItWrote(t *testing.T) { handsOnOnlyAPlanItWrote(t) }

func TestTheSecondStepHandsOnOnlyAPlanItWroteOnABranch(t *testing.T) {
	onBranch(t, handsOnOnlyAPlanItWrote)
}

func handsOnOnlyAPlanItWrote(t *testing.T) {
	t.Helper()

	_, s, trigger, ch, _ := gatedChange(t)
	dir := ownPlanDir(t, s, trigger, ch)

	stdout, code, gate := secondStep(t, trigger, dir)
	if code != 0 || gate != gateOpen || !strings.Contains(stdout, "pr pr-7: e2e 1 of 1") {
		t.Fatalf("the plan written: %d %s %s", code, gate, stdout)
	}

	stdout, code, gate = secondStep(t, trigger, dir)
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: planned, its suites on their way: nothing handed on\n") {
		t.Fatalf("planned a moment ago: %d %s %s", code, gate, stdout)
	}

	id := reportID(ch)
	if _, err := s.Write(t.Context(), "state", gitstore.Update{Ref: id.StateRef("e2e", "a"), Any: true, Files: map[string][]byte{"state.json": []byte(`{"state": "running"}`)}}); err != nil {
		t.Fatal(err)
	}

	stdout, code, gate = secondStep(t, trigger, dir)
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: planned, a suite began: nothing handed on\n") {
		t.Fatalf("a suite began: %d %s %s", code, gate, stdout)
	}
}

func TestTheSecondStepHandsAStalledHeadOnAgainOnce(t *testing.T) { handsAStalledHeadOnAgainOnce(t) }

func TestTheSecondStepHandsAStalledHeadOnAgainOnceOnABranch(t *testing.T) {
	onBranch(t, handsAStalledHeadOnAgainOnce)
}

func handsAStalledHeadOnAgainOnce(t *testing.T) {
	t.Helper()

	root, s, trigger, ch, _ := gatedChange(t)
	dir := ownPlanDir(t, s, trigger, ch)

	if err := report.WritePlan(t.Context(), pastStore(t, s), reportID(ch), []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	stdout, code, gate := secondStep(t, trigger, dir)
	if code != 0 || gate != gateOpen || !strings.Contains(stdout, "pr pr-7: planned, no suite began: hand it off again\n") {
		t.Fatalf("a stalled head: %d %s %s", code, gate, stdout)
	}

	stdout, code, gate = secondStep(t, trigger, dir)
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: taken up 1 times, the last at ") {
		t.Fatalf("handed on a moment ago: %d %s %s", code, gate, stdout)
	}

	api := httptest.NewServer(openPulls{openPull(7, ch.Commit, time.Now().Add(-3*time.Hour), false, "o/r")})
	t.Cleanup(api.Close)
	t.Setenv(envAPIURL, api.URL)

	if stdout, _ := sweepOnce(t, root, t.TempDir()); !strings.Contains(stdout, "#7 "+ch.Commit[:12]+": taken up 1 times") {
		t.Fatalf("the sweep after the hand-off:\n%s", stdout)
	}
}

func TestTheSecondStepHandsOnAHeadTheSweepGaveUp(t *testing.T) {
	_, s, trigger, ch, _ := gatedChange(t)
	id := reportID(ch)

	if err := report.WritePlan(t.Context(), pastStore(t, s), id, []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	mark := fmt.Sprintf(`{"at": %q, "times": %d}`, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339), maxTakes+1)
	if _, err := s.Write(t.Context(), "mark", gitstore.Update{Ref: report.SweptRef(id), Any: true, Files: map[string][]byte{sweptFile: []byte(mark)}}); err != nil {
		t.Fatal(err)
	}

	stdout, code, gate := secondStep(t, trigger, t.TempDir())
	if code != 0 || gate != gateOpen || !strings.Contains(stdout, "pr pr-7: planned, no suite began: hand it off again\n") {
		t.Fatalf("given up by the sweep: %d %s %s", code, gate, stdout)
	}

	stdout, code, gate = secondStep(t, trigger, t.TempDir())
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: taken up 5 times, the last at ") {
		t.Fatalf("handed on a moment ago: %d %s %s", code, gate, stdout)
	}
}

func TestTheSecondStepShutsItsGateWithoutAPlan(t *testing.T) {
	_, s, trigger, ch, api := gatedChange(t)

	stdout, code, gate := secondStep(t, trigger, t.TempDir())
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: no plan to write\n") {
		t.Fatalf("nothing left by the first step: %d %s %s", code, gate, stdout)
	}

	failed := t.TempDir()
	writeFile(t, filepath.Join(failed, planFailedOut), "the change doesn't merge\n")

	stdout, code, gate = secondStep(t, trigger, failed)
	if code == 0 || gate != gateShut || !strings.Contains(stdout, "the change doesn't merge") {
		t.Fatalf("a failed plan: %d %s %s", code, gate, stdout)
	}

	draft := httptest.NewServer(prAndStatuses{pulls: openPulls{openPull(7, ch.Commit, time.Now(), true, "o/r")}, api: api})
	t.Cleanup(draft.Close)
	t.Setenv(envAPIURL, draft.URL)

	stdout, code, gate = secondStep(t, trigger, ownPlanDir(t, s, trigger, ch))
	if code != 0 || gate != gateShut || !strings.Contains(stdout, "pr pr-7: not planned: ") {
		t.Fatalf("a draft: %d %s %s", code, gate, stdout)
	}
}

func TestTheFirstStepHasNoGate(t *testing.T) {
	if _, stderr, code := run(t, "plan", "-out", "a", "-gate", "b"); code != 2 || !strings.Contains(stderr, "hands nothing on") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestTwoSecondStepsAtOnceHandAHeadOnOnce(t *testing.T) {
	_, s, trigger, ch, _ := gatedChange(t)
	dir := ownPlanDir(t, s, trigger, ch)
	cfg := loadConfig(t, trigger)

	var (
		wg     sync.WaitGroup
		handed [2]bool
		errs   [2]error
	)

	for i := range handed {
		store := openStore(cfg)

		t.Cleanup(func() { closeOrFail(t, store) })

		wg.Go(func() {
			handed[i], errs[i] = planFrom(t.Context(), cfg, ch, store, trigger, dir, io.Discard)
		})
	}

	wg.Wait()

	if errs[0] != nil || errs[1] != nil || handed[0] == handed[1] {
		t.Fatalf("handed on %v, errors %v", handed, errs)
	}
}

func TestARerunTakesTheSlicesWithoutAPass(t *testing.T) {
	_, _, store := runEnv(t)

	r := testrepo.New(t)
	r.Write("pipelinegen.yaml", branchConfig)
	commit := r.Commit("config")

	id := report.ID{Run: "main", Change: "main", Commit: commit}
	plan := `{"head": "` + commit + `", "suites": [{"suite": "e2e", "selected": [
		{"name": "e2e/a.spec.ts", "label": "@pipelinegen_deps//slices:e2e/a.spec.ts", "sources": ["e2e/a.spec.ts"]},
		{"name": "e2e/b.spec.ts", "label": "@pipelinegen_deps//slices:e2e/b.spec.ts", "sources": ["e2e/b.spec.ts"]},
		{"name": "e2e/c.spec.ts", "label": "@pipelinegen_deps//slices:e2e/c.spec.ts", "sources": ["e2e/c.spec.ts"]}],
		"untouched": [], "whole_suite": []}]}`

	if err := report.WritePlan(t.Context(), store, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	results := t.TempDir()
	writeFile(t, filepath.Join(results, "a.json"), `{"passed": true}`)
	writeFile(t, filepath.Join(results, "b.json"), `{"passed": false}`)

	rep := &report.Reporter{Config: loadConfig(t, r.Dir), ID: id, Store: store, GitHub: host(loadConfig(t, r.Dir)), Warn: io.Discard}
	if _, err := rep.Slices(t.Context(), []report.SliceEvent{
		{Suite: "e2e", Slice: "e2e/a.spec.ts", Result: filepath.Join(results, "a.json")},
		{Suite: "e2e", Slice: "e2e/b.spec.ts", Result: filepath.Join(results, "b.json")},
		{Suite: "e2e", Slice: "e2e/c.spec.ts", Running: true},
	}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "batches", "-run", "main", "-suite", "e2e", "-trigger", r.Dir, "-out", filepath.Join(t.TempDir(), "batches"), "-root", r.Dir)
	if code != 0 || !strings.Contains(stdout, "main main: 2 slices of e2e in ") {
		t.Fatalf("batches: %d %s %s", code, stdout, stderr)
	}
}
