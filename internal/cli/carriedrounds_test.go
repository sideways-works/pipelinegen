package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const (
	checkoutSlice = "e2e.checkout"
	ordersSlice   = "e2e.orders"
	moneySlice    = "backendtests.money_test"
)

// prRounds is a pull request onto main whose run tests e2e's checkout (//web, //api/money) and orders
// (//web, //api/orders), and backend's money_test (//api/money); its branch is feature.
type prRounds struct {
	roundsRepo
}

func newPRRounds(t *testing.T, level int) *prRounds {
	t.Helper()
	testrepo.NeedBazel(t)

	w := &prRounds{roundsRepo{r: testrepo.New(t), remote: storetest.Bare(t), cache: t.TempDir(), bazelDiff: bazeldifftest.Tool(t).Path}}

	conf := readFile(t, filepath.Join(w.r.Dir, config.FileName))
	whole, specs := strings.Index(conf, "    whole_suite:"), strings.Index(conf, "  specs:")

	if whole < 0 || specs < whole {
		t.Fatalf("the fixture's config changed:\n%s", conf)
	}

	// no check of the whole suite: it would need every slice to run
	conf = conf[:whole] + conf[specs:]
	start, end := strings.Index(conf, "github:"), strings.Index(conf, "\nruns:")

	conf = conf[:start] + "git: {remote: \"" + w.remote + "\"}\n" + conf[strings.Index(conf, "concourse:"):end] + `
runs:
  pr:
    trigger: {pull_requests: {base: main}}
    compare: merged_target
    select: affected
    suites: {e2e: {}, backend: {}}
everything_on: [pipelinegen.yaml]
untested:
  - {paths: ["**"], reason: the fixture's other files}
`
	if level > 0 {
		conf += fmt.Sprintf("narrowing: %d\n", level)
	}

	w.r.Write(config.FileName, conf)
	dependOnWhatTheyTest(w.r)
	w.commit(t, "main runs the pull requests' e2e and backend")

	for k, v := range map[string]string{envGitURL: "", envAPIURL: "", envToken: "", envJobURL: "", envPR: "", envAlsoTo: ""} {
		t.Setenv(k, v)
	}

	testrepo.Bazel(t, filepath.Join(w.cache, "tree"))

	return w
}

// edit appends a line to each file and commits on the branch checked out.
func (w *prRounds) edit(t *testing.T, msg string, paths ...string) string {
	t.Helper()

	for _, p := range paths {
		w.r.Write(p, readFile(t, filepath.Join(w.r.Dir, p))+"\n// "+msg+"\n")
	}

	return w.r.Commit(msg)
}

// push makes the branch feature's tip the pull request's head on the remote, rewriting it if need be.
func (w *prRounds) push(t *testing.T, head string) string {
	t.Helper()
	w.r.Git("push", "-q", "--force", w.remote, head+":refs/heads/feature")

	return head
}

func prChange(head string) runs.Change {
	return runs.Change{Run: "pr", ID: "pr-7", Commit: head, PR: 7, Branch: "feature", Base: "main"}
}

// round plans the pull request at head as its two steps do, the second step's plan stored.
func (w *prRounds) round(t *testing.T, head string) *affected.Plan {
	t.Helper()

	trigger := w.trigger(t, head)
	cfg := loadConfig(t, trigger)
	ch := prChange(head)
	store := planStore(cfg, ch, w.cache)

	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // a scratch repository

	plan, err := planChange(t.Context(), cfg, ch, store, trigger, w.cache, w.bazelDiff)
	if err != nil {
		t.Fatalf("plan %s: %v", head[:12], err)
	}

	first, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}

	own, err := ownPlan(t.Context(), cfg, ch, store, trigger, first)
	if err != nil {
		t.Fatalf("the second step of %s: %v", head[:12], err)
	}

	data, err := json.Marshal(own)
	if err != nil {
		t.Fatal(err)
	}

	if err := report.WritePlan(t.Context(), store, reportID(ch), data); err != nil {
		t.Fatal(err)
	}

	return own
}

