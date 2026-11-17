package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// Observation is what one correlator saw a slice touch, or that it
// couldn't tell.
type Observation struct {
	Plugin    string
	CantTell  bool
	Reason    string
	Touched   []string
	SetupLike []string
}

// Observer asks the correlators what a slice touched: setup is the batch's
// setup directory, parts the slice's own, in order.
type Observer interface {
	Observe(ctx context.Context, source, slice, setup string, parts []string) ([]Observation, error)
}

// Reporter keeps one run's state in refs and shows it on GitHub.
type Reporter struct {
	Config *config.Config
	ID     ID
	Store  *gitstore.Store
	GitHub GitHub
	// Observer makes the recordings; without one a recording run's slices
	// error.
	Observer   Observer
	Collectors []Collector
	Nothing    string
	Checkout   string
	Attach     Attacher
	Stopped    string
	// RunEnded, when set, is told once every suite of the run has ended; its
	// error is a warning.
	RunEnded func(ctx context.Context) error

	attachTried bool
	attachErr   error
	endedTold   bool
	Warn        io.Writer
	// NoToken marks a batch's step run without a token: it writes nothing to
	// the refs, and the batch's end writes what its journal holds.
	NoToken bool
	// NoFlush keeps a slice's start and end off the network: they write the
	// journal, and a background `slice flush --every` writes it out.
	NoFlush bool
	// Meter measures the batch's cgroup at each slice's start and end; nil measures nothing.
	Meter Meter
	Now   func() time.Time
	URL   string
	// Throttle is how long a slice's report leaves a pending update on
	// GitHub before replacing it (default 15s; negative: never waits).
	Throttle time.Duration
}

// Context is a suite's commit status context in a run.
func Context(cfg *config.Config, run, suite string) string {
	return RunContext(cfg, run) + "/" + suite
}

// RunContext is the context of the status that sums a run's suites.
func RunContext(cfg *config.Config, run string) string {
	return cfg.GitHub.StatusPrefix + "/" + run
}

// rendersMax bounds how often one writer renders again because the refs
// moved under it; a later writer renders the rest.
const rendersMax = 5

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}

	return time.Now().UTC()
}

func (r *Reporter) warnf(format string, args ...any) {
	if r.Warn == nil {
		return
	}

	if _, err := fmt.Fprintf(r.Warn, format+"\n", args...); err != nil {
		return
	}
}

func (r *Reporter) runConfig() config.Run { return r.Config.Runs[r.ID.Run] }

func encode(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return append(data, '\n'), nil
}

// Start sets every suite's status and the run's, and makes the comment.
func (r *Reporter) Start(ctx context.Context) error {
	if err := r.ID.Check(); err != nil {
		return err
	}

	_, err := r.publish(ctx, scope{all: true, run: true})

	return err
}

// SliceEvent is one report from a slice's own steps: its start or its end.
type SliceEvent struct {
	Suite   string `json:"suite"`
	Slice   string `json:"slice"`
	Label   string `json:"label"`
	JobURL  string `json:"job_url,omitempty"`
	Running bool   `json:"running,omitempty"`
	// Result is the slice's result file; a missing one makes the slice
	// errored.
	Result string `json:"result,omitempty"`
	// Source is the tree under test, and Coverage the slice's coverage
	// directory, then the batch's setup's; a recording run records only when
	// it is set.
	Source       string   `json:"source,omitempty"`
	Coverage     []string `json:"coverage,omitempty"`
	CoverageRoot string   `json:"coverage_root,omitempty"`
	Parts        []string `json:"parts,omitempty"`
	// Extras is a directory of small files the task left for the slice, kept
	// in its ref for whole-suite checks to read.
	Extras       string  `json:"extras,omitempty"`
	Unrecordable string  `json:"unrecordable,omitempty"`
	NotRun       bool    `json:"not_run,omitempty"`
	Stopped      string  `json:"stopped,omitempty"`
	Seconds      float64 `json:"seconds,omitempty"`
	// Short is why a batch never started its slices: the worker was short of memory.
	Short string `json:"short,omitempty"`
	// Use is the batch's cgroup's reading at this line, Used what the slice used, on its end.
	Use  *Use `json:"use,omitempty"`
	Used *Use `json:"used,omitempty"`
}

