package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Hooks are what the batch does at a slice's boundaries.
type Hooks interface {
	Start(ctx context.Context, sl plugin.DriverSlice) error
	// Stop closes the part a slice's tests ran; unclosed, when set, says why
	// its stop was never marked.
	Stop(ctx context.Context, sl plugin.DriverSlice, unclosed string) error
	// End ends a slice with the driver's end event, or nil when it got no result (why is the driver's last
	// words); apart says another slice may be running, so its part stays open.
	End(ctx context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, apart bool) error
}

type Step struct {
	Driver Driver
	Desc   plugin.Description
	Batch  bool
	Base   plugin.DriverRequest
}

type Runner struct {
	Hooks Hooks
	// Record and Stack are the run's and the suite's: a recording run of a
	// stack suite bundles only through marks.
	Record, Stack bool
	// Collects says the suite names correlators, whose collect must come at each slice's real edges: a
	// recording run of such a suite bundles through marks too.
	Collects bool
	// Marks is how a driver marks; Marks.Dir is made afresh for each marked bundle and removed when it
	// ends, and only that bundle's call gets PIPELINEGEN_MARKS.
	Marks plugin.Marks
	Log   io.Writer

	mu sync.Mutex
}

// ErrHooks is a hook that failed: the batch's journal couldn't take a
// slice's boundary, which ends the batch.
var ErrHooks = errors.New("batch journal")

type mode int

const (
	perSlice mode = iota
	bundled
	marked
)

func (r *Runner) mode(st Step) mode {
	switch {
	case !st.Batch:
		return perSlice
	case r.Record && r.Stack:
		return marked
	case r.Record && !st.Desc.Apart:
		sayf(r.Log, "pipelinegen slice run: %s keeps no recording apart in a bundle, so in a recording run its slices run one by one\n", st.Driver.Name())

		return perSlice
	case r.Record && r.Collects:
		return marked
	default:
		return bundled
	}
}

// Run runs a step's slices one by one, or as one bundle whose slices left without a result run again one
// by one; it fails only when the batch's journal does, which says what a slice did.
func (r *Runner) Run(ctx context.Context, st Step, todo []plugin.DriverSlice) error {
	if len(todo) == 0 {
		return nil
	}

	switch r.mode(st) {
	case perSlice:
		return r.oneByOne(ctx, st, todo)
	case marked:
		sayf(r.Log, "pipelinegen slice run: %s runs the %d slices of its bundle one at a time and marks each, so the correlators collect at each slice's own start and end\n", st.Driver.Name(), len(todo))

		return r.bundle(ctx, st, todo, true)
	default:
		return r.bundle(ctx, st, todo, false)
	}
}

func (r *Runner) oneByOne(ctx context.Context, st Step, todo []plugin.DriverSlice) error {
	for _, sl := range todo {
		if err := r.alone(ctx, st, sl); err != nil {
			return err
		}
	}

	return nil
}

func (r *Runner) alone(ctx context.Context, st Step, sl plugin.DriverSlice) error {
	if err := r.hook(func() error { return r.Hooks.Start(ctx, sl) }); err != nil {
		return err
	}

	req := st.Base
	req.Batch = false
	req.Slices = []plugin.DriverSlice{sl}

	var got *plugin.DriverEvent

	tail, err := st.Driver.Run(ctx, req, func(ev plugin.DriverEvent) {
		if ev.Event == plugin.EventEnd && ev.Slice == sl.Name && got == nil {
			got = &ev
		}
	})

	why := ""
	if got == nil {
		why = noResult(st, err, tail)
	}

	return r.hook(func() error { return r.Hooks.End(ctx, sl, got, why, false) })
}

func noResult(st Step, err error, tail string) string {
	what := st.Driver.Name() + " gave no result for it"
	if err != nil {
		what += " (" + err.Error() + ")"
	}

	if tail != "" {
		what += "; its last lines:\n" + tail
	}

	return what
}

// bundleRun is one bundle's state while its call runs; the marks and the
// stream reach it side by side, through the runner's hook lock.
type bundleRun struct {
	r         *Runner
	driver    string
	byName    map[string]plugin.DriverSlice
	withMarks bool
	started   map[string]bool
	ended     map[string]bool
	failed    error
	open      string
}

const stopNotMarked = "its stop was not marked (its test was killed, or its driver ended first), so what its correlators collect at its end is missing"

func ranUnmarked(driver string) string {
	return "it ran without marks (a replayed result, or a driver or test fixture that doesn't mark), so what its correlators collect at its edges is missing. " +
		driver + " must mark each slice's start and stop in a recording bundle (`pipelinegen slice mark`, or PIPELINEGEN_MARKS for a fixture inside the tests); " +
		"or set batch: false on the run step, which gives each slice a call of its own and real edges without marks"
}

// closeOpen closes the slice still open, as one whose stop was never
// marked; under the hook lock.
func (b *bundleRun) closeOpen(ctx context.Context) error {
	if b.open == "" {
		return nil
	}

	sl := b.byName[b.open]
	b.open = ""

	return b.r.Hooks.Stop(ctx, sl, stopNotMarked)
}

