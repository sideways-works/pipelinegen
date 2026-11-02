package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestARunPlansOnlyItsSuites(t *testing.T) { runPlansOnlyItsSuites(t) }

func TestARunPlansOnlyItsSuitesOnABranch(t *testing.T) { onBranch(t, runPlansOnlyItsSuites) }

func runPlansOnlyItsSuites(t *testing.T) {
	t.Helper()
	testrepo.NeedBazel(t)

	bd := bazeldifftest.Tool(t).Path
	remote := storetest.Bare(t)

	r := testrepo.New(t)

	conf := readFile(t, filepath.Join(r.Dir, config.FileName))
	start, end := strings.Index(conf, "github:"), strings.Index(conf, "\nruns:")

	if start < 0 || end < start {
		t.Fatalf("the fixture's config changed:\n%s", conf)
	}

	conf = conf[:start] + "git: {remote: \"" + remote + "\"}\n" + conf[strings.Index(conf, "concourse:"):end] + `
runs:
  main:
    trigger: {branch: main}
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

	r.Write(config.FileName, conf)
	dependOnWhatTheyTest(r)

	head := r.Commit("a run of the backend suite alone")

	for k, v := range map[string]string{
		envGitURL: "", envAPIURL: "", envToken: "",
		envRun: "main", envChange: "main", envCommit: head,
		envPR: "", envAlsoTo: "", envSuite: "backend", envBatch: "1", envJobURL: "https://ci/jobs/backend",
		envSlice: "", envLabel: "",
	} {
		t.Setenv(k, v)
	}

	cfg := loadConfig(t, r.Dir)
	if got := cfg.SuiteNames(); len(got) != 3 {
		t.Fatalf("the fixture has the suites %v, want three", got)
	}

	s := openStore(cfg)

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	ch := runs.Change{Run: "main", ID: "main", Commit: head, Branch: "main"}

	var out bytes.Buffer
	if err := planOneStep(t.Context(), cfg, ch, s, r.Dir, cache, bd, &out); err != nil {
		t.Fatalf("plan: %v\n%s", err, out.String())
	}

	plan, ok, err := readPlan(t.Context(), s, reportID(ch))
	if err != nil || !ok {
		t.Fatalf("the plan: %v %v", ok, err)
	}

	if len(plan.Suites) != 1 || plan.Suites[0].Suite != "backend" || len(plan.Suites[0].Selected) == 0 {
		names := make([]string, 0, len(plan.Suites))
		for _, sp := range plan.Suites {
			names = append(names, sp.Suite)
		}

		t.Fatalf("the plan holds the suites %v, want backend alone, with its slices", names)
	}

	t.Chdir(t.TempDir())

	selected := plan.Suites[0].Selected
	writeFile(t, "batches/1/slices.json", batchSlices(t, selected))

	for _, sl := range selected {
		if _, stderr, code := run(t, "slice", "start", sl.Name, "-root", r.Dir); code != 0 {
			t.Fatalf("slice start %s: %d %s", sl.Name, code, stderr)
		}

		writeFile(t, "result/"+sl.Name+".json", `{"passed": true}`)

		if _, stderr, code := run(t, "slice", "end", sl.Name, "-root", r.Dir); code != 0 {
			t.Fatalf("slice end %s: %d %s", sl.Name, code, stderr)
		}
	}

	for _, step := range [][]string{{"report", "batch-end"}, {"report", "suite"}} {
		if stdout, stderr, code := run(t, append(step, "-root", r.Dir)...); code != 0 {
			t.Fatalf("%v: %d %s %s", step, code, stdout, stderr)
		}
	}

	if got, err := report.RunState(t.Context(), s, cfg, reportID(ch)); err != nil || got != "success" {
		t.Fatalf("the run's state in the store: %q %v", got, err)
	}

	lines, err := statusLines(t.Context(), s, cfg, head, "")
	if err != nil || len(lines) != 1 || lines[0].State != "passed" {
		t.Fatalf("status: %+v %v", lines, err)
	}

	if err := statusExit(cfg.Runs, "", lines); err != nil {
		t.Fatalf("status of a run whose one suite passed: %v", err)
	}

	r.Write("web/app.ts", readFile(t, filepath.Join(r.Dir, "web", "app.ts"))+"\n// changed\n")
	next := r.Commit("a change only e2e tests")
	ch = runs.Change{Run: "main", ID: "main", Commit: next, Branch: "main"}
	t.Setenv(envCommit, next)

	out.Reset()

	if err := planOneStep(t.Context(), cfg, ch, s, r.Dir, cache, bd, &out); err != nil {
		t.Fatalf("plan of the next commit: %v\n%s", err, out.String())
	}

	if plan, _, err = readPlan(t.Context(), s, reportID(ch)); err != nil || plan.Base != head || len(plan.Suites) != 1 || len(plan.Suites[0].Selected) != 0 {
		t.Fatalf("the next commit's plan: %+v %v", plan, err)
	}

	if got, err := report.RunState(t.Context(), s, cfg, reportID(ch)); err != nil || got != "success" {
		t.Fatalf("a run that selected nothing: %q %v", got, err)
	}

	if stdout, stderr, code := run(t, "report", "suite", "-root", r.Dir); code != 0 {
		t.Fatalf("report suite: %d %s %s", code, stdout, stderr)
	}

	if lines, err := statusLines(t.Context(), s, cfg, next, ""); err != nil || len(lines) != 1 || lines[0].State != "passed" {
		t.Fatalf("status of a run that selected nothing: %+v %v", lines, err)
	}
}

func dependOnWhatTheyTest(r *testrepo.Repo) {
	r.Write("e2e/BUILD.bazel", `filegroup(name = "checkout", srcs = ["checkout.spec.ts"], data = ["//api/money", "//web"], tags = ["slice-e2e"])

filegroup(name = "orders", srcs = ["orders.spec.ts"], data = ["//api/orders", "//web"], tags = ["slice-e2e"])

filegroup(name = "helpers", srcs = ["helpers.ts"])
`)
	r.Write("backendtests/BUILD.bazel", `filegroup(name = "money_test", srcs = ["money_test.go"], data = ["//api/money"], tags = ["manual", "slice-backend"])
`)
}

func TestAProposedChangeCantTakeASuiteOutOfItsRun(t *testing.T) {
	testrepo.NeedBazel(t)

	bd := bazeldifftest.Tool(t).Path
	r := testrepo.New(t)

	both := "suites: {e2e: {}, backend: {}}"
	conf := strings.Replace(readFile(t, filepath.Join(r.Dir, config.FileName)), "  - pipelinegen.yaml\n", "", 1) +
		"untested:\n  - {paths: [\"**\"], reason: the fixture's other files}\n"

	if strings.Count(conf, both) < 2 || strings.Contains(conf, "- pipelinegen.yaml") {
		t.Fatalf("the fixture's config changed:\n%s", conf)
	}

	r.Write(config.FileName, conf)
	dependOnWhatTheyTest(r)
	base := r.Commit("pr and main run e2e and backend")

	r.Write(config.FileName, strings.ReplaceAll(conf, both, "suites: {backend: {}}"))
	r.Write("e2e/orders.spec.ts", readFile(t, filepath.Join(r.Dir, "e2e", "orders.spec.ts"))+"\n// changed\n")
	dropped := r.Commit("e2e out of pr and main")

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	plan := func(cfg *config.Config, run, base, head string) *affected.Plan {
		t.Helper()

		p, err := planCommits(t.Context(), cfg, nil, r.Dir, run, base, head, false, cache, bd, "")
		if err != nil {
			t.Fatalf("plan of %s: %v", run, err)
		}

		return p
	}

	proposed := func(trusted config.Config) *config.Config {
		t.Helper()

		blob, err := json.Marshal(trusted.Trusted())
		if err != nil {
			t.Fatal(err)
		}

		t.Setenv(envTrusted, string(blob))

		cfg := loadConfig(t, r.Dir)
		if err := trustChange(cfg, runs.Change{Run: "pr", ID: "pr-7", PR: 7, Commit: r.Git("rev-parse", "HEAD")}); err != nil {
			t.Fatal(err)
		}

		return cfg
	}

	pr := proposed(configAt(t, r, base))
	if p := plan(pr, "pr", base, dropped); !slices.Equal(suiteNames(p), []string{"backend", "e2e"}) || !selects(p, "e2e", "//e2e:orders") {
		t.Fatalf("a pull request that takes e2e out of its run: %v %+v", suiteNames(p), p.Suites)
	}

	if !slices.ContainsFunc(pr.Warnings, func(w string) bool { return strings.Contains(w, "runs.pr.suites") }) {
		t.Errorf("the plan doesn't say the change edits its run's suites: %q", pr.Warnings)
	}

	if p := plan(loadConfig(t, r.Dir), "main", base, dropped); !slices.Equal(suiteNames(p), []string{"backend"}) {
		t.Fatalf("a branch commit that takes e2e out of its run: %v", suiteNames(p))
	}

	r.Write(config.FileName, conf)
	r.Write("e2e/orders.spec.ts", readFile(t, filepath.Join(r.Dir, "e2e", "orders.spec.ts"))+"// again\n")
	added := r.Commit("e2e back into pr and main")

	if p := plan(proposed(configAt(t, r, dropped)), "pr", dropped, added); !slices.Equal(suiteNames(p), []string{"backend", "e2e"}) || !selects(p, "e2e", "//e2e:orders") {
		t.Fatalf("a pull request that adds e2e to its run: %v %+v", suiteNames(p), p.Suites)
	}
}

func configAt(t *testing.T, r *testrepo.Repo, commit string) config.Config {
	t.Helper()

	cfg, err := config.Parse([]byte(r.Git("show", commit+":"+config.FileName)))
	if err != nil {
		t.Fatal(err)
	}

	return *cfg
}

func suiteNames(p *affected.Plan) []string {
	out := make([]string, 0, len(p.Suites))
	for _, sp := range p.Suites {
		out = append(out, sp.Suite)
	}

	slices.Sort(out)

	return out
}

func selects(p *affected.Plan, suite, label string) bool {
	sp, ok := suitePlan(p, suite)

	return ok && slices.ContainsFunc(sp.Selected, func(sl bazel.Slice) bool { return sl.Label == label })
}

func batchSlices(t *testing.T, slices []bazel.Slice) string {
	t.Helper()

	data, err := json.Marshal(slices)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

const everySuitePlan = `{"suites": [
	{"suite": "e2e", "selected": [{"label": "//e2e:a"}], "untouched": ["//e2e:c"]},
	{"suite": "unit", "selected": [{"label": "//unit:x"}, {"label": "//unit:y"}], "untouched": []}
]}`

func withUnitSuite(t *testing.T, text string) string {
	t.Helper()

	out := strings.Replace(text, "suites:\n", "suites:\n  unit:\n    tag: slice-unit\n    run_slice: ci/tasks/unit-slice.yml\n", 1)
	if out == text {
		t.Fatalf("no suites in the config:\n%s", text)
	}

	return out
}

func TestAPullRequestRunEndsWithAPlanOfEverySuite(t *testing.T) {
	pullRequestRunEndsWithAPlanOfEverySuite(t)
}

func TestAPullRequestRunEndsWithAPlanOfEverySuiteOnABranch(t *testing.T) {
	onBranch(t, pullRequestRunEndsWithAPlanOfEverySuite)
}

func pullRequestRunEndsWithAPlanOfEverySuite(t *testing.T) {
	t.Helper()

	root, api, s := runEnv(t)
	conf := withUnitSuite(t, readFile(t, filepath.Join(root, config.FileName)))
	writeFile(t, filepath.Join(root, config.FileName), conf)

	cfg := loadConfig(t, root)
	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	if err := report.WritePlan(t.Context(), s, id, []byte(everySuitePlan)); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/pr/e2e"); got != "pending 0 of 1 slices done, 1 untouched" {
		t.Fatalf("e2e: %q", got)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}]`)

	if _, stderr, code := run(t, "slice", "start", "e2e.a", "-root", root); code != 0 {
		t.Fatalf("slice start: %d %s", code, stderr)
	}

	writeFile(t, "result/e2e.a.json", `{"passed": true}`)

	if _, stderr, code := run(t, "slice", "end", "e2e.a", "-root", root); code != 0 {
		t.Fatalf("slice end: %d %s", code, stderr)
	}

	for _, step := range [][]string{{"report", "batch-end"}, {"report", "suite"}} {
		if stdout, stderr, code := run(t, append(step, "-root", root)...); code != 0 {
			t.Fatalf("%v: %d %s %s", step, code, stdout, stderr)
		}
	}

	if got := api.status("pipelinegen/pr"); got != "success 1 suite passed" {
		t.Fatalf("the run's status: %q", got)
	}

	if got := api.status("pipelinegen/pr/unit"); got != "" {
		t.Fatalf("a suite the run doesn't cover got a status: %q", got)
	}

	if body := api.comment(7); body == "" || strings.Contains(body, "unit") {
		t.Fatalf("the comment:\n%s", body)
	}

	lines, err := statusLines(t.Context(), s, cfg, id.Commit, "")
	if err != nil || len(lines) != 1 || lines[0].State != "passed" {
		t.Fatalf("status: %+v %v", lines, err)
	}
}