// Slice reports one slice; see Slices.
func (r *Reporter) Slice(ctx context.Context, ev SliceEvent) (State, error) {
	states, err := r.Slices(ctx, []SliceEvent{ev})
	if err != nil {
		return "", err
	}

	return states[ev.Suite+"/"+ev.Slice], nil
}

// Slices writes each slice's ref, its state and in a recording run its
// recording, in one atomic push, and shows the run again. It returns the
// states written, by "<suite>/<slice>".
func (r *Reporter) Slices(ctx context.Context, evs []SliceEvent) (map[string]State, error) {
	if err := r.ID.Check(); err != nil {
		return nil, err
	}

	states := map[string]State{}
	sc := scope{throttle: true}
	ends := make([]*ending, 0, len(evs))

	for _, ev := range evs {
		if err := errors.Join(checkName("suite", ev.Suite), checkSlice(ev.Slice)); err != nil {
			return nil, err
		}

		e := &ending{ev: ev, sf: StateFile{Label: ev.Label, State: Running, URL: ev.JobURL, Updated: r.now()}, files: map[string][]byte{}}

		if !ev.Running {
			e.found = r.ended(ctx, ev, &e.sf, e.files)

			if !slices.Contains(sc.suites, ev.Suite) {
				sc.suites = append(sc.suites, ev.Suite)
			}
		}

		ends = append(ends, e)
	}

	if len(ends) == 0 {
		return states, nil
	}

	r.capRun(ctx, ends)

	ups, err := updates(r.ID, ends)
	if err != nil {
		return nil, err
	}

	message := fmt.Sprintf("%s%d slices", r.ID.Prefix(), len(ups))

	restore := filesUntil(r.Store, ends, time.Now())
	_, err = r.Store.Write(ctx, message, ups...)

	restore()

	if err != nil && ctx.Err() == nil && withFiles(ends) {
		// the states matter more than the files: the store may refuse
		// their size, or be too slow for it
		r.warnf("report %d slices: writing them with their failure files: %v; writing them without", len(ends), err)

		for _, e := range ends {
			e.dropAll(capFileText("the store refused the write with them: "+err.Error(), maxWhy))
		}

		if ups, err = updates(r.ID, ends); err == nil {
			_, err = r.Store.Write(ctx, message, ups...)
		}
	}

	if err != nil {
		return nil, err
	}

	for _, e := range ends {
		states[e.ev.Suite+"/"+e.ev.Slice] = e.sf.State
	}

	// the states are safe in their refs; GitHub catches up with the next
	// writer, so its trouble doesn't fail the slices
	if _, err := r.publish(ctx, sc); err != nil {
		r.warnf("report %d slices: %v", len(ups), err)
	}

	return states, nil
}

// Close ends the slices of a batch that are still open (no ref, pending or
// running) once the batch is over.
func (r *Reporter) Close(ctx context.Context, evs []SliceEvent) (map[string]State, error) {
	if err := r.ID.Check(); err != nil {
		return nil, err
	}

	snap, err := readSnapshot(ctx, r.Store, r.Config, r.ID)
	if err != nil {
		return nil, err
	}

	states := map[string]State{}

	var open []SliceEvent

	// a batch none of whose slices started or left a result ended before
	// its first slice: its setup failed, and its slices never ran
	none := !slices.ContainsFunc(evs, func(ev SliceEvent) bool {
		sf, ok := snap.states[ev.Suite+"/"+ev.Slice]
		_, err := os.Stat(ev.Result)

		return ok && sf.State != Pending || err == nil
	})

	for _, ev := range evs {
		key := ev.Suite + "/" + ev.Slice

		sf, ok := snap.states[key]
		if ok && sf.State != Pending && sf.State != Running {
			states[key] = sf.State

			continue
		}

		ev.Running, ev.NotRun = false, none
		open = append(open, ev)
	}

	closed, err := r.Slices(ctx, open)
	if err != nil {
		return nil, err
	}

	maps.Copy(states, closed)

	return states, nil
}

