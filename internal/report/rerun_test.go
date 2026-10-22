package report

import (
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func ended(t *testing.T, slice, result string) SliceEvent {
	t.Helper()

	return SliceEvent{Suite: "e2e", Slice: "e2e." + slice, Label: "//e2e:" + slice, Result: writeResult(t, result), Coverage: []string{t.TempDir()}}
}

func whole(t *testing.T) SliceEvent {
	t.Helper()

	return SliceEvent{Suite: "e2e", Slice: "whole.money", Label: "whole.money", Result: writeResult(t, `{"passed": true}`)}
}

func rerunReporter(t *testing.T, remote string, gh GitHub) *Reporter {
	t.Helper()

	r := newReporter(t, remote, gh, "list")
	r.Observer = fakeObserver{rec: []byte("backend/orders#func:Total\n")}
	r.Store.Retries = config.DefaultRetries

	return r
}

func build(t *testing.T, r *Reporter, checkout, orders string) bool {
	t.Helper()

	ctx := t.Context()

	for _, ev := range []SliceEvent{
		{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Running: true},
		ended(t, "checkout", checkout),
		ended(t, "orders", orders),
		whole(t),
	} {
		if _, err := r.Slice(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	passed, err := r.Suite(ctx, "e2e")
	if err != nil {
		t.Fatal(err)
	}

	return passed
}

func TestTheSamePlanBuiltTwiceIsARerun(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	gh := newFake()
	r := rerunReporter(t, remote, gh)

	if err := WritePlan(ctx, r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}

	if !build(t, r, `{"passed": true}`, `{"passed": true}`) {
		t.Fatal("the first build failed")
	}

	if st, _ := gh.status(commit, "pipelinegen/pr/e2e"); st.State != "success" {
		t.Fatalf("suite status after the first build %+v", st)
	}

	r.Now = func() time.Time { return time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC) }

	if build(t, r, `{"passed": false}`, `{"passed": true}`) {
		t.Fatal("the second build passed")
	}

	got, err := States(ctx, r.Store, r.ID, "e2e")
	if err != nil || len(got) != 3 || got["e2e.checkout"] != Failed || got["e2e.orders"] != Passed || got["whole.money"] != Passed {
		t.Fatalf("states after the second build: %v %v", got, err)
	}

	if st, _ := gh.status(commit, "pipelinegen/pr/e2e"); st.State != "failure" {
		t.Fatalf("suite status %+v", st)
	}

	onlyBody(t, gh, 7)
}

func TestTwoBuildsOfOnePlanSideBySide(t *testing.T) {
	ctx := t.Context()
	remote := bareRemote(t)
	gh := newFake()
	a, b := rerunReporter(t, remote, gh), rerunReporter(t, remote, gh)

	if err := WritePlan(ctx, a.Store, a.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		r  *Reporter
		ev SliceEvent
	}{
		{a, SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Running: true}},
		{b, SliceEvent{Suite: "e2e", Slice: "e2e.checkout", Label: "//e2e:checkout", Running: true}},
		{a, ended(t, "checkout", `{"passed": true}`)},
		{b, ended(t, "checkout", `{"passed": false}`)},
		{b, ended(t, "orders", `{"passed": true}`)},
		{a, ended(t, "orders", `{"passed": true}`)},
		{a, whole(t)},
		{b, whole(t)},
	}

	for i, s := range steps {
		if _, err := s.r.Slice(ctx, s.ev); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}

	for _, r := range []*Reporter{a, b} {
		if passed, err := r.Suite(ctx, "e2e"); err != nil || passed {
			t.Fatalf("suite: %v %v", passed, err)
		}
	}

	got, err := States(ctx, a.Store, a.ID, "e2e")
	if err != nil || len(got) != 3 || got["e2e.checkout"] != Failed || got["e2e.orders"] != Passed || got["whole.money"] != Passed {
		t.Fatalf("states: %v %v", got, err)
	}

	if st, _ := gh.status(commit, "pipelinegen/pr/e2e"); st.State != "failure" {
		t.Fatalf("suite status %+v", st)
	}

	onlyBody(t, gh, 7)
}
