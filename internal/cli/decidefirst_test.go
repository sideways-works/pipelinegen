package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func (w *continuousWorld) decideFirst(t *testing.T, commit, gate string) <-chan decided {
	t.Helper()

	return w.decideFirstTo(t, commit, gate, &saying{})
}

func (w *continuousWorld) decideFirstTo(t *testing.T, commit, gate string, out *saying) <-chan decided {
	t.Helper()

	done := make(chan decided, 1)

	go func() {
		err := decideBeforePrepare(t.Context(), w.cfg, w.change(commit), w.store, w.trigger, gate, out)

		_, statErr := os.Stat(filepath.Join(gate, decidedFile))
		done <- decided{run: statErr == nil, err: err, out: out.String()}
	}()

	return done
}

func (w *continuousWorld) gateShut(t *testing.T, gate, commit string) {
	t.Helper()

	if got := readFile(t, filepath.Join(gate, "gate.json")); got != "[]\n" {
		t.Fatalf("the gate of a build that won't plan: %q", got)
	}

	if got := readFile(t, filepath.Join(gate, "version")); got != "full "+commit+"\n" {
		t.Fatalf("the hand-off's version: %q", got)
	}

	if _, err := os.Stat(filepath.Join(gate, decidedFile)); err == nil {
		t.Fatal("a build that won't plan left a decision for its plan step")
	}
}

func TestASupersededBuildDecidesBeforeItsPrepare(t *testing.T) {
	w := newContinuousWorld(t)

	if out, err := exec.CommandContext(t.Context(), "git", "-C", w.trigger, "checkout", "-q", "--detach", w.first).CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}

	newer := w.push()
	gate := filepath.Join(t.TempDir(), "handoff")

	stdout, stderr, code := runIn(t, t.TempDir(), nil, "plan", "--decide", "-run", "full", "-config", filepath.Join(w.root, "pipelinegen.yaml"), "-trigger", w.trigger, "-gate", gate)
	if code != 0 || stdout != "full "+w.first[:12]+": superseded by "+newer[:12]+": not run, the newer commit's run covers it\n" {
		t.Fatalf("the decision of an older commit: %d %q %s", code, stdout, stderr)
	}

	w.gateShut(t, gate, w.first)
	noLock(t, stdout, stderr)

	if _, ok, err := readPlan(t.Context(), w.store, reportID(w.change(w.first))); err != nil || ok {
		t.Fatalf("a superseded build planned: %v %v", ok, err)
	}

	w.startRound(t, newer)
	w.endRound(t, newer)

	again := filepath.Join(t.TempDir(), "handoff")

	got := <-w.decideFirst(t, newer, again)
	if got.err != nil || got.run || got.out != "full "+newer[:12]+": nothing changed since the full of "+newer[:12]+": not run\n" {
		t.Fatalf("the decision of a commit that had its round: %+v", got)
	}

	w.gateShut(t, again, newer)

	if _, _, code := runIn(t, t.TempDir(), nil, "plan", "--decide", "-run", "full", "-config", filepath.Join(w.root, "pipelinegen.yaml"), "-trigger", w.trigger); code != 2 {
		t.Fatalf("--decide without --gate: %d", code)
	}
}

func TestAWaitBeforeThePrepareGoesOnToPlan(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	w.startRound(t, first)

	second := w.push()
	gate := filepath.Join(t.TempDir(), "handoff")
	out := &saying{want: "waiting for the round of ", said: make(chan struct{})}
	done := w.decideFirstTo(t, second, gate, out)

	within(t, out.said, "the decision's wait for the round in progress")

	w.endRound(t, first)

	got := <-done
	if got.err != nil || !got.run || !strings.Contains(got.out, "full "+second[:12]+": waiting for the round of "+first[:12]+" to end") ||
		!strings.HasSuffix(got.out, "full "+second[:12]+": the newest of main, and no round in progress: planning\n") {
		t.Fatalf("the decision: %+v", got)
	}

	if _, err := os.Stat(filepath.Join(gate, "gate.json")); err == nil {
		t.Fatal("a build that goes on to plan wrote its gate before its plan")
	}

	prior, err := readDecided(gate)
	if err != nil || prior == nil || prior.Commit != second {
		t.Fatalf("the decision left for the plan step: %+v %v", prior, err)
	}

	var again bytes.Buffer

	run, _, err := decideRound(t.Context(), w.cfg, w.change(second), w.store, w.trigger, &again, prior)
	if err != nil || !run || again.String() != "full "+second[:12]+": the newest of main, and no round in progress: planning\n" {
		t.Fatalf("the plan step's decision: %v %v %q", run, err, again.String())
	}

	noLock(t, got.out, again.String())
}

