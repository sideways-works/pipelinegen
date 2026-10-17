package affected

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

// KindConfig is a slice that runs differently under the head's config: What
// names the part that changed. Such a slice runs at every narrowing level,
// and no sample leaves it out.
const KindConfig = "config"

// understood tells whether the change edits the config and the planner
// understood every part it changed: the config file is then no changed file
// of its own, though a Bazel target that holds it still sees it change.
func (o Options) understood() bool {
	return o.Meaning != nil && o.Meaning.Understood()
}

func (o Options) beyondConfig(changes []gitx.Change) []gitx.Change {
	if !o.understood() {
		return changes
	}

	return slices.DeleteFunc(slices.Clone(changes), func(c gitx.Change) bool { return c.Path == config.FileName })
}

type configured struct {
	// kept are the slices it selected: they run whatever samples
	kept map[string]bool
	pool map[string]bool
	left map[string][]string
}

// configSelect adds the slices the config's change makes run differently and
// those a changed driver's entry reaches: unlisted ones, or all while
// plugin-entry is off, run; recorded ones with a list go to the suite's
// sample.
func (o Options) configSelect(sp *SuitePlan, suite string, all []bazel.Slice, head string, rule *SampleRule) configured {
	out := configured{kept: map[string]bool{}, pool: map[string]bool{}, left: map[string][]string{}}

	if o.Meaning == nil || !o.Meaning.Changed() {
		return out
	}

	selected := map[string]bool{}
	for _, sl := range sp.Selected {
		selected[sl.Label] = true
	}

	if sp.Reasons == nil {
		sp.Reasons = map[string][]Reason{}
	}

	unlisted := map[string]bool{}

	if o.Unlisted != nil {
		for _, name := range o.Unlisted(head, suite) {
			unlisted[name] = true
		}
	}

	recorded := o.Config.Suites[suite].Bind == config.BindRecorded

	add := func(sl bazel.Slice, r Reason) {
		selected[sl.Label] = true
		out.kept[sl.Name] = true
		sp.Reasons[sl.Label] = appendNew(sp.Reasons[sl.Label], r)
	}

	for _, sl := range all {
		routed := config.RoutedSlice{Name: sl.Name, Label: sl.Label, Kind: sl.Kind}

		if why, ok := o.Meaning.SliceRuns(suite, routed); ok {
			add(sl, Reason{Kind: KindConfig, What: why})
		}

		drivers := o.Meaning.Drivers(suite, routed)
		if len(drivers) == 0 {
			continue
		}

		if rule.EntryOn && recorded && !unlisted[sl.Name] {
			out.pool[sl.Name] = true

			if !selected[sl.Label] {
				out.left[sl.Name] = drivers
			}

			continue
		}

		for _, d := range drivers {
			add(sl, Reason{Kind: KindPlugin, What: d})
		}
	}

	sp.Selected, sp.Untouched = []bazel.Slice{}, []string{}

	for _, sl := range all {
		if selected[sl.Label] {
			sp.Selected = append(sp.Selected, sl)
		} else {
			sp.Untouched = append(sp.Untouched, sl.Label)
		}
	}

	if len(all) > 0 && len(sp.Untouched) == 0 && !sp.All {
		sp.All, sp.Reason = true, ReachesEverySlice
	}

	return out
}

func (o Options) sampleConfigured(sp *SuitePlan, name string, b, h *Snapshot, seen map[string]bool, rule *SampleRule, on *sampleOn, changes []gitx.Change) {
	cfgd := o.configSelect(sp, name, h.Slices[name], h.Commit, rule)
	reached := o.entryReached(name, sp.Reasons)

	sampleSuite(sp, o.Config.Suites[name], name, h.Slices[name], seen, rule, h.Commit, on, changes, entrySide{
		reached: func(sl bazel.Slice) bool { return reached(sl) || cfgd.kept[sl.Name] },
		pool:    unite(o.entryPool(b.Commit, h.Commit, name), cfgd.pool), drivers: len(cfgd.pool) > 0,
	})
	driverNarrowed(sp, cfgd, rule)

	if o.Meaning != nil {
		o.wholeChecks(sp, name)
	}
}

func driverNarrowed(sp *SuitePlan, c configured, rule *SampleRule) {
	var dropped, drivers []string

	for _, name := range sortedKeysOf(c.left) {
		if slices.ContainsFunc(sp.Selected, func(sl bazel.Slice) bool { return sl.Name == name }) {
			continue
		}

		dropped = append(dropped, name)

		for _, d := range c.left[name] {
			if !slices.Contains(drivers, d) {
				drivers = append(drivers, d)
			}
		}
	}

	if len(dropped) == 0 {
		return
	}

	slices.Sort(drivers)

	what := make([]string, 0, len(drivers))
	for _, d := range drivers {
		what = append(what, "the entry of plugin "+d)
	}

	sp.Narrowed = append(sp.Narrowed, Narrowed{
		Kind: NarrowedBy, Narrowing: SampledByPluginEntry, OffAt: rule.OffAt[SampledByPluginEntry],
		Reason:  strings.Join(what, ", ") + " changed: each suite it reaches runs its sample of the recorded slices it reaches instead of all of them, and the nightly runs the rest",
		Reached: len(sp.Selected) + len(dropped), Dropped: dropped,
	})
}

func (o Options) wholeChecks(sp *SuitePlan, suite string) {
	for _, w := range o.Meaning.WholeChecks(suite) {
		if !slices.Contains(sp.WholeSuite, w) {
			sp.WholeSuite = append(sp.WholeSuite, w)
		}
	}
}
