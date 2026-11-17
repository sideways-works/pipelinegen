package cli

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

const roundPlan = `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}], "untouched": [], "whole_suite": []}]}`

func (w *continuousWorld) startRound(t *testing.T, commit string) {
	t.Helper()

	if err := report.WritePlan(t.Context(), w.store, reportID(w.change(commit)), []byte(roundPlan)); err != nil {
		t.Fatal(err)
	}
}

func (w *continuousWorld) endRound(t *testing.T, commit string) {
	t.Helper()

	id := reportID(w.change(commit))
	if _, err := w.store.Write(t.Context(), "end", gitstore.Update{Ref: id.EndRef("e2e"), Any: true, Files: map[string][]byte{"end.json": []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}
}

type decided struct {
	run bool
	err error
	out string
}

func (w *continuousWorld) decide(t *testing.T, commit string) <-chan decided {
	t.Helper()

	return w.decideIn(t, commit, w.trigger, w.store)
}

func (w *continuousWorld) decideIn(t *testing.T, commit, trigger string, store *gitstore.Store) <-chan decided {
	t.Helper()

	return w.decideTo(t, commit, trigger, store, &saying{})
}

func (w *continuousWorld) decideTo(t *testing.T, commit, trigger string, store *gitstore.Store, out *saying) <-chan decided {
	t.Helper()

	done := make(chan decided, 1)

	// a decision that never comes fails its test in minutes, not at the
	// package's time limit.
	ctx, cancel := context.WithTimeout(t.Context(), decisionWithin)

	go func() {
		defer cancel()

		run, err := awaitRound(ctx, w.cfg, w.change(commit), store, trigger, out)
		done <- decided{run, err, out.String()}
	}()

	return done
}

const decisionWithin = 5 * time.Minute

// said lets a test order what happens after the build found a round in progress, never by a sleep.
func (w *continuousWorld) decideSaying(t *testing.T, commit string) (done <-chan decided, said <-chan struct{}) {
	t.Helper()

	out := &saying{want: "waiting for the round of ", said: make(chan struct{})}

	return w.decideTo(t, commit, w.trigger, w.store, out), out.said
}

type saying struct {
	mu   sync.Mutex
	b    bytes.Buffer
	want string
	said chan struct{}
	once sync.Once
}

func (s *saying) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// a bytes.Buffer's Write never fails.
	s.b.Write(p)

	if s.said != nil && strings.Contains(s.b.String(), s.want) {
		s.once.Do(func() { close(s.said) })
	}

	return len(p), nil
}

func (s *saying) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}

func pauseAt(t *testing.T, commit, step string, n int) (reached <-chan struct{}, release func()) {
	t.Helper()

	var (
		mu    sync.Mutex
		seen  int
		there = make(chan struct{})
		goOn  = make(chan struct{})
		once  sync.Once
	)

	old := roundStep
	roundStep = func(c, s string) {
		if c != commit || s != step {
			return
		}

		mu.Lock()
		seen++
		pause := seen == n
		mu.Unlock()

		if pause {
			close(there)
			<-goOn
		}
	}

	release = func() { once.Do(func() { close(goOn) }) }

	t.Cleanup(func() {
		release()

		roundStep = old
	})

	return there, release
}

// A build that looked twice after something happened has decided on a reading taken after it.
func countLooks(t *testing.T, commit string) (after func(n int) <-chan struct{}) {
	t.Helper()

	type wait struct {
		at int
		c  chan struct{}
	}

	var (
		mu    sync.Mutex
		looks int
		waits []wait
	)

	old := roundStep
	roundStep = func(c, s string) {
		old(c, s)

		if c != commit || s != "looked" {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		looks++

		kept := waits[:0]

		for _, w := range waits {
			if w.at <= looks {
				close(w.c)
			} else {
				kept = append(kept, w)
			}
		}

		waits = kept
	}

	t.Cleanup(func() { roundStep = old })

	return func(n int) <-chan struct{} {
		mu.Lock()
		defer mu.Unlock()

		c := make(chan struct{})
		waits = append(waits, wait{at: looks + n, c: c})

		return c
	}
}

func within(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-c:
	case <-time.After(time.Minute):
		t.Fatalf("%s didn't happen", what)
	}
}

func noLock(t *testing.T, outs ...string) {
	t.Helper()

	for _, out := range outs {
		if w := heldWords.FindString(out); w != "" {
			t.Errorf("a continuous run's step says %q: %q", w, out)
		}
	}
}

