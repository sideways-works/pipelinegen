package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/osproc"
	"github.com/sideways-works/pipelinegen/internal/piperun"
	"github.com/sideways-works/pipelinegen/internal/report"
)

const flushCommand = "report flush"

// confirmedFlush writes the ends of a session's slices piperun confirmed, from the session's journal,
// which it only reads. It keeps what it wrote, so a round writes only what is new; a process started
// again writes everything once more, the same states.
type confirmedFlush struct {
	s         *step
	j         *report.Journal
	suite     string
	confirmed string
	written   map[string]string
}

// ready tells whether a slice's end can be written here: in a recording run only once the session kept
// what its correlators observed, as the session's end observes the rest.
func (fl *confirmedFlush) ready(ev report.SliceEvent) bool {
	r := fl.s.reporter
	if !r.Config.Records(r.ID.Run, fl.suite) || ev.Unrecordable != "" || len(ev.Coverage) == 0 {
		return true
	}

	if fl.s.plugins == nil || r.Observer == nil {
		return false
	}

	for _, c := range fl.s.plugins.sets {
		if _, ok := fl.s.plugins.kept(ev.Slice, c.name); !ok {
			return false
		}
	}

	return true
}

func (fl *confirmedFlush) round(ctx context.Context) (int, error) {
	keys, err := piperun.ReadList(fl.confirmed)
	if err != nil {
		return 0, err
	}

	ends, err := fl.j.Ends()
	if err != nil {
		return 0, err
	}

	var (
		evs  []report.SliceEvent
		sums []string
	)

	for _, ev := range ends {
		// a stopped slice's end is the session's end's to write
		if c, ok := keys[ev.Slice]; ev.Suite != fl.suite || !ok || c.Outcome == piperun.Stopped || !fl.ready(ev) {
			continue
		}

		data, err := json.Marshal(ev)
		if err != nil {
			return 0, fmt.Errorf("flush: %w", err)
		}

		if fl.written[ev.Slice] == string(data) {
			continue
		}

		evs = append(evs, ev)
		sums = append(sums, string(data))
	}

	if len(evs) == 0 {
		return 0, nil
	}

	if _, err := fl.s.reporter.Slices(ctx, evs); err != nil {
		return 0, err
	}

	for i, ev := range evs {
		fl.written[ev.Slice] = sums[i]
	}

	return len(evs), nil
}

// reportFlush is a piperun session's sidecar: it writes the slices piperun confirmed as they end, never
// one whose end piperun refused, and closes nothing; the session's end closes what is left.
func reportFlush(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		s                 step
		result, confirmed string
		every             time.Duration
	)

	if err := s.open(ctx, flushCommand, args, func(fs *flag.FlagSet) {
		pathVar(fs, &result, "result", "result", "the session's result directory, which holds its journal; only read", reads)
		pathVar(fs, &confirmed, "confirmed", "", "the items whose end piperun recorded ($PIPERUN_CONFIRMED), JSON lines {\"key\", \"outcome\"}", readsFile)
		fs.DurationVar(&every, "every", 0, "flush again every period until stopped, at the lowest priority (0: once)")
	}); err != nil {
		return err
	}

	defer s.close(&err)

	if err := needEnv(envSuite); err != nil {
		return err
	}

	if confirmed == "" {
		return fmt.Errorf("%w: --confirmed names the list of confirmed items", errUsage)
	}

	fl := &confirmedFlush{s: &s, j: report.JournalIn(result), suite: os.Getenv(envSuite), confirmed: confirmed, written: map[string]string{}}

	if every <= 0 {
		n, err := fl.round(ctx)
		if err != nil {
			return err
		}

		return say(stdout, "flushed %s\n", plural(n, "confirmed slice"))
	}

	osproc.LowestPriority()

	// piperun stops a sidecar when its session ends.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	inBackground(s.reporter.Store)

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		if n, err := fl.round(ctx); err != nil && ctx.Err() == nil {
			warnf(os.Stderr, "pipelinegen report flush: this round wrote nothing, the next will: %v\n", err)
		} else if n > 0 {
			warnf(os.Stderr, "pipelinegen report flush: flushed %s\n", plural(n, "confirmed slice"))
		}

		select {
		case <-ctx.Done():
			return say(stdout, "flushing stopped\n")
		case <-tick.C:
		}
	}
}

type sessionFiles struct {
	batch, result, coverage, confirmed, requeued, pools string
}

