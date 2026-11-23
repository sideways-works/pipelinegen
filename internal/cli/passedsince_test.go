package cli

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

// newPassedRepo is a branch whose run tests the e2e suite's two slices, checkout and orders, which both
// depend on //web; orders alone depends on //api/orders.
func newPassedRepo(t *testing.T) *roundsRepo {
	t.Helper()
	testrepo.NeedBazel(t)

	w := &roundsRepo{r: testrepo.New(t), remote: storetest.Bare(t), cache: t.TempDir(), bazelDiff: bazeldifftest.Tool(t).Path}

	conf := readFile(t, filepath.Join(w.r.Dir, config.FileName))
	start, end := strings.Index(conf, "github:"), strings.Index(conf, "\nruns:")
	whole := strings.Index(conf, "    whole_suite:")
	specs := strings.Index(conf, "  specs:")

	if start < 0 || end < start || whole < 0 || specs < whole {
		t.Fatalf("the fixture's config changed:\n%s", conf)
	}

	// no check of the whole suite: it would need every slice to run
	conf = conf[:whole] + conf[specs:]
	start, end = strings.Index(conf, "github:"), strings.Index(conf, "\nruns:")

	conf = conf[:start] + "git: {remote: \"" + w.remote + "\"}\n" + conf[strings.Index(conf, "concourse:"):end] + `
runs:
  main:
    trigger: {branch: main, continuous: true}
    compare: last_passed
    select: affected
    suites: {e2e: {}}
everything_on: [pipelinegen.yaml]
untested:
  - {paths: ["**"], reason: the fixture's other files}
`
	w.r.Write(config.FileName, conf)
	dependOnWhatTheyTest(w.r)

	for k, v := range map[string]string{envGitURL: "", envAPIURL: "", envToken: "", envJobURL: "", envPR: "", envAlsoTo: ""} {
		t.Setenv(k, v)
	}

	testrepo.Bazel(t, filepath.Join(w.cache, "tree"))

	return w
}

// e2eJob runs the e2e suite's job of a commit, each slice as outcomes says: passed, failed, no tests
// (passed with none run), or not run (in a batch of its own whose setup never started a slice).
func (w *roundsRepo) e2eJob(t *testing.T, commit string, outcomes map[string]string) {
	t.Helper()

	trigger := w.trigger(t, commit)
	work := t.TempDir()

	env := func(batch string) map[string]string {
		return map[string]string{
			envRun: "main", envChange: "main", envCommit: commit, envSuite: "e2e", envBatch: batch,
			envJobURL: "https://ci/jobs/e2e", envSlice: "", envLabel: "",
		}
	}

	step := func(batch string, mayFail bool, args ...string) {
		t.Helper()

		stdout, stderr, code := runIn(t, work, env(batch), append(args, "-root", trigger)...)
		if code != 0 && !mayFail {
			t.Fatalf("%v of %s: %d\n%s\n%s", args, commit[:12], code, stdout, stderr)
		}
	}

	step("1", false, "tree", "-run", "main", "-suite", "e2e", "-trigger", trigger, "-out", "source", "-gate", "gate")

	s := openStore(loadConfig(t, trigger))

	plan, ok, err := readPlan(t.Context(), s, report.ID{Run: "main", Change: "main", Commit: commit})
	if cerr := s.Close(); err != nil || cerr != nil || !ok {
		t.Fatalf("the plan of %s: %v %v %v", commit[:12], ok, err, cerr)
	}

	sp, _ := suitePlan(plan, "e2e")

	var ran, notRun []bazel.Slice

	for _, sl := range sp.Selected {
		if outcomes[sl.Name] == "not run" {
			notRun = append(notRun, sl)
		} else {
			ran = append(ran, sl)
		}
	}

	result := func(outcome string) string {
		switch outcome {
		case "failed":
			return `{"passed": false, "failures": [{"test": "it", "tail": "broke"}]}`
		case "no tests":
			return `{"passed": true, "tests": 0}`
		}

		return `{"passed": true}`
	}

	writeFile(t, filepath.Join(work, "batches", "1", "slices.json"), batchSlices(t, ran))

	for _, sl := range ran {
		step("1", false, "slice", "start", sl.Name)
		writeFile(t, filepath.Join(work, "result", sl.Name+".json"), result(outcomes[sl.Name]))
		step("1", true, "slice", "end", sl.Name)
	}

	step("1", true, "report", "batch-end")

	if len(notRun) > 0 {
		writeFile(t, filepath.Join(work, "batches", "2", "slices.json"), batchSlices(t, notRun))
		step("2", true, "report", "batch-end")
	}

	step("1", true, "report", "suite")
}