func TestAContinuousBuildOfAnOlderCommitDoesNotRun(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	newer := w.push()

	got := <-w.decide(t, first)
	if got.err != nil || got.run || got.out != "full "+first[:12]+": superseded by "+newer[:12]+": not run, the newer commit's run covers it\n" {
		t.Fatalf("the older commit's build: %+v", got)
	}

	if _, ok, err := supersededOf(t.Context(), w.store, "full", first); err != nil || !ok {
		t.Fatalf("no superseded record: %v %v", ok, err)
	}

	noLock(t, got.out)
}

func TestAContinuousBuildWaitsForTheRoundInProgress(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	w.startRound(t, first)

	second := w.push()
	done, said := w.decideSaying(t, second)

	within(t, said, "the build's wait for the round in progress")

	w.endRound(t, first)

	got := <-done
	if got.err != nil || !got.run || !strings.Contains(got.out, "full "+second[:12]+": waiting for the round of "+first[:12]+" to end (its last step was ") ||
		!strings.Contains(got.out, "; it counts as dead after ") ||
		!strings.HasSuffix(got.out, "full "+second[:12]+": the newest of main, and no round in progress: planning\n") {
		t.Fatalf("the waiting build: %+v", got)
	}

	noLock(t, got.out)
}

func TestAContinuousBuildThatWaitedForANewerCommitDoesNotRun(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	w.startRound(t, first)

	// its second look: the first comes before it records its round's start.
	second := w.push()
	reached, release := pauseAt(t, second, "looked", 2)
	done, said := w.decideSaying(t, second)

	within(t, reached, "the build's first look")

	third := w.push()

	release()
	within(t, said, "the build's wait for the round in progress")

	w.endRound(t, first)

	got := <-done
	if got.err != nil || got.run || !strings.Contains(got.out, "superseded by "+third[:12]+": not run") {
		t.Fatalf("the build that waited: %+v", got)
	}

	noLock(t, got.out)
}

func TestAWaitingBuildExitsWhileTheRoundGoesOn(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	w.startRound(t, first)

	second := w.push()
	done, said := w.decideSaying(t, second)

	within(t, said, "the build's wait for the round in progress")

	third := w.push()

	select {
	case got := <-done:
		if got.err != nil || got.run || !strings.Contains(got.out, "full "+second[:12]+": waiting for the round of "+first[:12]+" to end") ||
			!strings.HasSuffix(got.out, "full "+second[:12]+": superseded by "+third[:12]+": not run, the newer commit's run covers it\n") {
			t.Fatalf("the waiting build: %+v", got)
		}

		noLock(t, got.out)
	case <-time.After(30 * time.Second):
		t.Fatal("the waiting build didn't exit while the round it waited for went on")
	}

	if r, ok, err := newestRound(t.Context(), w.store, w.cfg, w.change(second)); err != nil || !ok || r.ended || r.commit != first[:12] {
		t.Fatalf("the round of %s: %+v %v %v", first[:12], r, ok, err)
	}
}

func TestADeadRoundDoesNotHoldTheNextBuild(t *testing.T) {
	w := newContinuousWorld(t)

	old := roundUnit
	roundUnit = time.Second

	t.Cleanup(func() { roundUnit = old })

	text := strings.Replace(readFile(t, w.root+"/pipelinegen.yaml"), "    record: true\n", "    record: true\n    idle_minutes: 2\n", 1)
	writeFile(t, w.root+"/pipelinegen.yaml", text)
	w.cfg = loadConfig(t, w.root)

	first := w.first
	w.startRound(t, first)

	second := w.push()

	got := <-w.decide(t, second)
	if got.err != nil || !got.run || !strings.Contains(got.out, "the round of "+first[:12]+" has recorded nothing for ") || !strings.Contains(got.out, "it counts as dead") {
		t.Fatalf("the build after a dead round: %+v", got)
	}

	if said := regexp.MustCompile(`recorded nothing for (\S+), more than 2s`).FindStringSubmatch(got.out); len(said) != 2 {
		t.Fatalf("the dead round's line: %q", got.out)
	} else if d, err := time.ParseDuration(said[1]); err != nil || d <= 2*time.Second {
		t.Fatalf("a silence of %s said to be more than 2s: %v", said[1], err)
	}

	noLock(t, got.out)
}