// sessionEnd closes a piperun session's slices: every confirmed one, from its journal's end, or errored
// when the journal holds none; a requeued one stays open for the session that takes it next, and one in
// neither list is another session's now.
func sessionEnd(ctx context.Context, s *step, f sessionFiles, stdout io.Writer) error {
	suite := os.Getenv(envSuite)

	confirmed, err := piperun.ReadList(f.confirmed)
	if err != nil {
		return err
	}

	requeued, err := piperun.ReadList(f.requeued)
	if err != nil {
		return err
	}

	var open []string

	for k := range requeued {
		if _, ok := confirmed[k]; !ok {
			open = append(open, k)
		}
	}

	slices.Sort(open)

	if len(open) > 0 {
		if err := say(stdout, "session %s: %s handed back to the pool, left open: %s\n", f.batch, plural(len(open), "slice"), strings.Join(open, ", ")); err != nil {
			return err
		}
	}

	if len(confirmed) == 0 {
		return say(stdout, "session %s: no slice confirmed, nothing to close\n", f.batch)
	}

	if f.coverage != "" && holdsNoFile(f.coverage) {
		s.reporter.Nothing = "session " + f.batch + " recorded nothing"
	}

	ended, evs, err := confirmedEvents(s, f, suite, confirmed)
	if err != nil {
		return err
	}

	if s.reporter.Observer != nil {
		s.plugins.sayLeftUnrunnable(suite, evs, os.Stderr)
	}

	// what the session ended goes in first, then what a person stopped, errored and never run again
	if _, err := s.reporter.Slices(ctx, ended); err != nil {
		return unwritten(f.batch, evs, err)
	}

	if _, err := s.reporter.Slices(ctx, stoppedEvents(s, f, suite, confirmed)); err != nil {
		return unwritten(f.batch, evs, err)
	}

	states, err := s.reporter.Close(ctx, evs)
	if err != nil {
		return unwritten(f.batch, evs, err)
	}

	more := ""
	if s.reporter.Nothing != "" {
		more = fmt.Sprintf("; %s: no slice left coverage in %s, so none of its slices is recorded", s.reporter.Nothing, f.coverage)
	}

	return batchNotPassed(ctx, s, f.batch, suite, evs, states, more)
}

// confirmedEvents are the journal's ends of the slices piperun confirmed as ended by the session, and
// all the confirmed slices as their session's end closes them: one the journal holds no end of ends
// errored, piperun having ended it, and a stopped one errors saying so, whatever the journal holds.
func confirmedEvents(s *step, f sessionFiles, suite string, confirmed map[string]piperun.Listed) (ended, evs []report.SliceEvent, err error) {
	ends, err := report.JournalIn(f.result).Ends()
	if err != nil {
		return nil, nil, err
	}

	pool, _ := readPool(f.pools, suite) //nolint:errcheck // without it a slice the journal never saw is named by its key

	seen := map[string]bool{}

	for _, ev := range ends {
		if c, ok := confirmed[ev.Slice]; ev.Suite == suite && ok && c.Outcome != piperun.Stopped {
			ended = append(ended, ev)
			seen[ev.Slice] = true
		}
	}

	evs = slices.Clone(ended)

	for _, k := range slices.Sorted(maps.Keys(confirmed)) {
		if seen[k] {
			continue
		}

		ev := sliceEvent(suite, batchSlice{Name: k, Label: pool[k].Label}, s.root, f.result, "")
		ev.Stopped = "the session never reported its end, and piperun ended it"

		if confirmed[k].Outcome == piperun.Stopped {
			ev.Result, ev.Stopped = "", stoppedWhy
		}

		evs = append(evs, ev)
	}

	return ended, evs, nil
}

const stoppedWhy = "a person stopped the build while the slice was the session's; it is not run again"

// stoppedEvents end the slices of a stopped build errored, from no result whatever their session wrote:
// never not run, which a sweep would hand on again.
func stoppedEvents(s *step, f sessionFiles, suite string, confirmed map[string]piperun.Listed) []report.SliceEvent {
	pool, _ := readPool(f.pools, suite) //nolint:errcheck // without it a slice is named by its key

	var out []report.SliceEvent

	for _, k := range slices.Sorted(maps.Keys(confirmed)) {
		if confirmed[k].Outcome != piperun.Stopped {
			continue
		}

		ev := sliceEvent(suite, batchSlice{Name: k, Label: pool[k].Label}, s.root, f.result, "")
		ev.Result, ev.Extras, ev.Stopped = "", "", stoppedWhy
		out = append(out, ev)
	}

	return out
}
