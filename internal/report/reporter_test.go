package report

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

const samplePlan = `{"base": "b", "head": "h", "all": false, "reason": "", "suites": [
  {"suite": "e2e", "all": false, "reason": "",
   "selected": [{"label": "//e2e:checkout", "sources": ["e2e/checkout.spec.ts"]}, {"label": "//e2e:orders", "sources": []}],
   "untouched": ["//e2e:search"], "whole_suite": ["money"],
   "reasons": {"//e2e:checkout": [
     {"kind": "test-changed", "what": "e2e/checkout.spec.ts", "through": []},
     {"kind": "depends", "what": "backend/orders#func:Create", "through": ["a#func:A", "b#func:B", "c#func:C", "d#func:D", "e#func:E", "f#func:F"]},
     {"kind": "executed", "what": "backend/orders#func:Total", "through": []},
     {"kind": "opens", "what": "apps/web/src/main.ts", "through": ["//apps/web:web"]}
   ]},
   "removed": ["//e2e:gone"]},
  {"suite": "lint", "all": false, "reason": "", "selected": [], "untouched": ["//:lint"], "whole_suite": []}
]}`

const commit = "0123456789abcdef0123456789abcdef01234567"

type fakeObserver struct {
	rec []byte
	err error
}

func (o fakeObserver) Observe(context.Context, string, string, string, []string) ([]Observation, error) {
	if o.err != nil {
		return nil, o.err
	}

	return []Observation{{Plugin: "go", Touched: strings.Fields(string(o.rec))}}, nil
}

var errTestCoverage = errors.New("no coverage")

func newReporter(t *testing.T, remote string, gh GitHub, format string) *Reporter {
	t.Helper()

	return &Reporter{
		Config: testConfig(format, true),
		ID:     ID{Run: "pr", Change: "pr-7", Commit: commit, PR: 7, AlsoTo: []int{8}},
		Store:  testStore(t, remote),
		GitHub: gh,
		Now:    func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
		// the tests read every update at once
		Throttle: -1,
	}
}

func wantStatus(t *testing.T, gh *fakeGitHub, ctx, state, desc string) {
	t.Helper()

	st, ok := gh.status(commit, ctx)
	if !ok || st.State != state || st.Description != desc {
		t.Fatalf("status %s: %+v, want %s %q", ctx, st, state, desc)
	}
}

func onlyBody(t *testing.T, gh *fakeGitHub, pr int) string {
	t.Helper()

	bodies := gh.bodies(pr)
	if len(bodies) != 1 {
		t.Fatalf("#%d has %d comments", pr, len(bodies))
	}

	return bodies[0]
}