func TestTwoBuildsOfOneCommitDecidingAtOnce(t *testing.T) {
	w := newContinuousWorld(t)
	tip := w.first
	checkouts, stores := w.builds(t, 2)

	a, b := w.decideIn(t, tip, checkouts[0], stores[0]), w.decideIn(t, tip, checkouts[1], stores[1])
	got := []decided{<-a, <-b}

	runs := 0

	for _, g := range got {
		noLock(t, g.out)

		switch {
		case g.err != nil:
			t.Fatalf("a build of the tip: %+v", g)
		case g.run:
			runs++
		case g.out != "full "+tip[:12]+": nothing changed since the full of "+tip[:12]+": not run\n":
			t.Fatalf("the build that didn't run said: %q", g.out)
		}
	}

	if runs != 1 {
		t.Fatalf("%d builds of one commit run: %+v", runs, got)
	}

	var wg sync.WaitGroup

	errs := make([]error, 2)

	// each build writes through a store of its own, as each process has.
	for i := range errs {
		wg.Go(func() {
			errs[i] = report.WritePlan(t.Context(), stores[i], reportID(w.change(tip)), []byte(roundPlan))
		})
	}

	wg.Wait()

	if (errs[0] == nil) == (errs[1] == nil) || !errors.Is(errors.Join(errs...), report.ErrPlanned) {
		t.Fatalf("both plans written, or neither: %v", errs)
	}
}

func (w *continuousWorld) builds(t *testing.T, n int) ([]string, []*gitstore.Store) {
	t.Helper()

	checkouts := make([]string, n)
	stores := make([]*gitstore.Store, n)

	for i := range n {
		checkouts[i] = filepath.Join(t.TempDir(), "trigger")
		if out, err := exec.CommandContext(t.Context(), "git", "clone", "-q", w.store.Remote, checkouts[i]).CombinedOutput(); err != nil {
			t.Fatalf("clone: %v: %s", err, out)
		}

		stores[i] = openStore(w.cfg)

		t.Cleanup(func() { _ = stores[i].Close() }) //nolint:errcheck // a scratch repository
	}

	return checkouts, stores
}

func TestTwoCommitsBuildsDecidingAtOnce(t *testing.T) {
	for _, at := range []struct {
		step string
		n    int
		why  string
	}{
		{"looked", 1, "it found its commit the tip and recorded nothing yet"},
		{"recorded", 1, "it recorded its round's start"},
		{"recorded", 2, "it recorded that its round plans"},
	} {
		t.Run(at.why, func(t *testing.T) {
			w := newContinuousWorld(t)
			older := w.first
			checkouts, stores := w.builds(t, 2)

			reached, release := pauseAt(t, older, at.step, at.n)
			doneOlder := w.decideIn(t, older, checkouts[0], stores[0])

			select {
			case <-reached:
			case got := <-doneOlder:
				t.Fatalf("the older build decided before it was paused: %+v", got)
			}

			newer := w.push()
			said := &saying{want: "waiting for the round of ", said: make(chan struct{})}
			doneNewer := w.decideTo(t, newer, checkouts[1], stores[1], said)

			if at.step == "recorded" {
				within(t, said.said, "the newer build's wait for the older's round")
			}

			release()

			gotOlder, gotNewer := <-doneOlder, <-doneNewer

			if gotOlder.err != nil || gotOlder.run || !strings.HasSuffix(gotOlder.out, "full "+older[:12]+": superseded by "+newer[:12]+": not run, the newer commit's run covers it\n") {
				t.Fatalf("the older build: %+v", gotOlder)
			}

			if gotNewer.err != nil || !gotNewer.run || !strings.HasSuffix(gotNewer.out, ": planning\n") {
				t.Fatalf("the newer build: %+v", gotNewer)
			}

			if at.step == "recorded" && !strings.Contains(gotNewer.out, "waiting for the round of "+older[:12]+" to end (it is deciding or planning;") {
				t.Fatalf("the newer build didn't say what it waited for: %q", gotNewer.out)
			}

			noLock(t, gotOlder.out, gotNewer.out)
		})
	}
}

func TestACommitPushedWhileARoundPlansWaits(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first

	if got := <-w.decide(t, first); got.err != nil || !got.run {
		t.Fatalf("the first build: %+v", got)
	}

	second := w.push()
	looks := countLooks(t, second)
	done, said := w.decideSaying(t, second)

	within(t, said, "the next build's wait for the round that plans")

	w.startRound(t, first)
	within(t, looks(2), "two looks of the next build after the plan")

	select {
	case got := <-done:
		t.Fatalf("the next commit's build ran beside the round that plans: %+v", got)
	default:
	}

	w.endRound(t, first)

	got := <-done
	if got.err != nil || !got.run || !strings.Contains(got.out, "full "+second[:12]+": waiting for the round of "+first[:12]+" to end (it is deciding or planning;") {
		t.Fatalf("the next commit's build: %+v", got)
	}

	noLock(t, got.out)
}

