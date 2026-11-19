package gitstore

import (
	"context"
	"io"
	"regexp"
	"strconv"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/progress"
)

// remoteCalls runs f with a meter and returns how many git commands reached the remote, by command.
func remoteCalls(t *testing.T, f func(ctx context.Context)) map[string]int {
	t.Helper()

	ctx, stop := progress.Start(t.Context(), io.Discard, "test")
	defer stop()

	f(ctx)

	got := map[string]int{}

	for _, m := range regexp.MustCompile(`(\d+) (fetch|ls-remote|push|clone)`).FindAllStringSubmatch(progress.Summary(ctx), -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}

		got[m[2]] = n
	}

	return got
}

func plannedRun(t *testing.T, remote string, own bool) {
	t.Helper()

	s := groupedStore(t, remote, "")
	up := []Update{{Ref: planRef(), Any: true, Files: files("plan.json", "{}")}}

	var err error
	if own {
		_, err = s.refsWrite(t.Context(), "plan", up)
	} else {
		_, err = s.Write(t.Context(), "plan", up...)
	}

	if err != nil {
		t.Fatal(err)
	}

	state(t, groupedStore(t, remote, "1"), "a.spec.ts", "passed")
}

func TestAListIsOneRoundTrip(t *testing.T) {
	remote := bare(t)
	plannedRun(t, remote, false)

	var refs map[string]Ref

	calls := remoteCalls(t, func(ctx context.Context) {
		var err error
		if refs, err = newStore(t, remote).List(ctx, runsRoot+run+"/", plansRoot+"pr/pr-7/"); err != nil {
			t.Fatal(err)
		}
	})

	if len(calls) != 1 || calls["fetch"] != 1 {
		t.Errorf("a list reached the remote %v, want one fetch", calls)
	}

	if _, ok := refs[stateRef("a.spec.ts")]; !ok || len(refs) != 2 {
		t.Errorf("listed %v", refs)
	}
}

func TestAListedPlanTellsItsRunsLayout(t *testing.T) {
	for _, own := range []bool{false, true} {
		remote := bare(t)
		plannedRun(t, remote, own)

		s := newStore(t, remote)
		if _, err := s.List(t.Context(), runsRoot+run+"/", plansRoot+"pr/pr-7/"); err != nil {
			t.Fatal(err)
		}

		calls := remoteCalls(t, func(ctx context.Context) {
			old, err := s.oneRefEach(ctx, run)
			if err != nil || old != own {
				t.Errorf("own ref %v: layout says own %v (%v)", own, old, err)
			}
		})

		if len(calls) != 0 {
			t.Errorf("own ref %v: the layout reached the remote %v", own, calls)
		}
	}
}

func TestARecordIsFoundInOneRoundTripInEitherLayout(t *testing.T) {
	for _, own := range []bool{false, true} {
		remote := bare(t)
		plannedRun(t, remote, own)

		s := newStore(t, remote)

		var (
			ref Ref
			ok  bool
		)

		calls := remoteCalls(t, func(ctx context.Context) {
			var err error
			if ref, ok, err = s.First(ctx, []string{planRef()}); err != nil {
				t.Fatal(err)
			}
		})

		if !ok || ref.Name != planRef() {
			t.Fatalf("own ref %v: first %+v %v", own, ref, ok)
		}

		if len(calls) != 1 || calls["fetch"] != 1 {
			t.Errorf("own ref %v: finding the plan reached the remote %v, want one fetch", own, calls)
		}

		got, err := s.Files(t.Context(), ref.Commit)
		if err != nil || string(got["plan.json"]) != "{}" {
			t.Errorf("own ref %v: plan %q %v", own, got, err)
		}

		// the plan's layout is known now: a write asks the remote nothing more about it
		calls = remoteCalls(t, func(ctx context.Context) {
			if _, err := s.oneRefEach(ctx, run); err != nil {
				t.Fatal(err)
			}
		})

		if len(calls) != 0 {
			t.Errorf("own ref %v: the layout reached the remote %v", own, calls)
		}

		if old, err := s.oneRefEach(t.Context(), run); err != nil || old != own {
			t.Errorf("own ref %v: layout says own %v (%v)", own, old, err)
		}
	}
}

func TestAMissingRecordIsNotFound(t *testing.T) {
	remote := bare(t)
	plannedRun(t, remote, false)

	s := newStore(t, remote)

	if _, ok, err := s.First(t.Context(), []string{plansRoot + "pr/pr-7/ffffffffffff"}); err != nil || ok {
		t.Errorf("a plan never written: found %v, %v", ok, err)
	}

	// nothing is learned of a run whose plan wasn't found
	s.g.mu.Lock()
	_, learned := s.g.layouts["pr/pr-7/ffffffffffff"]
	s.g.mu.Unlock()

	if learned {
		t.Error("learned the layout of a run without a plan")
	}
}
