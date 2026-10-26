package driver

import (
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

type timeline struct {
	mu    sync.Mutex
	lines []string
}

func (tl *timeline) add(line string) {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	tl.lines = append(tl.lines, line)
}

// working is a driver that runs a bundle's slices one after another, each
// with work that takes a moment, and streams each slice's end once it ran:
// a recording driver whose recordings are apart (go-driver with batch:
// true). Given marks it marks around each slice's work, unless deaf.
type working struct {
	tl   *timeline
	deaf bool
}

func (w *working) Name() string { return "working" }

func (w *working) Describe(context.Context) (plugin.Description, error) {
	return plugin.Description{Kind: plugin.KindDriver, Verbs: []string{plugin.CallRun}, Apart: true}, nil
}

func (w *working) Run(ctx context.Context, req plugin.DriverRequest, event func(plugin.DriverEvent)) (string, error) {
	w.tl.add("call " + strings.Join(Names(req.Slices), ","))

	mark := func(what, slice string) error {
		if req.Marks == nil || w.deaf {
			return nil
		}

		return MarkAndWait(ctx, req.Marks.Dir, Mark{Mark: what, Slice: slice}, 10*time.Second)
	}

	for _, sl := range req.Slices {
		if err := mark(plugin.MarkStart, sl.Name); err != nil {
			return "", err
		}

		w.tl.add("work " + sl.Name)
		time.Sleep(20 * time.Millisecond)

		if err := mark(plugin.MarkStop, sl.Name); err != nil {
			return "", err
		}

		event(plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true})
	}

	return "", nil
}

func (w *working) Setup(context.Context, plugin.DriverRequest) (Running, error) { //nolint:ireturn // the interface's own
	return nil, errNoSetup
}
func (w *working) Prepare(context.Context, plugin.DriverRequest) error { return nil }

// collecting are hooks that call a collector at a slice's edges, as the
// batch's journal does: its slice-start at Start, its slice-end at Stop or
// at an End that closes the slice's part; an End also notes why a slice
// has no part collected.
type collecting struct{ tl *timeline }

func (c *collecting) Start(_ context.Context, sl plugin.DriverSlice) error {
	c.tl.add("collect slice-start " + sl.Name)

	return nil
}

func (c *collecting) Stop(_ context.Context, sl plugin.DriverSlice, _ string) error {
	c.tl.add("collect slice-end " + sl.Name)

	return nil
}

func (c *collecting) End(_ context.Context, sl plugin.DriverSlice, _ *plugin.DriverEvent, why string, apart bool) error {
	if !apart {
		c.tl.add("collect slice-end " + sl.Name)
	}

	if why != "" {
		c.tl.add("unrecordable " + sl.Name + ": " + why)
	}

	return nil
}

func TestARecordingBundleCollectsAtTheSlicesOwnEdges(t *testing.T) {
	tl := &timeline{}
	r := &Runner{Hooks: &collecting{tl: tl}, Record: true, Collects: true, Log: io.Discard, Marks: plugin.Marks{Dir: t.TempDir()}}

	if err := r.Run(t.Context(), Step{Driver: &working{tl: tl}, Desc: plugin.Description{Apart: true}, Batch: true}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"call a,b",
		"collect slice-start a", "work a", "collect slice-end a",
		"collect slice-start b", "work b", "collect slice-end b",
	}
	if !slices.Equal(tl.lines, want) {
		t.Fatalf("the batch's log:\n%s\nwant:\n%s", strings.Join(tl.lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestARecordingBundleWithoutCorrelatorsBundlesAsBefore(t *testing.T) {
	tl := &timeline{}
	r := &Runner{Hooks: &collecting{tl: tl}, Record: true, Log: io.Discard, Marks: plugin.Marks{Dir: t.TempDir()}}

	if err := r.Run(t.Context(), Step{Driver: &working{tl: tl}, Desc: plugin.Description{Apart: true}, Batch: true}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"call a,b",
		"work a", "collect slice-start a", "collect slice-end a",
		"work b", "collect slice-start b", "collect slice-end b",
	}
	if !slices.Equal(tl.lines, want) {
		t.Fatalf("the batch's log:\n%s\nwant:\n%s", strings.Join(tl.lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestADriverThatDoesntMarkLeavesItsSlicesUncollected(t *testing.T) {
	tl := &timeline{}
	r := &Runner{Hooks: &collecting{tl: tl}, Record: true, Collects: true, Log: io.Discard, Marks: plugin.Marks{Dir: t.TempDir()}}

	if err := r.Run(t.Context(), Step{Driver: &working{tl: tl, deaf: true}, Desc: plugin.Description{Apart: true}, Batch: true}, slicesNamed("a")); err != nil {
		t.Fatal(err)
	}

	if slices.ContainsFunc(tl.lines, func(l string) bool { return strings.HasPrefix(l, "collect ") }) {
		t.Fatalf("a collector was called for a slice that never marked:\n%s", strings.Join(tl.lines, "\n"))
	}

	last := tl.lines[len(tl.lines)-1]
	for _, says := range []string{"unrecordable a: it ran without marks", "working must mark each slice's start and stop", "batch: false"} {
		if !strings.Contains(last, says) {
			t.Errorf("no %q in %q", says, last)
		}
	}
}
