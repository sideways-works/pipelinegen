package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/memory"
	"github.com/sideways-works/pipelinegen/internal/piperun"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

// envSession is the number piperun gives a session of a pool, which pipelinegen takes as the batch's.
const envSession = "PIPERUN_SESSION"

// How often a session looks for its next chunk, and how long a minute of PIPELINEGEN_LIMIT is; tests
// shorten both.
var (
	feedEvery  = 200 * time.Millisecond
	feedMinute = time.Minute
)

// batchFromEnv is the batch's number: PIPELINEGEN_BATCH, else on piperun the session's.
func batchFromEnv() string {
	if b := os.Getenv(envBatch); b != "" {
		return b
	}

	return os.Getenv(envSession)
}

// feedArg tells a `slice run --feed`, whose PIPELINEGEN_LIMIT is each slice's, not the command's.
func feedArg(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}

		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "feed" {
			return true
		}
	}

	return false
}

// runVerb is `slice run`'s.
const runVerb = "run"

func limitsEachSlice(command string, args []string) bool {
	return command == sliceCommand && len(args) > 0 && args[0] == runVerb && feedArg(args[1:])
}

// itemLimit is PIPELINEGEN_LIMIT taken as each slice's limit; minutes 0 sets none.
type itemLimit struct {
	key     string
	minutes int
}

// itemLimitFromEnv is PIPELINEGEN_LIMIT, else the suite's minutes, which a pool's template takes as each
// item's limit: piperun can't stop a session, so one whose slice hangs would hold its chunks forever.
func itemLimitFromEnv(cfg *config.Config, run, suite string) itemLimit {
	if l, ok := stepLimitFromEnv(time.Now()); ok {
		return itemLimit{key: l.key, minutes: l.minutes}
	}

	return itemLimit{key: "suites." + suite + ".minutes", minutes: cfg.SuiteMinutes(run, suite)}
}

// end is when a slice started at start stops on its own: a margin before its limit, a quarter of a
// short limit, so it can still say so before piperun ends it.
func (l itemLimit) end(start time.Time) time.Time {
	if l.minutes == 0 {
		return time.Time{}
	}

	d := time.Duration(l.minutes) * feedMinute

	return start.Add(d - min(limitMargin, d/4))
}

func (l itemLimit) why() string {
	return fmt.Sprintf("stopped at its time limit of %s (%s), before piperun would end it: raise %s if the slice needs longer",
		minutes(time.Duration(l.minutes)*time.Minute), l.key, l.key)
}

// feedHooks are a batch's journal hooks for one item at a time: an item its limit stopped errors saying
// so, and its end is journaled whatever is left of its time; a session that is being stopped journals
// no end, as its item goes back to the pool.
type feedHooks struct {
	*journalHooks

	limit            itemLimit
	expired, stopped func() bool
	ended            map[string]bool
}

func (h *feedHooks) End(ctx context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, apart bool) error {
	if h.stopped() {
		return nil
	}

	if h.expired() {
		lines := []string{h.limit.why()}

		if ev != nil {
			for _, f := range ev.Failures {
				lines = append(lines, f.Test+": "+f.Tail)
			}
		}

		why = strings.Join(lines, "\n")

		ev = nil
	}

	h.ended[sl.Name] = true

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limitMargin)
	defer cancel()

	return h.journalHooks.End(ctx, sl, ev, why, apart)
}

// feedSession is a piperun session of a suite's pool: one set-up, then chunk after chunk of slices.
type feedSession struct {
	s        *step
	b        *drivenBatch
	se       *session
	h        *feedHooks
	j        *report.Journal
	pool     map[string]piperun.Slice
	results  *piperun.Results
	files    *piperun.Files
	feed     piperun.Feed
	limit    itemLimit
	admit    *itemAdmission
	reported map[string]string
	out      io.Writer

	ran, notPassed int
}

