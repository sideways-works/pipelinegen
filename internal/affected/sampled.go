package affected

import (
	"maps"
	"slices"
	"sort"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/sample"
)

// SampledByConfig is the Narrowed name of a sample that a sample_on entry
// asked for; it rests on the config, not on a narrowing level.
const SampledByConfig = "sample_on"

func (o Options) sampleRule(suite string) *SampleRule {
	if o.Sample != nil {
		if r := o.Sample(suite); r != nil {
			return r
		}
	}

	s := o.Config.Suites[suite]

	n := s.Sample
	if n == 0 {
		n = s.Parallel
	}

	if n == 0 {
		n = config.DefaultParallel
	}

	return &SampleRule{N: n}
}

// sampleOn is the sample_on pattern a change hits for a suite, if any; only
// is set when every changed file matched one.
type sampleOn struct {
	entry, what string
	only        bool
}

func sampleOnHit(cfg *config.Config, suite string, changes []gitx.Change) *sampleOn {
	var hit *sampleOn

	only := true

	for _, c := range changes {
		p, ok := cfg.SampleOnFor(suite, c.Path)
		if !ok {
			only = false

			continue
		}

		if hit == nil {
			hit = &sampleOn{entry: p, what: c.Path}
		}
	}

	if hit != nil {
		hit.only = only
	}

	return hit
}

// sampleSuite turns a change to what every slice of the suite exercises the
// same into a sample: the slices the change selected on their own, and up to
// N more spread over the groups, the pick fixed by the head commit so a
// rerun picks the same.
func sampleSuite(sp *SuitePlan, s config.Suite, suite string, all []bazel.Slice, impacted map[string]bool, rule *SampleRule, head string, on *sampleOn, changes []gitx.Change, entry entrySide) {
	var by, reason string

	switch {
	case rule.On && impacted[bazel.SelectorLabel(SampleTarget(suite))]:
		by, reason = SampledBySample, "code every slice of the suite ran changed"
	case rule.On && impacted[bazel.SelectorLabel(SampleBoundTarget(suite))]:
		by, reason = SampledBySampleBound, "code every slice of the suite reaches only through what they all ran changed"
	case rule.On && impacted[bazel.SelectorLabel(recording.SetupTarget(suite))]:
		by, reason = SampledBySetup, "code only the suite's setup runs changed"
	case impacted[bazel.SelectorLabel(EntryTarget(suite))] || entry.drivers:
		// the target exists only where planning had plugin-entry on, and
		// it holds what the suite's slices gave up: it samples whatever
		// the rule says
		by, reason = SampledByPluginEntry, "a plugin's entry changed"
	case on != nil:
		by, reason = SampledByConfig, on.what+" changed (sample_on "+on.entry+")"
	default:
		return
	}

	must := mustRun(sp.Selected, on, by, changes, entry.reached)

	var picked []string

	if by == SampledByPluginEntry && len(entry.pool) > 0 {
		picked = entrySample(all, rule, head, must, entry.pool)
	} else {
		picked = sample.Pick(pool(all, rule), max(rule.N, 1), head, must)
	}

	if len(picked) >= len(all) {
		sp.Selected, sp.Untouched = slices.Clone(all), []string{}
		sp.All, sp.Reason = true, reason+", and the sample is the whole suite"

		return
	}

	sp.Selected, sp.Untouched = []bazel.Slice{}, []string{}

	var dropped []string

	for _, sl := range all {
		if slices.Contains(picked, sl.Name) {
			sp.Selected = append(sp.Selected, sl)

			continue
		}

		sp.Untouched = append(sp.Untouched, sl.Label)
		dropped = append(dropped, sl.Name)
	}

	sort.Strings(sp.Untouched)
	sort.Strings(dropped)

	// a sample is not the whole suite: the checks that run with every
	// slice run only when a target they name changed
	sp.All, sp.Reason, sp.WholeSuite = false, "", []string{}

	for _, w := range s.WholeSuite {
		if slices.ContainsFunc(w.AlsoOn, func(l string) bool { return impacted[l] }) {
			sp.WholeSuite = append(sp.WholeSuite, w.Name)
		}
	}

	// what a plugin's entry left out is only what it reached, which the
	// plan's narrowed record names slice by slice
	if by == SampledByPluginEntry {
		return
	}

	sp.Narrowed = append(sp.Narrowed, Narrowed{
		Kind: NarrowedSampled, Narrowing: by, OffAt: rule.OffAt[by], Reason: reason,
		Reached: len(all), Dropped: dropped,
	})
}

