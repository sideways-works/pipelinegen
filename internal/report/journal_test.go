package report

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func journalSetup(t *testing.T) (*Reporter, *fakeGitHub, *clock, *Journal) {
	t.Helper()

	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "list")
	r.Observer = fakeObserver{rec: []byte("x#func:X\n")}

	c := &clock{t: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)}
	r.Now = c.now

	if err := WritePlan(t.Context(), r.Store, r.ID, []byte(samplePlan)); err != nil {
		t.Fatal(err)
	}

	return r, gh, c, JournalIn(t.TempDir())
}

func sliceEv(t *testing.T, slice, result string) SliceEvent {
	t.Helper()

	return SliceEvent{Suite: "e2e", Slice: slice, Label: "//e2e:" + strings.TrimPrefix(slice, "e2e."),
		Result: writeResult(t, result), Coverage: []string{t.TempDir()}}
}

func written(t *testing.T, r *Reporter) map[string]State {
	t.Helper()

	got, err := States(t.Context(), r.Store, r.ID, "e2e")
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestJournalFlushesOnlyWhenItsPeriodIsOver(t *testing.T) {
	ctx := t.Context()
	r, _, c, j := journalSetup(t)
	col := &fakeCollector{}
	r.Collectors = []Collector{col}

	checkout := sliceEv(t, "e2e.checkout", `{"passed": true}`)

	if err := r.Started(ctx, j, checkout, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	c.t = c.t.Add(10 * time.Second)

	if st, err := r.Ended(ctx, j, checkout, 30*time.Second); err != nil || st != Passed || len(sliceEnds(col)) != 1 {
		t.Fatalf("end: %s %v %v", st, err, col.calls)
	}

	if got := written(t, r); got["e2e.checkout"] != Pending {
		t.Fatalf("written before the period ended: %v", got)
	}

	orders := sliceEv(t, "e2e.orders", `{"passed": true}`)
	if err := r.Started(ctx, j, orders, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	c.t = c.t.Add(25 * time.Second)

	if _, err := r.Ended(ctx, j, orders, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	if got := written(t, r); got["e2e.checkout"] != Passed || got["e2e.orders"] != Passed {
		t.Fatalf("after the period: %v", got)
	}

	if states, err := r.Flush(ctx, j); err != nil || len(states) != 0 {
		t.Fatalf("empty flush: %v %v", states, err)
	}
}

func TestJournalFlushesAFailedSliceAtOnce(t *testing.T) {
	ctx := t.Context()
	r, gh, _, j := journalSetup(t)

	a := sliceEv(t, "e2e.checkout", `{"passed": false, "failures": [{"test": "pays"}]}`)
	if err := r.Started(ctx, j, a, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	if st, err := r.Ended(ctx, j, a, time.Hour); err != nil || st != Failed {
		t.Fatalf("end: %s %v", st, err)
	}

	if got := written(t, r); got["e2e.checkout"] != Failed {
		t.Fatalf("failed slice not written at once: %v", got)
	}

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "pays") {
		t.Fatalf("comment:\n%s", body)
	}

	r.Collectors = []Collector{&fakeCollector{fail: map[string]bool{CollectSliceEnd: true}}}
	b := sliceEv(t, "e2e.orders", `{"passed": true}`)

	if err := r.Started(ctx, j, b, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	if st, err := r.Ended(ctx, j, b, time.Hour); err != nil || st != Errored {
		t.Fatalf("unrecordable: %s %v", st, err)
	}
}

func TestAKilledBatchLosesNothingItsEnsureCanRecover(t *testing.T) {
	ctx := t.Context()
	r, _, _, j := journalSetup(t)

	a := sliceEv(t, "e2e.checkout", `{"passed": true}`)
	b := sliceEv(t, "e2e.orders", `{"passed": true}`)

	if err := r.Started(ctx, j, a, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Ended(ctx, j, a, time.Hour); err != nil {
		t.Fatal(err)
	}

	if err := r.Started(ctx, j, b, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(j.Path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.WriteString(`{"event": "end", "slice": {"Sui`); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if got := written(t, r); got["e2e.checkout"] != Pending {
		t.Fatalf("nothing should be written yet: %v", got)
	}

	r2 := *r

	before, err := os.ReadFile(j.Path)
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		states, err := r2.Flush(ctx, j)
		if err != nil || states["e2e/e2e.checkout"] != Passed || states["e2e/e2e.orders"] != Running {
			t.Fatalf("flush: %v %v", states, err)
		}

		if err := os.WriteFile(j.Path, before, 0o600); err != nil { //nolint:gosec // the test's own journal
			t.Fatal(err)
		}
	}

	b.Result = filepath.Join(t.TempDir(), "missing.json")

	closed, err := r2.Close(ctx, []SliceEvent{a, b})
	if err != nil || closed["e2e/e2e.checkout"] != Passed || closed["e2e/e2e.orders"] != Errored {
		t.Fatalf("close: %v %v", closed, err)
	}
}

func TestNoSliceTakesTheJournalsName(t *testing.T) {
	for _, name := range []string{JournalDir, JournalDir + "/journal.jsonl"} {
		if checkSlice(name) == nil {
			t.Errorf("%s accepted as a slice's name", name)
		}
	}

	if checkSlice(".github/a.spec.ts") != nil {
		t.Error("another dot directory refused")
	}
}

func sliceEnds(f *fakeCollector) []string {
	var dirs []string

	for _, c := range f.calls {
		if f := strings.Fields(c); len(f) == 3 && f[0] == CollectSliceEnd {
			dirs = append(dirs, f[2])
		}
	}

	return dirs
}

type fakeCollector struct {
	calls []string
	fail  map[string]bool
}

func (f *fakeCollector) Collect(_ context.Context, action, slice, dir string) error {
	f.calls = append(f.calls, action+" "+slice+" "+dir)
	if f.fail[action] {
		return errTestCoverage
	}

	return nil
}

func TestSetupEndClosesTheSetupThroughEveryCollector(t *testing.T) {
	ctx := t.Context()
	r, _, _, j := journalSetup(t)
	first, second := &fakeCollector{}, &fakeCollector{}
	r.Collectors = []Collector{first, second}

	ev := sliceEv(t, "e2e.checkout", `{"passed": true}`)
	if err := r.Started(ctx, j, ev, true, time.Hour); !errors.Is(err, ErrNoSetupEnd) {
		t.Fatalf("a start before the setup's end: %v", err)
	}

	if err := r.SetupEnd(ctx, j, "cov/_setup"); err != nil {
		t.Fatal(err)
	}

	if len(first.calls) != 1 || first.calls[0] != "setup-end  cov/_setup" || len(second.calls) != 1 {
		t.Fatalf("after: %v %v", first.calls, second.calls)
	}

	if err := r.Started(ctx, j, ev, true, time.Hour); err != nil {
		t.Fatalf("a start after the setup's end: %v", err)
	}

	r.Collectors = []Collector{&fakeCollector{fail: map[string]bool{CollectSetupEnd: true}}}
	if err := r.SetupEnd(ctx, JournalIn(t.TempDir()), "cov/_setup"); !errors.Is(err, ErrSetup) {
		t.Fatalf("failed collector: %v", err)
	}
}

func TestAFailedCollectErrorsTheSlice(t *testing.T) {
	ctx := t.Context()
	r, _, _, j := journalSetup(t)
	broken, fine := &fakeCollector{fail: map[string]bool{CollectSliceStart: true}}, &fakeCollector{}
	r.Collectors = []Collector{broken, fine}

	a := sliceEv(t, "e2e.checkout", `{"passed": true}`)

	if err := r.Started(ctx, j, a, false, time.Hour); err != nil {
		t.Fatal(err)
	}

	if st, err := r.Ended(ctx, j, a, time.Hour); err != nil || st != Errored {
		t.Fatalf("end: %s %v", st, err)
	}

	if len(fine.calls) != 2 || !strings.HasPrefix(fine.calls[1], "slice-end e2e.checkout ") {
		t.Fatalf("calls %v", fine.calls)
	}
}

func TestASliceComesRoundInParts(t *testing.T) {
	ctx := t.Context()
	r, _, c, j := journalSetup(t)
	col := &fakeCollector{}
	r.Collectors = []Collector{col}

	a := sliceEv(t, "e2e.checkout", `{"passed": true}`)
	b := sliceEv(t, "e2e.orders", `{"passed": true}`)

	step := func(d time.Duration, f func() error) {
		t.Helper()

		c.t = c.t.Add(d)

		if err := f(); err != nil {
			t.Fatal(err)
		}
	}

	ended := func(ev SliceEvent) func() error {
		return func() error {
			_, err := r.Ended(ctx, j, ev, time.Hour)

			return err
		}
	}

	step(0, func() error { return r.Started(ctx, j, a, false, time.Hour) })
	step(5*time.Second, func() error { return r.Started(ctx, j, a, false, time.Hour) })
	step(0, func() error { return r.Started(ctx, j, b, false, time.Hour) })
	step(3*time.Second, func() error { return r.Started(ctx, j, a, false, time.Hour) })
	step(2*time.Second, ended(b))
	step(time.Second, ended(a))

	want := []string{a.Coverage[0], b.Coverage[0], a.Coverage[0]}
	if got := sliceEnds(col); !slices.Equal(got, want) {
		t.Fatalf("slice ends %v, want %v", got, want)
	}

	if _, err := r.Flush(ctx, j); err != nil {
		t.Fatal(err)
	}

	secs, err := Seconds(ctx, r.Store, r.ID, "e2e")
	if err != nil || secs["e2e.checkout"] != 7 || secs["e2e.orders"] != 3 {
		t.Fatalf("seconds %v %v", secs, err)
	}
}

func TestASliceThatRanNoTestsKeepsItsListAndIsNamed(t *testing.T) {
	ctx := t.Context()
	r, gh, _, j := journalSetup(t)
	r.Observer = fakeObserver{err: errTestCoverage}

	none := sliceEv(t, "e2e.checkout", `{"passed": true, "tests": 0}`)
	some := sliceEv(t, "e2e.orders", `{"passed": true, "tests": 3}`)

	for _, ev := range []SliceEvent{none, some} {
		if _, err := r.Ended(ctx, j, ev, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := r.Flush(ctx, j); err != nil {
		t.Fatal(err)
	}

	if got := written(t, r); got["e2e.checkout"] != Passed || got["e2e.orders"] != Errored {
		t.Fatalf("states %v", got)
	}

	if recs, err := Recordings(ctx, r.Store, r.ID); err != nil || len(recs) != 0 {
		t.Fatalf("recordings %v %v", recs, err)
	}

	if body := onlyBody(t, gh, 7); !strings.Contains(body, "Ran no tests: `e2e e2e.checkout`") {
		t.Fatalf("comment:\n%s", body)
	}

	if st, ok := gh.status(commit, "pipelinegen/pr/e2e"); !ok || !strings.Contains(st.Description, ", 1 ran no tests") {
		t.Fatalf("status %+v", st)
	}
}

func TestASwitchFlushesOnceThePeriodIsOver(t *testing.T) {
	ctx := t.Context()
	r, _, c, j := journalSetup(t)

	a := sliceEv(t, "e2e.checkout", `{"passed": true}`)
	b := sliceEv(t, "e2e.orders", `{"passed": true}`)

	if err := r.Started(ctx, j, a, false, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	c.t = c.t.Add(10 * time.Second)

	if err := r.Started(ctx, j, b, false, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	if got := written(t, r); got["e2e.checkout"] != Pending {
		t.Fatalf("written within the period: %v", got)
	}

	c.t = c.t.Add(25 * time.Second)

	if err := r.Started(ctx, j, a, false, 30*time.Second); err != nil {
		t.Fatal(err)
	}

	if got := written(t, r); got["e2e.checkout"] != Running || got["e2e.orders"] != Running {
		t.Fatalf("after the period: %v", got)
	}
}

type scheduleCollector struct {
	calls   []string
	pending int
}

func (s *scheduleCollector) Collect(_ context.Context, action, _, dir string) error {
	s.calls = append(s.calls, action+" "+dir)

	switch action {
	case CollectSliceStart:
		s.pending++
	case CollectSliceEnd:
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err //nolint:wrapcheck // the test's own collector
		}

		data := []byte(strconv.Itoa(s.pending) + "\n")
		s.pending = 0

		return os.WriteFile(filepath.Join(dir, "part.txt"), data, 0o600) //nolint:wrapcheck // the test's own collector
	}

	return nil
}

type partsObserver struct{ parts *[]string }

func (o partsObserver) Observe(_ context.Context, _, _, _ string, parts []string) ([]Observation, error) {
	*o.parts = slices.Clone(parts)

	return []Observation{{Plugin: "browser", Touched: []string{"web:App"}}}, nil
}

func TestABatchClosesEachPartBeforeTheNextStarts(t *testing.T) {
	ctx := t.Context()
	r, _, c, j := journalSetup(t)
	col := &scheduleCollector{}
	r.Collectors = []Collector{col}

	var observed []string

	r.Observer = partsObserver{parts: &observed}

	root := t.TempDir()
	a := sliceEv(t, "e2e.checkout", `{"passed": true}`)
	a.Coverage, a.CoverageRoot = []string{PartDir(root, a.Slice, 1), filepath.Join(root, "_setup")}, root
	b := sliceEv(t, "e2e.orders", `{"passed": true}`)
	b.Coverage, b.CoverageRoot = []string{PartDir(root, b.Slice, 1), filepath.Join(root, "_setup")}, root

	for _, ev := range []SliceEvent{a, b, a, b, a} {
		c.t = c.t.Add(time.Second)

		if err := r.Started(ctx, j, ev, false, time.Hour); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := r.Ended(ctx, j, a, time.Hour); err != nil {
		t.Fatal(err)
	}

	ends, starts := 0, 0

	for _, call := range col.calls {
		action, dir, _ := strings.Cut(call, " ")
		if !strings.Contains(dir, "e2e.checkout") {
			continue
		}

		switch action {
		case CollectSliceStart:
			if starts != ends {
				t.Fatalf("a part of a started before its last one ended: %v", col.calls)
			}

			starts++
		case CollectSliceEnd:
			ends++

			if want := PartDir(root, a.Slice, ends); dir != want {
				t.Fatalf("part %d closed into %s, want %s", ends, dir, want)
			}
		}
	}

	if starts != 3 || ends != 3 {
		t.Fatalf("%d starts, %d ends: %v", starts, ends, col.calls)
	}

	for k := 1; k <= 3; k++ {
		if data, err := os.ReadFile(filepath.Join(PartDir(root, a.Slice, k), "part.txt")); err != nil || string(data) != "1\n" {
			t.Fatalf("part %d: %q %v", k, data, err)
		}
	}

	if _, err := r.Flush(ctx, j); err != nil {
		t.Fatal(err)
	}

	want := []string{PartDir(root, a.Slice, 1), PartDir(root, a.Slice, 2), PartDir(root, a.Slice, 3)}
	if !slices.Equal(observed, want) {
		t.Fatalf("observed %v, want %v", observed, want)
	}
}
