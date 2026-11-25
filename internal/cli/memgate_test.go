package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/membroker"
	"github.com/sideways-works/pipelinegen/internal/memory"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// fakeMemory points the memory's reading at files of the test's, read every few milliseconds, with
// MemAvailable at freeMB and no memory pressure. Tests that call it don't run in parallel.
func fakeMemory(t *testing.T, freeMB int, wait time.Duration) string {
	t.Helper()

	root := t.TempDir()
	setFree(t, root, freeMB)
	setPressure(t, root, 0)
	writeFile(t, filepath.Join(root, "proc", "self", "cgroup"), "0::/\n")

	oldRoot, oldEvery, oldWait := memoryRoot, memoryEvery, memoryMaxWait
	memoryRoot, memoryEvery, memoryMaxWait = root, 10*time.Millisecond, wait

	t.Cleanup(func() { memoryRoot, memoryEvery, memoryMaxWait = oldRoot, oldEvery, oldWait })

	return root
}

func setFree(t *testing.T, root string, freeMB int) {
	t.Helper()

	writeFile(t, filepath.Join(root, "proc", "meminfo"), fmt.Sprintf("MemTotal: %d kB\nMemAvailable: %d kB\n", 16000*1024, freeMB*1024))
}

func setPressure(t *testing.T, root string, avg10 float64) {
	t.Helper()

	writeFile(t, filepath.Join(root, "proc", "pressure", "memory"), fmt.Sprintf("some avg10=%.2f avg60=0.00 avg300=0.00 total=1\n", avg10))
}

// setCgroup writes the task's cgroup: CPU seconds, its working set, its peak and its OOM kills.
func setCgroup(t *testing.T, root string, cpu float64, setMB, peakMB, oom int) {
	t.Helper()

	cg := filepath.Join(root, "sys", "fs", "cgroup")
	writeFile(t, filepath.Join(cg, "cpu.stat"), fmt.Sprintf("usage_usec %d\n", int(cpu*1e6)))
	writeFile(t, filepath.Join(cg, "memory.current"), fmt.Sprintf("%d\n", setMB<<20))
	writeFile(t, filepath.Join(cg, "memory.peak"), fmt.Sprintf("%d\n", peakMB<<20))
	writeFile(t, filepath.Join(cg, "memory.stat"), "inactive_file 0\n")
	writeFile(t, filepath.Join(cg, "memory.events"), fmt.Sprintf("oom_kill %d\n", oom))
}

func predicted(t *testing.T, batches, batch string, mb int) {
	t.Helper()

	writeFile(t, filepath.Join(batches, batch, predictedFile), fmt.Sprintf(`{"predicted_mb": %d, "why": "the test's"}`, mb))
}

func TestPredictTakesEachSlicesPercentileAndTheMedianForANewOne(t *testing.T) {
	peaks := map[string][]int64{"a": {1000, 1200}, "b": {3000}, "c": {2000, 2000, 2000}}

	if p, ok := predict(peaks, []string{"a", "c"}); !ok || p.MB != 2200 || strings.Contains(p.Why, "median") {
		t.Fatalf("known slices: %+v %v", p, ok)
	}

	// the slices' predictions are 1320, 2200 and 3300: a new slice takes the 90th percentile, 3300
	if p, ok := predict(peaks, []string{"a", "new"}); !ok || p.MB != 3300 || !strings.Contains(p.Why, "1 of 2 without any at the 90th percentile of the suite's others, 3300 MB") {
		t.Fatalf("a new slice: %+v %v", p, ok)
	}

	if p, ok := predict(map[string][]int64{}, []string{"a"}); ok {
		t.Fatalf("no peaks at all: %+v", p)
	}
}

func TestABatchStartsAtOnceWithTheMemoryItShouldUse(t *testing.T) {
	fakeMemory(t, 8000, time.Minute)

	batches := t.TempDir()
	predicted(t, batches, "1", 6600)

	var log strings.Builder
	if err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: t.TempDir()}, &log); err != nil {
		t.Fatal(err)
	}

	if got := log.String(); got != "memory: 8000 MB free: 8000 MB available of 16000 MB, no cgroup limit, pressure 0.0%; this batch should use 6600 MB: the test's\n" {
		t.Fatalf("%q", got)
	}
}

