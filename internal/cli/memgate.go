package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/memory"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// Where the memory is read, how often a short batch reads it again, the longest it waits and how
// often a batch of steps samples its memory; tests point them at fake files and a fast clock.
var (
	memoryRoot    = "/"
	memoryEvery   = 5 * time.Second
	memoryMaxWait = 5 * time.Minute
	memorySample  = 250 * time.Millisecond
)

// A batch starts only while the machine's memory pressure (PSI "some avg10") is below this, in percent.
const pressureAdmit = 10

const (
	// predictedFile, beside a batch's slices.json, is the peak `batches` predicts for the batch.
	predictedFile = "memory.json"
	// memoryFile, in the batch's result, says why the batch never started its slices; its end reads it.
	memoryFile = "memory.json"
)

var (
	errShortOfMemory = errors.New("the worker is short of memory")
	errNoMemoryHere  = errors.New("not enough free memory")
)

type prediction struct {
	MB  int64  `json:"predicted_mb"`
	Why string `json:"why"`
}

// predict is a batch's peak: the most any slice should reach, each its recent peaks' 90th percentile and
// a tenth more, one without any at the suite's median; a suite with no peaks predicts nothing.
func predict(peaks map[string][]int64, names []string) (prediction, bool) {
	var all []int64

	for _, name := range slices.Sorted(maps.Keys(peaks)) {
		if p := memory.Predict(peaks[name]); p > 0 {
			all = append(all, p)
		}
	}

	if len(all) == 0 {
		return prediction{}, false
	}

	slices.Sort(all)
	median := all[len(all)/2]

	var (
		most    int64
		unknown int
	)

	for _, name := range names {
		p := memory.Predict(peaks[name])
		if p == 0 {
			p, unknown = median, unknown+1
		}

		most = max(most, p)
	}

	why := fmt.Sprintf("the most its slices should use, each the 90th percentile of up to %d recent peaks and %d%% more", report.PeaksKept, memory.Headroom)
	if unknown > 0 {
		why += fmt.Sprintf("; %d of %d without any at the suite's median, %d MB", unknown, len(names), median)
	}

	return prediction{MB: most, Why: why}, true
}

// writePredictions puts each batch's predicted peak beside its slices; a suite without peaks gets none,
// and its batches start unchecked. Peaks that can't be read only warn.
func writePredictions(ctx context.Context, store *gitstore.Store, out, suite string, dealt [][]string, log io.Writer) error {
	peaks, err := report.Peaks(ctx, store, suite)
	if err != nil {
		warnf(log, "memory: %v; the batches start unchecked\n", err)

		return nil
	}

	for i, names := range dealt {
		p, ok := predict(peaks, names)
		if !ok {
			return nil
		}

		data, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("batches: %w", err)
		}

		if err := os.WriteFile(filepath.Join(out, strconv.Itoa(i), predictedFile), append(data, '\n'), 0o600); err != nil {
			return fmt.Errorf("batches: %w", err)
		}
	}

	return nil
}

func readPrediction(batches, batch string) (prediction, bool) {
	data, err := os.ReadFile(filepath.Join(batches, batch, predictedFile)) //nolint:gosec // the run's own batch
	if err != nil {
		return prediction{}, false
	}

	var p prediction
	if json.Unmarshal(data, &p) != nil || p.MB <= 0 {
		return prediction{}, false
	}

	return p, true
}

type memoryShort struct {
	Free      int64   `json:"free_mb"`
	Predicted int64   `json:"predicted_mb"`
	Pressure  float64 `json:"psi"`
	Waited    float64 `json:"waited_seconds"`
	Reading   string  `json:"reading"`
}

func (m memoryShort) why() string {
	s := fmt.Sprintf("the worker had %d MB free of %d MB predicted", m.Free, m.Predicted)
	if m.Free >= m.Predicted {
		s += fmt.Sprintf(", under memory pressure (%.0f%%)", m.Pressure)
	}

	return s + fmt.Sprintf(", after waiting %s", time.Duration(m.Waited*float64(time.Second)).Round(time.Second))
}

// memoryWait is the longest a batch waits for memory: 5 minutes, or a tenth of the suite's time limit
// when that is shorter, since the wait counts against the batch's own limit.
func memoryWait(minutes int) time.Duration {
	return min(memoryMaxWait, time.Duration(minutes)*time.Minute/10)
}

func admits(r memory.Reading, pressure float64, need int64) bool {
	return r.Free() >= need && pressure < pressureAdmit
}

