package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
)

// endedShort plays a suite build that began and ended three hours ago, each slice of it in state.
func endedShort(t *testing.T, h handed, state map[string]report.State) {
	t.Helper()

	past, id := pastStore(t, h.store), reportID(h.ch)
	cfg := loadConfig(t, h.root)

	if err := report.RecordBegan(t.Context(), past, id, "e2e", ""); err != nil {
		t.Fatal(err)
	}

	rep := &report.Reporter{Config: cfg, ID: id, Store: past, GitHub: host(cfg), Warn: io.Discard}

	var evs []report.SliceEvent

	for name, st := range state {
		ev := report.SliceEvent{Suite: "e2e", Slice: name, Label: "@pipelinegen_deps//slices:" + name, Result: filepath.Join(t.TempDir(), "none.json")}

		switch st {
		case report.NotRun:
			ev.NotRun, ev.Short = true, "not run: the worker had 3120 MB free of 6600 MB needed"
		case report.Passed:
			ev.Result = filepath.Join(t.TempDir(), "passed.json")
			writeFile(t, ev.Result, `{"passed": true, "tests": 1}`)
		}

		evs = append(evs, ev)
	}

	if _, err := rep.Slices(t.Context(), evs); err != nil {
		t.Fatal(err)
	}

	if _, err := rep.Suite(t.Context(), "e2e"); err != nil {
		t.Fatal(err)
	}
}

func TestTheSweepHandsOnARunLeftShortOfMemory(t *testing.T) { handsOnARunLeftShort(t) }

func TestTheSweepHandsOnARunLeftShortOfMemoryOnABranch(t *testing.T) {
	onBranch(t, handsOnARunLeftShort)
}

func handsOnARunLeftShort(t *testing.T) {
	t.Helper()

	h := handedHead(t)
	head := h.ch.Commit[:12]

	endedShort(t, h, map[string]report.State{"e2e/a.spec.ts": report.Passed, "e2e/b.spec.ts": report.NotRun, "e2e/c.spec.ts": report.NotRun})

	stdout, out := sweepOnce(t, h.root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+head+": planned, its run ended with slices not run for want of memory: hand it off again\n") {
		t.Fatalf("the sweep:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "handoff", "7")); err != nil {
		t.Fatalf("not handed on: %v", err)
	}

	// the suite build it starts deals only what didn't run, and from its start the run is its again
	if out := suiteStart(t, h.trigger); !strings.Contains(out, "e2e began\n") || !strings.Contains(out, "2 slices of e2e in ") {
		t.Fatalf("the build it started:\n%s", out)
	}

	if stdout, _ := sweepOnce(t, h.root, t.TempDir()); !strings.Contains(stdout, "#7 "+head+": ") || strings.Contains(stdout, "hand it off again") {
		t.Fatalf("the sweep after the build began:\n%s", stdout)
	}
}

func TestAPlanningBuildHandsOnARunLeftShortOfMemory(t *testing.T) {
	h := handedHead(t)

	endedShort(t, h, map[string]report.State{"e2e/a.spec.ts": report.NotRun, "e2e/b.spec.ts": report.NotRun, "e2e/c.spec.ts": report.NotRun})

	stdout, code, gate := secondStep(t, h.trigger, t.TempDir())
	if code != 0 || gate != gateOpen || !strings.Contains(stdout, "pr pr-7: planned, its run ended with slices not run for want of memory: hand it off again\n") {
		t.Fatalf("the planning build: %d %s %s", code, gate, stdout)
	}
}

func TestARunWithAFailureIsNotHandedOnForWantOfMemory(t *testing.T) {
	h := handedHead(t)

	endedShort(t, h, map[string]report.State{"e2e/a.spec.ts": report.Failed, "e2e/b.spec.ts": report.NotRun, "e2e/c.spec.ts": report.NotRun})

	if _, ok, err := h.store.First(t.Context(), []string{reportID(h.ch).ShortRef()}); err != nil || ok {
		t.Fatalf("marked short with a failure: %v %v", ok, err)
	}

	stdout, out := sweepOnce(t, h.root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+h.ch.Commit[:12]+": planned, a suite began\n") {
		t.Fatalf("the sweep:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "handoff", "7")); err == nil {
		t.Fatal("handed on a run whose failing test would run again unasked")
	}
}
