package cli

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/membroker"
	"github.com/sideways-works/pipelinegen/internal/memory"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// envDeadline is when the pipeline's step must be done, in Unix seconds, as its task's script sets it.
const envDeadline = "PIPELINEGEN_DEADLINE"

// The most a step of the pipeline's own waits for its memory, and the share of what is left of its
// time it may spend waiting.
const (
	stepWaitMost  = 15 * time.Minute
	stepWaitShare = 3
	// stepWaitBare is the wait of a step whose deadline isn't known.
	stepWaitBare = 10 * time.Minute
)

// How long a step's cores are leased. A step that ends in pipelinegen releases them at once, so its lease
// matters only where that release never comes: a step killed, or a suite's task file, whose wait ends
// before its work and whose cores count until the broker's probe finds its task gone. The lease is the
// step's recorded seconds and half again, else its kind's: a plan's Bazel queries take minutes, a report
// seconds to a few minutes, a prepare's build or a whole-suite check as long as a batch.
var stepLeases = map[string]time.Duration{
	config.StepPlanning:    20 * time.Minute,
	config.StepBookkeeping: 5 * time.Minute,
	config.StepPrepare:     30 * time.Minute,
	config.StepWhole:       30 * time.Minute,
	config.StepJob:         10 * time.Minute,
}

const (
	// stepLeaseLeast is the shortest lease a step's recorded seconds give: a step quick at most commits
	// may still take a while at one.
	stepLeaseLeast = time.Minute
	// stepCoresAfter is how long a step must have taken for its cores to be kept: the CPU seconds of a
	// shorter one over its seconds say little.
	stepCoresAfter = 5 * time.Second
)

// stepMemory is the wait of one of the pipeline's own steps for its memory and cores, and the record of
// what it used. Such a step is the pipeline's, not the change's: one that can't get them starts anyway, since a
// plan or a report that failed for room would stop the run it is for, and the worker's broker books it
// then.
type stepMemory struct {
	cfg *config.Config
	// store holds the steps' recorded use; nil reads none, and the step asks for its kind's defaults.
	store *gitstore.Store
	// step names the step among the steps' peaks: its command, its run and, for a suite's step, the suite.
	step, kind, commit string
	// suite is the suite whose memory.wait, cpu.wait and memory.broker hold for a prepare or a
	// whole-suite check; "" is the top level's, for the pipeline's own steps.
	suite string
	// record is whether the step may keep what it used: a trusted run's step, holding the store's
	// writing key.
	record bool
	// mb and cores are what a job's task asks for by its own flags; 0 is its kind's default.
	mb    int64
	cores float64
}

// stepWait is how long a step may wait for its memory: a share of what its deadline leaves, at most
// stepWaitMost.
func stepWait(now time.Time) time.Duration {
	at, err := strconv.ParseInt(os.Getenv(envDeadline), 10, 64)
	if err != nil {
		return stepWaitBare
	}

	return min(max(time.Unix(at, 0).Sub(now), 0)/stepWaitShare, stepWaitMost)
}

// stepNeed is what a step is expected to use, each with what says so: its memory, its cores and its
// lease.
type stepNeed struct {
	mb       int64
	mbWhy    string
	cores    float64
	coresWhy string
	lease    time.Duration
}

// command is the step's command among the steps' names, with its trailing space: a report's or a slice
// step's is two words.
func (s stepMemory) command() string {
	parts := strings.Fields(s.step)
	if len(parts) == 0 {
		return ""
	}

	n := 1
	if parts[0] == "report" || parts[0] == "slice" {
		n = 2
	}

	return strings.Join(parts[:min(n, len(parts))], " ") + " "
}

// alike is whether name is a step like s of another run: one of its command for the pipeline's own
// steps, one of its kind and suite for a suite's prepare or check, whose use is the suite's own.
func (s stepMemory) alike(name string) bool {
	if s.suiteTask() {
		return strings.HasPrefix(name, s.kind+" ") && strings.HasSuffix(name, " "+s.suite)
	}

	return strings.HasPrefix(name, s.command())
}

