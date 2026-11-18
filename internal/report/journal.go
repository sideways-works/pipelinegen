package report

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// JournalDir is where a batch keeps its journal, in its result directory so the batch's ensure step
// gets it with the results; checkSlice refuses a slice of that name.
const JournalDir = ".pipelinegen"

const journalFile = "journal.jsonl"

const (
	eventSetupEnd = "setup-end"
	eventStart    = "start"
	eventPartEnd  = "part-end"
	eventEnd      = "end"
	eventFlushed  = "flushed"
)

// The actions a collector is asked for, as the plugin protocol names them.
const (
	CollectSetupEnd   = "setup-end"
	CollectSliceStart = "slice-start"
	CollectSliceEnd   = "slice-end"
)

// Collector gathers what a batch's setup or slice left behind into its own
// directory under dir: a correlator's collect.
type Collector interface {
	Collect(ctx context.Context, action, slice, dir string) error
}

// PartDir is where the k-th part of a slice (from 1) leaves its coverage: the first in <root>/<slice>,
// each later one apart, so a collector that writes a fixed name never overwrites an earlier part's.
func PartDir(root, slice string, k int) string {
	if k <= 1 {
		return filepath.Join(root, filepath.FromSlash(slice))
	}

	return filepath.Join(root, JournalDir, fmt.Sprintf("part-%d", k), filepath.FromSlash(slice))
}

// ErrNoSetupEnd is a slice started in a recording run before its batch's
// setup was closed.
var ErrNoSetupEnd = errors.New("the batch's setup hasn't ended")

func (j *Journal) SetupEnded() (bool, error) {
	sum, err := j.load()
	if err != nil {
		return false, err
	}

	return sum.SetupEnded, nil
}

var ErrSetup = errors.New("setup")

type journalLine struct {
	Event string      `json:"event"`
	At    time.Time   `json:"at"`
	Lines int         `json:"lines,omitempty"`
	Slice *SliceEvent `json:"slice,omitempty"`
}

// Journal is a batch's record of its slices' starts and ends not yet in their refs; each line is
// synced before its step returns, so whatever kills the batch, its ensure finds every one.
type Journal struct {
	Path string

	sum   *summary
	dirty bool
}

func JournalIn(dir string) *Journal {
	return &Journal{Path: filepath.Join(dir, JournalDir, journalFile)}
}

func (j *Journal) append(line journalLine) error {
	data, err := json.Marshal(j.portableLine(line))
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(j.Path), 0o750); err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	f, err := os.OpenFile(j.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("journal: %w", err)
	}

	_, werr := f.Write(append(data, '\n'))
	serr := f.Sync()

	var size int64
	if info, err := f.Stat(); err == nil {
		size = info.Size()
	}

	cerr := f.Close()

	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("journal %s: %w", j.Path, err)
	}

	// a summary that misses this line would be made again from the
	// journal anyway; one that has it saves the next step the reading
	if j.sum != nil && j.sum.Size+int64(len(data))+1 == size {
		j.sum.add(line)
		j.sum.Size = size
		j.dirty = true
	} else {
		j.sum = nil
	}

	return nil
}

// read returns the journal's lines; a last line cut short by a crash
// mid-write is dropped, since its step never returned.
func (j *Journal) read() ([]journalLine, error) {
	f, err := os.Open(j.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("journal: %w", err)
	}

	defer f.Close() //nolint:errcheck // read only

	var out []journalLine

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 16<<20)

	for sc.Scan() {
		var l journalLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}

		out = append(out, j.localLine(l))
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("journal %s: %w", j.Path, err)
	}

	return out, nil
}

// Ends are the slices the journal holds as ended, each as its last end
// says it, with its paths this task's, in the order they ended.
func (j *Journal) Ends() ([]SliceEvent, error) {
	lines, err := j.read()
	if err != nil {
		return nil, err
	}

	var (
		order []string
		last  = map[string]SliceEvent{}
	)

	for _, l := range lines {
		if l.Slice == nil || l.Event != eventEnd {
			continue
		}

		key := l.Slice.Suite + "/" + l.Slice.Slice
		if _, ok := last[key]; !ok {
			order = append(order, key)
		}

		ev := *l.Slice
		ev.Running = false
		last[key] = ev
	}

	out := make([]SliceEvent, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}

	return out, nil
}