// feedSliceRun runs a session: after the set-up it reads chunks in order until the end, each slice
// through the steps as a batch's, and reports it in a line of the feed's results: a start, then its
// end, or a requeue when the worker can't fit it. A session started again keeps its journal, and
// takes nothing of what it left unended as its own.
func feedSliceRun(ctx context.Context, s *step, f drivenFlags, stdout io.Writer) error {
	suite, batch := os.Getenv(envSuite), batchFromEnv()
	if suite == "" || batch == "" {
		return fmt.Errorf("%w: %s, and %s or %s, must be set", errUsage, envSuite, envBatch, envSession)
	}

	s.reporter.Store.Batch = batch
	cfg := s.reporter.Config

	if !cfg.Suites[suite].HasSteps() {
		return fmt.Errorf("%w: suite %s has no steps; only a suite run by drivers takes a feed", errUsage, suite)
	}

	pool, err := readPool(f.pools, suite)
	if err != nil {
		return err
	}

	if err := emptyDir(f.work); err != nil {
		return fmt.Errorf("%w: %s holds what an earlier session left and can't be emptied: %w", errSteps, f.work, err)
	}

	if err := checkPlanOnce(ctx, s.reporter, f.result); err != nil {
		return err
	}

	rec, err := s.recordingIn(f, suite, "slice run")
	if err != nil {
		return err
	}

	s.reporter.NoFlush = true

	if s.reporter.ID.Run != localRun {
		defer s.sample(ctx)()
	}

	b := &drivenBatch{
		cfg: cfg, suite: suite, tree: absOr(s.root), tools: absOr(s.tools), plugins: filepath.Join(absOr(s.tools), "plugins"),
		work: f.work, cache: f.cache, prepared: f.prepared, result: f.result, coverage: rec, record: rec != "",
		params: cfg.Params(s.reporter.ID.Run, suite), secrets: secretNames(cfg, s.reporter.ID.Run, suite), log: os.Stderr,
		commit: s.reporter.ID.Commit,
	}

	if b.record {
		s.reporter.Collectors = s.plugins.collectors(cfg, suite, time.Time{})
	}

	j := report.JournalIn(f.result)

	if err := forgetUnended(s.reporter, j); err != nil {
		return err
	}

	fs := &feedSession{
		s: s, b: b, j: j, pool: pool, out: stdout, limit: itemLimitFromEnv(cfg, s.reporter.ID.Run, suite),
		results: &piperun.Results{Path: filepath.Join(f.feed, piperun.ResultsFile)},
		files:   &piperun.Files{Feed: f.feed},
		feed:    piperun.Feed{Dir: f.feed, Every: feedEvery},
	}

	if fs.reported, err = piperun.Reported(fs.results.Path); err != nil {
		return err
	}

	fs.admit = &itemAdmission{local: s.reporter.ID.Run == localRun, minutes: max(fs.limit.minutes, cfg.SuiteMinutes(s.reporter.ID.Run, suite)), log: os.Stderr}

	return fs.run(ctx, f, rec)
}

// recordingIn is the coverage directory of a recording run, after checking what it needs; empty in a
// run that doesn't record the suite.
func (s *step) recordingIn(f drivenFlags, suite, command string) (string, error) {
	cfg := s.reporter.Config
	if !cfg.Records(s.reporter.ID.Run, suite) {
		s.reporter.Collectors = nil

		return "", nil
	}

	if err := errors.Join(needCollector(cfg, s.plugins, suite), needObservable(cfg, s.plugins, suite), needInterpreters(s.plugins, suite, command)); err != nil {
		return "", err
	}

	return f.coverage, nil
}

// A session started again after its worker was lost keeps the journal it had uploaded, but the items
// it held unreported went back to the pool.
func forgetUnended(r *report.Reporter, j *report.Journal) error {
	open, err := j.Unended()
	if err != nil {
		return err
	}

	return r.Forget(j, open)
}

