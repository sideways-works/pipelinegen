package graph

import (
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// Sampled are what goes to each suite's sample, by suite, tree (base,
// head) and unit id, with the narrowing that put it there.
type Sampled map[string][2]map[string]string

// SampledBoth decides a pair's sample in a width.
func SampledBoth(b, h *deps.Tree, ss []plugins.BindSlice, width string, o Options) []Sampled {
	l := load(b, h)
	ch := changesOf(l, o, width)

	return []Sampled{
		named(l, sampleSuites(l, ss, ch, o)),
		named(l, sampledBy(l, ss, ch, o, true)),
		named(l, sampledBy(l, ss, ch, o, false)),
		named(l, sampleSuitesByWalks(l, ss, ch, o)),
	}
}

func sampledBy(l *loaded, ss []plugins.BindSlice, ch changes, o Options, closures bool) map[string]sampled {
	if !on(o, narrowSample) {
		return nil
	}

	out := map[string]sampled{}

	for suite, suiteSlices := range sampledSlices(ss) {
		if len(suiteSlices) < 2 {
			continue
		}

		roots := make([]sliceRoots, len(suiteSlices))
		for i, sl := range suiteSlices {
			roots[i] = rootsByWhy(l, sl)
		}

		var s sampled

		for _, tr := range []int{base, head} {
			s[tr] = map[int32]string{}

			changes := 0
			if closures {
				changes = len(roots)
			}

			sp := newSampler(l, tr, roots, on(o, narrowSampleBound), changes)

			for c, how := range ch.how {
				if _, ok := l.digest[tr][c]; ok && how != HowLinks {
					if n := sp.decide(c); n != "" {
						s[tr][c] = n
					}
				}
			}
		}

		if len(s[base])+len(s[head]) > 0 {
			out[suite] = s
		}
	}

	return out
}

func named(l *loaded, m map[string]sampled) Sampled {
	out := Sampled{}

	for suite, s := range m {
		var by [2]map[string]string

		for tr := range s {
			by[tr] = map[string]string{}
			for i, n := range s[tr] {
				by[tr][l.ids[i]] = n
			}
		}

		out[suite] = by
	}

	return out
}

// PlanSteps plans a pair in the widths given, with what each narrowing
// left out, and counts the work of its walks.
func PlanSteps(b, h *deps.Tree, ss []plugins.BindSlice, widths []string, o Options) int {
	l := load(b, h)

	leftOut(l, ss, widths, o, selectAll(l, ss, widths, o))

	return l.steps
}

// PlanWalks plans a pair in both widths, with what each narrowing left
// out, and counts the walks over the graph.
func PlanWalks(b, h *deps.Tree, ss []plugins.BindSlice, o Options) (walks, changes int) { //nolint:nonamedreturns // two counts
	l := load(b, h)
	widths := []string{Executed, Dependents}

	leftOut(l, ss, widths, o, selectAll(l, ss, widths, o))

	return l.walks, len(changesOf(l, o, Executed).how)
}
