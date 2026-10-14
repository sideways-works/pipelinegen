package graph_test

import (
	"fmt"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
)

func TestACorrelatorsEntryReachesWhatItObservesOrAnswered(t *testing.T) {
	kinds := []string{"uses", "reads", graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}
	partial := 0

	for seed := range uint64(40) {
		observed := seed%2 == 0
		p := graphtest.Params{
			Seed: seed, Units: 80 + 20*int(seed%4), GroupSize: 6, Links: 1, Kinds: kinds,
			Slices: 10, Listed: 2 + int(seed%3), Unlisted: int(seed % 2),
			Changed: int(seed % 3), Depth: int(seed % 3), GroupNodes: seed%2 == 0,
			Plugins: true, PinChanged: graphtest.CorrelatorName, CorrelatorEnds: 1 + int(seed%5),
		}

		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			pair := graphtest.Generate(p)
			plugs := []graph.Plugin{{Name: graphtest.SBOMName, SBOM: true, Namespaces: []string{"syn"}}, {Name: graphtest.CorrelatorName}}
			observes := func(string, string) bool { return observed }
			model := graphtest.Model{Plugins: plugs, UnreachedNew: true, Observes: observes}
			opts := graph.Options{Plugins: plugs, On: func(n string) bool { return n == "unreached-new" }, Observes: observes}

			bind := func(tr *deps.Tree) ([]deps.Slice, error) { return model.Bind(tr, pair.Slices), nil }

			oracle, _, err := graphtest.ForwardWith(bind, pair.Base, pair.Head)
			if err != nil {
				t.Fatal(err)
			}

			sel, err := graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents}, opts)
			if err != nil {
				t.Fatal(err)
			}

			cmp := graphtest.Compare(pair.Base, pair.Head, pair.Slices, oracle, sel.Selected, junctions)
			if !cmp.OK() || len(cmp.Explained) > 0 || len(cmp.Joined) > 0 || len(cmp.Sampled) > 0 || len(cmp.Kept) > 0 {
				t.Fatalf("the walk and the forward binding differ:\n%s", cmp)
			}

			if !observed && p.Changed == 0 {
				n := 0

				for k := range oracle {
					if k.Width == graph.Executed {
						n++
					}
				}

				t.Logf("unobserved: %d of %d selected, %d without a list", n, p.Slices, p.Unlisted)

				if n > p.Unlisted && n < p.Slices {
					partial++
				}
			}
		})
	}

	if partial < 3 {
		t.Fatalf("in %d cases only does an unobserved suite have slices the entry reaches and slices it leaves alone", partial)
	}
}