func TestABatchWaitsThenStartsWhenMemoryFreesAndPressureEases(t *testing.T) {
	root := fakeMemory(t, 3000, time.Minute)
	setPressure(t, root, 40)

	batches, result := t.TempDir(), t.TempDir()
	predicted(t, batches, "1", 6600)

	// eased is when the pressure fell: the batch may start only after it
	eased := make(chan time.Time, 1)

	go func() {
		time.Sleep(80 * time.Millisecond)
		setFree(t, root, 7000)
		time.Sleep(80 * time.Millisecond)

		at := time.Now()

		setPressure(t, root, 2)

		eased <- at
	}()

	var log strings.Builder

	if err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: result}, &log); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	if at := <-eased; started.Before(at) || !strings.Contains(log.String(), "memory: 7000 MB free of 6600 MB predicted after ") {
		t.Fatalf("started %s before the pressure eased:\n%s", at.Sub(started), log.String())
	}

	if shortOfMemoryIn(result) != "" {
		t.Fatal("a batch that started says it was short")
	}
}

func TestABatchStillShortLeavesItsSlicesNotRun(t *testing.T) {
	fakeMemory(t, 3120, 50*time.Millisecond)

	batches, result := t.TempDir(), t.TempDir()
	predicted(t, batches, "1", 6600)

	err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: result}, io.Discard)
	if !errors.Is(err, errShortOfMemory) || !strings.Contains(err.Error(), "a batch of e2e: the worker had 3120 MB free of 6600 MB predicted") {
		t.Fatalf("%v", err)
	}

	if got := shortOfMemoryIn(result); !strings.HasPrefix(got, "the worker had 3120 MB free of 6600 MB predicted, after waiting ") {
		t.Fatalf("%q", got)
	}
}

func TestWithoutPeaksNoBatchWaits(t *testing.T) {
	fakeMemory(t, 10, time.Hour)
	result := t.TempDir()

	var log strings.Builder
	if err := awaitMemory(t.Context(), gate{batches: t.TempDir(), batch: "1", suite: "e2e", minutes: 120, result: result}, &log); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(log.String(), "10 MB free") || !strings.Contains(log.String(), "e2e has no peaks measured yet, so the batch doesn't wait for memory") {
		t.Fatalf("%q", log.String())
	}

	if shortOfMemoryIn(result) != "" {
		t.Fatal("short without a prediction")
	}

	none := func(context.Context) (map[string][]int64, error) { return map[string][]int64{}, nil }
	if err := localMemory(t.Context(), none, "e2e", []string{"a"}); err != nil {
		t.Fatalf("on this machine: %v", err)
	}
}

func TestAPredictionNoWorkerCanGiveDoesntStrandTheBatch(t *testing.T) {
	fakeMemory(t, 3000, time.Hour)

	batches, result := t.TempDir(), t.TempDir()
	predicted(t, batches, "1", 20000)

	var log strings.Builder
	if err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: result}, &log); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(log.String(), "20000 MB predicted is more than the 16000 MB this worker could ever give: the batch starts unchecked") {
		t.Fatalf("%q", log.String())
	}
}

func TestTheWaitIsATenthOfAShortTimeLimit(t *testing.T) {
	if got := memoryWait(120); got != 5*time.Minute {
		t.Fatalf("120 minutes: %s", got)
	}

	if got := memoryWait(20); got != 2*time.Minute {
		t.Fatalf("20 minutes: %s", got)
	}
}

func TestOnThisMachineAShortBatchIsRefused(t *testing.T) {
	fakeMemory(t, 3000, time.Hour)

	peaks := func(context.Context) (map[string][]int64, error) { return map[string][]int64{"a": {6000}}, nil }

	err := localMemory(t.Context(), peaks, "e2e", []string{"a"})
	if !errors.Is(err, errNoMemoryHere) || !strings.Contains(err.Error(), "a batch of e2e should use 6600 MB (") ||
		!strings.Contains(err.Error(), "this machine has 3000 MB free") || !strings.Contains(err.Error(), "close what you can spare and run it again") {
		t.Fatalf("%v", err)
	}
}

// memoryBatch is a pull request's run of two e2e slices dealt into batch 1, in a directory of its own.
type memRun struct {
	root   string
	api    *githubAPI
	store  *gitstore.Store
	states func() map[string]report.StateFile
}

