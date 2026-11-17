package affected

import (
	"maps"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

// Rebase compares a reached slice from where it last passed in the same run, when that is newer than
// the plan's base: a slice the change from there to the head doesn't reach is left out.
type Rebase struct {
	OffAt int
	// Since says where a slice of a suite last passed with nothing from there to the head reaching it;
	// false keeps the plan's base.
	Since func(suite string, sl bazel.Slice) (Since, bool)
}

// Since is a slice's later base.
type Since struct {
	// Why says where it passed, the same for every slice of that commit.
	Why string
	// Sampled tells that the change from there samples the suite: the slice is left out only when this
	// plan samples it too.
	Sampled bool
}

// passed is what the rebase left out of a suite: an entry per later base, and the slices whose change
// since samples nothing, which no sample of this plan left out either.
type passed struct {
	entries  []Narrowed
	unsample map[string]bool
}

// rebase leaves out the reached slices whose own later pass covers the change, before reasons and the
// sample: a whole-suite check that needs every slice, and a suite its setup's change runs whole, keep the
// plan's base.
func (o Options) rebase(suite string, sp SuitePlan, all []bazel.Slice, seen map[string]bool, rule *SampleRule, on *sampleOn) (SuitePlan, map[string]bool, passed) {
	if o.Rebase == nil || len(sp.Selected) == 0 || sp.All && (sp.Reason != ReachesEverySlice || len(sp.WholeSuite) > 0) {
		return sp, seen, passed{}
	}

	samples := rule.On && (seen[bazel.SelectorLabel(SampleTarget(suite))] || seen[bazel.SelectorLabel(SampleBoundTarget(suite))] ||
		seen[bazel.SelectorLabel(recording.SetupTarget(suite))]) || seen[bazel.SelectorLabel(EntryTarget(suite))] || on != nil

	kept := maps.Clone(seen)
	out := passed{unsample: map[string]bool{}}

	for _, sl := range sp.Selected {
		s, ok := o.Rebase.Since(suite, sl)
		if !ok || s.Sampled && !samples {
			continue
		}

		kept[sl.Label] = false
		out.unsample[sl.Name] = !s.Sampled

		i := slices.IndexFunc(out.entries, func(n Narrowed) bool { return n.Reason == s.Why })
		if i < 0 {
			out.entries = append(out.entries, Narrowed{Kind: NarrowedBy, Narrowing: plugins.NarrowPassedSince, OffAt: o.Rebase.OffAt, Reason: s.Why})
			i = len(out.entries) - 1
		}

		out.entries[i].Dropped = append(out.entries[i].Dropped, sl.Name)
	}

	if len(out.entries) == 0 {
		return sp, seen, passed{}
	}

	return selectSuite(suite, o.Config.Suites[suite], all, kept, rule.On), kept, out
}

// passedSince keeps of the rebase's entries the slices the plan still left out. A slice whose change
// since samples nothing would stay out with the sample off too, so the sample's entry doesn't name it.
func passedSince(sp *SuitePlan, p passed) {
	for i, n := range sp.Narrowed {
		if n.Kind == NarrowedSampled {
			sp.Narrowed[i].Dropped = slices.DeleteFunc(slices.Clone(n.Dropped), func(name string) bool { return p.unsample[name] })
		}
	}

	sp.Narrowed = append(sp.Narrowed, p.entries...)
	keepLeftOut(sp)
}

// keepLeftOut drops from the passed-since entries each slice something else selected.
func keepLeftOut(sp *SuitePlan) {
	selected := map[string]bool{}
	for _, sl := range sp.Selected {
		selected[sl.Name] = true
	}

	out := sp.Narrowed[:0:0]

	for _, n := range sp.Narrowed {
		if n.Narrowing == plugins.NarrowPassedSince {
			n.Dropped = slices.DeleteFunc(slices.Clone(n.Dropped), func(name string) bool { return selected[name] })
			if len(n.Dropped) == 0 {
				continue
			}

			n.Reached = len(sp.Selected) + len(n.Dropped)
		}

		out = append(out, n)
	}

	sp.Narrowed = out
}

func passedOut(sp SuitePlan) map[string]bool {
	out := map[string]bool{}

	for _, n := range sp.Narrowed {
		if n.Narrowing == plugins.NarrowPassedSince {
			for _, name := range n.Dropped {
				out[name] = true
			}
		}
	}

	return out
}
