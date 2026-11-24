package affected

import (
	"context"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

func passSlices(names ...string) []bazel.Slice {
	out := make([]bazel.Slice, 0, len(names))
	for _, n := range names {
		out = append(out, bazel.Slice{Name: n, Label: bazel.SelectorLabel(n)})
	}

	return out
}

func passedAt(sampled bool, by map[string]string) *Rebase {
	return &Rebase{OffAt: 4, Since: func(_ string, sl bazel.Slice) (Since, bool) {
		c, ok := by[sl.Name]

		return Since{Why: "compared from " + c, Sampled: sampled}, ok
	}}
}

func TestALaterPassLeavesOutWhatItCovers(t *testing.T) {
	all := passSlices("a", "b", "c", "d")
	o := Options{Config: &config.Config{Suites: map[string]config.Suite{"s": {}}}, Rebase: passedAt(false, map[string]string{"a": "c1", "b": "c2"})}
	seen := map[string]bool{all[0].Label: true, all[1].Label: true, all[2].Label: true}
	rule := &SampleRule{}

	sp := selectSuite("s", config.Suite{}, all, seen, false)

	sp, kept, since := o.rebase("s", sp, all, seen, rule, nil)
	passedSince(&sp, since)

	if got := sliceNames(sp.Selected); !slices.Equal(got, []string{"c"}) || kept[all[0].Label] || !seen[all[0].Label] {
		t.Fatalf("selected %v; the reach is copied, not changed: %v", got, seen[all[0].Label])
	}

	if len(sp.Narrowed) != 2 {
		t.Fatalf("one entry per commit: %+v", sp.Narrowed)
	}

	for i, want := range []Narrowed{
		{Kind: NarrowedBy, Narrowing: plugins.NarrowPassedSince, OffAt: 4, Reason: "compared from c1", Reached: 2, Dropped: []string{"a"}},
		{Kind: NarrowedBy, Narrowing: plugins.NarrowPassedSince, OffAt: 4, Reason: "compared from c2", Reached: 2, Dropped: []string{"b"}},
	} {
		got := sp.Narrowed[i]
		if got.Kind != want.Kind || got.Narrowing != want.Narrowing || got.OffAt != want.OffAt || got.Reason != want.Reason ||
			got.Reached != want.Reached || !slices.Equal(got.Dropped, want.Dropped) {
			t.Errorf("entry %d: %+v, want %+v", i, got, want)
		}
	}
}

func TestTheSampleIsDrawnAfterTheLaterPasses(t *testing.T) {
	all := passSlices("a", "b", "c", "d", "e", "f")
	sample := bazel.SelectorLabel(SampleTarget("s"))
	rule := &SampleRule{On: true, N: 1}

	for _, c := range []struct {
		name            string
		sampledFromPass bool
		samples         bool
		left            []string
	}{
		{"the plan samples", false, true, []string{"a", "b", "c", "d", "e"}},
		{"both sample", true, true, []string{"a", "b", "c", "d", "e"}},
		// the change since the pass samples, this one doesn't: leaving them
		// out would sample nobody
		{"only the change since the pass samples", true, false, nil},
	} {
		seen := map[string]bool{}
		for _, sl := range all {
			seen[sl.Label] = true
		}

		seen[sample] = c.samples

		o := Options{
			Config: &config.Config{Suites: map[string]config.Suite{"s": {}}},
			Rebase: passedAt(c.sampledFromPass, map[string]string{"a": "c1", "b": "c1", "c": "c1", "d": "c1", "e": "c1"}),
		}

		sp := selectSuite("s", config.Suite{}, all, seen, rule.On)
		sp, kept, since := o.rebase("s", sp, all, seen, rule, nil)
		sampleSuite(&sp, config.Suite{}, "s", all, kept, rule, "0123456789abcdef", nil, nil, entrySide{})
		passedSince(&sp, since)

		var left, sampledOut []string

		for _, n := range sp.Narrowed {
			switch {
			case n.Narrowing == plugins.NarrowPassedSince:
				left = append(left, n.Dropped...)
			case n.Kind == NarrowedSampled:
				sampledOut = append(sampledOut, n.Dropped...)
			}
		}

		// a slice the change since its pass doesn't sample runs only from passed-since's level, unnamed
		// by the sample; one it samples runs once the sample is off, named under both
		for _, name := range left {
			if slices.Contains(sampledOut, name) != c.sampledFromPass {
				t.Errorf("%s: %s left out by the sample %v", c.name, name, slices.Contains(sampledOut, name))
			}
		}

		if c.left == nil {
			if len(sp.Selected) != len(all) || left != nil {
				t.Errorf("%s: runs %v, leaves out %v", c.name, sliceNames(sp.Selected), left)
			}

			continue
		}

		// f must run; the sample adds one slice, which then is no longer
		// left out
		if !slices.Contains(sliceNames(sp.Selected), "f") || len(sp.Selected) > 2 || len(left)+len(sp.Selected) != len(all) {
			t.Errorf("%s: runs %v, leaves out %v", c.name, sliceNames(sp.Selected), left)
		}
	}
}

func TestAWholeSuiteCheckKeepsTheRunsBase(t *testing.T) {
	all := passSlices("a", "b")
	o := Options{Config: &config.Config{Suites: map[string]config.Suite{"s": {}}}, Rebase: passedAt(false, map[string]string{"a": "c1"})}

	for _, c := range []struct {
		name  string
		suite config.Suite
		seen  map[string]bool
		left  bool
	}{
		{"a check of the whole suite", config.Suite{WholeSuite: []config.WholeSuiteCheck{{Name: "coverage"}}},
			map[string]bool{all[0].Label: true, all[1].Label: true}, false},
		// what changed from a's pass to the head would have reached it, setup and all
		{"the setup's change", config.Suite{}, map[string]bool{bazel.SelectorLabel(recording.SetupTarget("s")): true}, true},
		{"every slice reached", config.Suite{}, map[string]bool{all[0].Label: true, all[1].Label: true}, true},
	} {
		o.Config.Suites["s"] = c.suite

		sp := selectSuite("s", c.suite, all, c.seen, false)
		sp, _, since := o.rebase("s", sp, all, c.seen, &SampleRule{}, nil)

		if left := len(since.entries) > 0; left != c.left || left == slices.Contains(sliceNames(sp.Selected), "a") {
			t.Errorf("%s: leaves a out %v, runs %v", c.name, left, sliceNames(sp.Selected))
		}
	}
}

func TestASliceTheBasesConfigSelectsIsNotLeftOut(t *testing.T) {
	all := passSlices("a", "b")
	own := SuitePlan{
		Suite: "s", Selected: all[1:], Untouched: []string{all[0].Label}, slices: all,
		Narrowed: []Narrowed{{Kind: NarrowedBy, Narrowing: plugins.NarrowPassedSince, OffAt: 4, Reason: "compared from c1", Reached: 2, Dropped: []string{"a"}}},
	}
	byBase := SuitePlan{Suite: "s", Selected: all[:1], Untouched: []string{all[1].Label}, slices: all}

	got := Union(&Plan{Suites: []SuitePlan{own}}, &Plan{Suites: []SuitePlan{byBase}})

	if sp := got.Suites[0]; len(sp.Selected) != 2 || len(sp.Narrowed) != 0 {
		t.Errorf("runs %v, left out by %+v", sliceNames(sp.Selected), sp.Narrowed)
	}
}

func TestACarriedOverEntryNamesItsBuild(t *testing.T) {
	all := passSlices("a", "b", "c")
	from := "4d1c0ffee4d1c0ffee4d1c0ffee4d1c0ffee4d1c"
	o := Options{Config: &config.Config{Suites: map[string]config.Suite{"s": {}}}, Rebase: &Rebase{OffAt: 4, Since: func(_ string, sl bazel.Slice) (Since, bool) {
		return Since{Why: "carried over from " + from[:12], CarriedFrom: from}, sl.Name != "c"
	}}}
	seen := map[string]bool{all[0].Label: true, all[1].Label: true, all[2].Label: true}

	sp := selectSuite("s", config.Suite{}, all, seen, false)

	sp, _, since := o.rebase("s", sp, all, seen, &SampleRule{}, nil)
	passedSince(&sp, since)

	if got := sliceNames(sp.Selected); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("selected %v", got)
	}

	if len(sp.Narrowed) != 1 || sp.Narrowed[0].CarriedFrom != from || !slices.Equal(sp.Narrowed[0].Dropped, []string{"a", "b"}) ||
		sp.Narrowed[0].Narrowing != plugins.NarrowPassedSince || sp.Narrowed[0].Reached != 3 {
		t.Fatalf("one entry naming the build: %+v", sp.Narrowed)
	}
}