// results reports each slice of the round at head as outcomes says: passed or failed.
func (w *prRounds) results(t *testing.T, head string, outcomes map[string]string) {
	t.Helper()

	cfg := loadConfig(t, w.r.Dir)
	store := openStore(cfg)

	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // a scratch repository

	r := &report.Reporter{Config: cfg, ID: reportID(prChange(head)), Store: store, Warn: io.Discard, GitHub: host(cfg)}

	var evs []report.SliceEvent

	for name, outcome := range outcomes {
		suite := "e2e"
		if name == moneySlice {
			suite = "backend"
		}

		result := filepath.Join(t.TempDir(), "result.json")
		writeFile(t, result, fmt.Sprintf(`{"passed": %v}`, outcome == "passed"))

		evs = append(evs, report.SliceEvent{Suite: suite, Slice: name, Label: "//" + strings.Replace(name, ".", ":", 1), Result: result})
	}

	if _, err := r.Slices(t.Context(), evs); err != nil {
		t.Fatal(err)
	}
}

// carriedOf is what the plan runs and what it carried over, by slice name, and the build it names.
func carriedOf(p *affected.Plan) (ran, carried []string, from string) { //nolint:nonamedreturns // three answers
	ran, carried = []string{}, []string{}

	for _, sp := range p.Suites {
		for _, sl := range sp.Selected {
			ran = append(ran, sl.Name)
		}

		for _, n := range sp.Narrowed {
			if n.CarriedFrom != "" {
				carried = append(carried, n.Dropped...)
				from = n.CarriedFrom
			}
		}
	}

	slices.Sort(ran)
	slices.Sort(carried)

	return ran, carried, from
}

func wantRound(t *testing.T, round string, p *affected.Plan, ran, carried []string, from string) {
	t.Helper()

	gotRan, gotCarried, gotFrom := carriedOf(p)
	if !slices.Equal(gotRan, ran) || !slices.Equal(gotCarried, carried) || gotFrom != from {
		t.Fatalf("%s: runs %v and carries over %v from %.12s; want %v and %v from %.12s\n%q", round, gotRan, gotCarried, gotFrom, ran, carried, from, p.Warnings)
	}
}