// awaitMemory waits while the worker has less free than the batch's predicted peak, or is under pressure;
// still short, it writes why into the result for its end and fails. Unreadable memory starts unchecked.
func awaitMemory(ctx context.Context, batches, batch, suite string, minutes int, result string, log io.Writer) error {
	r, err := memory.Read(memoryRoot)
	if err != nil {
		warnf(log, "memory: %v; the batch starts unchecked\n", err)

		return nil
	}

	pressure := memory.Pressure(memoryRoot)

	p, ok := readPrediction(batches, batch)
	if !ok {
		warnf(log, "memory: %s, pressure %s; %s has no peaks measured yet, so the batch doesn't wait for memory\n", r, psi(pressure), suite)

		return nil
	}

	warnf(log, "memory: %s, pressure %s; this batch should use %d MB: %s\n", r, psi(pressure), p.MB, p.Why)

	// waiting for room that never comes would leave its slices never run
	if c := r.Capacity(); p.MB > c {
		warnf(log, "memory: %d MB predicted is more than the %d MB this worker could ever give: the batch starts unchecked\n", p.MB, c)

		return nil
	}

	limit := memoryWait(minutes)
	start := time.Now()
	told := start

	for !admits(r, pressure, p.MB) {
		waited := time.Since(start)
		if waited >= limit {
			short := memoryShort{Free: r.Free(), Predicted: p.MB, Pressure: pressure, Waited: waited.Seconds(), Reading: r.String()}

			return leaveNotRun(result, suite, short, log)
		}

		if time.Since(told) >= time.Minute {
			told = time.Now()

			warnf(log, "memory: %d MB free of %d MB predicted, pressure %s: waiting, %s of at most %s\n", r.Free(), p.MB, psi(pressure), waited.Round(time.Second), limit)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("memory: %w", ctx.Err())
		case <-time.After(min(memoryEvery, limit-waited)):
		}

		if r, err = memory.Read(memoryRoot); err != nil {
			warnf(log, "memory: %v; the batch starts unchecked\n", err)

			return nil
		}

		pressure = memory.Pressure(memoryRoot)
	}

	if waited := time.Since(start); waited >= memoryEvery {
		warnf(log, "memory: %d MB free of %d MB predicted after %s: the batch starts\n", r.Free(), p.MB, waited.Round(time.Second))
	}

	return nil
}

func psi(p float64) string {
	if p < 0 {
		return "unknown"
	}

	return fmt.Sprintf("%.1f%%", p)
}

