package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestTheWalksSampleIsTheSplits(t *testing.T) {
	kinds := []string{"uses", graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}

	for seed := range uint64(16) {
		for _, bound := range []bool{false, true} {
			t.Run(fmt.Sprintf("seed%d-bound%v", seed, bound), func(t *testing.T) {
				// lists that share much, so the sample has something to hold.
				p := graphtest.Params{
					Seed: seed, Units: 100, GroupSize: 5, Links: 1, Kinds: kinds, Slices: 8, Listed: 3, Core: true,
					Changed: 2, GroupNodes: true, Depth: int(seed % 3), Shared: 3, ChangeShared: seed%2 == 0,
				}
				pair := graphtest.Generate(p)

				first := pair.Slices[0]
				pair.Slices = append(pair.Slices, plugins.BindSlice{
					Suite: first.Suite, Name: recording.SetupTarget(first.Suite), Setup: true, HasList: true, List: first.List,
				})
				model := graphtest.Model{UnreachedNew: true}

				split := func(t *deps.Tree, ss []deps.Slice) []deps.Slice { return toSample(t, ss, true, bound) }

				oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
					out := model.Bind(tr, pair.Slices)

					return graphtest.AsBinder(tr)(tr, out), nil
				}, pair.Base, pair.Head, split)
				if err != nil {
					t.Fatal(err)
				}

				on := func(n string) bool {
					return n == "unreached-new" || n == "sample" || (bound && n == "sample-bound")
				}

				sel, err := graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents}, graph.Options{On: on})
				if err != nil && strings.Contains(err.Error(), "not built yet") {
					t.Skip("the walk isn't built yet")
				}

				if err != nil {
					t.Fatal(err)
				}

				// the trees are small: the walk compares every moved link
				// tree by tree and keeps no sampled unit as a source.
				c := graphtest.Compare(pair.Base, pair.Head, pair.Slices, oracle, sel.Selected, graphtest.Accept{})
				line, _, _ := strings.Cut(c.String(), "\n")
				t.Log(line)

				if !c.OK() || len(c.Explained) > 0 {
					t.Fatalf("the walk's sample differs from the split's:\n%s", c)
				}

				// a shared unit every slice ran changes, and with it what it links; one slice binds its
				// old end another way and never its new one, so it binds the same in both trees
				if _, ok := sel.Selected[graph.Key{Suite: "syn", Name: "s0006_test", Width: graph.Executed}]; ok && seed == 12 {
					t.Error("s0006_test is selected, though its binding is the same in both trees")
				}
			})
		}
	}
}

func TestASampledUnitsMovedLinksStillSelectTheSlices(t *testing.T) {
	tree := func(digest, to string) *deps.Tree {
		units := map[string]deps.Unit{}
		for _, id := range []string{"u:a", "u:b"} {
			units[id] = deps.Unit{ID: id, Group: "g", Digest: "1"}
		}

		units["u:x"] = deps.Unit{ID: "u:x", Group: "g", Digest: digest}

		return &deps.Tree{Units: units, Typed: []plugin.Link{{From: "u:x", To: to, Kind: graph.KindFollow}}, CannotTell: map[string]string{}}
	}

	base, head := tree("1", "u:a"), tree("2", "u:b")
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"u:x"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"u:x"}},
	}

	model := graphtest.Model{UnreachedNew: true}
	split := func(t *deps.Tree, ss []deps.Slice) []deps.Slice { return toSample(t, ss, true, false) }

	oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
		return graphtest.AsBinder(tr)(tr, model.Bind(tr, ss)), nil
	}, base, head, split)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"one", "two", affected.SampleTarget("s")} {
		if _, ok := oracle[graphtest.Key{Suite: "s", Name: name, Width: graph.Executed}]; !ok {
			t.Fatalf("binding forward doesn't select %s: %v", name, oracle)
		}
	}

	on := func(n string) bool { return n == "unreached-new" || n == "sample" }

	sel, err := graph.Select(t.Context(), base, head, ss, []string{graph.Executed, graph.Dependents}, graph.Options{On: on})
	if err != nil {
		t.Fatal(err)
	}

	if c := graphtest.Compare(base, head, ss, oracle, sel.Selected, graphtest.Accept{}); !c.OK() || len(c.Explained) > 0 {
		t.Fatalf("the walk differs from the split:\n%s", c)
	}
}

func TestPastTheCapASampledUnitWithMovedLinksStaysASource(t *testing.T) {
	ends := make([]string, 70)
	for i := range ends {
		ends[i] = fmt.Sprintf("u:b%02d", i)
	}

	tree := func(digest string, to ...string) *deps.Tree {
		units := map[string]deps.Unit{"u:a": {ID: "u:a", Group: "g", Digest: "1"}, "u:x": {ID: "u:x", Group: "g", Digest: digest}}
		for _, id := range ends {
			units[id] = deps.Unit{ID: id, Group: "g", Digest: "1"}
		}

		tr := &deps.Tree{Units: units, CannotTell: map[string]string{}}
		for _, id := range to {
			tr.Typed = append(tr.Typed, plugin.Link{From: "u:x", To: id, Kind: graph.KindFollow})
		}

		return tr
	}

	base, head := tree("1", "u:a"), tree("2", ends...)
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"u:x"}},
		{Suite: "s", Name: "two", HasList: true, List: append([]string{"u:x", "u:a"}, ends...)},
	}

	model := graphtest.Model{UnreachedNew: true}
	split := func(t *deps.Tree, ss []deps.Slice) []deps.Slice { return toSample(t, ss, true, false) }

	oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
		return graphtest.AsBinder(tr)(tr, model.Bind(tr, ss)), nil
	}, base, head, split)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := oracle[graphtest.Key{Suite: "s", Name: "two", Width: graph.Executed}]; ok {
		t.Fatalf("binding forward selects two: %v", oracle)
	}

	on := func(n string) bool { return n == "unreached-new" || n == "sample" }

	sel, err := graph.Select(t.Context(), base, head, ss, []string{graph.Executed, graph.Dependents}, graph.Options{On: on})
	if err != nil {
		t.Fatal(err)
	}

	c := graphtest.Compare(base, head, ss, oracle, sel.Selected, graphtest.Accept{KeptSources: true})
	if !c.OK() || c.Kept[graphtest.Key{Suite: "s", Name: "two", Width: graph.Executed}] != "u:x" {
		t.Fatalf("want two selected through u:x kept as a source:\n%s", c)
	}
}