func TestAContinuousRunOfSomeSuitesEndsItsRound(t *testing.T) {
	continuousRunOfSomeSuitesEndsItsRound(t)
}

func TestAContinuousRunOfSomeSuitesEndsItsRoundOnABranch(t *testing.T) {
	onBranch(t, continuousRunOfSomeSuitesEndsItsRound)
}

func continuousRunOfSomeSuitesEndsItsRound(t *testing.T) {
	t.Helper()

	w := newContinuousWorld(t)

	conf := withUnitSuite(t, readFile(t, filepath.Join(w.root, config.FileName)))
	w.root = w.trigger
	writeFile(t, filepath.Join(w.root, config.FileName), conf)
	w.cfg = loadConfig(t, w.root)

	tip := w.first
	id := reportID(w.change(tip))

	if err := report.WritePlan(t.Context(), w.store, id, []byte(everySuitePlan)); err != nil {
		t.Fatal(err)
	}

	for k, v := range map[string]string{
		envRun: "full", envChange: "main", envCommit: tip, envSuite: "e2e", envBatch: "1", envJobURL: "https://ci/jobs/e2e", envSlice: "", envLabel: "", envAlsoTo: "",
	} {
		t.Setenv(k, v)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}]`)

	for _, step := range []string{"start", "end"} {
		if step == "end" {
			writeFile(t, "result/e2e.a.json", `{"passed": true}`)
		}

		if _, stderr, code := run(t, "slice", step, "e2e.a", "-root", w.root); code != 0 {
			t.Fatalf("slice %s: %d %s", step, code, stderr)
		}
	}

	if _, stderr, code := run(t, "report", "batch-end", "-root", w.root); code != 0 {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if r, ok, err := newestRound(t.Context(), w.store, w.cfg, w.change(tip)); err != nil || !ok || r.ended {
		t.Fatalf("the round ended before the run's suite did: %+v %v %v", r, ok, err)
	}

	if _, stderr, code := run(t, "report", "suite", "-root", w.root); code != 0 {
		t.Fatalf("suite: %d %s", code, stderr)
	}

	if r, ok, err := newestRound(t.Context(), w.store, w.cfg, w.change(tip)); err != nil || !ok || !r.ended {
		t.Fatalf("the round goes on after its suite ended: %+v %v %v", r, ok, err)
	}

	if got, err := report.RunState(t.Context(), w.store, w.cfg, id); err != nil || got != "success" {
		t.Fatalf("the run's state in the store: %q %v", got, err)
	}

	if att, err := lists.Attachments(t.Context(), w.store, tip); err != nil || len(att) != 1 || att[0].Run != "full" {
		t.Fatalf("attached: %+v %v", att, err)
	}
}

func TestAPullRequestWaitsForASuiteItTookOut(t *testing.T) { pullRequestWaitsForASuiteItTookOut(t) }

func TestAPullRequestWaitsForASuiteItTookOutOnABranch(t *testing.T) {
	onBranch(t, pullRequestWaitsForASuiteItTookOut)
}

func pullRequestWaitsForASuiteItTookOut(t *testing.T) {
	t.Helper()

	root, api, s := runEnv(t)
	own := withUnitSuite(t, readFile(t, filepath.Join(root, config.FileName)))

	trusted := loadConfigText(t, strings.Replace(own, "suites: {e2e: {}}", "suites: {e2e: {}, unit: {}}", 1))

	blob, err := json.Marshal(trusted.Trusted())
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(envTrusted, string(blob))
	writeFile(t, filepath.Join(root, config.FileName), own)

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	plan := `{"suites": [
		{"suite": "e2e", "selected": [{"label": "//e2e:a"}]},
		{"suite": "unit", "selected": [{"label": "//unit:x"}]}
	]}`
	if err := report.WritePlan(t.Context(), s, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}]`)

	for _, step := range []string{"start", "end"} {
		if step == "end" {
			writeFile(t, "result/e2e.a.json", `{"passed": true}`)
		}

		if _, stderr, code := run(t, "slice", step, "e2e.a", "-root", root); code != 0 {
			t.Fatalf("slice %s: %d %s", step, code, stderr)
		}
	}

	for _, step := range [][]string{{"report", "batch-end"}, {"report", "suite"}} {
		if stdout, stderr, code := run(t, append(step, "-root", root)...); code != 0 {
			t.Fatalf("%v: %d %s %s", step, code, stdout, stderr)
		}
	}

	if got := api.status("pipelinegen/pr"); !strings.HasPrefix(got, "pending 1 of 2 suites done") {
		t.Fatalf("the run's status: %q", got)
	}

	if got := api.status("pipelinegen/pr/unit"); !strings.HasPrefix(got, "pending ") {
		t.Fatalf("unit's status: %q", got)
	}
}

func TestARunsPlanNamesCausesOfItsSuitesAlone(t *testing.T) {
	cfg := &config.Config{Runs: map[string]config.Run{"pr": {Suites: map[string]config.RunSuite{"browser": {}}}}}
	cause := affected.Cause{Kind: affected.KindTarget, What: "//runner:reporter.js", Slices: 2}
	left := affected.Narrowed{Kind: affected.NarrowedBy, Narrowing: "unobservable-edits", OffAt: 4, Dropped: []string{"a.spec.ts"}}

	p := &affected.Plan{Suites: []affected.SuitePlan{
		{Suite: "browser", Causes: []affected.Cause{cause}, Narrowed: []affected.Narrowed{left}},
		{Suite: "go", Causes: []affected.Cause{cause}, Narrowed: []affected.Narrowed{left}},
	}}

	ofRun(p, cfg, "pr")

	if len(p.Suites) != 1 || p.Suites[0].Suite != "browser" || len(p.Suites[0].Causes) != 1 || len(p.Suites[0].Narrowed) != 1 {
		t.Errorf("suites %+v", p.Suites)
	}

	if got := summary(p); strings.Contains(got, "go") {
		t.Errorf("summary %q names a suite the run doesn't", got)
	}
}
