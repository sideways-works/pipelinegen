package cli

import (
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/membroker"
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
	errShortOfCPU    = errors.New("the worker is short of CPU")
	errNoMemoryHere  = errors.New("not enough free memory")
)

type prediction struct {
	MB  int64  `json:"predicted_mb"`
	Why string `json:"why"`
	// Seconds is how long the batch should take by the suite's durations, 0 when unknown: a batch
	// that waits for memory waits only as long as it could still finish within its time limit.
	Seconds float64 `json:"seconds,omitempty"`
	// Cores is the most cores any of the batch's slices should keep busy, since the batch runs them one
	// at a time: each its recent cores' 90th percentile and a tenth more. 0 is unknown, a batch none of
	// whose slices has recorded cores, and the suite's cpu.cores stands for it.
	Cores float64 `json:"cores,omitempty"`
}

// predictCores is the most cores any of names should keep busy by their recorded cores, 0 for none.
func predictCores(cores map[string][]float64, names []string) float64 {
	var most float64

	for _, name := range names {
		most = max(most, memory.PredictCores(cores[name]))
	}

	return most
}

// predict is a batch's peak: the most any slice should reach, each its recent peaks' 90th percentile and
// a tenth more, one without any at the 90th percentile of the suite's others, so a suite measured only in
// part errs high; a suite with no peaks predicts nothing.
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
	high := all[(len(all)*9)/10]

	var (
		most    int64
		unknown int
	)

	for _, name := range names {
		p := memory.Predict(peaks[name])
		if p == 0 {
			p, unknown = high, unknown+1
		}

		most = max(most, p)
	}

	why := fmt.Sprintf("the most its slices should use, each the 90th percentile of up to %d recent peaks and %d%% more", report.PeaksKept, memory.Headroom)
	if unknown > 0 {
		why += fmt.Sprintf("; %d of %d without any at the 90th percentile of the suite's others, %d MB", unknown, len(names), high)
	}

	return prediction{MB: most, Why: why}, true
}