// alikeSaid names the steps alike s, for the log.
func (s stepMemory) alikeSaid() string {
	if s.suiteTask() {
		return s.kind + " steps of " + s.suite + " in other runs"
	}

	return strings.TrimSpace(s.command()) + " steps of other runs"
}

// recent is what of is expected to be by the step's own recent values, else by those of the steps alike
// it, with which of the two said so; nothing when neither has any.
func recent[T int64 | float64](s stepMemory, uses map[string]report.StepUse, of func(report.StepUse) []T, predict func([]T) T) (T, string) {
	if p := predict(of(uses[s.step])); p > 0 {
		return p, "its own"
	}

	var all []T

	for _, name := range slices.Sorted(maps.Keys(uses)) {
		if name != s.step && s.alike(name) {
			all = append(all, of(uses[name])...)
		}
	}

	if p := predict(all); p > 0 {
		return p, "those of the " + s.alikeSaid()
	}

	return 0, ""
}

// need is what the step is expected to use, by the recent use of its own, else of the steps alike it,
// else its kind's defaults: memory.steps for the pipeline's own steps, none for a prepare or a check,
// which then waits for the memory pressure alone; cpu.steps for the cores, and its kind's lease.
func (s stepMemory) need(uses map[string]report.StepUse, now, deadline time.Time) stepNeed {
	var n stepNeed

	n.mb, n.mbWhy = recent(s, uses, func(u report.StepUse) []int64 { return u.MB }, memory.Predict)

	switch {
	case n.mb > 0:
		n.mbWhy += " recent peaks"
	case s.suiteTask():
		n.mbWhy = "with no peaks recorded yet"
	case s.mb > 0:
		n.mb, n.mbWhy = s.mb, "its --mb"
	default:
		n.mb, n.mbWhy = s.cfg.StepMemoryMB(s.defaults()), "memory.steps."+s.defaults()+", with no peaks recorded yet"
	}

	n.cores, n.coresWhy = recent(s, uses, func(u report.StepUse) []float64 { return u.Cores }, memory.PredictCores)
	switch {
	case n.cores > 0:
		n.coresWhy += " recent cores"
	case s.cores > 0:
		n.cores, n.coresWhy = s.cores, "its --cores"
	default:
		n.cores, n.coresWhy = s.cfg.StepCores(s.defaults()), "cpu.steps."+s.defaults()+", with no cores recorded yet"
	}

	n.lease = stepLeases[s.kind]
	if secs, _ := recent(s, uses, func(u report.StepUse) []float64 { return u.Seconds }, p90); secs > 0 {
		n.lease = max(time.Duration(secs*1.5*float64(time.Second)), stepLeaseLeast)
	}

	// a lease never outlasts the step's own time limit, and is never none: none would be the broker's ramp
	if !deadline.IsZero() {
		n.lease = max(min(n.lease, deadline.Sub(now)), time.Second)
	}

	return n
}

// p90 is the 90th percentile of xs by nearest rank, 0 without any.
func p90(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}

	s := slices.Clone(xs)
	slices.Sort(s)

	return s[(len(s)*9+9)/10-1]
}

// uses reads the steps' recorded use, none without a store or when it can't be read.
func (s stepMemory) uses(ctx context.Context, log io.Writer) map[string]report.StepUse {
	if s.store == nil {
		return nil
	}

	uses, err := report.StepUses(ctx, s.store)
	if err != nil {
		warnf(log, "memory: %v; the step asks for its kind's defaults\n", err)

		return nil
	}

	return uses
}