func pending(lines []journalLine) ([]SliceEvent, time.Time) {
	from, since := 0, time.Time{}

	if len(lines) > 0 {
		since = lines[0].At
	}

	for i, l := range lines {
		if l.Event == eventFlushed {
			from, since = min(l.Lines, i), l.At
		}
	}

	var (
		order []string
		last  = map[string]SliceEvent{}
	)

	for _, l := range lines[from:] {
		if l.Slice == nil || l.Event == eventFlushed {
			continue
		}

		key := l.Slice.Suite + "/" + l.Slice.Slice
		if _, ok := last[key]; !ok {
			order = append(order, key)
		}

		ev := *l.Slice
		ev.Running = l.Event != eventEnd
		last[key] = ev
	}

	out := make([]SliceEvent, 0, len(order))
	for _, k := range order {
		out = append(out, last[k])
	}

	return out, since
}

// SetupEnd closes what the batch's setup ran into dir (coverage/_setup) through every collector,
// once, before the first slice, so each slice's own recording holds only what its tests ran.
func (r *Reporter) SetupEnd(ctx context.Context, j *Journal, dir string) error {
	for _, c := range r.Collectors {
		if err := c.Collect(ctx, CollectSetupEnd, "", dir); err != nil {
			return fmt.Errorf("%w: %w", ErrSetup, err)
		}
	}

	if _, err := j.load(); err != nil {
		return err
	}

	defer j.saveSummary()

	return j.append(journalLine{Event: eventSetupEnd, At: r.now()})
}

// Started notes that a slice's tests run now, its first part or a later one; the slice that ran
// until now is closed first, and a collector that fails leaves it unrecordable.
func (r *Reporter) Started(ctx context.Context, j *Journal, ev SliceEvent, needSetup bool, every time.Duration) error {
	sum, err := j.load()
	if err != nil {
		return err
	}

	defer j.saveSummary()

	if needSetup && !sum.SetupEnded {
		return ErrNoSetupEnd
	}

	if open := sum.Active; open != nil && same(*open, ev) {
		return nil
	} else if open != nil {
		if sum, err = r.closeOther(ctx, j, *open); err != nil {
			return err
		}
	}

	// a slice that comes round again leaves this part's coverage apart
	if k := len(sum.of(ev).Parts) + 1; k > 1 && len(ev.Coverage) > 0 && ev.CoverageRoot != "" {
		ev.Coverage = slices.Clone(ev.Coverage)
		ev.Coverage[0] = PartDir(ev.CoverageRoot, ev.Slice, k)
	}

	ev.Running = true
	ev.Unrecordable = r.collect(ctx, CollectSliceStart, ev)
	ev.Use = r.meter(true)

	if err := j.append(journalLine{Event: eventStart, At: r.now(), Slice: &ev}); err != nil {
		return err
	}

	// flush at a switch once the period is over, so a batch whose slices end only after its whole
	// run still shows progress; without a token the batch's end writes it
	if !r.NoToken && !r.NoFlush && r.now().Sub(sum.Since) >= every {
		if _, err := r.Flush(ctx, j); err != nil {
			r.warnf("flush at %s's start: %v", ev.Slice, err)
		}
	}

	return nil
}

func same(a, b SliceEvent) bool { return a.Suite == b.Suite && a.Slice == b.Slice }

func (r *Reporter) closePart(ctx context.Context, ev SliceEvent) string {
	// every collector resets for the next slice, whatever went wrong
	return r.collect(ctx, CollectSliceEnd, ev)
}

func (r *Reporter) collect(ctx context.Context, action string, ev SliceEvent) string {
	if len(ev.Coverage) == 0 {
		return ""
	}

	// each collector gets its call, so all of them stay in step; the
	// first failure is the one reported
	why := ""

	for _, c := range r.Collectors {
		if err := c.Collect(ctx, action, ev.Slice, ev.Coverage[0]); err != nil && why == "" {
			why = err.Error()
		}
	}

	return why
}

// Ended journals a slice's end with its parts' times, closing the running part first; it flushes at
// once when the slice didn't pass or the last flush is older than every, else returns unwritten.
func (r *Reporter) Ended(ctx context.Context, j *Journal, ev SliceEvent, every time.Duration) (State, error) {
	return r.endPart(ctx, j, ev, every, true)
}

