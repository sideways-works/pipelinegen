package cli

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestTheWalksEntrySampleIsTheSplits(t *testing.T) {
	kinds := []string{"uses", graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}
	plugs := []graph.Plugin{{Name: graphtest.SBOMName, SBOM: true, Namespaces: []string{"syn"}}, {Name: graphtest.CorrelatorName}}

	var entryLeft, entryTargets int

	for seed := range uint64(24) {
		for _, entry := range []bool{true, false} {
			t.Run(fmt.Sprintf("seed%d-entry%v", seed, entry), func(t *testing.T) {
				observed := seed%3 != 1
				sample := seed%2 == 0
				pinned := []string{graphtest.SBOMName, graphtest.CorrelatorName}[seed%2]

				p := graphtest.Params{
					Seed: seed, Units: 100, GroupSize: 5, Links: 1, Kinds: kinds, Slices: 8, Listed: 3, Core: true,
					Changed: int(seed % 3), GroupNodes: true, Depth: int(seed % 3), Shared: 3,
					Plugins: true, PinChanged: pinned, CorrelatorEnds: 4,
				}
				pair := graphtest.Generate(p)

				observes := func(string, string) bool { return observed }
				model := graphtest.Model{Plugins: plugs, UnreachedNew: true, Observes: observes}

				split := func(t *deps.Tree, ss []deps.Slice) []deps.Slice {
					return toSample(t, toEntrySample(pair.Slices, ss, entry), sample, sample)
				}

				oracle, bound, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
					return graphtest.AsBinder(tr)(tr, model.Bind(tr, pair.Slices)), nil
				}, pair.Base, pair.Head, split)
				if err != nil {
					t.Fatal(err)
				}

				on := func(n string) bool {
					return n == "unreached-new" || sample && (n == "sample" || n == "sample-bound") || entry && n == plugins.NarrowPluginEntry
				}

				sel, err := graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents},
					graph.Options{Plugins: plugs, On: on, Observes: observes})
				if err != nil {
					t.Fatal(err)
				}

				c := graphtest.Compare(pair.Base, pair.Head, pair.Slices, oracle, sel.Selected, graphtest.Accept{})
				if !c.OK() || len(c.Explained) > 0 {
					t.Fatalf("the walk differs from the split:\n%s", c)
				}

				want := forwardEntryLeft(pair.Base, pair.Head, bound, oracle)
				got := map[graphtest.Key]bool{}

				for k := range sel.LeftOut[plugins.NarrowPluginEntry] {
					got[graphtest.Key(k)] = true
				}

				if !maps.Equal(got, want) {
					t.Fatalf("plugin-entry leaves out %v in the walk, %v binding forward", got, want)
				}

				wantPool := forwardEntryPool(pair.Base, pair.Head, bound)
				gotPool := map[graphtest.Key]bool{}

				for k := range sel.EntryPool {
					gotPool[graphtest.Key(k)] = true
				}

				if !maps.Equal(gotPool, wantPool) {
					t.Fatalf("the entry's sample is drawn from %v in the walk, %v binding forward", gotPool, wantPool)
				}

				entryLeft += len(got)

				for k := range oracle {
					if k.Name == affected.EntryTarget(k.Suite) {
						entryTargets++
					}
				}
			})
		}
	}

	if entryLeft < 20 || entryTargets < 10 {
		t.Fatalf("plugin-entry left out %d slices and selected the entry target %d times: too few to tell", entryLeft, entryTargets)
	}
}

func forwardEntryLeft(base, head *deps.Tree, bound [2][]deps.Slice, selected map[graphtest.Key]string) map[graphtest.Key]bool {
	out := map[graphtest.Key]bool{}

	for _, ss := range bound {
		for _, d := range ss {
			k := graphtest.Key{Suite: d.Suite, Name: d.Name, Width: d.Width}
			if _, ok := selected[k]; ok {
				continue
			}

			if slices.ContainsFunc(d.Dropped[plugins.NarrowPluginEntry], func(id string) bool {
				return base.Units[id].Digest != head.Units[id].Digest
			}) {
				out[k] = true
			}
		}
	}

	return out
}

func forwardEntryPool(base, head *deps.Tree, bound [2][]deps.Slice) map[graphtest.Key]bool {
	out := map[graphtest.Key]bool{}

	for _, ss := range bound {
		for _, d := range ss {
			if slices.ContainsFunc(d.Dropped[plugins.NarrowPluginEntry], func(id string) bool {
				return base.Units[id].Digest != head.Units[id].Digest
			}) {
				out[graphtest.Key{Suite: d.Suite, Name: d.Name, Width: d.Width}] = true
			}
		}
	}

	return out
}