// admit waits, under memory.wait, until the worker has the memory the step is expected to use, and under
// cpu.wait until its CPU pressure is below cpu.pressure and, with the worker's broker, until the cores
// it holds leave room for the step's: asking the broker when memory.broker names one, else by its own
// reading. Waited out or unreadable, the step starts anyway, and says so. It returns whether the broker
// granted the step cores, which its end releases.
func (s stepMemory) admit(ctx context.Context, log io.Writer) bool {
	waitsForMemory, waitsForCPU := s.cfg.WaitsForMemory(s.suite), s.waitsForCPU()
	if !s.taskWait() && !secret.InPipeline() || !waitsForMemory && !waitsForCPU {
		return false
	}

	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}

	n := s.need(s.uses(ctx, log), time.Now(), deadline)

	switch {
	case !waitsForMemory:
		n.mb = 0
	case n.mb == 0:
		warnf(log, "memory: this %s step waits while the worker is under memory pressure, %s\n", s.kind, n.mbWhy)
	default:
		warnf(log, "memory: this %s step should use %d MB, by %s\n", s.kind, n.mb, n.mbWhy)
	}

	if waitsForCPU {
		warnf(log, "cpu: this %s step should keep %.1f cores busy, by %s, and waits while the worker's CPU pressure is at or above %.0f%%\n",
			s.kind, n.cores, n.coresWhy, s.cfg.CPUPressure(s.suite))
	}

	limit := stepWait(time.Now())

	if s.cfg.MemoryBroker(s.suite) != "" {
		if done, granted := s.askBroker(ctx, s.ask(n, limit), log); done {
			return granted
		}
	}

	s.awaitReading(ctx, n.mb, limit, log)

	return false
}

// waitsForCPU is the top level's cpu.wait, or a prepare's or a check's suite's.
func (s stepMemory) waitsForCPU() bool {
	return s.cfg.WaitsForCPU(s.suite)
}

// cpuPressure is the worker's CPU pressure for a step that waits for it, -1 for one that doesn't.
func (s stepMemory) cpuPressure() float64 {
	if !s.waitsForCPU() {
		return -1
	}

	return memory.CPUPressure(memoryRoot)
}

// who names the step in the broker's log, and its grant for a release.
func (s stepMemory) who() string {
	if s.commit == "" {
		return s.step
	}

	return s.step + " " + short12(s.commit)
}

// ask is the step's request of the broker: its memory, waiting at most limit, and under cpu.wait its
// cores for its lease. It is a step's, which goes ahead of a batch first in line when the worker has room,
// and is granted anyway when its wait ends, since the step then starts anyway.
func (s stepMemory) ask(n stepNeed, limit time.Duration) membroker.Request {
	req := membroker.Request{MB: n.mb, WaitSeconds: limit.Seconds(), Who: s.who(), Step: true, Anyway: true}
	if s.waitsForCPU() {
		req.CPU, req.Cores, req.LeaseSeconds = true, n.cores, n.lease.Seconds()
	}

	return req
}

// askBroker asks the worker's broker for req: done once it answered, granted or not, and whether it
// granted cores; not done when it can't be reached, and the step waits by its own reading.
func (s stepMemory) askBroker(ctx context.Context, req membroker.Request, log io.Writer) (done, cores bool) {
	addr, err := brokerAddr(s.cfg.MemoryBroker(s.suite))
	if err != nil {
		warnf(log, "memory: %v; the step waits by its own reading\n", err)

		return false, false
	}

	start := time.Now()

	ans, err := membroker.AskAgain(ctx, addr, req)
	if err != nil {
		if ctx.Err() == nil {
			warnf(log, "memory: %v; the step waits by its own reading\n", err)

			return false, false
		}

		return true, false
	}

	waited := time.Since(start).Round(time.Second)

	what := fmt.Sprintf("%d MB", req.MB)
	if req.CPU {
		what += fmt.Sprintf(" and %.1f cores for %s", req.Cores, time.Duration(req.LeaseSeconds*float64(time.Second)).Round(time.Second))
	}

	switch {
	case ans.Granted && ans.Anyway:
		warnf(log, "memory: the worker's broker granted %s anyway after %s, the worker short of them (%s): the step starts, counted\n", what, waited, ans.Why)
	case ans.Granted:
		warnf(log, "memory: the worker's broker granted %s after %s: the step starts\n", what, waited)
	default:
		warnf(log, "memory: the worker's broker granted nothing after %s (%s): the step starts anyway\n", waited, ans.Why)
	}

	return true, ans.Granted && req.CPU && req.Cores > 0
}

