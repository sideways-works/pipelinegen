package cli

import (
	"strconv"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestAStepExpectsItsOwnPeaksElseItsCommandsElseItsKindsDefault(t *testing.T) {
	cfg := &config.Config{}
	plan := stepMemory{cfg: cfg, step: "plan pr", kind: config.StepPlanning}
	suite := stepMemory{cfg: cfg, step: "report suite pr e2e", kind: config.StepBookkeeping}

	peaks := map[string][]int64{
		"plan main":                 {1000, 2000},
		"report suite main e2e":     {300},
		"report batch-end main e2e": {5000},
		"plan nightly":              {3000},
		"report suite main backend": {100},
	}

	// a proposed change's plan keeps no peaks: it expects what the trusted runs' plans used
	if mb, why := plan.need(peaks); mb != 3300 {
		t.Errorf("a plan of another run: %d MB, %s", mb, why)
	}

	// a suite's end expects the most another run's suite ends used, not a batch's end
	if mb, why := suite.need(peaks); mb != 330 {
		t.Errorf("a suite's end of another run: %d MB, %s", mb, why)
	}

	peaks["report suite pr e2e"] = []int64{50}
	if mb, _ := suite.need(peaks); mb != 55 {
		t.Errorf("its own peaks: %d MB", mb)
	}

	if mb, _ := plan.need(nil); mb != 4096 {
		t.Errorf("no peaks at all: %d MB", mb)
	}

	if mb, _ := suite.need(nil); mb != 1024 {
		t.Errorf("no peaks at all: %d MB", mb)
	}
}

func TestAStepWaitsAShareOfWhatItsDeadlineLeaves(t *testing.T) {
	now := time.Unix(1_000_000, 0)

	for _, tc := range []struct {
		deadline string
		want     time.Duration
	}{
		{"", stepWaitBare},
		{"not a time", stepWaitBare},
		{strconv.Itoa(1_000_000 + 1200), 400 * time.Second},
		{strconv.Itoa(1_000_000 + 7200), stepWaitMost},
		{strconv.Itoa(1_000_000 - 60), 0},
	} {
		t.Setenv(envDeadline, tc.deadline)

		if got := stepWait(now); got != tc.want {
			t.Errorf("deadline %q: %s, want %s", tc.deadline, got, tc.want)
		}
	}
}
