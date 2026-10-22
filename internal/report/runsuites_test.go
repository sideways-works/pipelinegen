package report

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

const everySuitePlan = `{"suites": [
	{"suite": "go", "selected": [{"label": "//a:a_test"}], "untouched": ["//b:b_test"]},
	{"suite": "e2e", "selected": [{"label": "//e:one"}], "untouched": ["//e:two"]}
]}`

func coveringGo(r *Reporter) *int {
	run := r.Config.Runs[r.ID.Run]
	run.Suites = map[string]config.RunSuite{"go": {}}
	r.Config.Runs[r.ID.Run] = run

	told := 0
	r.RunEnded = func(context.Context) error {
		told++

		return nil
	}

	return &told
}

func TestAPlanOfEverySuiteCountsTheRunsOwn(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Observer = fakeObserver{rec: []byte("a#func:A\n")}
	told := coveringGo(r)

	if err := WritePlan(ctx, r.Store, r.ID, []byte(everySuitePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, gh, "pipelinegen/pr/go", "pending", "0 of 1 slices done, 1 untouched")

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if passed, err := r.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	wantStatus(t, gh, "pipelinegen/pr", "success", "1 suite passed")

	if *told != 1 {
		t.Fatalf("the run's end was told %d times", *told)
	}

	if passed, err := r.Finish(ctx); err != nil || !passed {
		t.Fatalf("finish: %v %v", passed, err)
	}

	if st, ok := gh.status(commit, "pipelinegen/pr/e2e"); ok {
		t.Fatalf("a suite the run doesn't cover got a status: %+v", st)
	}

	if body := onlyBody(t, gh, 7); strings.Contains(body, "e2e") {
		t.Fatalf("the comment shows a suite the run doesn't cover:\n%s", body)
	}

	if got, err := RunState(ctx, r.Store, r.Config, r.ID); err != nil || got != statusSuccess {
		t.Fatalf("the run's state in the store: %q %v", got, err)
	}

	refs, err := r.Store.List(ctx, refRoot+"plans/", refRoot+"runs/"+r.ID.Run+"/")
	if err != nil {
		t.Fatal(err)
	}

	plans, err := readPlans(ctx, r.Store, r.Config, refs)
	if err != nil {
		t.Fatal(err)
	}

	got, err := passedRuns(ctx, r.Store, r.ID, refs, plans)
	if err != nil || len(got) != 1 || slices.Contains(mapValues(got), false) {
		t.Fatalf("earlier runs that passed: %v %v", got, err)
	}
}

func mapValues[K comparable, V any](m map[K]V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}

	return out
}

func TestARunWhoseSuitesNothingReachesSucceeds(t *testing.T) {
	ctx := t.Context()
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	told := coveringGo(r)

	plan := `{"suites": [
		{"suite": "go", "selected": [], "untouched": ["//a:a_test"]},
		{"suite": "e2e", "selected": [{"label": "//e:one"}]}
	]}`
	if err := WritePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	wantStatus(t, gh, "pipelinegen/pr/go", "success", "nothing selected, 1 untouched")
	wantStatus(t, gh, "pipelinegen/pr", "success", "1 suite passed")

	if passed, err := r.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	if *told != 1 {
		t.Fatalf("the run's end was told %d times", *told)
	}

	if _, ok := gh.status(commit, "pipelinegen/pr/e2e"); ok {
		t.Fatal("a suite the run doesn't cover got a status")
	}
}

func TestABranchRunOfSomeSuitesAttachesItsLists(t *testing.T) {
	ctx := t.Context()
	r, _, commits := branchRun(t)
	told := coveringGo(r)

	plan := `{"all": true, "suites": [
		{"suite": "go", "all": true, "selected": [{"label": "//a:a_test"}]},
		{"suite": "e2e", "all": true, "selected": [{"label": "//e:one"}]}
	]}`
	if err := ReplacePlan(ctx, r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Store.Write(ctx, "lists", gitstore.Update{Ref: lists.Ref(commits[0]), Any: true, Files: lists.Encode(recording.Lists{
		"go/a.a_test": {"a#func:Old"}, "e2e/one": {"e#func:E"},
	})}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if passed, err := r.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	if *told != 1 {
		t.Fatalf("the run's end was told %d times", *told)
	}

	got, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil || src.Back != 0 {
		t.Fatalf("no lists attached to the run's commit: %v %+v", err, src)
	}

	if !slices.Equal(got["go/a.a_test"], []recording.ID{"a#func:New"}) || !slices.Equal(got["e2e/one"], []recording.ID{"e#func:E"}) {
		t.Fatalf("lists %v", got)
	}
}
