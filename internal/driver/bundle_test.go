package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/plugin"
)

// fake is a driver in the test's process: it ends the slices of each call
// as told, and records what it was asked.
type fake struct {
	mu    sync.Mutex
	calls [][]string
	batch []bool
	// drop names slices a bundle leaves without an end, as a bundle that
	// died would
	drop map[string]bool
	mark func(m Mark) error
}

func (f *fake) Name() string { return "fake" }

func (f *fake) Describe(context.Context) (plugin.Description, error) {
	return plugin.Description{Kind: plugin.KindDriver, Verbs: []string{plugin.CallRun}, Apart: true}, nil
}

func (f *fake) Run(_ context.Context, req plugin.DriverRequest, event func(plugin.DriverEvent)) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, Names(req.Slices))
	f.batch = append(f.batch, req.Batch)
	f.mu.Unlock()

	for _, sl := range req.Slices {
		if req.Marks != nil {
			if err := f.mark(Mark{Mark: plugin.MarkStart, Slice: sl.Name}); err != nil {
				return "", err
			}

			if err := f.mark(Mark{Mark: plugin.MarkStop, Slice: sl.Name}); err != nil {
				return "", err
			}
		}

		if len(req.Slices) > 1 && f.drop[sl.Name] {
			continue
		}

		event(plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: !strings.Contains(sl.Name, "bad"), Seconds: 2})
	}

	if len(req.Slices) > 1 && len(f.drop) > 0 {
		return "panic: test killed", errExit
	}

	return "", nil
}

var errNoSetup = errors.New("the fake sets nothing up")

func (f *fake) Setup(context.Context, plugin.DriverRequest) (Running, error) { //nolint:ireturn // the interface's own
	return nil, errNoSetup
}
func (f *fake) Prepare(context.Context, plugin.DriverRequest) error { return nil }

var (
	errExit    = errors.New("exit status 2")
	errKilled  = errors.New("signal: killed")
	errNoStack = errors.New("no stack")
)

type journal struct {
	lines []string
}

func (j *journal) Start(_ context.Context, sl plugin.DriverSlice) error {
	j.lines = append(j.lines, "start "+sl.Name)

	return nil
}

func (j *journal) Stop(_ context.Context, sl plugin.DriverSlice, unclosed string) error {
	line := "stop " + sl.Name
	if unclosed != "" {
		line += " (not marked)"
	}

	j.lines = append(j.lines, line)

	return nil
}

func (j *journal) End(_ context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, apart bool) error {
	switch {
	case ev == nil:
		j.lines = append(j.lines, "errored "+sl.Name)
	case !ev.Passed:
		j.lines = append(j.lines, fmt.Sprintf("failed %s%s", sl.Name, apartMark(apart)))
	default:
		j.lines = append(j.lines, fmt.Sprintf("passed %s%s", sl.Name, apartMark(apart)))
	}

	if why != "" && ev == nil {
		j.lines = append(j.lines, "  why: "+why)
	}

	return nil
}

func apartMark(apart bool) string {
	if apart {
		return " (apart)"
	}

	return ""
}

func slicesNamed(names ...string) []plugin.DriverSlice {
	out := make([]plugin.DriverSlice, 0, len(names))
	for _, n := range names {
		out = append(out, plugin.DriverSlice{Name: n})
	}

	return out
}

func TestWithoutBatchEverySliceIsACallOfItsOwn(t *testing.T) {
	f, j := &fake{}, &journal{}
	r := &Runner{Hooks: j, Log: io.Discard}

	if err := r.Run(t.Context(), Step{Driver: f, Batch: false}, slicesNamed("a", "bad", "c")); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 3 || slices.Contains(f.batch, true) {
		t.Fatalf("calls %v, batch %v", f.calls, f.batch)
	}

	want := []string{"start a", "passed a", "start bad", "failed bad", "start c", "passed c"}
	if !slices.Equal(j.lines, want) {
		t.Fatalf("journal:\n%s", strings.Join(j.lines, "\n"))
	}
}