type entrySide struct {
	reached func(bazel.Slice) bool
	pool    map[string]bool
	drivers bool
}

func unite(a, b map[string]bool) map[string]bool {
	if len(b) == 0 {
		return a
	}

	out := maps.Clone(a)
	if out == nil {
		out = map[string]bool{}
	}

	maps.Copy(out, b)

	return out
}

// entrySample is a changed entry's sample of a suite: N of the slices the
// entry reached, on top of every slice that runs for another reason. A slice
// the entry doesn't reach tells nothing of the new entry, so it takes no
// place of the sample's.
func entrySample(all []bazel.Slice, rule *SampleRule, head string, must []string, reached map[string]bool) []string {
	var from []bazel.Slice

	for _, sl := range all {
		if reached[sl.Name] {
			from = append(from, sl)
		}
	}

	counted := slices.DeleteFunc(slices.Clone(must), func(name string) bool { return !reached[name] })

	picked := sample.Pick(pool(from, rule), max(rule.N, 1), head, counted)

	for _, name := range must {
		if !slices.Contains(picked, name) {
			picked = append(picked, name)
		}
	}

	return picked
}

// mustRun are the slices a sample keeps whatever it picks: those the change
// selected on its own. When the change is nothing but sample_on files, only
// the slices whose own test files changed must run, and those a changed
// plugin entry reached.
func mustRun(selected []bazel.Slice, on *sampleOn, by string, changes []gitx.Change, entry func(bazel.Slice) bool) []string {
	own := func(sl bazel.Slice) bool {
		return slices.ContainsFunc(changes, func(c gitx.Change) bool { return slices.Contains(sl.Sources, c.Path) })
	}

	out := make([]string, 0, len(selected))

	for _, sl := range selected {
		if by == SampledByConfig && on.only && !own(sl) && (entry == nil || !entry(sl)) {
			continue
		}

		out = append(out, sl.Name)
	}

	return out
}

func (o Options) entryPool(base, head, suite string) map[string]bool {
	if o.EntryPool == nil {
		return nil
	}

	return o.EntryPool(base, head, suite, o.width(suite))
}

func (o Options) entryReached(suite string, reasons map[string][]Reason) func(bazel.Slice) bool {
	byReasons := byEntry(reasons)

	return func(sl bazel.Slice) bool {
		if o.Select != nil && o.Select.Entries[graph.Key{Suite: suite, Name: sl.Name, Width: o.width(suite)}] {
			return true
		}

		return byReasons(sl)
	}
}

func byEntry(reasons map[string][]Reason) func(bazel.Slice) bool {
	entry := func(r Reason) bool { return r.Kind == KindPlugin }
	changed := false

	for _, rs := range reasons {
		changed = changed || slices.ContainsFunc(rs, entry)
	}

	return func(sl bazel.Slice) bool {
		rs := reasons[sl.Label]

		return slices.ContainsFunc(rs, entry) || changed && slices.ContainsFunc(rs, func(r Reason) bool { return r.Kind == KindEverything })
	}
}

func pool(all []bazel.Slice, rule *SampleRule) []sample.Slice {
	out := make([]sample.Slice, 0, len(all))

	for _, sl := range all {
		group := ""
		if rule.Group != nil {
			group = rule.Group(sl.Name)
		}

		out = append(out, sample.Slice{Name: sl.Name, Group: group})
	}

	return out
}