func TestRunFromStartToFinish(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	gh := newFake()
	r := newReporter(t, remote, gh, "list")

	if err := r.Start(ctx); !errors.Is(err, ErrNoPlan) {
		t.Fatalf("start without a plan: %v", err)
	}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, gh, "pipelinegen/pr/e2e", "pending", "0 of 3 slices done, 1 untouched")
	wantStatus(t, gh, "pipelinegen/pr/lint", "success", "nothing selected, 1 untouched")
	wantStatus(t, gh, "pipelinegen/pr", "pending", "1 of 2 suites done")

	body := onlyBody(t, gh, 7)
	for _, want := range []string{
		Marker("pr", 7) + "\n",
		"- **pending** `e2e.checkout`\n  - test changed: `e2e/checkout.spec.ts`\n",
		"  - reaches `backend/orders#func:Create`, which changed, through `a#func:A` → `b#func:B` → `c#func:C` → (2 more) → `f#func:F`\n",
		"  - and 1 more\n",
		"- **pending** `whole.money`",
		"- removed: `e2e:gone`",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("start comment lacks %q:\n%s", want, body)
		}
	}

	if onlyBody(t, gh, 8) != body {
		t.Fatal("also_to got another body")
	}

	if err := WriteAlsoTo(ctx, testStore(t, remote), "pr", "pr-7", []int{9}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", JobURL: "https://ci/jobs/e2e", Running: true}); err != nil {
		t.Fatal(err)
	}

	if body := onlyBody(t, gh, 9); !strings.Contains(body, "- **running** [`e2e.checkout`](https://ci/jobs/e2e)") {
		t.Fatalf("running:\n%s", body)
	}

	state, err := r.Slice(ctx, SliceEvent{
		Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", JobURL: "https://ci/jobs/e2e",
		Result: writeResult(t, `{"passed": false, "failures": [{"test": "pays by card", "tail": "got 500"}]}`),
	})
	if err != nil || state != Failed {
		t.Fatalf("checkout: %s %v", state, err)
	}

	wantStatus(t, gh, "pipelinegen/pr/e2e", "pending", "1 of 3 slices done, 1 failed so far, 1 untouched")

	r.Observer = fakeObserver{rec: []byte("backend/orders#func:Total\n")}

	state, err = r.Slice(ctx, SliceEvent{
		Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", JobURL: "https://ci/jobs/e2e",
		Result: writeResult(t, `{"passed": true}`), Coverage: []string{t.TempDir()},
	})
	if err != nil || state != Passed {
		t.Fatalf("orders: %s %v", state, err)
	}

	recs, err := Recordings(ctx, testStore(t, remote), r.ID)
	if err != nil || len(recs) != 1 || string(recs["e2e/e2e.orders.txt"]) != "# observed\nbackend/orders#func:Total\n" {
		t.Fatalf("recordings %q %v", recs, err)
	}

	body = onlyBody(t, gh, 7)
	if !strings.Contains(body, "- **passed** `e2e.orders`") || !strings.Contains(body, "  - `pays by card`") {
		t.Fatalf("after two slices:\n%s", body)
	}

	passed, err := r.Suite(ctx, "e2e")
	if err != nil || passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	wantStatus(t, gh, "pipelinegen/pr/e2e", "error", "3 of 4 slices ran, 1 errored, 1 failed")
	wantStatus(t, gh, "pipelinegen/pr", "error", "1 of 2 suites errored, 0 failed")

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "- **errored** `whole.money`") || !strings.Contains(body, "(no result)") {
		t.Fatalf("after the suite:\n%s", body)
	}

	if passed, err := r.Finish(ctx); err != nil || passed {
		t.Fatalf("finish: %v %v", passed, err)
	}

	for _, pr := range []int{7, 8, 9} {
		if onlyBody(t, gh, pr) != onlyBody(t, gh, 7) {
			t.Fatalf("#%d differs", pr)
		}
	}
}

func TestAPassingRunPasses(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "mermaid")

	plan := `{"base": "b", "head": "h", "suites": [{"suite": "go", "selected": [{"label": "//a:a_test", "sources": []}], "untouched": [], "whole_suite": []}]}`
	if err := WritePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, gh, "pipelinegen/pr/go", "pending", "0 of 1 slices done")

	if state, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", JobURL: "u", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil || state != Errored {
		t.Fatalf("no learner: %s %v", state, err)
	}

	r.Observer = fakeObserver{rec: []byte("a#func:A\n")}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", JobURL: "u", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if passed, err := r.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	wantStatus(t, gh, "pipelinegen/pr", "success", "1 suite passed")

	if passed, err := r.Finish(ctx); err != nil || !passed {
		t.Fatalf("finish: %v %v", passed, err)
	}

	wantStatus(t, gh, "pipelinegen/pr/go", "success", "1 of 1 slices ran, all passed")

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "```mermaid") || strings.Contains(body, "](u)") {
		t.Fatalf("a passed slice keeps no link:\n%s", body)
	}

	if recs, err := Recordings(ctx, r.Store, r.ID); err != nil || len(recs) != 1 {
		t.Fatalf("after finish: %v %v", recs, err)
	}
}

