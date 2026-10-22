package report

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

type sliceObserver map[string]Observation

func (o sliceObserver) Observe(_ context.Context, _, slice, _ string, _ []string) ([]Observation, error) {
	return []Observation{o[slice]}, nil
}

func failingRound(t *testing.T) (*Reporter, *fakeGitHub, []string) {
	t.Helper()

	r, gh, commits := branchRun(t)
	r.Observer = sliceObserver{
		"a.a_test": {Plugin: "go", Touched: []string{"a#func:New"}, SetupLike: []string{"s#func:A"}},
		"b.b_test": {Plugin: "go", Touched: []string{"b#func:Half"}},
		"c.c_test": {Plugin: "go", Touched: []string{"c#func:New"}, SetupLike: []string{"s#func:C"}},
	}

	plan := `{"all": true, "suites": [{"suite": "go", "all": true, "selected": [{"label": "//a:a_test"}, {"label": "//b:b_test"}, {"label": "//c:c_test"}]}]}`
	if err := ReplacePlan(t.Context(), r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Store.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(commits[0]), Any: true, Files: lists.Encode(recording.Lists{
		"go/a.a_test": {"a#func:Old"}, "go/b.b_test": {"b#func:B"}, "go/c.c_test": {"c#func:C"}, "go/_setup": {"s#func:Old"},
	})}); err != nil {
		t.Fatal(err)
	}

	return r, gh, commits
}

func (r *Reporter) sliceEnds(t *testing.T, slice, label, result string) {
	t.Helper()

	if _, err := r.Slice(t.Context(), SliceEvent{Suite: "go", Slice: slice, Label: label, Result: writeResult(t, result), Coverage: []string{"c", "s"}}); err != nil {
		t.Fatal(err)
	}
}

func wantList(t *testing.T, got recording.Lists, key string, want ...recording.ID) {
	t.Helper()

	if !slices.Equal(got[key], want) {
		t.Errorf("%s: %v, want %v", key, got[key], want)
	}
}

func TestARoundThatFailedAttachesWhatPassed(t *testing.T) {
	ctx := t.Context()
	r, gh, commits := failingRound(t)

	r.sliceEnds(t, "a.a_test", "//a:a_test", `{"passed": true}`)
	r.sliceEnds(t, "b.b_test", "//b:b_test", `{"passed": false, "failures": [{"test": "TestB", "output": "boom"}]}`)

	if passed, err := r.Suite(ctx, "go"); err != nil || passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	if st, ok := gh.status(r.ID.Commit, "pipelinegen/nightly"); !ok || st.State != "error" {
		t.Fatalf("run status %+v", st)
	}

	got, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil || src.Back != 0 {
		t.Fatalf("the round's lists: %+v %v", src, err)
	}

	wantList(t, got, "go/a.a_test", "a#func:New")
	wantList(t, got, "go/b.b_test", "b#func:B", "b#func:Half")
	wantList(t, got, "go/c.c_test", "c#func:C")
	wantList(t, got, "go/_setup", "s#func:A", "s#func:Old")

	if src.Replaced["go/a.a_test"] != commits[1] || src.Replaced["go/b.b_test"] != "" || src.Replaced["go/_setup"] != "" {
		t.Fatalf("replaced %v", src.Replaced)
	}

	again := *r
	again.attachTried, again.attachErr = false, nil
	again.sliceEnds(t, "b.b_test", "//b:b_test", `{"passed": true}`)
	again.sliceEnds(t, "c.c_test", "//c:c_test", `{"passed": true}`)

	if passed, err := again.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("the rerun's suite: %v %v", passed, err)
	}

	if st, ok := gh.status(r.ID.Commit, "pipelinegen/nightly"); !ok || st.State != "success" {
		t.Fatalf("run status after the rerun %+v", st)
	}

	got, _, err = lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil {
		t.Fatal(err)
	}

	wantList(t, got, "go/a.a_test", "a#func:New")
	wantList(t, got, "go/b.b_test", "b#func:Half")
	wantList(t, got, "go/c.c_test", "c#func:New")
	wantList(t, got, "go/_setup", "s#func:A", "s#func:C")
}

