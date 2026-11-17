package cli

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func missedHead(t *testing.T) (string, *gitstore.Store, report.ID) {
	t.Helper()

	root, _, s := runEnv(t)

	cfg := strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"pull_requests: {base: main, draft: false}", "pull_requests: {base: main, draft: false, missed_after: 1h}", 1)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)

	head := pushHead(t, strings.TrimPrefix(s.Remote, "file://"), 7)

	api := httptest.NewServer(openPulls{openPull(7, head, time.Now().Add(-5*time.Hour), false, "o/r")})
	t.Cleanup(api.Close)
	t.Setenv(envAPIURL, api.URL)

	return root, s, report.ID{Run: "pr", Change: "pr-7", Commit: head, PR: 7}
}

func TestTheSweepLeavesAHeadBeingPlanned(t *testing.T) { sweepLeavesAHeadBeingPlanned(t) }

func TestTheSweepLeavesAHeadBeingPlannedOnABranch(t *testing.T) {
	onBranch(t, sweepLeavesAHeadBeingPlanned)
}

func sweepLeavesAHeadBeingPlanned(t *testing.T) {
	t.Helper()

	root, s, id := missedHead(t)

	if _, err := report.RecordRound(t.Context(), s, id, report.RoundStarted, "", ""); err != nil {
		t.Fatal(err)
	}

	stdout, out := sweepOnce(t, root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+id.Commit[:12]+": its planning began ") || !strings.Contains(stdout, " ago: left to its build\n") {
		t.Fatalf("a head being planned:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "plan", "7")); err == nil {
		t.Fatal("the sweep plans a head whose planning build is on its way")
	}

	ref, _, err := s.First(t.Context(), []string{id.RoundRef(report.RoundStarted)})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := report.RecordRound(t.Context(), pastStore(t, s), id, report.RoundStarted, "", ref.Commit); err != nil {
		t.Fatal(err)
	}

	stdout, out = sweepOnce(t, root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+id.Commit[:12]+": no plan: plan it\n") {
		t.Fatalf("a head whose planning began past the plan's limit:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "plan", "7")); err != nil {
		t.Fatalf("no checkout to plan: %v", err)
	}

	again, ok, err := s.First(t.Context(), []string{id.RoundRef(report.RoundStarted)})
	if err != nil || !ok || time.Since(again.Time) > time.Hour {
		t.Fatalf("the sweep's own planning isn't recorded: %+v %v %v", again, ok, err)
	}
}

func TestTheSweepTakesUpAHeadWhosePlanningEnded(t *testing.T) { sweepTakesUpEndedPlanning(t) }

func TestTheSweepTakesUpAHeadWhosePlanningEndedOnABranch(t *testing.T) {
	onBranch(t, sweepTakesUpEndedPlanning)
}

func sweepTakesUpEndedPlanning(t *testing.T) {
	t.Helper()

	root, s, id := missedHead(t)

	if _, err := report.RecordRound(t.Context(), s, id, report.RoundStarted, "", ""); err != nil {
		t.Fatal(err)
	}

	var said strings.Builder

	endPlanning(t.Context(), runs.Change{Run: id.Run, ID: id.Change, Commit: id.Commit, PR: id.PR}, s, &said)

	if said.String() != "pr pr-7: planning ended without a plan; the sweep may take the head up\n" {
		t.Fatalf("ending: %q", said.String())
	}

	stdout, out := sweepOnce(t, root, t.TempDir())
	if !strings.Contains(stdout, "#7 "+id.Commit[:12]+": no plan: plan it\n") {
		t.Fatalf("a head whose planning ended:\n%s", stdout)
	}

	if _, err := os.Stat(filepath.Join(out, "plan", "7")); err != nil {
		t.Fatalf("no checkout to plan: %v", err)
	}
}

func TestAPlanningBuildThatEndsWithoutAPlanLetsTheHeadGo(t *testing.T) {
	_, s, trigger, ch, _ := gatedChange(t)
	id := reportID(ch)

	begin := func() {
		t.Helper()

		if _, _, code := run(t, "plan", "-run", "pr", "-trigger", trigger, "-begin", "-cache", t.TempDir()); code != 0 {
			t.Fatalf("begin: %d", code)
		}
	}

	recorded := func() bool {
		t.Helper()

		_, ok, err := s.First(t.Context(), []string{id.RoundRef(report.RoundStarted)})
		if err != nil {
			t.Fatal(err)
		}

		return ok
	}

	begin()

	stdout, code, gate := secondStep(t, trigger, t.TempDir())
	if code != 0 || gate == gateOpen || !strings.Contains(stdout, "pr pr-7: planning ended without a plan") || recorded() {
		t.Fatalf("an empty first step: %d %s %q, record kept: %v", code, gate, stdout, recorded())
	}

	begin()

	failed := t.TempDir()
	writeFile(t, filepath.Join(failed, planFailedOut), "the change doesn't merge\n")

	if stdout, code, _ := secondStep(t, trigger, failed); code == 0 || recorded() {
		t.Fatalf("a failed plan: %d %q, record kept: %v", code, stdout, recorded())
	}

	begin()

	if stdout, code, gate := secondStep(t, trigger, ownPlanDir(t, s, trigger, ch)); code != 0 || gate != gateOpen || strings.Contains(stdout, "planning ended") || !recorded() {
		t.Fatalf("a plan written: %d %s %q, record kept: %v", code, gate, stdout, recorded())
	}
}

func TestTheSweepHandsAPlannedHeadOnAgainWhatever(t *testing.T) {
	root, s, head, _ := stalledHead(t)

	if _, err := report.RecordRound(t.Context(), s, report.ID{Run: "pr", Change: "pr-9", Commit: head, PR: 9}, report.RoundStarted, "", ""); err != nil {
		t.Fatal(err)
	}

	stdout, out := sweepOnce(t, root, t.TempDir())
	if _, err := os.Stat(filepath.Join(out, "handoff", "9")); err != nil || !strings.Contains(stdout, "hand it off again") {
		t.Fatalf("a stalled planned head: %v\n%s", err, stdout)
	}
}

func TestAPlanningBuildRecordsItsBeginning(t *testing.T) {
	_, s, trigger, ch, _ := gatedChange(t)
	id := reportID(ch)

	for range 2 {
		stdout, stderr, code := run(t, "plan", "-run", "pr", "-trigger", trigger, "-begin", "-cache", t.TempDir())
		if code != 0 || stdout != "pr pr-7: planning "+ch.Commit[:12]+" begun\n" || stderr != "" {
			t.Fatalf("begin: %d %q %q", code, stdout, stderr)
		}

		if _, ok, err := s.First(t.Context(), []string{id.RoundRef(report.RoundStarted)}); err != nil || !ok {
			t.Fatalf("no record: %v %v", ok, err)
		}
	}

	stdout, code, gate := secondStep(t, trigger, ownPlanDir(t, s, trigger, ch))
	if code != 0 || gate != gateOpen || !strings.Contains(stdout, "pr pr-7: e2e 1 of 1") {
		t.Fatalf("the build's own plan after its record: %d %s %s", code, gate, stdout)
	}

	if stdout, _, code := run(t, "plan", "-run", "pr", "-trigger", trigger, "-begin", "-cache", t.TempDir()); code != 0 || stdout != "pr pr-7: planned already\n" {
		t.Fatalf("begin of a planned head: %d %q", code, stdout)
	}
}

func TestARecordThatCantBeWrittenOnlyWarns(t *testing.T) {
	_, s, trigger, ch, _ := gatedChange(t)

	t.Setenv(envGitURL, "file://"+filepath.Join(t.TempDir(), "nothing"))

	stdout, stderr, code := runIn(t, t.TempDir(), nil, "plan", "-run", "pr", "-trigger", trigger, "-begin", "-cache", t.TempDir())
	if code != 0 || stdout != "" || !strings.Contains(stderr, "the record that its planning began isn't written") {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}

	if _, ok, err := s.First(t.Context(), []string{reportID(ch).RoundRef(report.RoundStarted)}); err != nil || ok {
		t.Fatalf("a record where the step couldn't write: %v %v", ok, err)
	}
}

func TestBeginIsAStepOfItsOwn(t *testing.T) {
	for _, args := range [][]string{{"-begin", "-out", "a"}, {"-begin", "-from", "a"}, {"-begin", "-gate", "a"}} {
		if _, stderr, code := run(t, append([]string{"plan"}, args...)...); code != 2 || !strings.Contains(stderr, "a step of its own") {
			t.Errorf("%v: %d %s", args, code, stderr)
		}
	}
}
