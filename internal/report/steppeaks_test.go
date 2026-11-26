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
		if err := AddStepPeak(ctx, s, "plan main", commit, Peak{MB: int64(100 + i), Cores: 1.5, Seconds: float64(60 + i)}); err != nil {
			t.Fatal(err)
		}
	}

	// the same commit's peak told again adds nothing
	if err := AddStepPeak(ctx, s, "plan main", "l000000000000000000000000000000000000000", Peak{MB: 999}); err != nil {
		t.Fatal(err)
	}

	if err := AddStepPeak(ctx, s, "report suite main e2e", "a000000000000000000000000000000000000000", Peak{MB: 700}); err != nil {
		t.Fatal(err)
	}

	// a step whose cores or seconds weren't measured keeps its peak alone
	if err := AddStepPeak(ctx, s, "tree main e2e", "a000000000000000000000000000000000000000", Peak{Cores: 0.4, Seconds: 12}); err != nil {
		t.Fatal(err)
	}

	got, err := StepUses(ctx, s)
	if err != nil {
		t.Fatal(err)
	}

	if p := got["plan main"]; len(p.MB) != PeaksKept || p.MB[0] != 102 || p.MB[len(p.MB)-1] != 111 ||
		len(p.Cores) != PeaksKept || p.Cores[0] != 1.5 || len(p.Seconds) != PeaksKept || p.Seconds[0] != 62 {
		t.Errorf("plan main: %+v", p)
	}

	if p := got["report suite main e2e"]; len(p.MB) != 1 || p.MB[0] != 700 || p.Cores != nil || p.Seconds != nil {
		t.Errorf("report suite: %+v", p)
	}

	if p := got["tree main e2e"]; p.MB != nil || len(p.Cores) != 1 || p.Cores[0] != 0.4 || len(p.Seconds) != 1 || p.Seconds[0] != 12 {
		t.Errorf("tree: %+v", p)
	}

	if suite, err := Peaks(ctx, s, "e2e"); err != nil || len(suite) != 0 {
		t.Errorf("a suite's peaks hold no step's: %v %v", suite, err)
	}
}
