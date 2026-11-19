package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
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

	// the slices' predictions are 1320, 2200 and 3300: a new slice takes 2200
	if p, ok := predict(peaks, []string{"a", "new"}); !ok || p.MB != 2200 || !strings.Contains(p.Why, "1 of 2 without any at the suite's median, 2200 MB") {
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
	if err := awaitMemory(t.Context(), batches, "1", "e2e", 120, t.TempDir(), &log); err != nil {
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

	if err := awaitMemory(t.Context(), batches, "1", "e2e", 120, result, &log); err != nil {
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

	err := awaitMemory(t.Context(), batches, "1", "e2e", 120, result, io.Discard)
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
	if err := awaitMemory(t.Context(), t.TempDir(), "1", "e2e", 120, result, &log); err != nil {
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
	if err := awaitMemory(t.Context(), batches, "1", "e2e", 120, result, &log); err != nil {
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

	if got := api.status("pipelinegen/pr/e2e"); got != "error 0 of 2 slices ran, 2 not run (worker short of memory)" {
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
