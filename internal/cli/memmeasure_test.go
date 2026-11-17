package cli

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/report"
)

func TestEachSliceRecordsItsCPUAndPeakAndAnOOMKillCrashesIt(t *testing.T) {
	m := memoryBatch(t)
	root, s, states := m.root, m.store, m.states
	mem := fakeMemory(t, 8000, time.Minute)

	step := func(args ...string) {
		t.Helper()

		if _, stderr, code := run(t, append(append([]string{"slice"}, args...), "-root", root)...); code != 0 {
			t.Fatalf("slice %v: %d %s", args, code, stderr)
		}
	}

	setCgroup(t, mem, 10, 1000, 1000, 0)
	step("start", "e2e.a")
	setCgroup(t, mem, 14, 2500, 3000, 0)
	writeFile(t, "result/e2e.a.json", `{"passed": true, "tests": 3}`)
	step("end", "e2e.a")
	step("start", "e2e.b")
	setCgroup(t, mem, 15, 1200, 3000, 1)
	writeFile(t, "result/e2e.b.json", `{"passed": true, "tests": 2}`)
	step("end", "e2e.b")

	if _, stderr, code := run(t, "report", "batch-end", "-root", root); code != 1 || !strings.Contains(stderr, "e2e.b errored") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	got := states()

	if a := got["e2e.a"]; a.State != report.Passed || a.CPUSeconds != 4 || a.PeakMB != 3000 {
		t.Errorf("e2e.a: %+v", a)
	}

	// the batch had its 2500 MB and 8000 MB free when e2e.b started
	b := got["e2e.b"]
	if b.State != report.Errored || b.CPUSeconds != 1 || b.PeakMB != 10500 || len(b.Failures) == 0 || b.Failures[0].Test != report.OOMTest ||
		!strings.HasPrefix(b.Failures[0].Tail, "crashed: the OOM killer killed 1 processes of the batch while the slice ran; the batch had room for 10500 MB") {
		t.Errorf("e2e.b: %+v", b)
	}

	if _, _, code := run(t, "report", "suite", "-root", root, "-batches", "batches"); code != 1 {
		t.Fatalf("suite: %d", code)
	}

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if _, ok, err := s.First(t.Context(), []string{id.ShortRef()}); err != nil || !ok {
		t.Fatalf("a run whose slice crashed for want of memory isn't handed on: %v %v", ok, err)
	}
}

func TestATrustedRunKeepsItsPeaksOnceAndBatchesPredictFromThem(t *testing.T) { keepsPeaks(t) }

func TestATrustedRunKeepsItsPeaksOnABranch(t *testing.T) { onBranch(t, keepsPeaks) }

func keepsPeaks(t *testing.T) {
	t.Helper()

	h := handedHead(t)
	cfg := loadConfig(t, h.root)

	trusted := reportID(h.ch)
	trusted.Proposed = false

	end := func(peaks map[string]int64) {
		t.Helper()

		rep := &report.Reporter{Config: cfg, ID: trusted, Store: h.store, GitHub: host(cfg), Warn: io.Discard}

		var evs []report.SliceEvent

		for name, mb := range peaks {
			result := filepath.Join(t.TempDir(), "r.json")
			writeFile(t, result, `{"passed": true, "tests": 1}`)
			evs = append(evs, report.SliceEvent{Suite: "e2e", Slice: name, Label: "@pipelinegen_deps//slices:" + name, Result: result, Used: &report.Use{CPU: 1, Peak: mb}})
		}

		if _, err := rep.Slices(t.Context(), evs); err != nil {
			t.Fatal(err)
		}

		if _, err := rep.Suite(t.Context(), "e2e"); err != nil {
			t.Fatal(err)
		}
	}

	end(map[string]int64{"e2e/a.spec.ts": 2000, "e2e/b.spec.ts": 4000})
	// the suite's end told again, as a rerun's is, adds no second peak of the same commit
	end(map[string]int64{"e2e/a.spec.ts": 2000})

	peaks, err := report.Peaks(t.Context(), h.store, "e2e")
	if err != nil || len(peaks["e2e/a.spec.ts"]) != 1 || len(peaks["e2e/b.spec.ts"]) != 1 {
		t.Fatalf("peaks: %v %v", peaks, err)
	}

	out := filepath.Join(t.TempDir(), "batches")
	if stdout, stderr, code := run(t, "batches", "-run", "pr", "-suite", "e2e", "-trigger", h.trigger, "-out", out, "-root", h.trigger); code != 0 {
		t.Fatalf("batches: %d %s %s", code, stdout, stderr)
	}

	// a and b passed: only c is dealt, into batch 0
	if got := readFile(t, filepath.Join(out, "batches.json")); strings.TrimSpace(got) != `["0"]` {
		t.Fatalf("batches: %s", got)
	}

	var p prediction
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, "0", predictedFile))), &p); err != nil {
		t.Fatal(err)
	}

	// c has no peaks: the suite's median of a's 2200 and b's 4400
	if p.MB != 4400 || !strings.Contains(p.Why, "1 of 1 without any at the suite's median, 4400 MB") {
		t.Fatalf("c's batch: %+v", p)
	}
}