func TestARerunAfterTheSuiteEndedShowsAsRunning(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	clock := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return clock }

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Running: true}); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)

	if passed, err := r.Suite(ctx, "e2e"); err != nil || passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "- **errored** `e2e.orders`") {
		t.Fatalf("lost slice:\n%s", body)
	}

	clock = clock.Add(time.Minute)

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", JobURL: "rerun", Running: true}); err != nil {
		t.Fatal(err)
	}

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "- **running** [`e2e.orders`](rerun)") {
		t.Fatalf("rerun:\n%s", body)
	}
}

func TestRecordingFailureErrorsTheSlice(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "")
	r.Observer = fakeObserver{err: errTestCoverage}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	state, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}})
	if err != nil || state != Errored {
		t.Fatalf("%s %v", state, err)
	}

	state, err = r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "whole.money", Label: "whole.money", Result: writeResult(t, `{"passed": true}`)})
	if err != nil || state != Passed {
		t.Fatalf("check: %s %v", state, err)
	}

	state, err = r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Result: t.TempDir() + "/none"})
	if err != nil || state != Errored {
		t.Fatalf("missing result: %s %v", state, err)
	}

	if len(gh.bodies(7)) != 0 {
		t.Fatal("a run without a comment made one")
	}
}

func TestLastWriterShowsTheNewestState(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	gh := newFake()
	gh.jitter = 15 * time.Millisecond

	const n = 10

	var sel []string
	for i := range n {
		sel = append(sel, fmt.Sprintf(`{"label": "//e2e:s%02d", "sources": []}`, i))
	}

	plan := `{"suites": [{"suite": "e2e", "selected": [` + strings.Join(sel, ",") + `], "untouched": [], "whole_suite": []}]}`

	first := newReporter(t, remote, gh, "list")
	if err := WritePlan(ctx, first.Store, first.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup

	for i := range n {
		wg.Go(func() {
			r := newReporter(t, remote, gh, "list")
			r.Observer = fakeObserver{rec: []byte("x\n")}
			ev := SliceEvent{Suite: "e2e", Slice: fmt.Sprintf("e2e.s%02d", i), Label: fmt.Sprintf("//e2e:s%02d", i), JobURL: "u", Running: true}

			if _, err := r.Slice(ctx, ev); err != nil {
				t.Error(err)
			}

			ev.Running, ev.Coverage = false, []string{"c"}
			ev.Result = writeResult(t, map[bool]string{true: `{"passed": true}`, false: `{"passed": false, "failures": [{"test": "t"}]}`}[i%3 != 0])

			if _, err := r.Slice(ctx, ev); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	r := newReporter(t, remote, gh, "list")

	snap, err := readSnapshot(ctx, r.Store, r.Config, r.ID)
	if err != nil {
		t.Fatal(err)
	}

	run := buildRun(r.ID, snap, nil)
	run.Reasoning = true

	if got, want := onlyBody(t, gh, 7), Comment(run, 7, List); got != want {
		t.Fatalf("comment is stale:\n%s\nwant:\n%s", got, want)
	}

	state, desc := run.Suites[0].Status()
	wantStatus(t, gh, "pipelinegen/pr/e2e", state, desc)

	if state != "failure" || desc != "10 of 10 slices ran, 4 failed (1 test)" {
		t.Fatalf("final: %s %q", state, desc)
	}
}

func TestCommentIsFoundByItsMarkerAndKeptSingle(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	gh.comments[7] = []IssueComment{
		{ID: 1, Body: "a person's comment quoting " + Marker("pr", 7)},
		{ID: 2, Body: Marker("pr", 7) + "\nold"},
		{ID: 3, Body: Marker("pr", 7) + "\nduplicate"},
		{ID: 4, Body: Marker("main", 7) + "\nanother run's"},
	}

	r := newReporter(t, bareRemote(t), gh, "list")
	r.ID.AlsoTo = nil

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	got := gh.bodies(7)
	if len(got) != 3 || !strings.HasPrefix(got[0], "a person's") || !strings.Contains(got[1], "e2e.checkout") || !strings.HasSuffix(got[2], "another run's") {
		t.Fatalf("%q", got)
	}
}

func TestGitHubTroubleFailsOnlyTheEnds(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")

	var warn strings.Builder

	r.Warn = &warn

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	gh.fail = errFake

	if state, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Running: true}); err != nil || state != Running {
		t.Fatalf("a slice failed on GitHub's trouble: %v", err)
	}

	if !strings.Contains(warn.String(), "github is down") {
		t.Fatalf("no warning: %q", warn.String())
	}

	if _, err := r.Suite(ctx, "e2e"); !errors.Is(err, errFake) {
		t.Fatalf("suite: %v", err)
	}
}

func TestNamesThatCantBePaths(t *testing.T) {
	r := &Reporter{ID: ID{Run: "pr", Change: "x\ny", Commit: commit}, Store: &gitstore.Store{}}
	if _, err := r.Slice(t.Context(), SliceEvent{Suite: "e2e", Slice: "a"}); !errors.Is(err, ErrID) {
		t.Fatal(err)
	}

	r.ID.Change = "release/1.2"
	for _, bad := range []string{"a//b", "/a", "a/", ""} {
		if _, err := r.Slice(t.Context(), SliceEvent{Suite: "e2e", Slice: bad}); !errors.Is(err, ErrID) {
			t.Fatalf("%q: %v", bad, err)
		}
	}

	if SliceName("//e2e/console:search") != "e2e.console.search" {
		t.Fatal(SliceName("//e2e/console:search"))
	}
}

func TestABatchReportsInOneWrite(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Observer = fakeObserver{rec: []byte("x#func:X\n")}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	batch := []SliceEvent{
		{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", JobURL: "u", Running: true},
		{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", JobURL: "u", Running: true},
	}

	if states, err := r.Slices(ctx, batch); err != nil || states["e2e/e2e.orders"] != Running {
		t.Fatalf("start: %v %v", states, err)
	}

	if body := onlyBody(t, gh, 7); strings.Count(body, "- **running**") != 2 {
		t.Fatalf("batch start:\n%s", body)
	}

	results := t.TempDir()

	batch[0].Running, batch[0].Result, batch[0].Coverage = false, writeResult(t, `{"passed": true}`), []string{"c"}
	batch[1].Running, batch[1].Result, batch[1].Coverage = false, results+"/e2e.orders.json", []string{"c"}

	states, err := r.Slices(ctx, batch)
	if err != nil || states["e2e/e2e.checkout"] != Passed || states["e2e/e2e.orders"] != Errored {
		t.Fatalf("end: %v %v", states, err)
	}

	recs, err := Recordings(ctx, r.Store, r.ID)
	if err != nil || len(recs) != 2 {
		t.Fatalf("recordings: %v %v", recs, err)
	}

	wantStatus(t, gh, "pipelinegen/pr/e2e", "pending", "2 of 3 slices done, 1 failed so far, 1 untouched")
}

func TestBatchEndClosesTheRest(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Observer = fakeObserver{rec: []byte("x#func:X\n")}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	end := SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Result: writeResult(t, `{"passed": true}`),
		Coverage: []string{"cov/e2e.checkout", "cov/_setup"}}

	if state, err := r.Slice(ctx, end); err != nil || state != Passed {
		t.Fatalf("end: %s %v", state, err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "whole.money", Label: "whole.money", Running: true}); err != nil {
		t.Fatal(err)
	}

	states, err := r.Close(ctx, []SliceEvent{
		{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Result: "missing"},
		{Suite: "e2e", Slice: "whole.money", Label: "whole.money", Result: writeResult(t, `{"passed": false, "failures": [{"test": "sums"}]}`)},
	})
	if err != nil || states["e2e/e2e.checkout"] != Passed || states["e2e/whole.money"] != Failed {
		t.Fatalf("close: %v %v", states, err)
	}
}

func TestStatesForARerun(t *testing.T) {
	ctx := t.Context()
	r := newReporter(t, bareRemote(t), newFake(), "")

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	r.Observer = fakeObserver{rec: []byte("x\n")}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Running: true}); err != nil {
		t.Fatal(err)
	}

	got, err := States(ctx, r.Store, r.ID, "e2e")
	if err != nil || got["e2e.orders"] != Passed || got["e2e.checkout"] != Running || got["whole.money"] != Pending || len(got) != 3 {
		t.Fatalf("before the end: %v %v", got, err)
	}

	r.Now = func() time.Time { return time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC) }

	if _, err := r.Suite(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}

	got, err = States(ctx, r.Store, r.ID, "e2e")
	if err != nil || got["e2e.orders"] != Passed || got["e2e.checkout"] != Errored || got["whole.money"] != Errored {
		t.Fatalf("after the end: %v %v", got, err)
	}
}