func (b *bundleRun) mark(ctx context.Context, m Mark) error {
	sl, ok := b.byName[m.Slice]
	if !ok {
		return fmt.Errorf("%w: no slice %q in this bundle", ErrMark, m.Slice)
	}

	return b.r.hook(func() error {
		// a slice's tests may come round again before its end (shards, a retry): each start opens a part
		// that adds to its recording; after its end there is none to add to
		if b.ended[sl.Name] {
			return fmt.Errorf("%w: slice %s already ended in this bundle", ErrMark, sl.Name)
		}

		if m.Mark == plugin.MarkStart {
			// another slice still open lost its stop: a test killed hard
			// marks nothing
			if b.open != "" && b.open != sl.Name {
				if err := b.closeOpen(ctx); err != nil {
					return err
				}
			}

			b.started[sl.Name] = true
			b.open = sl.Name

			return b.r.Hooks.Start(ctx, sl)
		}

		if b.open == sl.Name {
			b.open = ""
		}

		return b.r.Hooks.Stop(ctx, sl, "")
	})
}

func (b *bundleRun) event(ctx context.Context, ev plugin.DriverEvent) {
	sl, ok := b.byName[ev.Slice]
	if ev.Event != plugin.EventEnd || !ok {
		return
	}

	err := b.r.hook(func() error {
		if b.ended[ev.Slice] {
			return nil
		}

		b.ended[ev.Slice] = true

		// its result came while its tests were still open: their stop
		// was never marked
		if b.open == ev.Slice {
			if err := b.closeOpen(ctx); err != nil {
				return err
			}
		}

		if b.withMarks {
			var why string
			if !b.started[sl.Name] {
				why = ranUnmarked(b.driver)
			}

			return b.r.Hooks.End(ctx, sl, &ev, why, true)
		}

		if err := b.r.Hooks.Start(ctx, sl); err != nil {
			return err
		}

		return b.r.Hooks.End(ctx, sl, &ev, "", false)
	})
	if err != nil && b.failed == nil {
		b.failed = err
	}
}

func (b *bundleRun) serve(ctx context.Context, dir string) (stop func(), err error) {
	if err := freshDir(dir); err != nil {
		return nil, err
	}

	mctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		serveMarks(mctx, dir, func(m Mark) error { return b.mark(ctx, m) })
	}()

	// the directory goes with the bundle: nothing after it, a slice run
	// alone included, finds one to leave marks in
	return func() {
		cancel()
		<-done

		if err := os.RemoveAll(dir); err != nil {
			sayf(b.r.Log, "pipelinegen slice run: %v\n", err)
		}
	}, nil
}

func (r *Runner) bundle(ctx context.Context, st Step, todo []plugin.DriverSlice, withMarks bool) error {
	b := &bundleRun{r: r, driver: st.Driver.Name(), byName: map[string]plugin.DriverSlice{}, withMarks: withMarks, started: map[string]bool{}, ended: map[string]bool{}}
	for _, sl := range todo {
		b.byName[sl.Name] = sl
	}

	req := st.Base
	req.Batch = true
	req.Slices = todo

	stop := func() {}

	if withMarks {
		var err error
		if stop, err = b.serve(ctx, r.Marks.Dir); err != nil {
			return err
		}

		m := r.Marks
		req.Marks = &m
	}

	tail, err := st.Driver.Run(ctx, req, func(ev plugin.DriverEvent) { b.event(ctx, ev) })

	stop()

	// a slice still open when the call ended lost its stop too
	if cerr := r.hook(func() error { return b.closeOpen(ctx) }); cerr != nil && b.failed == nil {
		b.failed = cerr
	}

	if b.failed != nil {
		return b.failed
	}

	var again []plugin.DriverSlice

	for _, sl := range todo {
		if !b.ended[sl.Name] {
			again = append(again, sl)
		}
	}

	if len(again) == 0 {
		return nil
	}

	sayf(r.Log, "pipelinegen slice run: %s left %d of %d slices without a result%s; each runs again on its own\n",
		st.Driver.Name(), len(again), len(todo), because(err, tail))

	return r.oneByOne(ctx, st, again)
}

func because(err error, tail string) string {
	if err == nil {
		return ""
	}

	last := tail
	if _, after, ok := strings.CutLast(tail, "\n"); ok {
		last = after
	}

	if last == "" {
		return " (" + err.Error() + ")"
	}

	return " (" + err.Error() + ": " + last + ")"
}

// hook runs one of the journal's hooks, never two at once: the marks and
// the stream arrive side by side.
func (r *Runner) hook(f func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if err := f(); err != nil {
		return fmt.Errorf("%w: %w", ErrHooks, err)
	}

	return nil
}

func freshDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("marks: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("marks: %w", err)
	}

	return nil
}

func Names(todo []plugin.DriverSlice) []string {
	out := make([]string, 0, len(todo))
	for _, sl := range todo {
		out = append(out, sl.Name)
	}

	return slices.Clip(out)
}