func memoryBatch(t *testing.T) memRun {
	t.Helper()

	root, api, s := runEnv(t)

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)
	writeFile(t, "batches/batches.json", `["1"]`)

	states := func() map[string]report.StateFile {
		t.Helper()

		got, err := report.StateFiles(t.Context(), s, id, "e2e")
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	return memRun{root: root, api: api, store: s, states: states}
}

func TestABatchShortOfMemoryIsNotRunAndSaysWhy(t *testing.T) {
	m := memoryBatch(t)
	root, api, states := m.root, m.api, m.states

	fakeMemory(t, 3120, 50*time.Millisecond)
	predicted(t, "batches", "1", 6600)

	_, stderr, code := run(t, "slice", "memory", "-root", root)
	if code != 1 || !strings.Contains(stderr, "the worker is short of memory: a batch of e2e: the worker had 3120 MB free of 6600 MB predicted") {
		t.Fatalf("slice memory: %d %s", code, stderr)
	}

	_, stderr, code = run(t, "report", "batch-end", "-root", root)
	if code != 1 || !strings.Contains(stderr, "batch 1 ran nothing: none of its 2 slices ran: the worker had 3120 MB free of 6600 MB predicted") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	for name, st := range states() {
		if st.State != report.NotRun || len(st.Failures) != 1 || st.Failures[0].Test != report.MemoryTest ||
			!strings.HasPrefix(st.Failures[0].Tail, "not run: the worker had 3120 MB free of 6600 MB predicted") {
			t.Errorf("%s: %+v", name, st)
		}
	}

	if _, _, code := run(t, "report", "suite", "-root", root, "-batches", "batches"); code != 1 {
		t.Fatalf("suite: %d", code)
	}

	if got := api.status("pipelinegen/pr/e2e"); got != "error 0 of 2 slices ran, 2 not run (worker short of memory or CPU)" {
		t.Fatalf("suite status %q", got)
	}

	if body := api.comment(7); !strings.Contains(body, "**not run** 2 slices of e2e: not run: the worker had 3120 MB free") {
		t.Fatalf("the comment:\n%s", body)
	}
}

func TestSliceRunWaitsForMemoryBeforeItsSetup(t *testing.T) {
	root := memoryBatch(t).root
	writeFile(t, filepath.Join(root, config.FileName), strings.Replace(reportConfig, "    run_slice: ci/tasks/e2e-slice.yml\n",
		"    steps:\n      - {setup: command, with: {each: [sh, -c, 'touch \"$SETUP\"']}}\n      - {run: command, with: {each: [\"true\"]}}\n", 1))
	fakeMemory(t, 3120, 50*time.Millisecond)
	predicted(t, "batches", "1", 6600)

	setup := filepath.Join(t.TempDir(), "setup-ran")
	t.Setenv("SETUP", setup)

	_, stderr, code := run(t, "slice", "run", "-root", root)
	if code != 1 || !strings.Contains(stderr, "a batch of e2e: the worker had 3120 MB free of 6600 MB predicted") {
		t.Fatalf("slice run: %d %s", code, stderr)
	}

	if _, err := os.Stat(setup); err == nil {
		t.Fatal("the setup ran on a worker short of memory")
	}

	if shortOfMemoryIn("result") == "" {
		t.Fatal("the batch's end can't tell why it ran nothing")
	}
}

func TestABatchThatWaitsForMemoryWaitsPastTheShortWait(t *testing.T) {
	root := fakeMemory(t, 3000, 50*time.Millisecond)

	batches, result := t.TempDir(), t.TempDir()
	predicted(t, batches, "1", 6600)

	go func() {
		time.Sleep(300 * time.Millisecond)
		setFree(t, root, 7000)
	}()

	var log strings.Builder
	if err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: result, block: true}, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	if shortOfMemoryIn(result) != "" || !strings.Contains(log.String(), "7000 MB free of 6600 MB predicted after ") {
		t.Fatalf("%s", log.String())
	}
}

func TestABatchNeedsItsPredictionLessWhatItHolds(t *testing.T) {
	fakeMemory(t, 4000, 50*time.Millisecond)

	batches, result := t.TempDir(), t.TempDir()
	predicted(t, batches, "1", 6600)

	var log strings.Builder
	if err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: result, held: 3000}, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}

	if !strings.Contains(log.String(), "the batch holds 3000 MB already, so it needs 3600 MB more") {
		t.Fatalf("%s", log.String())
	}
}