func TestTheBranchMovingWhileTheBuildPrepares(t *testing.T) {
	w := newContinuousWorld(t)
	gate := filepath.Join(t.TempDir(), "handoff")

	if got := <-w.decideFirst(t, w.first, gate); got.err != nil || !got.run {
		t.Fatalf("the decision: %+v", got)
	}

	prior, err := readDecided(gate)
	if err != nil || prior == nil {
		t.Fatalf("the decision left: %+v %v", prior, err)
	}

	newer := w.push()

	var out bytes.Buffer

	run, _, err := decideRound(t.Context(), w.cfg, w.change(w.first), w.store, w.trigger, &out, prior)
	if err != nil || run || out.String() != "full "+w.first[:12]+": superseded by "+newer[:12]+": not run, the newer commit's run covers it\n" {
		t.Fatalf("the plan step's decision after the branch moved: %v %v %q", run, err, out.String())
	}

	var next bytes.Buffer

	if run, err := awaitRound(t.Context(), w.cfg, w.change(newer), w.store, w.trigger, &next); err != nil || !run || strings.Contains(next.String(), "waiting") {
		t.Fatalf("the newer commit's build: %v %v %q", run, err, next.String())
	}
}

func TestARoundsRecordsAreWrittenOnceAcrossTwoDecisions(t *testing.T) {
	w := newContinuousWorld(t)
	gate := filepath.Join(t.TempDir(), "handoff")
	tip := w.first

	if got := <-w.decideFirst(t, tip, gate); got.err != nil || !got.run {
		t.Fatalf("the decision: %+v", got)
	}

	records := func() (started, planning gitstore.Ref) {
		t.Helper()

		rounds, err := report.Rounds(t.Context(), w.store, "full", "main")
		if err != nil || len(rounds) != 1 {
			t.Fatalf("the rounds recorded: %+v %v", rounds, err)
		}

		r := rounds[tip[:12]]

		return r.Records[report.RoundStarted], r.Records[report.RoundPlanning]
	}

	started, planning := records()
	if started.Commit == "" || planning.Commit == "" {
		t.Fatalf("the decision recorded start %q and planning %q", started.Commit, planning.Commit)
	}

	prior, err := readDecided(gate)
	if err != nil || prior == nil || prior.Planning != planning.Commit {
		t.Fatalf("the decision left %+v, its planning record is %s: %v", prior, planning.Commit, err)
	}

	var out bytes.Buffer

	run, after, err := decideRound(t.Context(), w.cfg, w.change(tip), w.store, w.trigger, &out, prior)
	if err != nil || !run || strings.Contains(out.String(), "nothing changed") {
		t.Fatalf("the plan step's decision: %v %v %q", run, err, out.String())
	}

	startedNow, planningNow := records()
	if startedNow.Commit != started.Commit {
		t.Fatalf("the plan step recorded a second start: %s, then %s", started.Commit, startedNow.Commit)
	}

	if planningNow.Commit == planning.Commit || after.Planning != planningNow.Commit {
		t.Fatalf("the plan step's planning record: %s, then %s (it says %s)", planning.Commit, planningNow.Commit, after.Planning)
	}

	queued := newContinuousWorld(t)
	t.Setenv(envSerialPlan, "1")

	qgate := filepath.Join(t.TempDir(), "handoff")
	if got := <-queued.decideFirst(t, queued.first, qgate); got.err != nil || !got.run {
		t.Fatalf("the queued build's decision: %+v", got)
	}

	qprior, err := readDecided(qgate)
	if err != nil || qprior == nil {
		t.Fatalf("the queued build's decision left: %+v %v", qprior, err)
	}

	if run, _, err := decideRound(t.Context(), queued.cfg, queued.change(queued.first), queued.store, queued.trigger, &bytes.Buffer{}, qprior); err != nil || !run {
		t.Fatalf("the queued build's plan step: %v %v", run, err)
	}

	if rounds, err := report.Rounds(t.Context(), queued.store, "full", "main"); err != nil || len(rounds) != 0 {
		t.Fatalf("a queued build recorded its round: %+v %v", rounds, err)
	}
}