// writePredictions puts each batch's predicted peak and cores beside its slices; a batch with neither
// gets none, and starts unchecked. Peaks that can't be read only warn.
func writePredictions(ctx context.Context, store *gitstore.Store, out, suite string, dealt [][]string, secs []float64, log io.Writer) error {
	peaks, err := report.Peaks(ctx, store, suite)
	if err != nil {
		warnf(log, "memory: %v; the batches start unchecked\n", err)

		return nil
	}

	// the cores are in the peaks' own file: read once, it reads again
	cores, err := report.Cores(ctx, store, suite)
	if err != nil {
		cores = nil
	}

	for i, names := range dealt {
		p, ok := predict(peaks, names)
		if p.Cores = predictCores(cores, names); !ok && p.Cores == 0 {
			continue
		}

		if i < len(secs) {
			p.Seconds = secs[i]
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

	// a prediction of a batch's cores alone predicts no peak: its MB is 0
	var p prediction
	if json.Unmarshal(data, &p) != nil || p.MB < 0 || p.Cores < 0 || p.MB == 0 && p.Cores == 0 {
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
	// CPU, when the memory was there and the worker's CPU was not, says what the CPU was.
	CPU string `json:"cpu,omitempty"`
}

func (m memoryShort) why() string {
	if m.CPU != "" {
		return m.CPU + fmt.Sprintf(", after waiting %s", time.Duration(m.Waited*float64(time.Second)).Round(time.Second))
	}

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

// blockingWait is the longest a batch of a suite that waits for memory (memory.wait) waits: for as long
// as it could still finish within its time limit, its expected time half again and a margin left over;
// a batch of no known time keeps half its limit for its slices. Never less than memoryWait.
func blockingWait(minutes int, seconds float64) time.Duration {
	limit := time.Duration(minutes) * time.Minute

	need := limit / 2
	if seconds > 0 {
		need = time.Duration(seconds*1.5*float64(time.Second)) + limitMargin
	}

	return max(limit-need, memoryWait(minutes))
}

// gate is a batch's wait for memory, and for CPU under cpu.wait.
type gate struct {
	batches, batch, suite, result string
	minutes                       int
	// block waits for as long as the batch could still finish (memory.wait or cpu.wait), not memoryWait.
	block bool
	// held is what the batch's own cgroup uses already, in MB: a task file's batch reaches
	// pipelinegen first at its first slice, when its set-up runs already.
	held int64
	// broker is memory.broker: the worker's broker a blocking wait asks.
	broker string
	// cpu waits for the worker's CPU too (cpu.wait): its pressure below cpuPressure, and with a broker
	// its cores granted lately leaving room for the batch's, cores when the prediction has none.
	cpu         bool
	cpuPressure float64
	cores       float64
}

// batchGate is the wait of a batch of suite by the config.
func batchGate(cfg *config.Config, run, suite, batches, batch, result string) gate {
	return gate{
		batches: batches, batch: batch, suite: suite, result: result, minutes: cfg.SuiteMinutes(run, suite),
		block: cfg.WaitsForMemory(suite) || cfg.WaitsForCPU(suite), broker: cfg.MemoryBroker(suite),
		cpu: cfg.WaitsForCPU(suite), cpuPressure: cfg.CPUPressure(suite), cores: cfg.CPUCores(suite),
	}
}

// who names the batch in the broker's log, and its grant for a release: a branch's batch has no change
// of its own, and its commit names it.
func (g gate) who() string {
	return fmt.Sprintf("%s %s %s batch %s", os.Getenv(envRun), cmp.Or(os.Getenv(envChange), short12(os.Getenv(envCommit))), g.suite, g.batch)
}

// coresOf is the cores the batch should keep busy: its prediction's, else the suite's cpu.cores.
func (g gate) coresOf(p prediction) float64 {
	if p.Cores > 0 {
		return p.Cores
	}

	return g.cores
}

// cpuAdmits is whether the worker's CPU pressure lets the batch start: always when it doesn't wait for
// CPU or the pressure can't be read.
func (g gate) cpuAdmits(cpu float64) bool {
	return !g.cpu || cpu < 0 || cpu < g.cpuPressure
}

// cpuShort says what the worker's CPU pressure was, for a batch whose wait ended short of CPU.
func (g gate) cpuShort(cpu float64) string {
	return fmt.Sprintf("the worker's CPU pressure was %.0f%%, at or above the %.0f%% a batch waits below (cpu.pressure)", cpu, g.cpuPressure)
}

// need is what the batch waits for: its prediction less what it holds already, logged with the reading.
func (g gate) need(r memory.Reading, pressure float64, p prediction, log io.Writer) int64 {
	warnf(log, "memory: %s, pressure %s; this batch should use %d MB: %s\n", r, psi(pressure), p.MB, p.Why)

	need := max(p.MB-g.held, 0)
	if g.held > 0 {
		warnf(log, "memory: the batch holds %d MB already, so it needs %d MB more\n", g.held, need)
	}

	return need
}

func (g gate) limit(p prediction) time.Duration {
	if g.block {
		return blockingWait(g.minutes, p.Seconds)
	}

	return memoryWait(g.minutes)
}

func admits(r memory.Reading, pressure float64, need int64) bool {
	return r.Free() >= need && pressure < pressureAdmit
}

// awaitMemory waits while the worker has less free than the batch's predicted peak, less what it holds
// already, or is under pressure, and under cpu.wait while its CPU pressure is at or above cpu.pressure;
// still short, it writes why into the result for its end and fails. Unreadable memory starts unchecked.
func awaitMemory(ctx context.Context, g gate, log io.Writer) error {
	r, err := memory.Read(memoryRoot)
	if err != nil {
		warnf(log, "memory: %v; the batch starts unchecked\n", err)

		return nil
	}

	pressure := memory.Pressure(memoryRoot)

	p, ok := readPrediction(g.batches, g.batch)

	need, wait := g.memoryNeed(r, pressure, p, ok, log)
	if !wait {
		return nil
	}

	limit := g.limit(p)

	if g.cpu {
		warnf(log, "cpu: pressure %s; this batch should keep %.1f cores busy, and waits while the pressure is at or above %.0f%%\n",
			psi(memory.CPUPressure(memoryRoot)), g.coresOf(p), g.cpuPressure)
	}

	if g.block && g.broker != "" {
		if done, err := g.askBroker(ctx, need, g.ask(need, limit, p), log); done {
			return err
		}
	}

	return g.awaitReading(ctx, r, pressure, need, limit, log)
}

// memoryNeed is the memory the batch waits for, and whether it waits at all: a suite without peaks, or a
// prediction no worker could give, waits for nothing but its CPU under cpu.wait.
func (g gate) memoryNeed(r memory.Reading, pressure float64, p prediction, ok bool, log io.Writer) (int64, bool) {
	if !ok || p.MB <= 0 {
		if !g.cpu {
			warnf(log, "memory: %s, pressure %s; %s has no peaks measured yet, so the batch doesn't wait for memory\n", r, psi(pressure), g.suite)

			return 0, false
		}

		warnf(log, "memory: %s, pressure %s; %s has no peaks measured yet, so the batch waits for its CPU alone\n", r, psi(pressure), g.suite)

		return 0, true
	}

	need := g.need(r, pressure, p, log)

	// waiting for room that never comes would leave its slices never run
	if c := r.Capacity(); p.MB > c {
		if !g.cpu {
			warnf(log, "memory: %d MB predicted is more than the %d MB this worker could ever give: the batch starts unchecked\n", p.MB, c)

			return 0, false
		}

		warnf(log, "memory: %d MB predicted is more than the %d MB this worker could ever give: the batch waits for its CPU alone\n", p.MB, c)

		return 0, true
	}

	return need, true
}

// ask is the batch's request of the broker: need, waiting at most limit, and under cpu.wait its cores,
// held for half again its expected time, else for as long as it may wait.
func (g gate) ask(need int64, limit time.Duration, p prediction) membroker.Request {
	req := membroker.Request{MB: need, WaitSeconds: limit.Seconds(), Who: g.who()}
	if !g.cpu {
		return req
	}

	req.CPU, req.Cores, req.LeaseSeconds = true, g.coresOf(p), limit.Seconds()
	if p.Seconds > 0 {
		req.LeaseSeconds = p.Seconds * 1.5
	}

	return req
}

// awaitReading waits by the worker's own reading until it admits the batch, at most limit; still short,
// the batch's slices are left not run.
func (g gate) awaitReading(ctx context.Context, r memory.Reading, pressure float64, need int64, limit time.Duration, log io.Writer) error {
	start := time.Now()
	told := start

	var err error

	cpu := g.cpuPressureNow()

	for !admits(r, pressure, need) || !g.cpuAdmits(cpu) {
		waited := time.Since(start)
		if waited >= limit {
			short := memoryShort{Free: r.Free(), Predicted: need, Pressure: pressure, Waited: waited.Seconds(), Reading: r.String()}
			if admits(r, pressure, need) {
				short.CPU = g.cpuShort(cpu)
			}

			return leaveNotRun(g.result, g.suite, short, log)
		}

		if time.Since(told) >= time.Minute {
			told = time.Now()

			warnf(log, "memory: %d MB free of %d MB predicted, pressure %s%s: waiting, %s of at most %s\n", r.Free(), need, psi(pressure), g.cpuSaid(cpu), waited.Round(time.Second), limit)
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

		pressure, cpu = memory.Pressure(memoryRoot), g.cpuPressureNow()
	}

	if waited := time.Since(start); waited >= memoryEvery {
		warnf(log, "memory: %d MB free of %d MB predicted%s after %s: the batch starts\n", r.Free(), need, g.cpuSaid(cpu), waited.Round(time.Second))
	}

	return nil
}

// cpuPressureNow is the worker's CPU pressure for a batch that waits for CPU, -1 for one that doesn't.
func (g gate) cpuPressureNow() float64 {
	if !g.cpu {
		return -1
	}

	return memory.CPUPressure(memoryRoot)
}

// cpuSaid is the CPU pressure in a line of the wait's log, nothing for a batch that doesn't wait for CPU.
func (g gate) cpuSaid(cpu float64) string {
	if !g.cpu {
		return ""
	}

	return ", CPU pressure " + psi(cpu)
}

// askBroker asks the worker's broker for req, waiting at most its wait: done with the broker's answer, or
// not done when it can't be reached, and the batch waits by its own reading.
func (g gate) askBroker(ctx context.Context, need int64, req membroker.Request, log io.Writer) (bool, error) {
	addr, err := brokerAddr(g.broker)
	if err != nil {
		warnf(log, "memory: %v; the batch waits by its own reading\n", err)

		return false, nil
	}

	start := time.Now()

	ans, err := membroker.AskAgain(ctx, addr, req)
	if err != nil {
		if ctx.Err() != nil {
			return true, fmt.Errorf("memory: %w", ctx.Err())
		}

		warnf(log, "memory: %v; the batch waits by its own reading\n", err)

		return false, nil
	}

	waited := time.Since(start)

	if ans.Granted {
		cores := ""
		if req.CPU {
			cores = fmt.Sprintf(" and %.1f cores", req.Cores)
		}

		warnf(log, "memory: the worker's broker granted %d MB%s after %s (%d MB free less %d MB granted lately): the batch starts\n",
			need, cores, waited.Round(time.Second), ans.FreeMB+ans.HeldMB-need, ans.HeldMB-need)

		return true, nil
	}

	short := memoryShort{Free: ans.FreeMB, Predicted: need, Waited: waited.Seconds(), Reading: "the broker: " + ans.Why}
	if req.CPU && ans.FreeMB >= need {
		short.CPU = "the worker's broker found no room for its CPU: " + ans.Why
	}

	return true, leaveNotRun(g.result, g.suite, short, log)
}

// releaseCores tells the worker's broker the batch has ended, so the cores it was granted count no
// longer; a broker that can't be told lets the grant's lease run out.
func (g gate) releaseCores(ctx context.Context, log io.Writer) {
	if !g.cpu || !g.block || g.broker == "" {
		return
	}

	addr, err := brokerAddr(g.broker)
	if err == nil {
		// the batch's own context may have ended with it
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		err = membroker.Release(ctx, addr, g.who())
	}

	if err != nil {
		warnf(log, "cpu: releasing the batch's cores: %v; they count until the grant's lease ends\n", err)
	}
}

// brokerAddr is memory.broker with the host "gateway" read as the task's default gateway.
func brokerAddr(broker string) (string, error) {
	host, port, err := net.SplitHostPort(broker)
	if err != nil {
		return "", fmt.Errorf("memory.broker %q: host:port: %w", broker, err)
	}

	if host != "gateway" {
		return broker, nil
	}

	gw, err := defaultGateway(memoryRoot)
	if err != nil {
		return "", fmt.Errorf("memory.broker %q: %w", broker, err)
	}

	return net.JoinHostPort(gw, port), nil
}

var errNoGateway = errors.New("no default gateway in /proc/net/route")

// defaultGateway is the default route's gateway in root/proc/net/route, the kernel's own table.
func defaultGateway(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "proc", "net", "route")) //nolint:gosec // the machine's own file
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoGateway, err)
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != "00000000" || f[2] == "00000000" {
			continue
		}

		// the gateway is a little-endian hex IPv4 address
		raw, err := hex.DecodeString(f[2])
		if err != nil || len(raw) != net.IPv4len {
			continue
		}

		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String(), nil
	}

	return "", errNoGateway
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

	what, cause := "memory:", errShortOfMemory
	if short.CPU != "" {
		what, cause = "cpu:", errShortOfCPU
	}

	warnf(log, "%s %s: the batch's slices are left not run; a pull request's sweep hands its head on again once its run has ended, and a branch's next round selects them again\n", what, short.why())

	return fmt.Errorf("%w: a batch of %s: %s", cause, suite, short.why())
}

// shortOfMemoryIn is why a batch never started its slices, as it wrote it, empty when it did start.
func shortOfMemoryIn(result string) string {
	data, err := os.ReadFile(filepath.Join(result, report.JournalDir, memoryFile)) //nolint:gosec // the batch's own result
	if err != nil {
		return ""
	}

	var short memoryShort
	if err := json.Unmarshal(data, &short); err != nil || short.Predicted == 0 && short.CPU == "" {
		return ""
	}

	return short.why()
}

// sayMemoryCoverage says, per suite the plan runs, how many of its selected slices have a recorded
// peak: a suite whose batches wait for memory (memory.wait) predicts a slice without one at the
// suite's median, so a worker can take more tasks at once when they all have one.
func sayMemoryCoverage(ctx context.Context, cfg *config.Config, store *gitstore.Store, plan *affected.Plan, out io.Writer) {
	for _, sp := range plan.Suites {
		if len(sp.Selected) == 0 {
			continue
		}

		peaks, err := report.Peaks(ctx, store, sp.Suite)
		if err != nil {
			continue
		}

		measured := 0

		for _, sl := range sp.Selected {
			if memory.Predict(peaks[sl.Name]) > 0 {
				measured++
			}
		}

		waits := "its batches start without waiting for memory"
		if cfg.WaitsForMemory(sp.Suite) {
			waits = "its batches wait for their predicted memory (memory.wait)"
		}

		_ = say(out, "memory: %s: %d of %d selected slices have a recorded peak; %s\n", sp.Suite, measured, len(sp.Selected), waits) //nolint:errcheck // a line of the log

		if cfg.WaitsForCPU(sp.Suite) {
			sayCoresCoverage(ctx, cfg, store, sp, out)
		}
	}
}

// sayCoresCoverage says how many of a suite's selected slices have recorded cores: a batch none of whose
// slices has any is predicted the suite's cpu.cores.
func sayCoresCoverage(ctx context.Context, cfg *config.Config, store *gitstore.Store, sp affected.SuitePlan, out io.Writer) {
	cores, err := report.Cores(ctx, store, sp.Suite)
	if err != nil {
		return
	}

	measured := 0

	for _, sl := range sp.Selected {
		if len(cores[sl.Name]) > 0 {
			measured++
		}
	}

	_ = say(out, "cpu: %s: %d of %d selected slices have recorded cores; its batches wait for their predicted cores (cpu.wait), %.1f for a batch of none\n", //nolint:errcheck // a line of the log
		sp.Suite, measured, len(sp.Selected), cfg.CPUCores(sp.Suite))
}

// envMeasure set to 1 has a batch on this machine measure its slices' memory as one on CI does: `memory
// sample` sets it for a batch in its image, whose container's cgroup holds the batch alone.
const envMeasure = "PIPELINEGEN_MEASURE"

func measuring() bool { return os.Getenv(envMeasure) == "1" }

// memoryAdmitted, in a task file's batch result, says its first slice's start waited for its memory.
const memoryAdmitted = ".memory-admitted"

// awaitMemoryOnce is a task file's batch's wait for memory, at its first slice's start, for a suite that
// waits for memory (memory.wait): the task file runs its own set-up before any pipelinegen step, so the
// batch needs what its prediction says less what its set-up holds already. Other suites' task files
// start unchecked, as they always did.
func awaitMemoryOnce(ctx context.Context, r *report.Reporter, batches, suite, result string) error {
	if r.ID.Run == localRun || !r.Config.WaitsForMemory(suite) && !r.Config.WaitsForCPU(suite) {
		return nil
	}

	mark := filepath.Join(result, memoryAdmitted)
	if _, err := os.Stat(mark); err == nil {
		return nil
	}

	var held int64
	if u, err := memory.Measure(memoryRoot); err == nil {
		held = u.Set
	}

	g := batchGate(r.Config, r.ID.Run, suite, batches, os.Getenv(envBatch), result)
	g.block, g.held = true, held

	if err := awaitMemory(ctx, g, os.Stderr); err != nil {
		return err
	}

	if err := os.MkdirAll(result, 0o750); err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	if err := os.WriteFile(mark, nil, 0o600); err != nil {
		return fmt.Errorf("memory: %w", err)
	}

	return nil
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

	return awaitMemory(ctx, batchGate(cfg, run, suite, batches, os.Getenv(envBatch), result), os.Stderr)
}

// releaseVerb is the slice subcommand that hands a batch's cores back.
const releaseVerb = "release"

// sliceRelease is a task file's own call at its batch's end, in its exit trap: it hands back the cores
// the worker's broker granted slice memory, so they count no longer once the batch is done. A finished
// task's container may stay on the worker for a while, so the broker's probe can't tell it ended. It
// only warns when the broker can't be told, and does nothing on this machine.
func sliceRelease(ctx context.Context, args []string) error {
	var c common

	fs := flag.NewFlagSet(sliceCommand, flag.ContinueOnError)
	c.register(fs)
	fs.Lookup("root").DefValue = treeDir
	c.root = treeDir

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if err := needEnv(envSuite, envBatch); err != nil {
		return err
	}

	run, suite := os.Getenv(envRun), os.Getenv(envSuite)
	if run == localRun {
		return nil
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	batchGate(cfg, run, suite, "", os.Getenv(envBatch), "").releaseCores(ctx, os.Stderr)

	return nil
}

// admit waits for a batch's memory, then samples each slice's use until stop, which also hands back
// the cores the worker's broker granted it; on this machine run and record checked beforehand and nothing
// is measured, since its cgroup holds more than the batch.
func (s *step) admit(ctx context.Context, f drivenFlags, batch, suite string) (stop func(), err error) {
	run := s.reporter.ID.Run
	if run == localRun {
		if measuring() {
			return s.sample(ctx), nil
		}

		return func() {}, nil
	}

	g := batchGate(s.reporter.Config, run, suite, f.batches, batch, f.result)
	if err := awaitMemory(ctx, g, os.Stderr); err != nil {
		return nil, err
	}

	sampled := s.sample(ctx)

	return func() {
		sampled()
		g.releaseCores(ctx, os.Stderr)
	}, nil
}

// sample measures each slice's use until stop.
func (s *step) sample(ctx context.Context) (stop func()) {
	var sm sampler

	sampling, stop := context.WithCancel(ctx)

	go sm.run(sampling)

	s.reporter.Meter = sm.meter()

	return stop
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