func TestFileSlicesOfABranchRun(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "")
	r.ID = ID{Run: "pr", Change: "release/1.2", Commit: commit}
	r.Observer = fakeObserver{rec: []byte("x\n")}

	plan := `{"suites": [{"suite": "e2e", "selected": [
		{"name": "e2e/console/search.spec.ts"}, {"name": "e2e/.env.spec.ts"}, {"name": "e2e/a b.lock"}, {"label": "//lint:go"}
	], "untouched": [], "whole_suite": []}]}`
	if err := WritePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	var evs []SliceEvent
	for _, name := range []string{"e2e/console/search.spec.ts", "e2e/.env.spec.ts", "e2e/a b.lock", "lint.go"} {
		evs = append(evs, SliceEvent{Suite: "e2e", Slice: name, Label: name, Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}})
	}

	if _, err := r.Slices(ctx, evs); err != nil {
		t.Fatal(err)
	}

	got, err := States(ctx, r.Store, r.ID, "e2e")
	if err != nil || len(got) != 4 || got["e2e/a b.lock"] != Passed || got["lint.go"] != Passed || got["e2e/.env.spec.ts"] != Passed {
		t.Fatalf("states: %v %v", got, err)
	}

	recs, err := Recordings(ctx, r.Store, r.ID)
	if err != nil || len(recs) != 4 || recs["e2e/e2e/console/search.spec.ts.txt"] == nil {
		t.Fatalf("recordings: %v %v", recs, err)
	}

	wantStatus(t, gh, "pipelinegen/pr/e2e", "success", "4 of 4 slices ran, all passed")
}