// ended reads how a slice ended into its state and its ref's files, and
// returns the failure files of a slice that failed or errored, read and
// within the slice's cap; the run's cap is applied to every slice of a
// write together.
func (r *Reporter) ended(ctx context.Context, ev SliceEvent, sf *StateFile, files map[string][]byte) []candidate {
	if ev.NotRun {
		notRunIn(sf, ev)

		return nil
	}

	sf.State, sf.Failures = readResult(ev.Result, secret.Redact)

	if _, err := os.Stat(ev.Result); ev.Stopped != "" && errors.Is(err, os.ErrNotExist) {
		sf.Failures = []Failure{{Test: "(stopped)", Tail: ev.Stopped}}
	}

	sf.Seconds = ev.Seconds
	usedIn(sf, ev.Used)

	// a slice that passed without running a test records nothing, so its
	// list stays as it was, and says so: a test file that runs nothing is
	// a finding
	noTests := sf.State == Passed && RanNoTests(ev.Result)
	if noTests {
		zero := 0
		sf.Tests = &zero
	}

	// in a recording run every slice records (a whole-suite check never
	// does); one that ended without its coverage can't turn green
	if r.Config.Records(r.ID.Run, ev.Suite) && !strings.HasPrefix(ev.Slice, WholeName("")) && !noTests {
		rec, marks, err := r.record(ctx, ev)
		sf.Recorded = true

		switch {
		case err == nil:
			files[recordingFile] = rec
			sf.CannotTell = marks

			for _, m := range marks {
				r.warnf("slice %s: %s could not tell, so it is bound to everything until a recording tells again: %s", ev.Slice, m.Plugin, secret.Redact(m.Reason))
			}
		case sf.State == Passed:
			sf.State = Errored
			sf.Failures = append(sf.Failures, Failure{Test: "(recording)", Tail: err.Error()})
		}
	}

	if ev.Extras != "" {
		extras, err := readExtras(ev.Extras)
		if err != nil {
			sf.Failures = append(sf.Failures, Failure{Test: "(extras)", Tail: err.Error()})
			if sf.State == Passed {
				sf.State = Errored
			}
		}

		for p, data := range extras {
			files[extraDir+p] = data
		}
	}

	// a passed slice's job page is gone once its run retires
	if sf.State == Passed {
		sf.URL = ""
	}

	r.scrub(sf, files)

	if sf.State != Failed && sf.State != Errored {
		return nil
	}

	// failure files are masked as they are read, text only: scrub would
	// break a binary one
	return r.failureFiles(ev, sf)
}

// notRunIn is the state of a slice its batch never ran: stopped before
// it, or short of memory.
func notRunIn(sf *StateFile, ev SliceEvent) {
	f := notRun
	if ev.Stopped != "" {
		f.Tail = ev.Stopped
	}

	if ev.Short != "" {
		f = Failure{Test: MemoryTest, Tail: ev.Short}
	}

	sf.State, sf.Failures = NotRun, []Failure{f}
}

func (r *Reporter) failureFiles(ev SliceEvent, sf *StateFile) []candidate {
	list := resultFiles(ev.Result)
	if len(list) == 0 {
		return nil
	}

	sliceCap, _ := r.Config.FilesCaps()
	if sliceCap == 0 {
		return nil
	}

	got, dropped := readFailureFiles(resultRoot(ev), ev.Slice, list, sliceCap)
	got, over := capFiles(got, sliceCap, errOverSlice)
	sf.Dropped = append(append(sf.Dropped, dropped...), over...)

	return got
}

func (r *Reporter) scrub(sf *StateFile, files map[string][]byte) {
	for i, f := range sf.Failures {
		sf.Failures[i] = Failure{Test: secret.Redact(f.Test), Tail: secret.Redact(f.Tail)}
	}

	for name, data := range files {
		files[name] = []byte(secret.Redact(string(data)))
	}
}

const maxExtras = 1 << 20

var errExtras = errors.New("too many extras")

