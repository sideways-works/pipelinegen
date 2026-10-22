package report

import (
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
)

func planOf(head string) []byte {
	return fmt.Appendf(nil, `{"head": %q, "suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`, head)
}

func storedPlan(t *testing.T, remote string, id ID) string {
	t.Helper()

	s := testStore(t, remote)

	ref, ok, err := s.First(t.Context(), []string{id.PlanRef()})
	if err != nil || !ok {
		t.Fatalf("plan: %v %v", ok, err)
	}

	data, ok, err := s.File(t.Context(), ref.Commit, planFileName)
	if err != nil || !ok {
		t.Fatalf("plan.json: %v %v", ok, err)
	}

	return string(data)
}

func TestPlannersThatMeetLeaveOnePlan(t *testing.T) {
	remote := storetest.Bare(t)
	id := ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	const planners = 6

	errs := make([]error, planners)

	var wg sync.WaitGroup

	for i := range planners {
		s := testStore(t, remote)

		wg.Go(func() { errs[i] = WritePlan(t.Context(), s, id, planOf(strconv.Itoa(i))) })
	}

	wg.Wait()

	winner := -1

	for i, err := range errs {
		switch {
		case err == nil && winner >= 0:
			t.Fatalf("planners %d and %d both wrote the plan", winner, i)
		case err == nil:
			winner = i
		case !errors.Is(err, ErrPlanned):
			t.Fatalf("planner %d: %v", i, err)
		}
	}

	if winner < 0 {
		t.Fatalf("no planner wrote the plan: %v", errs)
	}

	if got, want := storedPlan(t, remote, id), string(planOf(strconv.Itoa(winner))); got != want {
		t.Fatalf("the plan is %s, the writer's was %s", got, want)
	}

	if err := WritePlan(t.Context(), testStore(t, remote), id, planOf("later")); !errors.Is(err, ErrPlanned) {
		t.Fatalf("a later planner: %v", err)
	}

	if err := ReplacePlan(t.Context(), testStore(t, remote), id, planOf("again")); err != nil {
		t.Fatal(err)
	}

	if got := storedPlan(t, remote, id); got != string(planOf("again")) {
		t.Fatalf("a local recording planned again left %s", got)
	}
}