func (w *roundsRepo) e2ePlan(t *testing.T, commit string) affected.SuitePlan {
	t.Helper()

	s := openStore(loadConfig(t, w.r.Dir))

	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // a scratch repository

	plan, ok, err := readPlan(t.Context(), s, report.ID{Run: "main", Change: "main", Commit: commit})
	if err != nil || !ok {
		t.Fatalf("the plan of %s: %v %v", commit[:12], ok, err)
	}

	sp, _ := suitePlan(plan, "e2e")
	sp.Reason = plan.Base

	return sp
}

func (w *roundsRepo) change(t *testing.T, path, msg string) string {
	t.Helper()

	w.r.Write(path, readFile(t, filepath.Join(w.r.Dir, path))+"\n// "+msg+"\n")

	c := w.commit(t, msg)
	w.plan(t, c, true)

	return c
}

func passedSinceOf(sp affected.SuitePlan) (dropped []string, reasons []string) { //nolint:nonamedreturns // the two lists
	for _, n := range sp.Narrowed {
		if n.Narrowing == plugins.NarrowPassedSince {
			dropped = append(dropped, n.Dropped...)
			reasons = append(reasons, n.Reason)
		}
	}

	return dropped, reasons
}

func TestASliceComparesFromWhereItLastPassed(t *testing.T) {
	w := newPassedRepo(t)
	orders := "api/orders/orders.go"

	first := w.commit(t, "the e2e suite on main")
	w.plan(t, first, true)
	w.e2eJob(t, first, map[string]string{"e2e.checkout": "passed", "e2e.orders": "passed"})

	// orders keeps failing from here on, so the run's base stays at first
	c1 := w.change(t, orders, "orders breaks")
	w.e2eJob(t, c1, map[string]string{"e2e.orders": "failed"})

	shared := w.change(t, "web/app.ts", "a change both slices reach")
	w.e2eJob(t, shared, map[string]string{"e2e.checkout": "no tests", "e2e.orders": "failed"})

	// a pass with no tests run moves nothing
	c3 := w.change(t, orders, "orders again")
	if sp := w.e2ePlan(t, c3); sp.Reason != first || selectedNames(sp) != "e2e.checkout,e2e.orders" {
		t.Fatalf("after a pass with no tests: base %.12s, runs %v", sp.Reason, selectedNames(sp))
	} else if dropped, _ := passedSinceOf(sp); dropped != nil {
		t.Fatalf("after a pass with no tests, left out %v", dropped)
	}

	w.e2eJob(t, c3, map[string]string{"e2e.checkout": "not run", "e2e.orders": "failed"})

	// nor does a slice whose batch never started
	c4 := w.change(t, orders, "orders once more")
	if sp := w.e2ePlan(t, c4); sp.Reason != first || len(sp.Selected) != 2 {
		t.Fatalf("after a slice that didn't run: base %.12s, runs %v", sp.Reason, selectedNames(sp))
	}

	w.e2eJob(t, c4, map[string]string{"e2e.checkout": "passed", "e2e.orders": "failed"})

	// checkout passed at c4 with the change both reach; what came since
	// reaches orders alone, which has no pass and keeps the run's base
	head := w.change(t, orders, "orders still")
	sp := w.e2ePlan(t, head)

	dropped, reasons := passedSinceOf(sp)
	if sp.Reason != first || selectedNames(sp) != "e2e.orders" || !slices.Equal(dropped, []string{"e2e.checkout"}) {
		t.Fatalf("base %.12s, runs %v, left out %v", sp.Reason, selectedNames(sp), dropped)
	}

	want := "compared from " + c4[:12] + ", where it last passed in run main"
	if len(reasons) != 1 || !strings.HasPrefix(reasons[0], want) {
		t.Fatalf("the reason %q, want %q", reasons, want)
	}

	stdout, stderr, code := runIn(t, w.r.Dir, nil, "why", "-root", w.r.Dir, "-run", "main", "-change", "main", "-commit", head, "-slice", "e2e.checkout")
	if code != 0 || !strings.Contains(stdout, "the narrowing passed-since left it out: "+want) || !strings.Contains(stdout, "it would have run at narrowing level 4 or above") {
		t.Fatalf("why: %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestPassedSinceIsOffFromItsLevel(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "one"},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two"},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "three"},
	} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	base, head := revParse(t, repo, "HEAD~2"), revParse(t, repo, "HEAD")

	// a store no read can reach: reading it says so in a note
	cfg := loadConfigText(t, strings.Replace(noHostConfig, "REMOTE", "file://"+filepath.Join(t.TempDir(), "none.git"), 1))
	store := openStore(cfg)

	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // nothing written

	ch := runs.Change{Run: "main", ID: "main", Commit: head, Branch: "main"}

	if rebase, notes := passedSinceBase(t.Context(), cfg, ch, store, repo, base, head, t.TempDir(), ""); rebase != nil || len(notes) != 1 {
		t.Fatalf("at level 0 the store is read: %v %q", rebase != nil, notes)
	}

	offAt := plugins.CoreOffAt(plugins.NarrowPassedSince)
	if offAt != 4 {
		t.Fatalf("passed-since is off from %d", offAt)
	}

	for _, level := range []int{offAt, config.MaxNarrowing} {
		cfg.Narrowing = level

		if rebase, notes := passedSinceBase(t.Context(), cfg, ch, store, repo, base, head, t.TempDir(), ""); rebase != nil || notes != nil {
			t.Fatalf("at level %d: %v %q", level, rebase != nil, notes)
		}
	}

	cfg.Narrowing = 0
	cfg.Runs["main"] = config.Run{Trigger: cfg.Runs["main"].Trigger, Compare: config.CompareNone, Select: config.SelectAffected}

	if rebase, notes := passedSinceBase(t.Context(), cfg, ch, store, repo, base, head, t.TempDir(), ""); rebase != nil || notes != nil {
		t.Fatalf("a run that compares with nothing: %v %q", rebase != nil, notes)
	}
}