func TestABlockingWaitLeavesTheBatchTimeToFinish(t *testing.T) {
	for _, c := range []struct {
		minutes int
		seconds float64
		want    time.Duration
	}{
		{120, 0, 60 * time.Minute},
		{120, 1200, 120*time.Minute - 30*time.Minute - limitMargin},
		{30, 1800, memoryWait(30)},
	} {
		if got := blockingWait(c.minutes, c.seconds); got != c.want {
			t.Errorf("%d minutes, %v s: %s, want %s", c.minutes, c.seconds, got, c.want)
		}
	}
}

// startBroker serves a broker over the fake machine at root.
func startBroker(t *testing.T, ramp time.Duration) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	b := &membroker.Broker{
		Read: func() (memory.Reading, float64, error) {
			r, err := memory.Read(memoryRoot)

			return r, memory.Pressure(memoryRoot), err
		},
		Ramp: ramp, Every: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = b.Serve(ctx, l) }() //nolint:errcheck // ends with the test

	return l.Addr().String()
}

// Two batches that would both fit by the free memory alone are granted one after the other by the
// broker: the second waits until the first's grant has ramped out.
func TestABrokerGrantsBatchesStartingAtOnceOneAfterTheOther(t *testing.T) {
	fakeMemory(t, 8000, 50*time.Millisecond)
	addr := startBroker(t, 300*time.Millisecond)

	batches := t.TempDir()
	predicted(t, batches, "1", 6000)
	predicted(t, batches, "2", 6000)

	var logs [2]strings.Builder

	done := make(chan time.Time, 2)

	for i, batch := range []string{"1", "2"} {
		go func() {
			err := awaitMemory(t.Context(), gate{batches: batches, batch: batch, suite: "e2e", minutes: 120, result: t.TempDir(), block: true, broker: addr}, &logs[i])
			if err != nil {
				t.Error(err)
			}

			done <- time.Now()
		}()
	}

	first, second := <-done, <-done
	if second.Sub(first) < 200*time.Millisecond {
		t.Fatalf("granted %s apart:\n%s\n%s", second.Sub(first), logs[0].String(), logs[1].String())
	}

	for i := range logs {
		if !strings.Contains(logs[i].String(), "the worker's broker granted 6000 MB") {
			t.Fatalf("batch %d: %s", i+1, logs[i].String())
		}
	}
}

func TestAnUnreachableBrokerLeavesTheBatchItsOwnReading(t *testing.T) {
	fakeMemory(t, 8000, 50*time.Millisecond)

	batches := t.TempDir()
	predicted(t, batches, "1", 6000)

	var log strings.Builder
	if err := awaitMemory(t.Context(), gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: t.TempDir(), block: true, broker: "127.0.0.1:1"}, &log); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(log.String(), "memory broker unreachable") || !strings.Contains(log.String(), "the batch waits by its own reading") {
		t.Fatalf("%s", log.String())
	}
}

func TestTheGatewayIsTheDefaultRoutes(t *testing.T) {
	root := fakeMemory(t, 8000, time.Minute)
	writeFile(t, filepath.Join(root, "proc", "net", "route"), "Iface\tDestination\tGateway \tFlags\n"+
		"eth0\t0050A8C0\t00000000\t0001\n"+
		"eth0\t00000000\t0150A8C0\t0003\n")

	if got, err := brokerAddr("gateway:7480"); err != nil || got != "192.168.80.1:7480" {
		t.Fatalf("%q %v", got, err)
	}

	if got, err := brokerAddr("10.0.0.5:7480"); err != nil || got != "10.0.0.5:7480" {
		t.Fatalf("%q %v", got, err)
	}
}

func setCPUPressure(t *testing.T, root string, avg10 float64) {
	t.Helper()

	writeFile(t, filepath.Join(root, "proc", "pressure", "cpu"), fmt.Sprintf("some avg10=%.2f avg60=0.00 avg300=0.00 total=1\n", avg10))
}

