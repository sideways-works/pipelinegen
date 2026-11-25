package report

import (
	"testing"
)

// A step's peaks are kept by step, the last PeaksKept, one per commit, apart from any suite's.
func TestAStepKeepsItsOwnRecentPeaks(t *testing.T) {
	ctx := t.Context()
	s := testStore(t, bareRemote(t))

	for i := range PeaksKept + 2 {
		commit := string(rune('a'+i)) + "0000000000000000000000000000000000000000"[1:]
		if err := AddStepPeak(ctx, s, "plan main", commit, int64(100+i)); err != nil {
			t.Fatal(err)
		}
	}

	// the same commit's peak told again adds nothing
	if err := AddStepPeak(ctx, s, "plan main", "l000000000000000000000000000000000000000", 999); err != nil {
		t.Fatal(err)
	}

	if err := AddStepPeak(ctx, s, "report suite main e2e", "a000000000000000000000000000000000000000", 700); err != nil {
		t.Fatal(err)
	}

	got, err := StepPeaks(ctx, s)
	if err != nil {
		t.Fatal(err)
	}

	if p := got["plan main"]; len(p) != PeaksKept || p[0] != 102 || p[len(p)-1] != 111 {
		t.Errorf("plan main: %v", p)
	}

	if p := got["report suite main e2e"]; len(p) != 1 || p[0] != 700 {
		t.Errorf("report suite: %v", p)
	}

	if suite, err := Peaks(ctx, s, "e2e"); err != nil || len(suite) != 0 {
		t.Errorf("a suite's peaks hold no step's: %v %v", suite, err)
	}
}
