package cli

import (
	"context"
	"io"
	"os"
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

// stepMemory is a planning or bookkeeping step's wait for its own memory, and the record of the most it
// used. Such a step is the pipeline's, not the change's: one that can't get its memory starts anyway,
// since a plan or a report that failed for memory would stop the run it is for.
type stepMemory struct {
	cfg   *config.Config
	store *gitstore.Store
	// step names the step among the steps' peaks: its command, its run and, for a suite's step, the suite.
	step, kind, commit string
	// record is whether the step may keep its peak: a trusted run's step, holding the store's writing key.
	record bool
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

// need is what the step is expected to use: what its own recent peaks predict, else the most its command's
// steps of other runs and suites predict, else its kind's default.
func (s stepMemory) need(peaks map[string][]int64) (int64, string) {
	if p := memory.Predict(peaks[s.step]); p > 0 {
		return p, "its recent peaks"
	}

	command, _, _ := strings.Cut(s.step, " ")
	command += " "

	if strings.HasPrefix(s.step, "report ") {
		parts := strings.Fields(s.step)
		command = strings.Join(parts[:min(2, len(parts))], " ") + " "
	}

	var alike []int64

	for name, p := range peaks {
		if strings.HasPrefix(name, command) {
			alike = append(alike, p...)
		}
	}

	if p := memory.Predict(alike); p > 0 {
		return p, "the recent peaks of " + strings.TrimSpace(command) + " steps of other runs"
	}

	return s.cfg.StepMemoryMB(s.kind), "memory.steps." + s.kind + ", with no peaks recorded yet"
}

// admit waits, under memory.wait, until the worker has the memory the step is expected to use: asking the
// worker's broker when memory.broker names one, else by its own reading. Waited out or unreadable, the step
// starts anyway, and says so.
func (s stepMemory) admit(ctx context.Context, log io.Writer) {
	if !secret.InPipeline() || s.cfg.Memory.Wait == nil || !*s.cfg.Memory.Wait {
		return
	}

	peaks, err := report.StepPeaks(ctx, s.store)
	if err != nil {
		warnf(log, "memory: %v; the step starts unchecked\n", err)

		return
	}

	need, why := s.need(peaks)
	limit := stepWait(time.Now())

	warnf(log, "memory: this %s step should use %d MB, by %s\n", s.kind, need, why)

	if s.cfg.Memory.Broker != "" && s.askBroker(ctx, need, limit, log) {
		return
	}

	s.awaitReading(ctx, need, limit, log)
}

// askBroker asks the worker's broker for need: done once it answered, granted or not; not done when it
// can't be reached, and the step waits by its own reading.
func (s stepMemory) askBroker(ctx context.Context, need int64, limit time.Duration, log io.Writer) bool {
	addr, err := brokerAddr(s.cfg.Memory.Broker)
	if err != nil {
		warnf(log, "memory: %v; the step waits by its own reading\n", err)

		return false
	}

	start := time.Now()

	ans, err := membroker.Ask(ctx, addr, membroker.Request{MB: need, WaitSeconds: limit.Seconds(), Who: s.step + " " + short12(s.commit)})
	if err != nil {
		if ctx.Err() == nil {
			warnf(log, "memory: %v; the step waits by its own reading\n", err)

			return false
		}

		return true
	}

	waited := time.Since(start).Round(time.Second)

	if ans.Granted {
		warnf(log, "memory: the worker's broker granted %d MB after %s: the step starts\n", need, waited)
	} else {
		warnf(log, "memory: the worker's broker granted nothing after %s (%s): the step starts anyway\n", waited, ans.Why)
	}

	return true
}

// awaitReading waits until the worker's free memory covers need and it is under no pressure, at most
// limit; then the step starts whatever it reads.
func (s stepMemory) awaitReading(ctx context.Context, need int64, limit time.Duration, log io.Writer) {
	start := time.Now()

	for {
		r, err := memory.Read(memoryRoot)
		if err != nil {
			warnf(log, "memory: %v; the step starts unchecked\n", err)

			return
		}

		pressure := memory.Pressure(memoryRoot)

		switch waited := time.Since(start); {
		case admits(r, pressure, need):
			if waited >= memoryEvery {
				warnf(log, "memory: %d MB free of %d MB after %s: the step starts\n", r.Free(), need, waited.Round(time.Second))
			}

			return
		case need > r.Capacity():
			warnf(log, "memory: %d MB is more than the %d MB this worker could ever give: the step starts\n", need, r.Capacity())

			return
		case waited >= limit:
			warnf(log, "memory: %s, pressure %s, after %s: the step starts anyway\n", r, psi(pressure), waited.Round(time.Second))

			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(memoryEvery):
		}
	}
}

// during waits for the step's memory, and returns what keeps its peak once the step is done.
func (s stepMemory) during(ctx context.Context, log io.Writer) func() {
	s.admit(ctx, log)

	return func() { s.keep(ctx, log) }
}

// keep records the most the step's task used, in a trusted run; a failed write only warns.
func (s stepMemory) keep(ctx context.Context, log io.Writer) {
	if !s.record || !secret.InPipeline() || s.store.Proposed || s.store.ReadOnly {
		return
	}

	u, err := memory.Measure(memoryRoot)
	if err != nil {
		return
	}

	if peak := max(u.Peak, u.Set); peak > 0 {
		if err := report.AddStepPeak(ctx, s.store, s.step, s.commit, peak); err != nil {
			warnf(log, "memory: keeping this step's peak: %v\n", err)
		}
	}
}

// memory is the memory wait and record of a suite's bookkeeping step, command naming it.
func (s *step) memory(command string) stepMemory {
	id := s.reporter.ID

	return stepMemory{
		cfg: s.reporter.Config, store: s.reporter.Store, kind: config.StepBookkeeping,
		step: command + " " + id.Run + " " + os.Getenv(envSuite), commit: id.Commit, record: !id.Proposed,
	}
}

// planMemory is the memory wait and record of a plan that does a plan's work: not a decision, a beginning
// or the write of a plan another step made.
func planMemory(cfg *config.Config, ch runs.Change, store *gitstore.Store) stepMemory {
	return stepMemory{cfg: cfg, store: store, kind: config.StepPlanning, step: "plan " + ch.Run, commit: ch.Commit, record: !ch.Proposed()}
}