func TestABundleIsOneCall(t *testing.T) {
	f, j := &fake{}, &journal{}
	r := &Runner{Hooks: j, Log: io.Discard}

	if err := r.Run(t.Context(), Step{Driver: f, Batch: true}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 1 || !f.batch[0] {
		t.Fatalf("calls %v", f.calls)
	}

	if want := []string{"start a", "passed a", "start b", "passed b"}; !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestSlicesADeadBundleLeftRunAgainAlone(t *testing.T) {
	f, j := &fake{drop: map[string]bool{"b": true, "c": true}}, &journal{}
	r := &Runner{Hooks: j, Log: io.Discard}

	if err := r.Run(t.Context(), Step{Driver: f, Batch: true}, slicesNamed("a", "b", "c")); err != nil {
		t.Fatal(err)
	}

	if want := [][]string{{"a", "b", "c"}, {"b"}, {"c"}}; !slices.EqualFunc(f.calls, want, slices.Equal) {
		t.Fatalf("calls %v", f.calls)
	}

	if want := []string{"start a", "passed a", "start b", "passed b", "start c", "passed c"}; !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestASliceWithoutAnyResultIsErroredWithTheDriversLastLines(t *testing.T) {
	j := &journal{}
	r := &Runner{Hooks: j, Log: io.Discard}

	silent := &silentDriver{}
	if err := r.Run(t.Context(), Step{Driver: silent, Batch: true}, slicesNamed("a")); err != nil {
		t.Fatal(err)
	}

	if len(j.lines) != 3 || j.lines[1] != "errored a" || !strings.Contains(j.lines[2], "signal: killed") || !strings.Contains(j.lines[2], "fatal error: out of memory") {
		t.Fatalf("journal: %q", j.lines)
	}
}

// silentDriver dies without an end, every time.
type silentDriver struct{ fake }

func (s *silentDriver) Run(context.Context, plugin.DriverRequest, func(plugin.DriverEvent)) (string, error) {
	return "fatal error: out of memory", errKilled
}

func TestARecordingRunBundlesOnlyWhatKeepsRecordingsApart(t *testing.T) {
	f, j := &fake{}, &journal{}
	r := &Runner{Hooks: j, Log: io.Discard, Record: true}

	if err := r.Run(t.Context(), Step{Driver: f, Desc: plugin.Description{}, Batch: true}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 2 {
		t.Fatalf("a driver that keeps no recording apart got a bundle in a recording run: %v", f.calls)
	}
}

func TestARecordedStackBundleIsMarkedSliceBySlice(t *testing.T) {
	dir := t.TempDir()
	j := &journal{}
	r := &Runner{Hooks: j, Log: io.Discard, Record: true, Stack: true, Marks: plugin.Marks{Dir: dir}}

	f := &fake{}
	f.mark = func(m Mark) error { return MarkAndWait(t.Context(), dir, m, 10*time.Second) }

	if err := r.Run(t.Context(), Step{Driver: f, Batch: true}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	if len(f.calls) != 1 {
		t.Fatalf("calls %v", f.calls)
	}

	want := []string{"start a", "stop a", "passed a (apart)", "start b", "stop b", "passed b (apart)"}
	if !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestAMarkOfASliceNotInTheBundleFails(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{Hooks: &journal{}, Log: io.Discard, Record: true, Stack: true, Marks: plugin.Marks{Dir: dir}}

	var got error

	f := &fake{}
	f.mark = func(Mark) error {
		if got == nil {
			got = MarkAndWait(t.Context(), dir, Mark{Mark: plugin.MarkStart, Slice: "elsewhere"}, 10*time.Second)
		}

		return nil
	}

	if err := r.Run(t.Context(), Step{Driver: f, Batch: true}, slicesNamed("a")); err != nil {
		t.Fatal(err)
	}

	if got == nil || !strings.Contains(got.Error(), `no slice "elsewhere"`) {
		t.Fatalf("mark: %v", got)
	}
}

func TestAMarkNobodyTakesTimesOut(t *testing.T) {
	err := MarkAndWait(t.Context(), t.TempDir(), Mark{Mark: plugin.MarkStart, Slice: "a"}, 100*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("mark: %v", err)
	}
}

// scripted is a driver that marks and ends as its script says, line by line ("start a", "stop a",
// "end a", "fail a"), keeping each mark's answer.
type scripted struct {
	fake

	script  []string
	answers []string
}

func (s *scripted) Run(ctx context.Context, req plugin.DriverRequest, event func(plugin.DriverEvent)) (string, error) {
	// a slice run again on its own: just its end
	if req.Marks == nil {
		for _, sl := range req.Slices {
			event(plugin.DriverEvent{Slice: sl.Name, Event: plugin.EventEnd, Passed: true})
		}

		return "", nil
	}

	for _, line := range s.script {
		what, name, _ := strings.Cut(line, " ")

		switch what {
		case plugin.MarkStart, plugin.MarkStop:
			err := MarkAndWait(ctx, req.Marks.Dir, Mark{Mark: what, Slice: name}, 10*time.Second)

			answer := "ok"
			if err != nil {
				answer = err.Error()
			}

			s.answers = append(s.answers, line+": "+answer)
		default:
			event(plugin.DriverEvent{Slice: name, Event: plugin.EventEnd, Passed: what == "end"})
		}
	}

	return "", nil
}

func runScript(t *testing.T, script ...string) (*journal, *scripted) {
	t.Helper()

	j := &journal{}
	r := &Runner{Hooks: j, Log: io.Discard, Record: true, Stack: true, Marks: plugin.Marks{Dir: t.TempDir()}}
	s := &scripted{script: script}

	if err := r.Run(t.Context(), Step{Driver: s, Batch: true}, slicesNamed("a", "b")); err != nil {
		t.Fatal(err)
	}

	return j, s
}

func TestAStopThatNeverCameIsClosedByTheNextStart(t *testing.T) {
	j, s := runScript(t, "start a", "fail a", "start b", "stop b", "end b")

	if slices.ContainsFunc(s.answers, func(a string) bool { return !strings.HasSuffix(a, ": ok") }) {
		t.Fatalf("answers: %q", s.answers)
	}

	if want := []string{"start a", "stop a (not marked)", "failed a (apart)", "start b", "stop b", "passed b (apart)"}; !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestASecondStartBeforeTheEndOpensAnotherPart(t *testing.T) {
	j, _ := runScript(t, "start a", "stop a", "start a", "stop a", "end a", "start b", "stop b", "end b")

	want := []string{"start a", "stop a", "start a", "stop a", "passed a (apart)", "start b", "stop b", "passed b (apart)"}
	if !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestAStartAfterTheSlicesEndIsRefused(t *testing.T) {
	_, s := runScript(t, "start a", "stop a", "end a", "start a", "start b", "stop b", "end b")

	if len(s.answers) != 5 || !strings.Contains(s.answers[2], "already ended") || !strings.HasSuffix(s.answers[3], ": ok") {
		t.Fatalf("answers: %q", s.answers)
	}
}

func TestAMarksAnswerIsWrittenWhole(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"mark":"start","slice":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	answerMark(dir, "1", func(Mark) error { return errNoStack })

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}

	// written under a temporary name and renamed: none is left behind
	if !slices.Equal(names, []string{"1.failed", "1.json"}) {
		t.Fatalf("files: %v", names)
	}

	if data, err := os.ReadFile(filepath.Join(dir, "1.failed")); err != nil || string(data) != "no stack" {
		t.Fatalf("answer: %q %v", data, err)
	}
}

func TestAStartWhileAnotherSliceIsOpenClosesItUnmarked(t *testing.T) {
	j, s := runScript(t, "start a", "start b", "stop b", "fail a", "end b")

	if slices.ContainsFunc(s.answers, func(a string) bool { return !strings.HasSuffix(a, ": ok") }) {
		t.Fatalf("answers: %q", s.answers)
	}

	want := []string{"start a", "stop a (not marked)", "start b", "stop b", "failed a (apart)", "passed b (apart)"}
	if !slices.Equal(j.lines, want) {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestASliceStillOpenWhenTheCallEndsIsClosedUnmarked(t *testing.T) {
	j, _ := runScript(t, "start a", "stop a", "end a", "start b")

	if len(j.lines) < 5 || j.lines[3] != "start b" || j.lines[4] != "stop b (not marked)" {
		t.Fatalf("journal: %v", j.lines)
	}
}

func TestAMarkMayBeNamedAnyWay(t *testing.T) {
	dir := t.TempDir()

	// the name carries a shard and a process, as a test tool's hook names
	// its requests so a retry never reads a stale answer
	name := filepath.Join(dir, "shard-2-attempt-1-4711.json")
	if err := os.WriteFile(name, []byte(`{"mark":"start","slice":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ids, err := pendingMarks(dir)
	if err != nil || !slices.Equal(ids, []string{"shard-2-attempt-1-4711"}) {
		t.Fatalf("pending: %v %v", ids, err)
	}
}
