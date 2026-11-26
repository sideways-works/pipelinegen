package cli

import (
	"context"
	"errors"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/membroker"
	"github.com/sideways-works/pipelinegen/internal/memory"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// mbOf is recorded use of peaks alone, by step.
func mbOf(peaks map[string][]int64) map[string]report.StepUse {
	out := map[string]report.StepUse{}
	for name, p := range peaks {
		out[name] = report.StepUse{MB: p}
	}

	return out
}

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

	var never time.Time

	// a proposed change's plan keeps no peaks: it expects what the trusted runs' plans used
	if n := plan.need(mbOf(peaks), time.Now(), never); n.mb != 3300 {
		t.Errorf("a plan of another run: %d MB, %s", n.mb, n.mbWhy)
	}

	// a suite's end expects the most another run's suite ends used, not a batch's end
	if n := suite.need(mbOf(peaks), time.Now(), never); n.mb != 330 {
		t.Errorf("a suite's end of another run: %d MB, %s", n.mb, n.mbWhy)
	}

	peaks["report suite pr e2e"] = []int64{50}
	if n := suite.need(mbOf(peaks), time.Now(), never); n.mb != 55 || n.mbWhy != "its own recent peaks" {
		t.Errorf("its own peaks: %d MB, %s", n.mb, n.mbWhy)
	}

	if n := plan.need(nil, time.Now(), never); n.mb != 4096 {
		t.Errorf("no peaks at all: %d MB", n.mb)
	}

	if n := suite.need(nil, time.Now(), never); n.mb != 1024 {
		t.Errorf("no peaks at all: %d MB", n.mb)
	}
}

// A step's cores are its own recent ones, else those of the steps alike it, else its kind's cpu.steps;
// its lease its recorded seconds and half again, else its kind's, never past its deadline.
func TestAStepExpectsItsOwnCoresAndSecondsElseItsKindsDefaults(t *testing.T) {
	cfg := &config.Config{CPU: config.TopCPU{Steps: config.StepCores{Bookkeeping: 0.25}}}
	now := time.Unix(1_000_000, 0)

	var never time.Time

	uses := map[string]report.StepUse{
		"plan main":                   {Cores: []float64{1, 3}, Seconds: []float64{100, 400}},
		"report suite main e2e":       {Cores: []float64{0.5}, Seconds: []float64{10}},
		"prepare main e2e":            {MB: []int64{2000}, Cores: []float64{4}, Seconds: []float64{1200}},
		"prepare main backend":        {MB: []int64{9000}, Cores: []float64{8}, Seconds: []float64{3600}},
		"slice prepare main e2e":      {Cores: []float64{6}},
		"slice bookkeeping-files x e": {Cores: []float64{0.1}, Seconds: []float64{2}},
	}

	for _, tc := range []struct {
		name     string
		s        stepMemory
		deadline time.Time
		mb       int64
		cores    float64
		lease    time.Duration
	}{
		{"a plan by the other runs' plans", stepMemory{step: "plan pr", kind: config.StepPlanning}, never, 4096, 3.3, 600 * time.Second},
		{"its own over its command's", stepMemory{step: "plan main", kind: config.StepPlanning}, never, 4096, 3.3, 600 * time.Second},
		{"a short step's lease is a minute at least", stepMemory{step: "report suite pr e2e", kind: config.StepBookkeeping}, never, 1024, 0.55, time.Minute},
		{"a step of no recorded use at its kind's defaults", stepMemory{step: "tree pr e2e", kind: config.StepBookkeeping}, never, 1024, 0.25, 5 * time.Minute},
		{"a slice step by its own command's alone", stepMemory{step: "slice bookkeeping-files pr e2e", kind: config.StepBookkeeping}, never, 1024, 0.11, time.Minute},
		{"never past its deadline", stepMemory{step: "plan pr", kind: config.StepPlanning}, now.Add(4 * time.Minute), 4096, 3.3, 4 * time.Minute},
		{"a deadline gone leaves a second", stepMemory{step: "plan pr", kind: config.StepPlanning}, now.Add(-time.Minute), 4096, 3.3, time.Second},
		// a prepare's use is its suite's own: another suite's says nothing of it
		{"a prepare by its suite's", stepMemory{step: "prepare pr e2e", kind: config.StepPrepare, suite: "e2e"}, never, 2200, 4.4, 1800 * time.Second},
		{"a prepare of no recorded use waits for the pressure", stepMemory{step: "prepare pr web", kind: config.StepPrepare, suite: "web"}, never, 0, 2, 30 * time.Minute},
		{"a whole-suite check at its kind's defaults", stepMemory{step: "whole pr e2e", kind: config.StepWhole, suite: "e2e"}, never, 0, 2, 30 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.s.cfg = cfg

			n := tc.s.need(uses, now, tc.deadline)
			if n.mb != tc.mb || math.Abs(n.cores-tc.cores) > 1e-9 || n.lease != tc.lease {
				t.Errorf("%d MB (%s), %v cores (%s), lease %s; want %d MB, %v cores, lease %s", n.mb, n.mbWhy, n.cores, n.coresWhy, n.lease, tc.mb, tc.cores, tc.lease)
			}
		})
	}
}