func (fs *feedSession) run(ctx context.Context, f drivenFlags, rec string) error {
	s, suite := fs.s, fs.b.suite

	byName := make(map[string]batchSlice, len(fs.pool))
	for name, sl := range fs.pool {
		byName[name] = batchSlice{Name: sl.Name, Label: sl.Label, Target: sl.Target, Kind: sl.Kind, Sources: sl.Sources, Tests: sl.Tests}
	}

	fs.h = &feedHooks{
		journalHooks: &journalHooks{
			r: s.reporter, j: fs.j, suite: suite, root: s.root, result: f.result, rec: rec,
			every: flushEvery(s.reporter.Config, s.reporter.ID.Run), byName: byName, out: fs.out,
		},
		limit: fs.limit, expired: func() bool { return false }, stopped: func() bool { return ctx.Err() != nil }, ended: map[string]bool{},
	}

	setupEnd := func() error {
		if err := s.reporter.SetupEnd(ctx, fs.j, filepath.Join(f.coverage, "_setup")); err != nil {
			return err
		}

		return say(fs.out, "setup ended\n")
	}

	se, err := fs.b.open(ctx, fs.h, setupEnd)
	defer se.close(ctx)

	if err != nil {
		return err
	}

	fs.se = se

	for c := 1; ; c++ {
		chunk, ok, err := fs.feed.Next(ctx, c)
		if err != nil {
			return err
		}

		if !ok {
			break
		}

		for _, it := range chunk.Items {
			if err := fs.item(ctx, it.Key); err != nil {
				return err
			}
		}
	}

	return say(fs.out, "session %s: ran %s, %d of them not passed\n", batchFromEnv(), plural(fs.ran, "slice"), fs.notPassed)
}

// item runs one slice and writes its lines; an error ends the session, whose unreported items piperun
// then ends errored.
func (fs *feedSession) item(ctx context.Context, key string) error {
	// a session started again skips what it ended before
	if fs.reported[key] == piperun.Ended {
		return nil
	}

	sl, ok := fs.pool[key]
	if !ok {
		return fs.results.End(piperun.End{Key: key, Outcome: piperun.Errored, Summary: key + " is no slice of the pool's file " + plugin.PiperunSlices(fs.b.suite) + ".json"})
	}

	if why, fits := fs.admit.fits(ctx, sl); !fits {
		return fs.results.Requeue(key, why)
	}

	// an item comes back to a session only when piperun never took its end: what the journal holds of
	// it is void, and it starts anew
	if err := fs.s.reporter.ForgetIfHeld(fs.j, report.SliceEvent{Suite: fs.b.suite, Slice: key}); err != nil {
		return err
	}

	if err := fs.results.Start(key); err != nil {
		return err
	}

	start := time.Now()
	bs := fs.h.byName[key]
	ds := fs.b.driverSlices([]batchSlice{bs})[0]

	ictx, cancel := ctx, context.CancelFunc(func() {})
	if end := fs.limit.end(start); !end.IsZero() {
		ictx, cancel = context.WithDeadline(ctx, end)
	}

	fs.b.deadline = fs.limit.end(start)
	fs.h.expired = func() bool { return errors.Is(ictx.Err(), context.DeadlineExceeded) }
	delete(fs.h.ended, key)

	routed, err := fs.b.route([]batchSlice{bs})
	if err == nil {
		err = fs.se.run(ictx, routed)
	}

	cancel()

	if ctx.Err() != nil {
		return fmt.Errorf("session stopped while %s ran: %w", key, ctx.Err())
	}

	if !fs.h.ended[key] {
		why := "it ended without a result"
		if err != nil {
			why = err.Error()
		}

		if eerr := fs.h.End(ctx, ds, nil, why, false); eerr != nil {
			return eerr
		}
	}

	// a run step that broke, not the slice's own limit, ends the session
	if err != nil && !fs.h.expired() && !errors.Is(err, errSteps) {
		return err
	}

	return fs.ended(ctx, key, start)
}