func readExtras(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	total := 0

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.Type().IsRegular() {
			return nil
		}

		data, err := os.ReadFile(p) //nolint:gosec // the slice task's own output
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		total += len(data)
		if total > maxExtras {
			return fmt.Errorf("%w: %s holds more than %d bytes, the most a slice keeps", errExtras, dir, maxExtras)
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return fmt.Errorf("path: %w", err)
		}

		out[filepath.ToSlash(rel)] = data

		return nil
	})

	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("extras: %w", err)
	}

	return out, nil
}

var (
	errNoRecorder   = errors.New("this pipelinegen can't record coverage")
	errNoCoverage   = errors.New("the slice never ended in its batch, so it left no coverage to record")
	errUnrecordable = errors.New("no recording")
)

func (r *Reporter) record(ctx context.Context, ev SliceEvent) ([]byte, []recording.Mark, error) {
	if ev.Unrecordable != "" {
		return nil, nil, fmt.Errorf("%w: %s", errUnrecordable, ev.Unrecordable)
	}

	if len(ev.Coverage) == 0 {
		return nil, nil, errNoCoverage
	}

	if r.Nothing != "" {
		return nil, nil, fmt.Errorf("%w: %s", errUnrecordable, r.Nothing)
	}

	if r.Observer == nil {
		return nil, nil, errNoRecorder
	}

	rec, err := r.observe(ctx, ev)
	if err != nil {
		return nil, nil, err
	}

	return rec.Format(), rec.CannotTell, nil
}

func nothingRecorded(result string) string {
	if n := testsRan(result); n > 0 {
		return plural(n, "test") + " ran and nothing was recorded"
	}

	return "its tests ran and nothing was recorded"
}

var errNoObserver = errors.New("no configured correlator observes")

// observe asks the correlators what the slice touched over its setup and
// every part. One correlator that couldn't tell leaves the slice's list as
// it was.
func (r *Reporter) observe(ctx context.Context, ev SliceEvent) (recording.Recording, error) {
	setup, parts := ObserveInputs(ev)

	obs, err := r.Observer.Observe(ctx, ev.Source, ev.Slice, setup, parts)
	if err != nil {
		return recording.Recording{}, err
	}

	rec := recording.Recording{Observed: true}
	if len(obs) == 0 {
		rec.CannotTell = []recording.Mark{{Plugin: "-", Reason: errNoObserver.Error(), Since: r.now()}}

		return rec, nil
	}

	touched, setupLike := map[string]bool{}, map[string]bool{}

	for _, o := range obs {
		if o.CantTell {
			rec.CannotTell = append(rec.CannotTell, recording.Mark{Plugin: o.Plugin, Reason: secret.Redact(o.Reason), Since: r.now()})

			continue
		}

		for _, id := range o.Touched {
			touched[id] = true
		}

		for _, id := range o.SetupLike {
			setupLike[id] = true
		}
	}

	if len(rec.CannotTell) > 0 {
		return rec, nil
	}

	// tests that ran and touched nothing at all are far likelier a
	// correlator that answered wrongly than a test file that runs nothing
	// of the tree; an empty list would bind the slice to nothing
	if len(touched) == 0 && len(setupLike) == 0 && r.Config.Suites[ev.Suite].EmptyRecordings != config.EmptyAllow {
		rec.CannotTell = []recording.Mark{{Plugin: "correlators", Reason: nothingRecorded(ev.Result), Since: r.now()}}

		return rec, nil
	}

	for id := range touched {
		if !setupLike[id] {
			rec.Own = append(rec.Own, id)
		}
	}

	for id := range setupLike {
		rec.Setup = append(rec.Setup, id)
	}

	return rec, nil
}

// ObserveInputs are what the correlators observe of a slice that ended
// with coverage: the batch's setup directory and the slice's parts.
func ObserveInputs(ev SliceEvent) (setup string, parts []string) {
	parts = ev.Parts
	if len(parts) == 0 && len(ev.Coverage) > 0 {
		parts = ev.Coverage[:1]
	}

	if len(ev.Coverage) > 1 {
		setup = ev.Coverage[1]
	}

	return setup, parts
}