// predictedUse writes a batch's prediction of memory, cores and seconds.
func predictedUse(t *testing.T, batches, batch string, mb int, cores, seconds float64) {
	t.Helper()

	writeFile(t, filepath.Join(batches, batch, predictedFile), fmt.Sprintf(`{"predicted_mb": %d, "why": "the test's", "cores": %v, "seconds": %v}`, mb, cores, seconds))
}

// cpuGate is batch 1 of e2e waiting for CPU below 40% pressure, 1 core when its prediction has none.
func cpuGate(batches, result string) gate {
	return gate{batches: batches, batch: "1", suite: "e2e", minutes: 120, result: result, cpu: true, cpuPressure: 40, cores: 1}
}

func TestABatchThatWaitsForCPUStartsOnceThePressureEases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cpu   bool
		waits bool
	}{
		{"cpu.wait", true, true},
		{"no cpu.wait", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeMemory(t, 8000, time.Minute)
			setCPUPressure(t, root, 70)

			batches, result := t.TempDir(), t.TempDir()
			predicted(t, batches, "1", 1000)

			eased := make(chan time.Time, 1)

			go func() {
				time.Sleep(100 * time.Millisecond)

				at := time.Now()

				setCPUPressure(t, root, 10)

				eased <- at
			}()

			g := cpuGate(batches, result)
			g.cpu = tc.cpu

			var log strings.Builder
			if err := awaitMemory(t.Context(), g, &log); err != nil {
				t.Fatalf("%v\n%s", err, log.String())
			}

			started := time.Now()
			if at := <-eased; started.After(at) != tc.waits {
				t.Fatalf("started %s after the pressure eased:\n%s", started.Sub(at), log.String())
			}

			if tc.waits && !strings.Contains(log.String(), "memory: 8000 MB free of 1000 MB predicted, CPU pressure 10.0% after ") {
				t.Fatalf("%s", log.String())
			}
		})
	}
}

func TestABatchStillShortOfCPULeavesItsSlicesNotRunAndSaysCPU(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prediction func(t *testing.T, batches string)
	}{
		{"with a prediction", func(t *testing.T, batches string) { t.Helper(); predicted(t, batches, "1", 1000) }},
		// a suite without peaks still waits for its CPU, its memory need none
		{"without one", func(*testing.T, string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fakeMemory(t, 8000, 50*time.Millisecond)
			setCPUPressure(t, root, 70)

			batches, result := t.TempDir(), t.TempDir()
			tc.prediction(t, batches)

			err := awaitMemory(t.Context(), cpuGate(batches, result), io.Discard)
			if !errors.Is(err, errShortOfCPU) || !strings.Contains(err.Error(), "a batch of e2e: the worker's CPU pressure was 70%, at or above the 40% a batch waits below (cpu.pressure)") {
				t.Fatalf("%v", err)
			}

			if got := shortOfMemoryIn(result); !strings.HasPrefix(got, "the worker's CPU pressure was 70%, at or above the 40% a batch waits below (cpu.pressure), after waiting ") {
				t.Fatalf("%q", got)
			}
		})
	}
}

func TestAnUnreadableCPUPressureHoldsNoBatchBack(t *testing.T) {
	fakeMemory(t, 8000, 50*time.Millisecond)

	batches, result := t.TempDir(), t.TempDir()
	predicted(t, batches, "1", 1000)

	if err := awaitMemory(t.Context(), cpuGate(batches, result), io.Discard); err != nil || shortOfMemoryIn(result) != "" {
		t.Fatalf("%v", err)
	}
}