// release tells the worker's broker the step has ended, so the cores it was granted count no longer; a
// broker that can't be told lets the grant's lease run out.
func (s stepMemory) release(ctx context.Context, log io.Writer) {
	addr, err := brokerAddr(s.cfg.MemoryBroker(s.suite))
	if err == nil {
		// the step's own context may have ended with it
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		err = membroker.Release(ctx, addr, s.who())
	}

	if err != nil {
		warnf(log, "cpu: releasing the step's cores: %v; they count until the grant's lease ends\n", err)
	}
}

// awaitReading waits until the worker's free memory covers need and it is under no pressure, nor under
// CPU pressure for a step that waits for CPU, at most limit; then the step starts whatever it reads.
func (s stepMemory) awaitReading(ctx context.Context, need int64, limit time.Duration, log io.Writer) {
	start := time.Now()

	for {
		r, err := memory.Read(memoryRoot)
		if err != nil {
			warnf(log, "memory: %v; the step starts unchecked\n", err)

			return
		}

		pressure, cpu := memory.Pressure(memoryRoot), s.cpuPressure()

		switch waited := time.Since(start); {
		case admits(r, pressure, need) && (cpu < 0 || cpu < s.cfg.CPUPressure(s.suite)):
			if waited >= memoryEvery {
				warnf(log, "memory: %d MB free of %d MB after %s: the step starts\n", r.Free(), need, waited.Round(time.Second))
			}

			return
		case need > r.Capacity():
			warnf(log, "memory: %d MB is more than the %d MB this worker could ever give: the step starts\n", need, r.Capacity())

			return
		case waited >= limit:
			cpuSaid := ""
			if s.waitsForCPU() {
				cpuSaid = ", CPU pressure " + psi(cpu)
			}

			warnf(log, "memory: %s, pressure %s%s, after %s: the step starts anyway\n", r, psi(pressure), cpuSaid, waited.Round(time.Second))

			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(memoryEvery):
		}
	}
}

// during waits for the step's memory and cores, and returns what ends the step: it releases the cores the
// broker granted and keeps what the step used from its start.
func (s stepMemory) during(ctx context.Context, log io.Writer) func() {
	cores := s.admit(ctx, log)
	start := s.mark()

	return func() {
		used, ok := start.used()

		if cores {
			s.release(ctx, log)
		}

		if ok {
			s.keep(ctx, used, log)
		}
	}
}

// stepStart is when the step's work began and the CPU its task's cgroup had used by then, -1 unread.
type stepStart struct {
	at  time.Time
	cpu float64
}

func (s stepMemory) mark() stepStart {
	st := stepStart{at: time.Now(), cpu: -1}
	if u, err := memory.Measure(memoryRoot); err == nil {
		st.cpu = u.CPU
	}

	return st
}

// used is what the step's task used from st to now: the most memory it used, the cores it kept busy,
// its CPU seconds over its seconds, and those seconds; nothing where the memory can't be read. A task's
// cgroup holds what ran before the step too, so its cores are what it used since st alone.
func (st stepStart) used() (report.Peak, bool) {
	u, err := memory.Measure(memoryRoot)
	if err != nil {
		return report.Peak{}, false
	}

	secs := time.Since(st.at).Seconds()
	p := report.Peak{MB: max(u.Peak, u.Set), Seconds: secs}

	if st.cpu >= 0 && u.CPU > st.cpu && secs >= stepCoresAfter.Seconds() {
		p.Cores = (u.CPU - st.cpu) / secs
	}

	return p, true
}