// Suite ends a suite: it marks the end, so the suite's slices that never
// reported show as errored, sets the suite's status and the run's, and
// tells whether every slice of the suite passed.
func (r *Reporter) Suite(ctx context.Context, suite string) (bool, error) {
	if err := errors.Join(r.ID.Check(), checkName("suite", suite)); err != nil {
		return false, err
	}

	data, err := encode(endMark{Ended: r.now(), Why: r.Stopped})
	if err != nil {
		return false, err
	}

	if _, err := r.Store.Write(ctx, r.ID.Prefix()+" "+suite+" ends",
		gitstore.Update{Ref: r.ID.EndRef(suite), Any: true, Files: map[string][]byte{endFile: data}}); err != nil {
		return false, r.endUnrecorded(ctx, suite, err)
	}

	run, err := r.publish(ctx, scope{suites: []string{suite}, run: true, attach: true})
	if err != nil {
		return false, err
	}

	r.markShort(ctx, run)
	r.recordPeaks(ctx, run, suite)

	// the suite's batches' refs move into the run's; one left behind is
	// read all the same, and the sweep takes it with the run
	if _, err := r.Store.Compact(ctx, r.ID.Prefix(), suite); err != nil {
		r.warnf("moving %s's batches into the run's ref: %v", suite, err)
	}

	if r.attachErr != nil {
		return false, r.attachErr
	}

	return passed(run, []string{suite}), nil
}

// ErrUnrecorded is a step whose records could not be written to the store
// after every retry: what it ran is not lost, but the run doesn't know it,
// and a rerun of the build runs it again.
var ErrUnrecorded = errors.New("the records could not be written")

const unrecordedText = "the suite's end could not be recorded: rerun the build"

// endUnrecorded is a suite's end whose mark could not be written: the
// statuses still turn to error, so neither shows a state the run's refs
// don't back.
func (r *Reporter) endUnrecorded(ctx context.Context, suite string, cause error) error {
	missed := []string{"the suite's end mark", "its status and the run's as the run stands"}

	switch snap, err := readSnapshot(ctx, r.Store, r.Config, r.ID); {
	case !r.attaches():
	case err != nil:
		missed = append(missed, "the run's lists, if this was its last suite to end (the run's refs could not be read to tell)")
	case lastToEnd(snap, suite):
		missed = append(missed, "the run's lists: this was its last suite to end, so its recordings are not attached to the commit")
	}

	if r.runConfig().Report.Statuses() {
		for _, st := range []CommitStatus{
			{State: statusError, Context: Context(r.Config, r.ID.Run, suite), Description: unrecordedText, TargetURL: r.URL},
			{State: statusError, Context: RunContext(r.Config, r.ID.Run), Description: unrecordedText, TargetURL: r.URL},
		} {
			if err := r.GitHub.SetStatus(ctx, r.ID.Commit, st); err != nil {
				missed = append(missed, "the error status "+st.Context+" ("+err.Error()+")")
			}
		}
	}

	return fmt.Errorf("suite %s: %w, so these were not done: %s; rerun the build: %w", suite, ErrUnrecorded, strings.Join(missed, "; "), cause)
}

func lastToEnd(snap snapshot, suite string) bool {
	for _, sp := range snap.plan.Suites {
		if sp.Suite != suite && snap.ends[sp.Suite].IsZero() {
			return false
		}
	}

	return true
}

// Finish shows the run's final state and tells whether every selected slice
// of every suite passed. The run's refs stay until the sweep.
func (r *Reporter) Finish(ctx context.Context) (bool, error) {
	if err := r.ID.Check(); err != nil {
		return false, err
	}

	run, err := r.publish(ctx, scope{all: true, run: true, final: true, attach: true})
	if err != nil {
		return false, err
	}

	if r.attachErr != nil {
		return false, r.attachErr
	}

	return passed(run, nil), nil
}