func TestBatchEndNeverPassesASliceWithoutItsRecording(t *testing.T) {
	ctx := t.Context()
	r := newReporter(t, bareRemote(t), newFake(), "")
	r.Observer = fakeObserver{rec: []byte("x\n")}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Running: true}); err != nil {
		t.Fatal(err)
	}

	states, err := r.Close(ctx, []SliceEvent{
		{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Result: writeResult(t, `{"passed": true}`)},
		{Suite: "e2e", Slice: "whole.money", Label: "whole.money", Result: writeResult(t, `{"passed": true}`)},
	})
	if err != nil || states["e2e/e2e.orders"] != Errored || states["e2e/whole.money"] != Passed {
		t.Fatalf("close: %v %v", states, err)
	}

	recs, err := Recordings(ctx, r.Store, r.ID)
	if err != nil || len(recs) != 0 {
		t.Fatalf("recordings: %v %v", recs, err)
	}
}

func TestSliceReportsSpareGitHub(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Throttle = time.Hour
	r.Observer = fakeObserver{rec: []byte("x\n")}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	before := gh.posts

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Running: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.orders", Label: "//e2e:orders", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if gh.posts != before {
		t.Fatalf("%d writes within the throttle", gh.posts-before)
	}

	if _, err := r.Suite(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "- **passed** `e2e.orders`") {
		t.Fatalf("suite end:\n%s", body)
	}

	wantStatus(t, gh, "pipelinegen/pr/e2e", "error", "3 of 4 slices ran, 2 errored")

	after := gh.posts

	if _, err := r.Finish(ctx); err != nil {
		t.Fatal(err)
	}

	if gh.posts != after {
		t.Fatalf("finish wrote %d unchanged things", gh.posts-after)
	}
}

