package report

import (
	"path/filepath"
	"testing"
)

func stoppedSetup(t *testing.T) *Reporter {
	t.Helper()

	r, _, c, _ := journalSetup(t)
	r.Now = c.now

	return r
}

func failureOf(t *testing.T, r *Reporter, slice string) (State, Failure) {
	t.Helper()

	snap, err := readSnapshot(t.Context(), r.Store, r.Config, r.ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range buildRun(r.ID, snap, nil).Suites {
		for _, sl := range s.Slices {
			if sl.Name == slice && len(sl.Failures) == 1 {
				return sl.State, sl.Failures[0]
			}
		}
	}

	t.Fatalf("%s has no one failure", slice)

	return "", Failure{}
}

func TestAStoppedBatchsSlicesSayWhy(t *testing.T) {
	r := stoppedSetup(t)

	why := "batch 1 stopped while e2e.checkout ran; its time limit is 120 minutes (suites.e2e.minutes)"

	open := sliceEv(t, "e2e.checkout", "")
	open.Result = filepath.Join(t.TempDir(), "missing.json")
	open.Stopped = why

	written := sliceEv(t, "e2e.orders", `{"passed": false, "failures": [{"test": "lists", "tail": "boom"}]}`)
	written.Stopped = why

	if _, err := r.Close(t.Context(), []SliceEvent{open, written}); err != nil {
		t.Fatal(err)
	}

	if st, f := failureOf(t, r, "e2e.checkout"); st != Errored || f.Test != "(stopped)" || f.Tail != why {
		t.Fatalf("the open slice: %s %+v", st, f)
	}

	if st, f := failureOf(t, r, "e2e.orders"); st != Failed || f.Test != "lists" {
		t.Fatalf("the slice with a result: %s %+v", st, f)
	}

	r2 := stoppedSetup(t)

	never := sliceEv(t, "e2e.checkout", "")
	never.Result, never.NotRun, never.Stopped = filepath.Join(t.TempDir(), "missing.json"), true, why

	if _, err := r2.Slices(t.Context(), []SliceEvent{never}); err != nil {
		t.Fatal(err)
	}

	if st, f := failureOf(t, r2, "e2e.checkout"); st != NotRun || f.Tail != why {
		t.Fatalf("the slice never run: %s %+v", st, f)
	}
}

func TestASuitesEndSaysWhyItsSlicesNeverReported(t *testing.T) {
	r := stoppedSetup(t)
	r.Stopped = "suite e2e stopped 130 minutes after it began; its time limit is 120 minutes (suites.e2e.minutes)"

	if _, err := r.Suite(t.Context(), "e2e"); err != nil {
		t.Fatal(err)
	}

	if st, f := failureOf(t, r, "e2e.orders"); st != Errored || f.Test != noResult.Test || f.Tail != r.Stopped {
		t.Fatalf("with a reason: %s %+v", st, f)
	}

	plain := stoppedSetup(t)

	if _, err := plain.Suite(t.Context(), "e2e"); err != nil {
		t.Fatal(err)
	}

	if st, f := failureOf(t, plain, "e2e.orders"); st != Errored || f != noResult {
		t.Fatalf("without one: %s %+v", st, f)
	}
}