func TestAPullRequestRoundRunsOnlyWhatChangedSinceItsLastBuild(t *testing.T) {
	w := newPRRounds(t, 0)
	main0 := revParse(t, w.r.Dir, "HEAD")

	w.r.Git("checkout", "-q", "-b", "feature")

	// round 1: the change reaches all three; orders fails
	h1 := w.push(t, w.edit(t, "web and money's test", "web/app.ts", "backendtests/money_test.go"))
	wantRound(t, "round 1", w.round(t, h1), []string{moneySlice, checkoutSlice, ordersSlice}, []string{}, "")
	w.results(t, h1, map[string]string{checkoutSlice: "passed", ordersSlice: "failed", moneySlice: "passed"})

	// round 2: one spec file changes; its slice and the failed one run, money_test is carried over
	h2 := w.push(t, w.edit(t, "checkout's spec", "e2e/checkout.spec.ts"))
	p2 := w.round(t, h2)
	wantRound(t, "round 2", p2, []string{checkoutSlice, ordersSlice}, []string{moneySlice}, h1)

	if p2.Merged != h2 || p2.Base != main0 {
		t.Fatalf("round 2 records %s merged into %s", p2.Merged, p2.Base)
	}

	w.results(t, h2, map[string]string{checkoutSlice: "passed", ordersSlice: "passed"})

	// round 3: nothing any slice binds changes; all three are carried, money_test a second time
	h3 := w.push(t, w.edit(t, "the readme", "docs/readme.md"))
	wantRound(t, "round 3", w.round(t, h3), []string{}, []string{moneySlice, checkoutSlice, ordersSlice}, h2)

	// main moves under money, which checkout and money_test bind: the change from round 3's tree
	// reaches them, money_test carried twice runs again, and orders stays carried
	w.r.Git("checkout", "-q", "main")
	w.r.Write("api/money/money.go", readFile(t, filepath.Join(w.r.Dir, "api/money/money.go"))+"\n// money moves on main\n")
	main1 := w.commit(t, "money moves on main")

	w.r.Git("checkout", "-q", "feature")
	h4 := w.push(t, w.edit(t, "the readme again", "docs/readme.md"))
	p4 := w.round(t, h4)
	wantRound(t, "round 4", p4, []string{moneySlice, checkoutSlice}, []string{ordersSlice}, h3)

	if p4.Base != main1 {
		t.Fatalf("round 4 merged into %s, want main's tip %s", p4.Base, main1)
	}

	w.results(t, h4, map[string]string{checkoutSlice: "passed", moneySlice: "passed"})

	// the run's status and why name the build and the count
	cfg := loadConfig(t, w.r.Dir)
	store := openStore(cfg)

	t.Cleanup(func() { _ = store.Close() }) //nolint:errcheck // a scratch repository

	if _, desc, err := report.RunStatusOf(t.Context(), store, cfg, reportID(prChange(h4))); err != nil || !strings.Contains(desc, "1 carried over from "+h3[:12]) {
		t.Fatalf("round 4's status %q: %v", desc, err)
	}

	stdout, stderr, code := runIn(t, w.r.Dir, nil, "why", "-root", w.r.Dir, "-run", "pr", "-change", "pr-7", "-commit", h4, "-slice", ordersSlice)
	if code != 0 || !strings.Contains(stdout, "the narrowing passed-since left it out: carried over from "+h3[:12]+", the last build of pr-7, where it passed") ||
		!strings.Contains(stdout, "1 slice of e2e carried over from "+h3[:12]) {
		t.Fatalf("why: %d\n%s\n%s", code, stdout, stderr)
	}

	// a force-push rewrites the history onto main's tip: the trees are compared, not the commits, so
	// only orders, whose spec the rewrite changed, runs
	w.r.Git("checkout", "-q", "-B", "rewritten", main1)
	w.r.Git("checkout", h4, "--", "web", "backendtests", "e2e", "docs")
	h5 := w.push(t, w.edit(t, "one commit, orders' spec changed", "e2e/orders.spec.ts"))

	if out, err := exec.CommandContext(t.Context(), "git", "-C", w.r.Dir, "merge-base", "--is-ancestor", h4, h5).CombinedOutput(); err == nil {
		t.Fatalf("the rewrite kept round 4's head: %s", out)
	}

	wantRound(t, "round 5", w.round(t, h5), []string{ordersSlice}, []string{moneySlice, checkoutSlice}, h4)
	w.results(t, h5, map[string]string{ordersSlice: "passed"})

	// rewritten again, with round 5's head gone from the remote: its tree can't be made again, so
	// nothing is carried over and the plan says why
	w.r.Git("commit", "-q", "--amend", "-m", "one commit, reworded")
	h6 := w.push(t, revParse(t, w.r.Dir, "HEAD"))

	bare := strings.TrimPrefix(w.remote, "file://")
	if out, err := exec.CommandContext(t.Context(), "git", "-C", bare, "gc", "-q", "--prune=now").CombinedOutput(); err != nil {
		t.Fatalf("gc: %v: %s", err, out)
	}

	p6 := w.round(t, h6)
	wantRound(t, "round 6", p6, []string{moneySlice, checkoutSlice, ordersSlice}, []string{}, "")

	if !slices.ContainsFunc(p6.Warnings, func(s string) bool {
		return strings.HasPrefix(s, "nothing is carried over from "+h5[:12]+", the last build of pr-7") && strings.Contains(s, "can't be fetched")
	}) {
		t.Fatalf("round 6's warnings: %q", p6.Warnings)
	}
}

func TestFromLevel4APullRequestRoundCarriesNothingOver(t *testing.T) {
	w := newPRRounds(t, 4)

	w.r.Git("checkout", "-q", "-b", "feature")

	h1 := w.push(t, w.edit(t, "web and money's test", "web/app.ts", "backendtests/money_test.go"))
	wantRound(t, "round 1", w.round(t, h1), []string{moneySlice, checkoutSlice, ordersSlice}, []string{}, "")
	w.results(t, h1, map[string]string{checkoutSlice: "passed", ordersSlice: "failed", moneySlice: "passed"})

	h2 := w.push(t, w.edit(t, "checkout's spec", "e2e/checkout.spec.ts"))
	p := w.round(t, h2)
	wantRound(t, "round 2", p, []string{moneySlice, checkoutSlice, ordersSlice}, []string{}, "")

	// the plan never read the last build: it has nothing to say of it
	if slices.ContainsFunc(p.Warnings, func(s string) bool { return strings.Contains(s, "carried over") }) {
		t.Fatalf("round 2 warns %q", p.Warnings)
	}
}