func TestExtrasAreKeptAndCollected(t *testing.T) {
	ctx := t.Context()
	r := newReporter(t, bareRemote(t), newFake(), "")
	r.Observer = fakeObserver{rec: []byte("x#func:X\n")}

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	extras := t.TempDir()
	writeFileT(t, filepath.Join(extras, "report.json"), `{"sum": 3}`)
	writeFileT(t, filepath.Join(extras, "deep", "trace.txt"), "t")

	if state, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e/checkout.spec.ts", Label: "e2e/checkout.spec.ts",
		Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}, Extras: extras}); err != nil || state != Passed {
		t.Fatalf("%s %v", state, err)
	}

	big := t.TempDir()
	writeFileT(t, filepath.Join(big, "huge.bin"), strings.Repeat("x", maxExtras+1))

	if state, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e/orders.spec.ts", Label: "e2e/orders.spec.ts",
		Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}, Extras: big}); err != nil || state != Errored {
		t.Fatalf("over the cap: %s %v", state, err)
	}

	out := t.TempDir()

	n, err := Collect(ctx, r.Store, r.ID, "e2e", out)
	if err != nil || n != 2 {
		t.Fatalf("collect: %d %v", n, err)
	}

	for p, want := range map[string]string{
		"e2e/checkout.spec.ts/extra/report.json":    `{"sum": 3}`,
		"e2e/checkout.spec.ts/extra/deep/trace.txt": "t",
		"e2e/checkout.spec.ts/recording.txt":        "# observed\nx#func:X\n",
	} {
		got, err := os.ReadFile(filepath.Join(out, p))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", p, got, err)
		}
	}

	state, err := os.ReadFile(filepath.Join(out, "e2e/orders.spec.ts/state.json"))
	if err != nil || !strings.Contains(string(state), "the most a slice keeps") {
		t.Fatalf("over the cap: %s %v", state, err)
	}
}

func writeFileT(t *testing.T, p, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestASecretATestPrintedNeverLeaves(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Observer = fakeObserver{rec: []byte("x#func:X\n")}

	secret.Add("ghs_secretvalue")

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	extras := t.TempDir()
	writeFileT(t, filepath.Join(extras, "env.txt"), "GITHUB_TOKEN=ghs_secretvalue\n")

	if _, err := r.Slice(ctx, SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Coverage: []string{"c"}, Extras: extras,
		Result: writeResult(t, `{"passed": false, "failures": [{"test": "uses ghs_secretvalue", "tail": "token ghs_secretvalue refused"}]}`)}); err != nil {
		t.Fatal(err)
	}

	refs, err := r.Store.List(ctx, r.ID.Prefix())
	if err != nil {
		t.Fatal(err)
	}

	for name, ref := range refs {
		files, err := r.Store.Files(ctx, ref.Commit)
		if err != nil {
			t.Fatal(err)
		}

		for p, data := range files {
			if strings.Contains(string(data), "ghs_secretvalue") {
				t.Errorf("%s %s holds the secret:\n%s", name, p, data)
			}
		}
	}

	if body := onlyBody(t, gh, 7); strings.Contains(body, "ghs_secretvalue") || !strings.Contains(body, "token *** refused") {
		t.Fatalf("comment:\n%s", body)
	}

	long := "ghs_secretvalue" + strings.Repeat("x", 1495)
	if _, failures := readResult(writeResult(t, `{"passed": false, "failures": [{"test": "`+strings.Repeat("n", 5000)+`", "tail": "`+long+`"}]}`),
		secret.Redact); strings.Contains(failures[0].Tail, "value") || len(failures[0].Test) > maxTestName+3 {
		t.Fatalf("cut: %q, name of %d bytes", failures[0].Tail[len(failures[0].Tail)-20:], len(failures[0].Test))
	}
}