// Every ask of a step is a step's, granted anyway when its wait ends; under cpu.wait it asks for its
// cores and its lease, without it for its memory alone.
func TestAStepAsksAsAStepForItsCoresUnderCPUWait(t *testing.T) {
	on := true
	n := stepNeed{mb: 500, cores: 1.5, lease: 90 * time.Second}

	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want membroker.Request
	}{
		{"cpu.wait", &config.Config{CPU: config.TopCPU{CPU: config.CPU{Wait: &on}}},
			membroker.Request{MB: 500, WaitSeconds: 60, Who: "plan pr 0123456789ab", CPU: true, Cores: 1.5, LeaseSeconds: 90, Step: true, Anyway: true}},
		{"no cpu.wait", &config.Config{},
			membroker.Request{MB: 500, WaitSeconds: 60, Who: "plan pr 0123456789ab", Step: true, Anyway: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := stepMemory{cfg: tc.cfg, step: "plan pr", kind: config.StepPlanning, commit: "0123456789abcdef"}

			if got := s.ask(n, time.Minute); got != tc.want {
				t.Errorf("%+v, want %+v", got, tc.want)
			}
		})
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
	s := stepMemory{cfg: &config.Config{CPU: config.TopCPU{CPU: config.CPU{Wait: &on}}}, step: "plan pr", kind: config.StepPlanning}

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

// Under cpu.wait a step asks the broker for its cores too. Short of them when its wait ends, it is granted
// anyway: its cores count against the batches' until it releases them at its end.
func TestAStepUnderCPUWaitIsGrantedItsCoresAnywayAndReleasesThem(t *testing.T) {
	root := fakeMemory(t, 15000, time.Minute)
	setCPUPressure(t, root, 70)

	on := true
	addr := startCPUBroker(t, time.Minute, 4)

	cfg := &config.Config{CPU: config.TopCPU{CPU: config.CPU{Wait: &on}, Steps: config.StepCores{Planning: 3}}}
	cfg.Memory.Broker = addr
	s := stepMemory{cfg: cfg, step: "plan pr", kind: config.StepPlanning}

	var log strings.Builder

	done, cores := s.askBroker(t.Context(), s.ask(s.need(nil, time.Now(), time.Time{}), 100*time.Millisecond), &log)
	if !done || !cores || !strings.Contains(log.String(), "the worker's broker granted 4096 MB and 3.0 cores for 20m0s anyway after 0s, the worker short of them (") {
		t.Fatalf("%s", log.String())
	}

	// the pressure eased, the step's 3 cores of 4 leave a batch's 2 no room
	setCPUPressure(t, root, 10)

	batch := membroker.Request{MB: 100, WaitSeconds: 0.1, Who: "e2e batch 1", CPU: true, Cores: 2, LeaseSeconds: 60}

	if ans, err := membroker.Ask(t.Context(), addr, batch); err != nil || ans.Granted {
		t.Fatalf("a batch beside the step: %+v %v", ans, err)
	}

	log.Reset()
	s.release(t.Context(), &log)

	if ans, err := membroker.Ask(t.Context(), addr, batch); err != nil || !ans.Granted || log.String() != "" {
		t.Fatalf("a batch once the step released its cores: %+v %v %s", ans, err, log.String())
	}

	// without cpu.wait it asks for its memory alone, and has no cores to release
	s.cfg = &config.Config{}
	s.cfg.Memory.Broker = addr

	log.Reset()

	if done, cores := s.askBroker(t.Context(), s.ask(s.need(nil, time.Now(), time.Time{}), 100*time.Millisecond), &log); !done || cores ||
		!strings.Contains(log.String(), "the worker's broker granted 4096 MB after 0s: the step starts") {
		t.Fatalf("no cpu.wait: %s", log.String())
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

	// the broker holds back an ask at 70% CPU pressure: the check waits its while, then starts anyway, its
	// kind's 2 cores booked
	s := suiteStepMemory(cfg, config.StepWhole, "pr", "e2e", "")
	if done, cores := s.askBroker(t.Context(), s.ask(s.need(nil, time.Now(), time.Time{}), 100*time.Millisecond), &log); !done || !cores ||
		!strings.Contains(log.String(), "the worker's broker granted 0 MB and 2.0 cores for 30m0s anyway after 0s") {
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

// A step's use is its task's cgroup read at its end: the most memory it used, and the CPU seconds since
// its start over its seconds, the cores of a step too short to say left out.
func TestAStepsUseIsItsCoresSinceItsStart(t *testing.T) {
	root := fakeMemory(t, 8000, time.Minute)
	setCgroup(t, root, 30, 200, 300, 0)

	for _, tc := range []struct {
		name  string
		start stepStart
		cores float64
	}{
		{"its CPU seconds over its seconds", stepStart{at: time.Now().Add(-10 * time.Second), cpu: 10}, 2},
		{"a step too short to say", stepStart{at: time.Now().Add(-time.Second), cpu: 10}, 0},
		{"a start whose CPU wasn't read", stepStart{at: time.Now().Add(-10 * time.Second), cpu: -1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, ok := tc.start.used()
			if !ok || used.MB != 300 || math.Abs(used.Cores-tc.cores) > 0.01 || used.Seconds < time.Since(tc.start.at).Seconds()-1 {
				t.Errorf("%+v %v, want 300 MB and %v cores", used, ok, tc.cores)
			}
		})
	}
}

// A trusted run's step keeps its peak, cores and seconds; a proposed change's keeps nothing.
func TestAStepKeepsWhatItUsedInATrustedRun(t *testing.T) {
	_, _, store := runEnv(t)
	cfg := &config.Config{}

	used := report.Peak{MB: 300, Cores: 1.5, Seconds: 42}

	for _, s := range []stepMemory{
		{cfg: cfg, store: store, step: "tree main e2e", kind: config.StepBookkeeping, commit: "0123456789abcdef0123456789abcdef01234567", record: true},
		{cfg: cfg, store: store, step: "tree pr e2e", kind: config.StepBookkeeping, commit: "0123456789abcdef0123456789abcdef01234567"},
		{cfg: cfg, step: "slice bookkeeping-files main e2e", kind: config.StepBookkeeping, commit: "0123456789abcdef0123456789abcdef01234567", record: true},
	} {
		var log strings.Builder

		s.keep(t.Context(), used, &log)

		if log.String() != "" {
			t.Fatalf("%s: %s", s.step, log.String())
		}
	}

	uses, err := report.StepUses(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}

	if u, ok := uses["tree main e2e"]; !ok || len(u.MB) != 1 || u.MB[0] != 300 || u.Cores[0] != 1.5 || u.Seconds[0] != 42 || len(uses) != 1 {
		t.Fatalf("%+v", uses)
	}
}

// The bookkeeping files step has no store: it waits by the config it copies, for its kind's defaults, and
// only in a pipeline.
func TestTheBookkeepingFilesStepWaitsByTheConfigItCopies(t *testing.T) {
	root := fakeMemory(t, 8000, time.Minute)
	setCPUPressure(t, root, 10)

	t.Setenv(envRun, "pr")
	t.Setenv(envSuite, "e2e")
	t.Setenv(envCommit, "0123456789abcdef0123456789abcdef01234567")

	s := bookkeepingFilesMemory([]byte(reportConfig + "cpu: {wait: true, steps: {bookkeeping: 0.75}}\n"))
	if s.step != "slice bookkeeping-files pr e2e" || s.who() != "slice bookkeeping-files pr e2e 0123456789ab" || s.store != nil || s.record {
		t.Fatalf("%+v", s)
	}

	for _, tc := range []struct {
		name, job string
		want      string
	}{
		{"in a pipeline", "https://ci/jobs/e2e", "cpu: this bookkeeping step should keep 0.8 cores busy, by cpu.steps.bookkeeping, with no cores recorded yet"},
		{"on this machine", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envJobURL, tc.job)
			t.Setenv(secret.PipelineStepVar, "")

			var log strings.Builder

			if s.admit(t.Context(), &log) || !strings.Contains(log.String(), tc.want) || tc.want == "" && log.String() != "" {
				t.Fatalf("%s", log.String())
			}
		})
	}

	// a config it can't read waits for nothing
	if s := bookkeepingFilesMemory([]byte("suites: [")); s.cfg.WaitsForCPU("") || s.cfg.WaitsForMemory("") {
		t.Fatalf("%+v", s.cfg)
	}
}

// lockedLog is a broker's log the test reads once the step has ended.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	_, _ = l.b.Write(p)

	return len(p), nil
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.b.String()
}

// A suite job's tree waits for its memory and cores only when its suite runs something: one with nothing
// to run ends at once, asking nothing. The step's grant is released at its end.
func TestTheTreeStepWaitsOnlyWhenItsSuiteRuns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		planned bool
	}{
		{"nothing to run", false},
		{"a suite that runs", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var trigger string
			if tc.planned {
				trigger = handedHead(t).trigger
			} else {
				_, _, trigger, _, _ = gatedChange(t)
			}

			root := fakeMemory(t, 15000, time.Minute)
			setCPUPressure(t, root, 0)

			l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}

			var log lockedLog

			b := &membroker.Broker{
				Read: func() (memory.Reading, float64, error) {
					r, err := memory.Read(memoryRoot)

					return r, memory.Pressure(memoryRoot), err
				},
				Ramp: time.Minute, Every: 10 * time.Millisecond, CPUs: 8, CPUHold: 40, Log: &log,
				ReadCPU: func() float64 { return memory.CPUPressure(memoryRoot) },
			}

			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			go func() { _ = b.Serve(ctx, l) }() //nolint:errcheck // ends with the test

			cfg := filepath.Join(trigger, "pipelinegen.yaml")
			writeFile(t, cfg, readFile(t, cfg)+"cpu: {wait: true}\nmemory: {broker: \""+l.Addr().String()+"\"}\n")

			treeStep(t, trigger)

			got := log.String()

			switch {
			case !tc.planned && got != "":
				t.Fatalf("a suite with nothing to run asked:\n%s", got)
			case tc.planned && (!strings.Contains(got, `granted "tree pr e2e `) || !strings.Contains(got, "0.5 cores") || !strings.Contains(got, `released "tree pr e2e `)):
				t.Fatalf("the tree of a suite that runs:\n%s", got)
			}
		})
	}
}
