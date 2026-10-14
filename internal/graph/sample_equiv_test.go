package graph_test

import (
	"maps"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func sampleCase(seed uint64) graphtest.Params {
	return graphtest.Params{
		Seed: seed, Units: 40 + 10*int(seed%4), GroupSize: 5, Links: 2,
		Kinds:  []string{"uses", graph.KindFollow, graph.KindBind, graph.KindDependentsOnly},
		Slices: 3 + int(seed%4), Listed: 6 + int(seed%5), Shared: 2 + int(seed%5), ChangeShared: seed%2 == 0,
		Core: seed%3 == 0, GroupNodes: seed%2 == 1, Changed: 3 + int(seed%6), Added: int(seed % 2), Removed: int(seed % 3),
		CannotTell: int(seed % 3), Plugins: true, PinChanged: []string{graphtest.SBOMName, ""}[seed%2],
	}
}

func sampleOptions(bound bool) graph.Options {
	return graph.Options{
		Plugins: []graph.Plugin{{Name: graphtest.SBOMName, SBOM: true, Namespaces: []string{"syn"}}, {Name: graphtest.CorrelatorName}},
		On:      func(n string) bool { return n == plugins.NarrowSample || (bound && n == plugins.NarrowSampleBound) },
	}
}

func TestTheSamplerDecidesAsAWalkPerChange(t *testing.T) {
	var sampled, bound int

	for seed := range uint64(400) {
		pair := graphtest.Generate(sampleCase(seed))

		for _, withBound := range []bool{true, false} {
			for _, width := range []string{graph.Executed, graph.Dependents} {
				all := graph.SampledBoth(pair.Base, pair.Head, pair.Slices, width, sampleOptions(withBound))
				want := all[len(all)-1]

				for k, got := range all[:len(all)-1] {
					if !maps.EqualFunc(got, want, func(a, b [2]map[string]string) bool { return maps.Equal(a[0], b[0]) && maps.Equal(a[1], b[1]) }) {
						t.Fatalf("seed %d, sample-bound %v, %s, sampler %d: it gives %v, the walks %v", seed, withBound, width, k, got, want)
					}
				}

				for _, s := range want {
					for _, by := range s {
						for _, n := range by {
							if n == plugins.NarrowSampleBound {
								bound++
							} else {
								sampled++
							}
						}
					}
				}
			}
		}
	}

	if sampled < 200 || bound < 50 {
		t.Fatalf("the cases put %d changes to the sample and %d to sample-bound: too few to tell", sampled, bound)
	}

	t.Logf("%d changes to the sample, %d to sample-bound", sampled, bound)
}

func TestAChangedPinCostsNoWalkPerUnit(t *testing.T) {
	const slices = 40

	for _, units := range []int{1000, 4000} {
		pair := graphtest.Generate(graphtest.Params{
			Seed: 7, Units: units, GroupSize: 10, Links: 4, Kinds: []string{"uses", graph.KindFollow, graph.KindBind},
			Slices: slices, Listed: 30, Shared: 3, GroupNodes: true, Plugins: true, PinChanged: graphtest.SBOMName,
		})

		walks, changes := graph.PlanWalks(pair.Base, pair.Head, pair.Slices, sampleOptions(true))

		// per tree, each slice's closure counted and kept; per width the
		// walk, the suite's walk and its moved walks (two per moved end,
		// at most 64 ends); a tenth of the changes may need a cone
		limit := 2*2*slices + 2*(2+2*2*64) + changes/10

		t.Logf("%d units, %d changes: %d walks, at most %d", units, changes, walks, limit)

		if walks > limit {
			t.Fatalf("%d units: %d walks for %d changes, more than %d", units, walks, changes, limit)
		}
	}
}