func TestABuildThatDiedBeforeItsPlan(t *testing.T) {
	w := newContinuousWorld(t)

	old := roundUnit
	roundUnit = time.Second

	t.Cleanup(func() { roundUnit = old })

	writeFile(t, filepath.Join(w.root, "pipelinegen.yaml"), "limits: {plan: 1}\n"+readFile(t, filepath.Join(w.root, "pipelinegen.yaml")))
	w.cfg = loadConfig(t, w.root)

	died := w.first
	if _, err := report.RecordRound(t.Context(), w.store, reportID(w.change(died)), report.RoundStarted, "", ""); err != nil {
		t.Fatal(err)
	}

	next := w.push()
	done := w.decide(t, next)

	// past limits.plan, within its margin, the round may still write its
	// plan: the next build still waits.
	time.Sleep(time.Duration(w.cfg.Limits.PlanMinutes())*roundUnit + time.Second)

	select {
	case got := <-done:
		t.Fatalf("the build stopped waiting within the margin past limits.plan: %+v", got)
	default:
	}

	got := <-done
	if got.err != nil || !got.run || !strings.Contains(got.out, "waiting for the round of "+died[:12]+" to end (it is deciding or planning;") ||
		!strings.Contains(got.out, "the round of "+died[:12]+" has written no plan and recorded nothing for ") || !strings.Contains(got.out, "more than 11s: it counts as dead") {
		t.Fatalf("the build after one that died: %+v", got)
	}

	noLock(t, got.out)

	stdout, stderr, code := run(t, "end", "--run", "full", "--commit", next[:12], "-root", w.root)
	if code != 0 || stdout != "full main "+next[:12]+": the round ended by hand before its plan\n" {
		t.Fatalf("end: %d %q %s", code, stdout, stderr)
	}

	third := w.push()

	if got := <-w.decide(t, third); got.err != nil || !got.run || strings.Contains(got.out, "waiting") {
		t.Fatalf("the build after a round ended by hand: %+v", got)
	}
}

func TestABuildOfASerialJobRecordsItsRound(t *testing.T) {
	w := newContinuousWorld(t)
	t.Setenv(serialPlanEnv, "1")

	if got := <-w.decide(t, w.first); got.err != nil || !got.run {
		t.Fatalf("the build: %+v", got)
	}

	if rounds, err := report.Rounds(t.Context(), w.store, "full", "main"); err != nil || len(rounds) != 1 {
		t.Fatalf("a build of a planning job written serial recorded no round: %v %v", rounds, err)
	}
}

func TestARoundEndedByHand(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	w.startRound(t, first)

	second := w.push()
	done, said := w.decideSaying(t, second)

	within(t, said, "the build's wait for the round in progress")

	stdout, stderr, code := run(t, "end", "--run", "full", "--commit", first[:12], "-root", w.root)
	if code != 0 || stdout != "full main "+first[:12]+": suite e2e ended by hand\n" {
		t.Fatalf("end: %d %q %s", code, stdout, stderr)
	}

	got := <-done
	if got.err != nil || !got.run {
		t.Fatalf("the build waiting for a round ended by hand: %+v", got)
	}

	gate := t.TempDir()

	var out bytes.Buffer

	ended, err := endedEarly(t.Context(), w.store, w.change(first), "e2e", gate, &out)
	if err != nil || !ended || !strings.Contains(readFile(t, gate+"/gate.json"), "[]") || !strings.Contains(out.String(), "ended by hand: e2e runs nothing") {
		t.Fatalf("a suite job of the ended round: %v %v %q", ended, err, out.String())
	}

	noLock(t, got.out, stdout, stderr, out.String())

	if _, _, code := run(t, "end", "--run", "full", "--commit", "0123456789ab", "-root", w.root); code != 2 {
		t.Fatalf("a round with no plan: %d", code)
	}
}