func TestASetupChangeLeavesOutWhatPassedSince(t *testing.T) {
	all := []bazel.Slice{walkSlice("s/a_test.go"), walkSlice("s/b_test.go")}
	cfg := &config.Config{Suites: map[string]config.Suite{"s": {Tests: []string{"s/*_test.go"}}}}
	o := Options{
		Config:  cfg,
		Reasons: func(_, _, target string) ([]Reason, bool) { return []Reason{{Kind: KindTarget, What: target}}, true },
		Prepare: func(context.Context, string, string, map[string][]bazel.Slice) (string, error) { return "", nil },
		Rebase:  passedAt(false, map[string]string{"s/a_test.go": "c1"}),
	}
	b := &Snapshot{Commit: commitBase, Slices: map[string][]bazel.Slice{"s": all}}
	h := &Snapshot{Commit: commitHead, Slices: map[string][]bazel.Slice{"s": all}}
	impacted := map[string]bool{bazel.SelectorLabel(recording.SetupTarget("s")): true}

	sps, err := selectSuites(t.Context(), o, b, h, impacted, nil)
	if err != nil {
		t.Fatal(err)
	}

	sp := suitePlan(t, sps, "s")
	if sp.All || names(sp) != "s/b_test.go" || !slices.Equal(sp.Untouched, []string{all[0].Label}) {
		t.Fatalf("all %v, runs %s, leaves out %v", sp.All, names(sp), sp.Untouched)
	}

	byTheSetup := func(r Reason) bool {
		return r.Kind == KindTarget && r.What == bazel.SelectorLabel(recording.SetupTarget("s"))
	}
	if !slices.ContainsFunc(sp.Reasons[all[1].Label], byTheSetup) {
		t.Errorf("b runs for %+v, not the setup's change", sp.Reasons[all[1].Label])
	}

	if left := passedOut(sp); !left["s/a_test.go"] || len(left) != 1 {
		t.Errorf("passed-since leaves out %v", left)
	}
}