// ended writes an item's end line once everything its bookkeeping reads is on disk: the journal's end
// and the result, written by the end hook, then its observation and its failure files.
func (fs *feedSession) ended(ctx context.Context, key string, start time.Time) error {
	s := fs.s

	if fs.b.record && s.reporter.Observer != nil {
		until := fs.limit.end(start)
		if until.IsZero() {
			until = time.Now().Add(limitMargin)
		}

		s.plugins.observeAhead(ctx, fs.j, fs.b.suite, until, os.Stderr)
	}

	result := filepath.Join(fs.b.result, filepath.FromSlash(key)+".json")
	state, failures := report.ReadResult(result)

	end := piperun.End{Key: key, Outcome: outcome(state), Summary: failureSummary(state, failures), DurationMS: time.Since(start).Milliseconds()}

	if ev, ok := lastEnd(fs.j, fs.b.suite, key); ok && ev.Used != nil {
		end.CPUSeconds, end.PeakMB = ev.Used.CPU, ev.Used.Peak
	}

	kept, left, err := fs.files.Kept(fs.b.result, report.FilesPrefix(key), key, resultFilePaths(result))
	if err != nil {
		warnf(os.Stderr, "pipelinegen slice run: %s: failure files: %v\n", key, err)
	}

	if len(left) > 0 {
		warnf(os.Stderr, "pipelinegen slice run: %s: %s left out of what piperun keeps (over its caps, or not regular files in the result)\n", key, plural(len(left), "failure file"))
	}

	end.Files = kept

	fs.ran++
	if state != report.Passed {
		fs.notPassed++
	}

	return fs.results.End(end)
}

func outcome(st report.State) string {
	switch st {
	case report.Passed:
		return piperun.Passed
	case report.Failed:
		return piperun.Failed
	default:
		return piperun.Errored
	}
}

func failureSummary(st report.State, failures []report.Failure) string {
	if st == report.Passed {
		return ""
	}

	parts := make([]string, 0, len(failures))
	for _, f := range failures {
		parts = append(parts, f.Test+": "+strings.TrimSpace(f.Tail))
	}

	return strings.Join(parts, "\n")
}

func lastEnd(j *report.Journal, suite, key string) (report.SliceEvent, bool) {
	ends, err := j.Ends()
	if err != nil {
		return report.SliceEvent{}, false
	}

	for _, ev := range ends {
		if ev.Suite == suite && ev.Slice == key {
			return ev, true
		}
	}

	return report.SliceEvent{}, false
}

func resultFilePaths(result string) []string {
	data, err := os.ReadFile(result) //nolint:gosec // the session's own result
	if err != nil {
		return nil
	}

	var r report.ResultFile
	if json.Unmarshal(data, &r) != nil {
		return nil
	}

	out := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		out = append(out, f.Path)
	}

	return out
}

// itemAdmission starts a slice only while the worker has room for the peak it should reach beyond what
// the session holds, and memory pressure is low; it waits while that may come, at most as long as a
// batch waits for its start, counted from the first slice that didn't fit, and hands the slice back.
type itemAdmission struct {
	local   bool
	minutes int
	log     io.Writer
	short   time.Time
}

func (a *itemAdmission) fits(ctx context.Context, sl piperun.Slice) (string, bool) {
	if a.local || sl.PredictedMB <= 0 {
		return "", true
	}

	limit := memoryWait(a.minutes)

	for {
		r, err := memory.Read(memoryRoot)
		if err != nil || sl.PredictedMB > r.Capacity() {
			// a reading it can't make, or a peak no worker could give, starts unchecked
			return "", true
		}

		var held int64
		if u, err := memory.Measure(memoryRoot); err == nil {
			held = u.Set
		}

		pressure := memory.Pressure(memoryRoot)
		need := max(sl.PredictedMB-held, 0)

		if r.Free() >= need && pressure < pressureAdmit {
			a.short = time.Time{}

			return "", true
		}

		if a.short.IsZero() {
			a.short = time.Now()
		}

		if time.Since(a.short) >= limit || ctx.Err() != nil {
			why := fmt.Sprintf("needs ~%d MB more for a peak of %d MB, %d MB free", need, sl.PredictedMB, r.Free())
			if r.Free() >= need {
				why += fmt.Sprintf(", under memory pressure (%s)", psi(pressure))
			}

			warnf(a.log, "memory: %s: %s: handed back to the pool\n", sl.Name, why)

			return why, false
		}

		select {
		case <-ctx.Done():
		case <-time.After(min(memoryEvery, limit-time.Since(a.short))):
		}
	}
}