// EndedApart is Ended leaving another slice's part running: a driver may report a slice's result
// after the next slice started.
func (r *Reporter) EndedApart(ctx context.Context, j *Journal, ev SliceEvent, every time.Duration) (State, error) {
	return r.endPart(ctx, j, ev, every, false)
}

// PartStopped closes a slice's part that still runs without ending the
// slice: its result comes later.
func (r *Reporter) PartStopped(ctx context.Context, j *Journal, ev SliceEvent) error {
	sum, err := j.load()
	if err != nil {
		return err
	}

	defer j.saveSummary()

	open := sum.Active
	if open == nil || !same(*open, ev) {
		return nil
	}

	part := *open
	part.Unrecordable = r.closePart(ctx, part)
	part.Use = r.meter(false)

	if ev.Unrecordable != "" {
		part.Unrecordable = ev.Unrecordable
	}

	return j.append(journalLine{Event: eventPartEnd, At: r.now(), Slice: &part})
}

func (r *Reporter) endPart(ctx context.Context, j *Journal, ev SliceEvent, every time.Duration, closeOthers bool) (State, error) {
	sum, err := j.load()
	if err != nil {
		return "", err
	}

	defer j.saveSummary()

	// what the slice's driver measured, when it says, is its time: in a
	// bundle the journal sees only the end
	measured := ev.Seconds

	// the part still running closes at the first end, whichever slice's,
	// so no slice's time takes in the others' ends
	if open := sum.Active; open != nil && same(*open, ev) {
		ev.Unrecordable = r.closePart(ctx, *open)
	} else if open != nil && closeOthers {
		if sum, err = r.closeOther(ctx, j, *open); err != nil {
			return "", err
		}
	}

	mine := sum.of(ev)

	// what went wrong in an earlier part stays with the slice
	if mine.Trouble != "" && ev.Unrecordable == "" {
		ev.Unrecordable = mine.Trouble
	}

	at := r.now()
	ev.Running = false
	ev.Parts = mine.Parts
	ev.Seconds = mine.Seconds

	if !mine.From.IsZero() {
		ev.Seconds += at.Sub(mine.From).Seconds()
	}

	if measured > 0 {
		ev.Seconds = measured
	}

	ev.Use = r.meter(false)
	ev.Used = closeUse(mine.Use, ev.Use)

	if err := j.append(journalLine{Event: eventEnd, At: at, Slice: &ev}); err != nil {
		return "", err
	}

	result, _ := ReadResult(ev.Result)

	if r.NoToken || r.NoFlush || result == Passed && ev.Unrecordable == "" && r.now().Sub(sum.Since) < every {
		return result, nil
	}

	// the journal holds the end, so a flush that fails here loses
	// nothing: the next one, or the batch's ensure, writes it
	states, err := r.Flush(ctx, j)
	if err != nil {
		r.warnf("flush after %s: %v", ev.Slice, err)

		return result, nil
	}

	return states[ev.Suite+"/"+ev.Slice], nil
}

func (r *Reporter) closeOther(ctx context.Context, j *Journal, open SliceEvent) (*summary, error) {
	open.Unrecordable = r.closePart(ctx, open)
	open.Use = r.meter(false)

	if err := j.append(journalLine{Event: eventPartEnd, At: r.now(), Slice: &open}); err != nil {
		return nil, err
	}

	if j.sum != nil {
		return j.sum, nil
	}

	return j.load()
}

// Flush writes the journal's starts and ends since its last flush in one push and one render; a crash
// between the push and the note is harmless, since each write replaces the slice's ref.
func (r *Reporter) Flush(ctx context.Context, j *Journal) (map[string]State, error) {
	lines, err := j.read()
	if err != nil {
		return nil, err
	}

	return r.flushLines(ctx, j, lines)
}

func (r *Reporter) flushLines(ctx context.Context, j *Journal, lines []journalLine) (map[string]State, error) {
	evs, _ := pending(lines)
	if len(evs) == 0 {
		return map[string]State{}, nil
	}

	states, err := r.Slices(ctx, evs)
	if err != nil {
		return nil, err
	}

	if err := j.append(journalLine{Event: eventFlushed, At: r.now(), Lines: len(lines)}); err != nil {
		return nil, err
	}

	return states, nil
}