func TestARoundThatFailedMarksOnlyWhatPassed(t *testing.T) {
	ctx := t.Context()
	r, _, commits := failingRound(t)
	r.Observer = sliceObserver{
		"a.a_test": {Plugin: "go", CantTell: true, Reason: "no coverage of the server"},
		"b.b_test": {Plugin: "go", CantTell: true, Reason: "the stack never started"},
	}

	r.sliceEnds(t, "a.a_test", "//a:a_test", `{"passed": true}`)
	r.sliceEnds(t, "b.b_test", "//b:b_test", `{"passed": false}`)

	if passed, err := r.Suite(ctx, "go"); err != nil || passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	got, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil || src.Back != 0 {
		t.Fatalf("the round's lists: %+v %v", src, err)
	}

	wantList(t, got, "go/a.a_test", "a#func:Old")

	if ms := src.CannotTell["go/a.a_test"]; len(ms) != 1 || ms[0].Reason != "no coverage of the server" {
		t.Fatalf("the mark of the slice that passed: %+v", src.CannotTell)
	}

	if ms := src.CannotTell["go/b.b_test"]; len(ms) != 0 {
		t.Fatalf("the slice that failed is marked in the lists: %+v", ms)
	}
}

func TestRunsThatGiveNothingAttachNothing(t *testing.T) {
	ctx := t.Context()

	for _, pr := range []bool{false, true} {
		r, _, commits := failingRound(t)
		if pr {
			r.Config.Runs["nightly"] = config.Run{Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}}, Record: true}
			r.sliceEnds(t, "a.a_test", "//a:a_test", `{"passed": true}`)
		}

		r.sliceEnds(t, "b.b_test", "//b:b_test", `{"passed": false}`)

		if passed, err := r.Suite(ctx, "go"); err != nil || passed {
			t.Fatalf("suite: %v %v", passed, err)
		}

		if _, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1]); err != nil || src.Back != 1 {
			t.Fatalf("pull request %v: lists attached: %+v %v", pr, src, err)
		}
	}
}

func TestARunEndedByHandAttachesWhatPassed(t *testing.T) {
	ctx := t.Context()
	r, _, commits := failingRound(t)
	r.sliceEnds(t, "a.a_test", "//a:a_test", `{"passed": true}`)

	ended := *r
	ended.Stopped = "the run was ended by hand"

	if passed, err := ended.Suite(ctx, "go"); err != nil || passed {
		t.Fatalf("ended: %v %v", passed, err)
	}

	got, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil || src.Back != 0 {
		t.Fatalf("lists: %+v %v", src, err)
	}

	wantList(t, got, "go/a.a_test", "a#func:New")
	wantList(t, got, "go/c.c_test", "c#func:C")

	wantList(t, got, "go/_setup", "s#func:A", "s#func:Old")

	r2, _, _ := failingRound(t)
	r2.Attach, r2.Stopped = nil, "the run was ended by hand"
	r2.sliceEnds(t, "a.a_test", "//a:a_test", `{"passed": true}`)

	if passed, err := r2.Suite(ctx, "go"); err != nil || passed {
		t.Fatalf("ended without an attacher: %v %v", passed, err)
	}
}

func TestTheSweepKeepsAFailedRoundsLists(t *testing.T) {
	ctx := t.Context()
	r, _, commits := failingRound(t)

	r.sliceEnds(t, "a.a_test", "//a:a_test", `{"passed": true}`)

	if _, err := r.Suite(ctx, "go"); err != nil {
		t.Fatal(err)
	}

	if _, err := Sweep(ctx, r.Store, time.Now().Add(100*24*time.Hour), Keep{Lists: KeepLists{Age: 24 * time.Hour}}, nil); err != nil {
		t.Fatal(err)
	}

	left, err := r.Store.List(ctx, refRoot+"lists/")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := left[lists.Ref(commits[0])]; ok {
		t.Errorf("the older lists stayed")
	}

	got, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil || src.Back != 0 {
		t.Fatalf("the round's lists after the sweep: %+v %v", src, err)
	}

	wantList(t, got, "go/b.b_test", "b#func:B")
	wantList(t, got, "go/c.c_test", "c#func:C")
}