// ending does what a suite's end does once the run is over: a branch's
// recording run hangs its recordings on its commit once the run passed, or
// once every suite of a failed run ended with a slice that passed. The run
// is red then whatever the attach does, so a failed attach is a warning.
func (r *Reporter) ending(ctx context.Context, run Run, snap snapshot) {
	if r.attaches() && !r.attachTried {
		switch end := endingOf(run, snap.plan.All); {
		case end.Passed:
			r.attachTried = true
			r.attachErr = r.attach(ctx, end)
		case end.Some && allEnded(snap) && r.Attach != nil:
			r.attachTried = true

			if err := r.attach(ctx, end); err != nil {
				r.warnf("warning: the lists of the slices that passed: %v", err)
			}
		}
	}

	if r.RunEnded != nil && !r.endedTold && allEnded(snap) {
		r.endedTold = true

		if err := r.RunEnded(ctx); err != nil {
			r.warnf("ending the run: %v", err)
		}
	}
}

func allEnded(snap snapshot) bool {
	for _, sp := range snap.plan.Suites {
		if snap.ends[sp.Suite].IsZero() {
			return false
		}
	}

	return true
}

func passed(run Run, suites []string) bool {
	for _, s := range run.Suites {
		if suites != nil && !slices.Contains(suites, s.Name) {
			continue
		}

		for _, sl := range s.Slices {
			if sl.State != Passed {
				return false
			}
		}
	}

	return true
}

type scope struct {
	all    bool
	suites []string
	run    bool
	final  bool
	// throttle lets a slice's report skip a pending update GitHub got
	// moments ago: a hundred slices would otherwise run into GitHub's limits
	// on writes.
	throttle bool
	attach   bool
}

// publish shows the run as its refs hold it. When the refs moved meanwhile
// it renders again from the new listing, so the last writer always leaves
// the newest state.
func (r *Reporter) publish(ctx context.Context, sc scope) (Run, error) {
	snap, err := readSnapshot(ctx, r.Store, r.Config, r.ID)
	if err != nil {
		return Run{}, err
	}

	// the job's output carries the plan's warnings too, where the steps
	// that judge the run read them
	if sc.run || sc.all {
		for _, w := range (Run{PlanWarnings: snap.plan.Warnings, Plugins: snap.plan.Plugins}).Warnings() {
			r.warnf("warning: %s", w)
		}
	}

	var run Run

	for range rendersMax {
		if sc.final {
			for _, sp := range snap.plan.Suites {
				snap.ends[sp.Suite] = r.now()
			}
		}

		run = buildRun(r.ID, snap, r.groups())
		run.Now = r.now()
		run.Reasoning = r.runConfig().Report.Reasoning

		if sc.attach {
			r.ending(ctx, run, snap)
		}

		// a suite's end and the finish of a run of everything trace its
		// failures to the narrowed changes that left them out
		if sc.run {
			if run.Net, err = r.net(ctx, run, snap); err != nil {
				r.warnf("tracing failures to narrowed changes: %v", err)

				run.Net = nil
			}
		}

		if err := r.show(ctx, sc, r.statuses(run, sc), r.comment(run), snap.alsoTo); err != nil {
			return run, err
		}

		next, err := readSnapshot(ctx, r.Store, r.Config, r.ID)
		if err != nil {
			return run, err
		}

		if next.same(snap) {
			return run, nil
		}

		snap = next
	}

	return run, nil
}

func (r *Reporter) groups() map[string]*regexp.Regexp {
	out := map[string]*regexp.Regexp{}

	for name, s := range r.Config.Suites {
		if s.Group == "" {
			continue
		}

		if re, err := regexp.Compile(s.Group); err == nil {
			out[name] = re
		}
	}

	return out
}

func (r *Reporter) statuses(run Run, sc scope) []CommitStatus {
	if !r.runConfig().Report.Statuses() {
		return nil
	}

	var out []CommitStatus

	for _, s := range run.Suites {
		if !sc.all && !slices.Contains(sc.suites, s.Name) {
			continue
		}

		state, desc := s.Status()
		out = append(out, CommitStatus{State: state, Context: Context(r.Config, r.ID.Run, s.Name), Description: desc, TargetURL: r.URL})
	}

	if sc.run {
		state, desc := run.RunStatus()
		if r.attachErr != nil {
			state, desc = "error", statusText("passed, but its lists weren't attached: "+r.attachErr.Error())
		}

		out = append(out, CommitStatus{State: state, Context: RunContext(r.Config, r.ID.Run), Description: desc, TargetURL: r.URL})
	}

	return out
}