// startCPUBroker serves a broker over the fake machine at root that grants at most cpus cores at once.
func startCPUBroker(t *testing.T, ramp time.Duration, cpus float64) string {
	t.Helper()

	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	b := &membroker.Broker{
		Read: func() (memory.Reading, float64, error) {
			r, err := memory.Read(memoryRoot)

			return r, memory.Pressure(memoryRoot), err
		},
		Ramp: ramp, Every: 10 * time.Millisecond, CPUs: cpus, CPUHold: 40,
		ReadCPU: func() float64 { return memory.CPUPressure(memoryRoot) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() { _ = b.Serve(ctx, l) }() //nolint:errcheck // ends with the test

	return l.Addr().String()
}

// Two batches the memory fits at once but whose cores don't: the second waits until the first's batch
// ends and releases its cores, though its lease runs for minutes.
func TestABrokerGrantsTheCoresOfABatchThatEndedToTheNext(t *testing.T) {
	fakeMemory(t, 8000, 50*time.Millisecond)
	addr := startCPUBroker(t, 10*time.Millisecond, 4)

	batches := t.TempDir()
	predictedUse(t, batches, "1", 1000, 3, 600)
	predictedUse(t, batches, "2", 1000, 3, 600)

	first, second := cpuGate(batches, t.TempDir()), cpuGate(batches, t.TempDir())
	first.block, first.broker = true, addr
	second.block, second.broker, second.batch = true, addr, "2"

	var logs [2]strings.Builder
	if err := awaitMemory(t.Context(), first, &logs[0]); err != nil || !strings.Contains(logs[0].String(), "the worker's broker granted 1000 MB and 3.0 cores") {
		t.Fatalf("%v\n%s", err, logs[0].String())
	}

	done := make(chan error, 1)

	go func() { done <- awaitMemory(t.Context(), second, &logs[1]) }()

	select {
	case err := <-done:
		t.Fatalf("the second started beside the first's cores: %v\n%s", err, logs[1].String())
	case <-time.After(200 * time.Millisecond):
	}

	first.releaseCores(t.Context(), &logs[0])

	select {
	case err := <-done:
		if err != nil || !strings.Contains(logs[1].String(), "the worker's broker granted 1000 MB and 3.0 cores") {
			t.Fatalf("%v\n%s", err, logs[1].String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the second still waits after the first released its cores:\n%s", logs[1].String())
	}
}

// A suite of steps' batch hands its cores back at its end: a second batch is granted them at once,
// though the first's lease would hold them for 15 minutes.
func TestSliceRunReleasesItsCoresAtItsEnd(t *testing.T) {
	root := memoryBatch(t).root
	mem := fakeMemory(t, 8000, 50*time.Millisecond)
	setCPUPressure(t, mem, 0)

	addr := startCPUBroker(t, 10*time.Millisecond, 4)
	writeFile(t, filepath.Join(root, config.FileName), strings.Replace(reportConfig, "    run_slice: ci/tasks/e2e-slice.yml\n",
		"    steps:\n      - {run: command, with: {each: [\"true\"]}}\n", 1)+"cpu: {wait: true}\nmemory: {broker: \""+addr+"\"}\n")
	predictedUse(t, "batches", "1", 1000, 3, 600)

	if _, stderr, code := run(t, "slice", "run", "-root", root); code != 0 {
		t.Fatalf("slice run: %d %s", code, stderr)
	}

	if ans, err := membroker.Ask(t.Context(), addr, membroker.Request{MB: 100, WaitSeconds: 0.5, Who: "next", CPU: true, Cores: 3}); err != nil || !ans.Granted {
		t.Fatalf("the next batch: %+v %v", ans, err)
	}
}

// A batch asks the broker for its predicted cores, held for half again its expected time, and without a
// prediction for the suite's cpu.cores, held for as long as it may wait.
func TestABatchAsksForItsCoresForHalfAgainItsTime(t *testing.T) {
	g := cpuGate("", "")

	if req := g.ask(1000, time.Hour, prediction{MB: 1000, Cores: 2.5, Seconds: 600}); !req.CPU || req.Cores != 2.5 || req.LeaseSeconds != 900 || req.MB != 1000 {
		t.Fatalf("predicted: %+v", req)
	}

	if req := g.ask(0, time.Hour, prediction{}); !req.CPU || req.Cores != 1 || req.LeaseSeconds != 3600 {
		t.Fatalf("unpredicted: %+v", req)
	}

	g.cpu = false
	if req := g.ask(1000, time.Hour, prediction{MB: 1000, Cores: 2.5, Seconds: 600}); req.CPU || req.Cores != 0 || req.LeaseSeconds != 0 {
		t.Fatalf("no cpu.wait: %+v", req)
	}
}

func TestABatchsCoresAreItsBusiestSlices(t *testing.T) {
	cores := map[string][]float64{"a": {1, 2}, "b": {0.5}}

	if got := predictCores(cores, []string{"a", "b", "new"}); math.Abs(got-2.2) > 1e-9 {
		t.Fatalf("%v", got)
	}

	if got := predictCores(cores, []string{"new"}); got != 0 {
		t.Fatalf("no slice with cores: %v", got)
	}
}