// keep records what the step used, in a trusted run; a failed write only warns.
func (s stepMemory) keep(ctx context.Context, used report.Peak, log io.Writer) {
	if !s.record || s.store == nil || !secret.InPipeline() || s.store.Proposed || s.store.ReadOnly {
		return
	}

	if used.MB <= 0 && used.Cores <= 0 {
		return
	}

	if err := report.AddStepPeak(ctx, s.store, s.step, s.commit, used); err != nil {
		warnf(log, "memory: keeping this step's peak: %v\n", err)
	}
}

// memory is the wait and record of a suite's bookkeeping step, command naming it, suite its suite.
func (s *step) memory(command, suite string) stepMemory {
	id := s.reporter.ID

	return stepMemory{
		cfg: s.reporter.Config, store: s.reporter.Store, kind: config.StepBookkeeping,
		step: command + " " + id.Run + " " + suite, commit: id.Commit, record: !id.Proposed,
	}
}

// planMemory is the wait and record of a plan that does a plan's work: not a decision, a beginning or the
// write of a plan another step made.
func planMemory(cfg *config.Config, ch runs.Change, store *gitstore.Store) stepMemory {
	return stepMemory{cfg: cfg, store: store, kind: config.StepPlanning, step: "plan " + ch.Run, commit: ch.Commit, record: !ch.Proposed()}
}

// buildMemory is the wait and record of a suite job's bookkeeping step that opens the store itself,
// command naming it: the tree under test, the batches.
func buildMemory(cfg *config.Config, ch runs.Change, store *gitstore.Store, command, suite string) stepMemory {
	return stepMemory{
		cfg: cfg, store: store, kind: config.StepBookkeeping,
		step: command + " " + ch.Run + " " + suite, commit: ch.Commit, record: !ch.Proposed(),
	}
}

// prepareMemory is the wait and record of a suite's prepare steps, run in pipelinegen to their end: it
// reads the recorded use of the suite's prepares, and keeps its own in a trusted run whose task holds the
// store's writing key.
func (s *step) prepareMemory(suite string) stepMemory {
	id := s.reporter.ID

	sm := suiteStepMemory(s.reporter.Config, config.StepPrepare, id.Run, suite, id.Commit)
	sm.store, sm.record = s.reporter.Store, !id.Proposed && !s.reporter.NoToken

	return sm
}

// suiteTask is whether the step is a suite's prepare or whole-suite check: a task of the pipeline's
// whatever its environment says, since its callers leave out a run on this machine.
func (s stepMemory) suiteTask() bool {
	return s.kind == config.StepPrepare || s.kind == config.StepWhole
}

// defaults is the kind whose memory.steps and cpu.steps the step asks for with nothing recorded: a job's
// task asks for a bookkeeping step's.
func (s stepMemory) defaults() string {
	if s.kind == config.StepJob {
		return config.StepBookkeeping
	}

	return s.kind
}

// taskWait is whether the step is a task's own wait: a suite's prepare or check, or a job's task, which
// waits wherever it runs, as its task calls it only in a pipeline.
func (s stepMemory) taskWait() bool {
	return s.suiteTask() || s.kind == config.StepJob
}

// jobStepMemory is the wait of the task name of a job the repository writes itself: under the top-level
// memory.wait or cpu.wait it waits for mb and cores, else a bookkeeping step's defaults, then starts
// anyway. As a task file's wait it reads and keeps none.
func jobStepMemory(cfg *config.Config, name string, mb int64, cores float64) stepMemory {
	return stepMemory{cfg: cfg, kind: config.StepJob, step: config.StepJob + " " + name, mb: mb, cores: cores}
}

// suiteStepMemory is the wait of a suite's prepare or whole-suite check, kind: under the suite's
// memory.wait or cpu.wait it waits for the memory and cores its kind's recorded use predicts, then starts
// anyway. A task file's wait reads and keeps none: it ends before the task's work, so it never sees what
// the work used, and its cores count until the broker finds its task gone or its lease ends.
func suiteStepMemory(cfg *config.Config, kind, run, suite, commit string) stepMemory {
	return stepMemory{cfg: cfg, kind: kind, suite: suite, step: kind + " " + run + " " + suite, commit: commit}
}
