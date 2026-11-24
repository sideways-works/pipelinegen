package affected

import (
	"slices"
	"sort"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// KeepPassed leaves out of p, a plan of every slice, for a run with passed_within, each slice that passed
// in the run within the period with nothing from there to the head reaching it (rb.Since says so).
// A suite with whole-suite checks runs whole: they need every slice. The slices left out go under
// the passed-since narrowing, one entry per commit they passed at; it reports whether it left any.
func KeepPassed(p *Plan, cfg *config.Config, rb *Rebase) bool {
	if rb == nil || !p.All {
		return false
	}

	left := false

	for i := range p.Suites {
		sp := &p.Suites[i]
		if len(cfg.Suites[sp.Suite].WholeSuite) > 0 || len(sp.Selected) == 0 {
			continue
		}

		if keepPassed(sp, rb) {
			left = true
		}
	}

	if left {
		p.All, p.Reason = false, ""
	}

	return left
}

func keepPassed(sp *SuitePlan, rb *Rebase) bool {
	var (
		keep    []bazel.Slice
		entries []Narrowed
	)

	for _, sl := range sp.Selected {
		s, ok := rb.Since(sp.Suite, sl)
		if !ok {
			keep = append(keep, sl)

			continue
		}

		i := slices.IndexFunc(entries, func(n Narrowed) bool { return n.Reason == s.Why })
		if i < 0 {
			entries = append(entries, Narrowed{Kind: NarrowedBy, Narrowing: plugins.NarrowPassedSince, OffAt: rb.OffAt, Reason: s.Why})
			i = len(entries) - 1
		}

		entries[i].Dropped = append(entries[i].Dropped, sl.Name)
		sp.Untouched = append(sp.Untouched, sl.Label)
	}

	if len(entries) == 0 {
		return false
	}

	for i := range entries {
		entries[i].Reached = len(sp.Selected)
	}

	sort.Strings(sp.Untouched)

	sp.Selected = append([]bazel.Slice{}, keep...)
	sp.All, sp.Reason = false, ""
	sp.Narrowed = append(sp.Narrowed, entries...)

	return true
}
