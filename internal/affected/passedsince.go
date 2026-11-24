package affected

import (
	"maps"
	"slices"
	"sort"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

// Rebase compares a reached slice from where it last passed in the same run, when that is newer than
// the plan's base: a slice the change from there to the head doesn't reach is left out. For a pull
// request, that is the tree its last build tested, and a slice left out is carried over from it.
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
	// CarriedFrom is, for a pull request, the change's commit of the last build the slice passed in.
	CarriedFrom string
}

// passed is what the rebase left out of a suite: an entry per later base, and the slices whose change
// since samples nothing, which no sample of this plan left out either.
type passed struct {
	entries  []Narrowed
	unsample map[string]bool
	// setup is the suite's setup target when its change alone ran the suite whole: the slices that
	// still run give it as their reason.
	setup string
}

// rebase leaves out the reached slices whose own later pass covers the change, before reasons and the
// sample; a suite run whole with its whole-suite checks keeps the plan's base. A slice that passed after
// a change to the suite's setup is left out too: what changed from its pass to the head would have
// reached it, setup and all.
func (o Options) rebase(suite string, sp SuitePlan, all []bazel.Slice, seen map[string]bool, rule *SampleRule, on *sampleOn) (SuitePlan, map[string]bool, passed) {
	if o.Rebase == nil || len(sp.Selected) == 0 || sp.All && len(sp.WholeSuite) > 0 {
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
			out.entries = append(out.entries, Narrowed{
				Kind: NarrowedBy, Narrowing: plugins.NarrowPassedSince, OffAt: o.Rebase.OffAt, Reason: s.Why, CarriedFrom: s.CarriedFrom,
			})
			i = len(out.entries) - 1
		}

		out.entries[i].Dropped = append(out.entries[i].Dropped, sl.Name)
	}

	if len(out.entries) == 0 {
		return sp, seen, passed{}
	}

	next := selectSuite(suite, o.Config.Suites[suite], all, kept, rule.On)
	if next.All && next.Reason != ReachesEverySlice {
		next = leaveOut(next, out.unsample)
		out.setup = bazel.SelectorLabel(recording.SetupTarget(suite))
	}

	return next, kept, out
}

// leaveOut takes the slices that passed out of a suite its setup's change ran whole.
func leaveOut(sp SuitePlan, left map[string]bool) SuitePlan {
	var keep []bazel.Slice

	for _, sl := range sp.Selected {
		if _, ok := left[sl.Name]; ok {
			sp.Untouched = append(sp.Untouched, sl.Label)

			continue
		}

		keep = append(keep, sl)
	}

	sort.Strings(sp.Untouched)

	sp.Selected, sp.All, sp.Reason = append([]bazel.Slice{}, keep...), false, ""

	return sp
}

// setupReasons gives each slice that still runs the setup's change as its reason, the suite no longer
// running whole for it.
func setupReasons(sp *SuitePlan, p passed) {
	if p.setup == "" {
		return
	}

	if sp.Reasons == nil {
		sp.Reasons = map[string][]Reason{}
	}

	for _, sl := range sp.Selected {
		sp.Reasons[sl.Label] = append(sp.Reasons[sl.Label], Reason{Kind: KindTarget, What: p.setup})
	}
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