func leaveNotRun(result, suite string, short memoryShort, log io.Writer) error {
	data, err := json.Marshal(short)
	if err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	dir := filepath.Join(result, report.JournalDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, memoryFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	warnf(log, "memory: %s: the batch's slices are left not run; a pull request's sweep hands its head on again once its run has ended, and a branch's next round selects them again\n", short.why())

	return fmt.Errorf("%w: a batch of %s: %s", errShortOfMemory, suite, short.why())
}

// shortOfMemoryIn is why a batch never started its slices, as it wrote it, empty when it did start.
func shortOfMemoryIn(result string) string {
	data, err := os.ReadFile(filepath.Join(result, report.JournalDir, memoryFile)) //nolint:gosec // the batch's own result
	if err != nil {
		return ""
	}

	var short memoryShort
	if err := json.Unmarshal(data, &short); err != nil || short.Predicted == 0 {
		return ""
	}

	return short.why()
}

// localMemory refuses a batch this machine hasn't the free memory for by the store's peaks, waiting for
// nothing since a person is there to free some; peaks it can't read, or none, check nothing.
func localMemory(ctx context.Context, peaks func(context.Context) (map[string][]int64, error), suite string, names []string) error {
	known, err := peaks(ctx)
	if err != nil {
		return nil //nolint:nilerr // without the shared peaks the batch runs unchecked
	}

	p, ok := predict(known, names)
	if !ok {
		return nil
	}

	r, err := memory.Read(memoryRoot)
	if err != nil || r.Free() >= p.MB || p.MB > r.Capacity() {
		return nil //nolint:nilerr // a machine whose memory can't be read runs the batch unchecked
	}

	return fmt.Errorf("%w: a batch of %s should use %d MB (%s) and this machine has %s; close what you can spare and run it again",
		errNoMemoryHere, suite, p.MB, p.Why, r)
}

// ready finds the plugins, installs the suite's packages and then refuses a batch this machine hasn't the
// memory for, before the setup, in CI's order: install, then the wait for memory.
func (b *localBuild) ready(ctx context.Context, c *common, slices []string) error {
	return b.readyWith(ctx, sharedPeaks(c, b.suite), slices)
}

func (b *localBuild) readyWith(ctx context.Context, peaks func(context.Context) (map[string][]int64, error), slices []string) error {
	if err := b.findPlugins(ctx); err != nil {
		return err
	}

	if err := b.installPackages(ctx); err != nil {
		return err
	}

	return localMemory(ctx, peaks, b.suite, slices)
}

// sharedPeaks reads a suite's peaks from the store the config names, for a run on this machine.
func sharedPeaks(c *common, suite string) func(context.Context) (map[string][]int64, error) {
	return func(ctx context.Context) (out map[string][]int64, err error) {
		cfg, err := c.load()
		if err != nil {
			return nil, err
		}

		if withOrigin(ctx, cfg, c.root); codeRemote(cfg) == "" {
			return nil, nil
		}

		store := openStore(cfg)
		store.ReadOnly = true

		defer closeStore(store, &err)

		return report.Peaks(ctx, store, suite)
	}
}

// sliceMemory is a task file's own call before its setup: the wait slice run does for a suite of steps.
func sliceMemory(ctx context.Context, args []string) error {
	var (
		c               common
		batches, result string
	)

	fs := flag.NewFlagSet(sliceCommand, flag.ContinueOnError)
	c.register(fs)
	fs.Lookup("root").DefValue = treeDir
	c.root = treeDir

	pathVar(fs, &batches, "batches", "batches", "the directory of the run's batches", reads)
	pathVar(fs, &result, "result", "result", "the batch's result directory, where a batch short of memory says so", writes)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if err := needEnv(envSuite, envBatch); err != nil {
		return err
	}

	c.history = true

	cfg, err := c.load()
	if err != nil {
		return err
	}

	run, suite := os.Getenv(envRun), os.Getenv(envSuite)

	// on this machine run and record checked before the batch
	if run == localRun {
		return nil
	}

	return awaitMemory(ctx, batches, os.Getenv(envBatch), suite, cfg.SuiteMinutes(run, suite), result, os.Stderr)
}

// admit waits for a batch's memory, then samples each slice's use until stop; on this machine run and
// record checked beforehand and nothing is measured, since its cgroup holds more than the batch.
func (s *step) admit(ctx context.Context, f drivenFlags, batch, suite string) (stop func(), err error) {
	run := s.reporter.ID.Run
	if run == localRun {
		return func() {}, nil
	}

	if err := awaitMemory(ctx, f.batches, batch, suite, s.reporter.Config.SuiteMinutes(run, suite), f.result, os.Stderr); err != nil {
		return nil, err
	}

	var sm sampler

	sampling, stop := context.WithCancel(ctx)

	go sm.run(sampling)

	s.reporter.Meter = sm.meter()

	return stop, nil
}

// cgroupMeter reads the task's cgroup at each slice's start and end; separate processes can't watch
// between the two, so a slice's peak is the batch's up to the slice's end.
func cgroupMeter() report.Meter {
	return func(bool) *report.Use {
		u, err := memory.Measure(memoryRoot)
		if err != nil {
			return nil
		}

		return &report.Use{CPU: u.CPU, Peak: max(u.Set, u.Peak), OOM: u.OOM, Room: u.Set + u.Free, Pressure: u.Pressure}
	}
}

// sampler watches the working set of a batch of steps, which runs its slices one at a time: a slice's
// peak is the most it saw from the slice's start.
type sampler struct {
	mu   sync.Mutex
	peak int64
}

func (s *sampler) meter() report.Meter {
	return func(start bool) *report.Use {
		u, err := memory.Measure(memoryRoot)
		if err != nil {
			return nil
		}

		s.mu.Lock()
		defer s.mu.Unlock()

		if start {
			s.peak = 0
		}

		s.peak = max(s.peak, u.Set)

		return &report.Use{CPU: u.CPU, Peak: s.peak, OOM: u.OOM, Room: u.Set + u.Free, Pressure: u.Pressure}
	}
}

func (s *sampler) run(ctx context.Context) {
	t := time.NewTicker(memorySample)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		if u, err := memory.Measure(memoryRoot); err == nil {
			s.mu.Lock()
			s.peak = max(s.peak, u.Set)
			s.mu.Unlock()
		}
	}
}
