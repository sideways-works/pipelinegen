package cli

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

// heldWords are what a continuous run's steps never say: it waits for a round by reading records and
// holds nothing.
var heldWords = regexp.MustCompile(`(?i)\b(un)?lock|\blease|\bturn\b|\bturns\b|\buntil \d{4}-`)

// MODULE.bazel.lock is a file's name, not a lock.
func saysNothingHeld(t *testing.T, step string, outs ...string) {
	t.Helper()

	for _, out := range outs {
		if w := heldWords.FindString(strings.ReplaceAll(out, "MODULE.bazel.lock", "")); w != "" {
			t.Errorf("%s says %q:\n%s", step, w, out)
		}
	}
}

type roundsRepo struct {
	r             *testrepo.Repo
	remote, cache string
	bazelDiff     string
}

func newRoundsRepo(t *testing.T) *roundsRepo {
	t.Helper()
	testrepo.NeedBazel(t)

	w := &roundsRepo{r: testrepo.New(t), remote: storetest.Bare(t), cache: t.TempDir(), bazelDiff: bazeldifftest.Tool(t).Path}

	conf := readFile(t, filepath.Join(w.r.Dir, config.FileName))
	start, end := strings.Index(conf, "github:"), strings.Index(conf, "\nruns:")

	if start < 0 || end < start {
		t.Fatalf("the fixture's config changed:\n%s", conf)
	}

	conf = conf[:start] + "git: {remote: \"" + w.remote + "\"}\n" + conf[strings.Index(conf, "concourse:"):end] + `
runs:
  main:
    trigger: {branch: main, continuous: true}
    compare: last_passed
    select: affected
    suites: {backend: {}}
everything_on: [pipelinegen.yaml]
untested:
  - {paths: ["**"], reason: the fixture's other files}
`
	if onBranchStore {
		conf += "store: {backend: branch}\n"
	}

	w.r.Write(config.FileName, conf)
	dependOnWhatTheyTest(w.r)

	for k, v := range map[string]string{envGitURL: "", envAPIURL: "", envToken: "", envJobURL: "", envPR: "", envAlsoTo: ""} {
		t.Setenv(k, v)
	}

	testrepo.Bazel(t, filepath.Join(w.cache, "tree"))

	return w
}

func (w *roundsRepo) commit(t *testing.T, msg string) string {
	t.Helper()

	c := w.r.Commit(msg)
	w.r.Git("push", "-q", "--force", w.remote, "HEAD:refs/heads/main")

	return c
}

func (w *roundsRepo) trigger(t *testing.T, commit string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "trigger")

	for _, args := range [][]string{{"clone", "-q", w.remote, dir}, {"-C", dir, "checkout", "-q", commit}} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	return dir
}

func (w *roundsRepo) plan(t *testing.T, commit string, wantPlan bool) string {
	t.Helper()

	trigger := w.trigger(t, commit)
	gate := filepath.Join(t.TempDir(), "gate")

	stdout, stderr, code := runIn(t, t.TempDir(), nil, "plan", "-run", "main", "-trigger", trigger, "-cache", w.cache, "-bazel-diff", w.bazelDiff, "-gate", gate)
	if code != 0 {
		t.Fatalf("plan %s: %d\n%s\n%s", commit[:12], code, stdout, stderr)
	}

	saysNothingHeld(t, "plan of "+commit[:12], stdout, stderr)

	if open := strings.Contains(readFile(t, filepath.Join(gate, "gate.json")), `"1"`); open != wantPlan {
		t.Fatalf("plan %s: its hand-off is open %v, want %v:\n%s", commit[:12], open, wantPlan, stdout)
	}

	if got := readFile(t, filepath.Join(gate, "version")); got != "main "+commit+"\n" {
		t.Fatalf("plan %s: its hand-off's version %q", commit[:12], got)
	}

	return stdout
}

func suiteEnv(commit string) map[string]string {
	return map[string]string{
		envRun: "main", envChange: "main", envCommit: commit, envSuite: "backend", envBatch: "1",
		envJobURL: "https://ci/jobs/backend", envSlice: "", envLabel: "",
	}
}

func (w *roundsRepo) suiteJob(t *testing.T, commit string) bool {
	t.Helper()

	trigger := w.trigger(t, commit)
	work := t.TempDir()
	env := suiteEnv(commit)

	step := func(args ...string) {
		t.Helper()

		stdout, stderr, code := runIn(t, work, env, append(args, "-root", trigger)...)
		if code != 0 {
			t.Fatalf("%v of %s: %d\n%s\n%s", args, commit[:12], code, stdout, stderr)
		}

		saysNothingHeld(t, strings.Join(args[:min(2, len(args))], " ")+" of "+commit[:12], stdout, stderr)
	}

	stdout, stderr, code := runIn(t, work, env, "tree", "-run", "main", "-suite", "backend", "-trigger", trigger, "-out", "source", "-gate", "gate")
	if code != 0 {
		t.Fatalf("tree of %s: %d\n%s\n%s", commit[:12], code, stdout, stderr)
	}

	saysNothingHeld(t, "tree of "+commit[:12], stdout, stderr)

	open := strings.Contains(readFile(t, filepath.Join(work, "gate", "gate.json")), `"1"`)
	if open {
		s := openStore(loadConfig(t, trigger))

		plan, ok, err := readPlan(t.Context(), s, report.ID{Run: "main", Change: "main", Commit: commit})
		if cerr := s.Close(); err != nil || cerr != nil || !ok {
			t.Fatalf("the plan of %s: %v %v %v", commit[:12], ok, err, cerr)
		}

		sp, _ := suitePlan(plan, "backend")
		writeFile(t, filepath.Join(work, "batches", "1", "slices.json"), batchSlices(t, sp.Selected))

		for _, sl := range sp.Selected {
			step("slice", "start", sl.Name)
			writeFile(t, filepath.Join(work, "result", sl.Name+".json"), `{"passed": true}`)
			step("slice", "end", sl.Name)
		}

		step("report", "batch-end")
	}

	step("report", "suite")

	return open
}

