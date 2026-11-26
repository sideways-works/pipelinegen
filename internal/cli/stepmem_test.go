package cli

import (
	"errors"
	"strconv"
	"strings"
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

func TestAStepUnderCPUWaitWaitsForThePressureThenStartsAnyway(t *testing.T) {
	root := fakeMemory(t, 8000, time.Minute)
	setCPUPressure(t, root, 70)

	on := true
	s := stepMemory{cfg: &config.Config{CPU: config.CPU{Wait: &on}}, step: "plan pr", kind: config.StepPlanning}

	// eased below cpu.pressure, the step starts
	eased := make(chan time.Time, 1)

	go func() {
		time.Sleep(100 * time.Millisecond)

		at := time.Now()

		setCPUPressure(t, root, 10)

		eased <- at
	}()

	var log strings.Builder

	s.awaitReading(t.Context(), 0, time.Minute, &log)

	if started, at := time.Now(), <-eased; started.Before(at) {
		t.Fatalf("started before the pressure eased:\n%s", log.String())
	}

	// never eased, it starts anyway once its wait ends
	setCPUPressure(t, root, 70)
	log.Reset()

	s.awaitReading(t.Context(), 0, 50*time.Millisecond, &log)

	if !strings.Contains(log.String(), ", CPU pressure 70.0%, after ") || !strings.Contains(log.String(), "the step starts anyway") {
		t.Fatalf("%s", log.String())
	}

	// without cpu.wait the CPU holds nothing back
	log.Reset()

	s.cfg = &config.Config{}
	s.awaitReading(t.Context(), 0, time.Minute, &log)

	if log.String() != "" {
		t.Fatalf("%s", log.String())
	}
}

func TestAStepUnderCPUWaitAsksTheBrokerForThePressureAlone(t *testing.T) {
	root := fakeMemory(t, 8000, time.Minute)
	setCPUPressure(t, root, 70)

	on := true
	addr := startCPUBroker(t, time.Minute, 4)

	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"cpu.wait", &config.Config{CPU: config.CPU{Wait: &on}}, "the worker's broker granted nothing after 0s ("},
		{"no cpu.wait", &config.Config{}, "the worker's broker granted 100 MB after 0s: the step starts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Memory.Broker = addr
			s := stepMemory{cfg: tc.cfg, step: "plan pr", kind: config.StepPlanning}

			var log strings.Builder
			if !s.askBroker(t.Context(), 100, 100*time.Millisecond, &log) || !strings.Contains(log.String(), tc.want) {
				t.Fatalf("%s", log.String())
			}
		})
	}
}

// A prepare and a whole-suite check are a suite's steps: its own cpu.wait, cpu.pressure and memory.broker
// hold for them, not the top level's alone.
func TestASuitesPrepareWaitsUnderTheSuitesOwnSettings(t *testing.T) {
	root := fakeMemory(t, 8000, time.Minute)
	setCPUPressure(t, root, 70)

	on, high := true, 80.0
	addr := startCPUBroker(t, time.Minute, 4)

	cfg := &config.Config{Suites: map[string]config.Suite{
		"e2e":     {CPU: config.CPU{Wait: &on}, Memory: config.Memory{Broker: addr}},
		"backend": {CPU: config.CPU{Wait: &on, Pressure: &high}},
	}}

	var log strings.Builder

	// the broker holds back an ask at 70% CPU pressure: the check waits its while, then starts anyway
	if s := suiteStepMemory(cfg, config.StepWhole, "pr", "e2e", ""); !s.askBroker(t.Context(), 0, 100*time.Millisecond, &log) ||
		!strings.Contains(log.String(), "the worker's broker granted nothing after 0s (") {
		t.Fatalf("e2e's check: %s", log.String())
	}

	// backend's own cpu.pressure, 80%, lets it start at 70%
	log.Reset()

	done := make(chan struct{})

	go func() {
		suiteStepMemory(cfg, config.StepPrepare, "pr", "backend", "").awaitReading(t.Context(), 0, time.Minute, &log)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("backend's prepare waited below its suite's cpu.pressure")
	}

	// a suite without cpu.wait, under a top level without it, never waits
	if s := suiteStepMemory(cfg, config.StepPrepare, "pr", "web", ""); s.waitsForCPU() {
		t.Fatal("web's prepare waits for CPU")
	}
}

func TestWaitNamesAKindOfTheSuitesTasks(t *testing.T) {
	t.Setenv(envSuite, "e2e")

	if err := runWait(t.Context(), []string{"batch"}, nil); !errors.Is(err, errUsage) {
		t.Fatalf("wait batch: %v", err)
	}

	if err := runWait(t.Context(), nil, nil); !errors.Is(err, errUsage) {
		t.Fatalf("wait: %v", err)
	}
}