func TestAPassIsOfTheSliceItsBatchRecordedItAs(t *testing.T) {
	file := bazel.Slice{Name: "backend/cmd/check_test.go", Label: bazel.SelectorLabel("backend/cmd/check_test.go")}
	target := bazel.Slice{Name: "ci.slices.markdown", Label: "//ci/slices:markdown"}

	for _, c := range []struct {
		label string
		sl    bazel.Slice
		want  bool
	}{
		// a test file's batch records its slice under its name
		{"backend/cmd/check_test.go", file, true},
		{"@pipelinegen_deps//slices:backend/cmd/check_test.go", file, true},
		{"@@+deps+pipelinegen_deps//slices:backend/cmd/check_test.go", bazel.Slice{Name: file.Name, Label: "@@+deps+pipelinegen_deps//slices:backend/cmd/check_test.go"}, true},
		{"", file, true},
		{"backend/cmd/other_test.go", file, false},
		{"//ci/slices:markdown", target, true},
		// a target's slice is its label's, never its name's
		{"ci.slices.markdown", target, false},
		{"//ci/slices:other", target, false},
	} {
		if got := passedAs(c.label, c.sl); got != c.want {
			t.Errorf("passedAs(%q, %q) = %v, want %v", c.label, c.sl.Label, got, c.want)
		}
	}
}

// The label a batch's slice is recorded under, as the report writes it, is the one passedAs takes for
// the slice the plan selected: a test file's and a target's alike.
func TestTheLabelABatchRecordsIsThePlansSlice(t *testing.T) {
	for _, sl := range []bazel.Slice{
		{Name: "backend/cmd/check_test.go", Label: bazel.SelectorLabel("backend/cmd/check_test.go"), Sources: []string{"backend/cmd/check_test.go"}},
		{Name: "ci.slices.markdown", Label: "//ci/slices:markdown", Kind: "sh_test"},
	} {
		ev := sliceEvent("s", fromBazel([]bazel.Slice{sl})[0], t.TempDir(), t.TempDir(), "")
		if !passedAs(ev.Label, sl) {
			t.Errorf("%s recorded as %q: not its pass", sl.Label, ev.Label)
		}
	}
}