func (w *roundsRepo) selected(t *testing.T, commit string) (base string, labels []string) {
	t.Helper()

	s := openStore(loadConfig(t, w.r.Dir))

	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // a scratch repository

	plan, ok, err := readPlan(t.Context(), s, report.ID{Run: "main", Change: "main", Commit: commit})
	if err != nil || !ok {
		t.Fatalf("the plan of %s: %v %v", commit[:12], ok, err)
	}

	sp, _ := suitePlan(plan, "backend")
	for _, sl := range sp.Selected {
		labels = append(labels, sl.Label)
	}

	return plan.Base, labels
}

func TestASkippedCommitIsTestedByTheNextRound(t *testing.T) {
	skippedCommitIsTestedByTheNextRound(t)
}

func skippedCommitIsTestedByTheNextRound(t *testing.T) {
	t.Helper()

	w := newRoundsRepo(t)
	moneyTest := "//backendtests:money_test"

	first := w.commit(t, "a continuous run of the backend suite")

	if out := w.plan(t, first, true); !strings.Contains(out, "main "+first[:12]+": the newest of main, and no round in progress: planning\n") {
		t.Fatalf("the first round's plan said:\n%s", out)
	}

	w.suiteJob(t, first)

	if out := w.plan(t, first, false); out != "main "+first[:12]+": nothing changed since the main of "+first[:12]+": not run\n" {
		t.Fatalf("the first commit planned again said:\n%s", out)
	}

	w.r.Write("api/money/money.go", readFile(t, filepath.Join(w.r.Dir, "api", "money", "money.go"))+"\n// changed\n")
	skipped := w.commit(t, "a change the backend suite tests")
	w.r.Write("web/app.ts", readFile(t, filepath.Join(w.r.Dir, "web", "app.ts"))+"\n// changed\n")
	tip := w.commit(t, "a change only e2e tests")

	if out := w.plan(t, skipped, false); out != "main "+skipped[:12]+": superseded by "+tip[:12]+": not run, the newer commit's run covers it\n" {
		t.Fatalf("the skipped commit's plan said:\n%s", out)
	}

	if w.suiteJob(t, skipped) {
		t.Fatal("the skipped commit's suite job ran")
	}

	w.plan(t, tip, true)

	if base, labels := w.selected(t, tip); base != first || !slices.Contains(labels, moneyTest) {
		t.Fatalf("the round after a skipped commit compared with %s and selected %v; want %s and %s", base, labels, first[:12], moneyTest)
	}

	w.suiteJob(t, tip)

	w.r.Write("api/money/money.go", readFile(t, filepath.Join(w.r.Dir, "api", "money", "money.go"))+"\n// changed again\n")
	ended := w.commit(t, "a change whose round is ended by hand")
	w.plan(t, ended, true)

	if stdout, stderr, code := runIn(t, w.r.Dir, nil, "end", "--run", "main", "--commit", ended[:12]); code != 0 {
		t.Fatalf("end: %d %s %s", code, stdout, stderr)
	} else {
		saysNothingHeld(t, "end", stdout, stderr)
	}

	w.r.Write("web/app.ts", readFile(t, filepath.Join(w.r.Dir, "web", "app.ts"))+"\n// changed again\n")
	after := w.commit(t, "the change after it")
	w.plan(t, after, true)

	if base, labels := w.selected(t, after); base != tip || !slices.Contains(labels, moneyTest) {
		t.Fatalf("the round after one ended by hand compared with %s and selected %v; want %s and %s", base, labels, tip[:12], moneyTest)
	}
}

func TestNoPassWithinTheWalkSelectsWide(t *testing.T) {
	w := newContinuousWorld(t)
	text := strings.Replace(readFile(t, filepath.Join(w.root, config.FileName)), "    compare: none\n", "    compare: last_passed\n", 1)
	writeFile(t, filepath.Join(w.root, config.FileName), text)
	w.cfg = loadConfig(t, w.root)

	if err := report.WritePlan(t.Context(), w.store, reportID(w.change(w.first)), []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	base := func(commit string) (string, bool) {
		t.Helper()

		if err := runs.FetchCommit(t.Context(), w.trigger, commit, runs.Auth{}); err != nil {
			t.Fatal(err)
		}

		base, inFlight, err := lastPassed(t.Context(), w.cfg, w.change(commit), w.store, w.trigger)
		if err != nil {
			t.Fatal(err)
		}

		return base, inFlight
	}

	var last string
	for range 10 {
		last = w.push()
	}

	if got, inFlight := base(last); got != w.first || inFlight {
		t.Fatalf("the base within the walk: %q %v, want %s", got, inFlight, w.first)
	}

	for range 50 {
		last = w.push()
	}

	w.startRound(t, last)
	w.endRound(t, last)

	if got, inFlight := base(last); got != "" || inFlight {
		t.Fatalf("the base past the walk: %q %v, want none", got, inFlight)
	}
}