func TestNothingChangedRunsNothing(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first

	nightly := loadConfigText(t, strings.Replace(readFile(t, w.root+"/pipelinegen.yaml"),
		"trigger: {branch: main, continuous: true}", `trigger: {timer: {start: "3:00", stop: "4:00"}, branch: main}`, 1))

	for name, cfg := range map[string]*config.Config{"nightly": nightly, "continuous": w.cfg} {
		var out bytes.Buffer

		if ran, err := ranAlready(t.Context(), cfg, w.change(first), w.store, &out); err != nil || ran {
			t.Fatalf("%s: the first round ever: %v %v", name, ran, err)
		}
	}

	w.startRound(t, first)
	w.endRound(t, first)

	for name, cfg := range map[string]*config.Config{"nightly": nightly, "continuous": w.cfg} {
		var out bytes.Buffer

		ran, err := ranAlready(t.Context(), cfg, w.change(first), w.store, &out)
		if err != nil || !ran || out.String() != "full "+first[:12]+": nothing changed since the full of "+first[:12]+": not run\n" {
			t.Fatalf("%s: an unchanged commit: %v %v %q", name, ran, err, out.String())
		}

		noLock(t, out.String())
	}

	second := w.push()

	if ran, err := ranAlready(t.Context(), nightly, w.change(second), w.store, &bytes.Buffer{}); err != nil || ran {
		t.Fatalf("a new commit: %v %v", ran, err)
	}

	w.startRound(t, second)
	w.endRound(t, second)

	if ran, err := ranAlready(t.Context(), nightly, w.change(second), w.store, &bytes.Buffer{}); err != nil || !ran {
		t.Fatalf("after a passed round: %v %v", ran, err)
	}
}

func TestASilenceIsSaidPastItsBound(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		2*time.Second + time.Nanosecond:                    3 * time.Second,
		2*time.Second + 999*time.Millisecond:               3 * time.Second,
		3 * time.Second:                                    3 * time.Second,
		4*time.Hour + 30*time.Minute + 12*time.Millisecond: 4*time.Hour + 30*time.Minute + time.Second,
	} {
		if got := pastBound(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestARecordedRoundPlansWithinLimitsPlan(t *testing.T) {
	w := newContinuousWorld(t)

	ctx, cancel := planWithin(t.Context(), w.cfg)
	defer cancel()

	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Duration(w.cfg.Limits.PlanMinutes())*time.Minute {
		t.Fatalf("the plan's deadline: %v %v", deadline, ok)
	}

	expired, stop := context.WithTimeout(t.Context(), time.Nanosecond)
	defer stop()

	<-expired.Done()

	if err := planTookTooLong(expired, w.cfg, expired.Err()); err == nil || !strings.Contains(err.Error(), "raise limits.plan") {
		t.Fatalf("a plan past its limit: %v", err)
	}

	t.Setenv(serialPlanEnv, "1")

	serial, stopSerial := planWithin(t.Context(), w.cfg)
	defer stopSerial()

	if _, ok := serial.Deadline(); !ok {
		t.Fatal("a build of a planning job written serial plans without limits.plan")
	}
}

func TestAnExitedBuildHandsNothingOn(t *testing.T) {
	w := newContinuousWorld(t)

	if out, err := exec.CommandContext(t.Context(), "git", "-C", w.trigger, "checkout", "-q", "--detach", w.first).CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}

	newer := w.push()
	gate := filepath.Join(t.TempDir(), "gate")

	stdout, stderr, code := runIn(t, t.TempDir(), nil, "plan", "-run", "full", "-config", filepath.Join(w.root, "pipelinegen.yaml"), "-trigger", w.trigger, "-gate", gate)
	if code != 0 || stdout != "full "+w.first[:12]+": superseded by "+newer[:12]+": not run, the newer commit's run covers it\n" {
		t.Fatalf("plan of an older commit: %d %q %s", code, stdout, stderr)
	}

	if got := readFile(t, filepath.Join(gate, "gate.json")); got != "[]\n" {
		t.Fatalf("the gate of a build that exited: %q", got)
	}

	if got := readFile(t, filepath.Join(gate, "version")); got != "full "+w.first+"\n" {
		t.Fatalf("the hand-off's version: %q", got)
	}

	noLock(t, stdout, stderr)
}

func TestEndingARoundLeavesAnEndedSuite(t *testing.T) {
	w := newContinuousWorld(t)
	first := w.first
	w.startRound(t, first)

	id := reportID(w.change(first))
	if _, err := w.store.Write(t.Context(), "end", gitstore.Update{Ref: id.EndRef("e2e"), Any: true,
		Files: map[string][]byte{"end.json": []byte(`{"ended": "2026-11-15T15:00:00Z"}` + "\n")}}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "end", "--run", "full", "--commit", first[:12], "-root", w.root)
	if code != 0 || stdout != "full main "+first[:12]+": suite e2e had ended at 2026-11-15T15:00:00Z: left as it is\n" {
		t.Fatalf("end: %d %q %s", code, stdout, stderr)
	}

	ends, err := report.Ends(t.Context(), w.store, w.cfg, id)
	if err != nil || !ends["e2e"].Equal(time.Date(2026, 11, 15, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("the suite's end mark: %v %v", ends, err)
	}
}