func (r *Reporter) comment(run Run) string {
	cm := r.runConfig().Report.Comment
	if cm == nil || r.ID.PR <= 0 {
		return ""
	}

	return Comment(run, r.ID.PR, Format(cm.Format))
}

const defaultThrottle = 15 * time.Second

func (r *Reporter) throttled(sc scope, updated time.Time) bool {
	limit := r.Throttle
	if limit == 0 {
		limit = defaultThrottle
	}

	return sc.throttle && limit > 0 && !updated.IsZero() && time.Since(updated) < limit
}

func (r *Reporter) show(ctx context.Context, sc scope, statuses []CommitStatus, body string, alsoTo []int) error {
	last, err := r.setStatuses(ctx, sc, statuses)
	if err != nil {
		return err
	}

	if body == "" {
		return nil
	}

	// the comment goes with the run's statuses: within the throttle of
	// their last write, it waits for a later report too
	if sc.throttle && len(statuses) == 0 {
		if last, err = r.lastStatus(ctx); err != nil {
			return err
		}
	}

	if r.throttled(sc, last) {
		return nil
	}

	var errs []error

	for _, pr := range r.commentTargets(alsoTo) {
		if err := r.GitHub.Comment(ctx, pr, Marker(r.ID.Run, r.ID.PR), body); err != nil {
			errs = append(errs, fmt.Errorf("comment on #%d: %w", pr, err))
		}
	}

	return errors.Join(errs...)
}

// setStatuses posts the statuses that differ from what the commit shows; a
// change of state always goes, a new count of a pending suite may wait
// for the next report. It returns when the commit last got one of them.
func (r *Reporter) setStatuses(ctx context.Context, sc scope, statuses []CommitStatus) (time.Time, error) {
	var last time.Time

	if len(statuses) == 0 {
		return last, nil
	}

	current, err := r.GitHub.Statuses(ctx, r.ID.Commit)
	if err != nil {
		return last, fmt.Errorf("statuses: %w", err)
	}

	have := map[string]CommitStatus{}
	for _, st := range current {
		have[st.Context] = st
	}

	for _, st := range statuses {
		old, ok := have[st.Context]
		if ok && old.UpdatedAt != nil && old.UpdatedAt.After(last) {
			last = *old.UpdatedAt
		}

		if ok && old.State == st.State && old.Description == st.Description && old.TargetURL == st.TargetURL {
			continue
		}

		if ok && old.State == st.State && old.UpdatedAt != nil && r.throttled(sc, *old.UpdatedAt) {
			continue
		}

		if err := r.GitHub.SetStatus(ctx, r.ID.Commit, st); err != nil {
			return last, fmt.Errorf("status %s: %w", st.Context, err)
		}
	}

	return last, nil
}

// lastStatus is when the commit last got one of the run's statuses.
func (r *Reporter) lastStatus(ctx context.Context) (time.Time, error) {
	var last time.Time

	current, err := r.GitHub.Statuses(ctx, r.ID.Commit)
	if err != nil {
		return last, fmt.Errorf("statuses: %w", err)
	}

	own := RunContext(r.Config, r.ID.Run)

	for _, st := range current {
		if (st.Context == own || strings.HasPrefix(st.Context, own+"/")) && st.UpdatedAt != nil && st.UpdatedAt.After(last) {
			last = *st.UpdatedAt
		}
	}

	return last, nil
}

func (r *Reporter) commentTargets(alsoTo []int) []int {
	out := []int{r.ID.PR}

	for _, pr := range slices.Concat(r.ID.AlsoTo, alsoTo) {
		if pr > 0 && !slices.Contains(out, pr) {
			out = append(out, pr)
		}
	}

	return out
}
