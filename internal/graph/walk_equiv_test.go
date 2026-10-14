package graph_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

var junctions = graphtest.Accept{Junctions: []string{graph.NodePrefix + "syn:impl:", graph.NodePrefix + "go:impl:"}}

type equivCase struct {
	name                        string
	p                           graphtest.Params
	unreachedNew, referencedNew bool
}

func equivCases() []equivCase {
	kinds := []string{"uses", "reads", graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}

	var out []equivCase

	for seed := range uint64(24) {
		p := graphtest.Params{
			Seed: seed, Units: 120 + 40*int(seed%5), GroupSize: 6, Links: 1, Kinds: kinds,
			Slices: 12, Listed: 2 + int(seed%3), Unlisted: int(seed % 3),
			Changed: 1 + int(seed%4), Added: int(seed % 2), Removed: int(seed % 3), CannotTell: int(seed % 4),
			Depth: int(seed % 4), Core: seed%4 == 1, GroupNodes: seed%2 == 0, Junctions: int(seed % 3),
			Plugins: true, PinChanged: []string{"", "", "", graphtest.SBOMName, "", "", "", graphtest.CorrelatorName}[seed%8],
			Refs: seed%4 == 2,
		}

		if seed%5 == 4 {
			p.OwnFiles = 2
		}

		if seed%3 == 0 {
			p.Narrowings, p.RuledOut = []string{"wide-links"}, 6
		}

		if seed%7 == 3 {
			p.UntoldCorrelator = graphtest.CorrelatorName
		}

		out = append(out, equivCase{name: fmt.Sprintf("seed%d", seed), p: p, unreachedNew: seed%4 != 2, referencedNew: seed%8 == 6})
	}

	return out
}

func TestTheWalkSelectsWhatTheForwardBindingSelects(t *testing.T) {
	for _, c := range equivCases() {
		t.Run(c.name, func(t *testing.T) {
			pair := graphtest.Generate(c.p)
			model, opts := modelFor(c)

			bind := func(tr *deps.Tree) ([]deps.Slice, error) { return model.Bind(tr, pair.Slices), nil }

			oracle, _, err := graphtest.ForwardWith(bind, pair.Base, pair.Head)
			if err != nil {
				t.Fatal(err)
			}

			sel, err := graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents}, opts)
			if err != nil && strings.Contains(err.Error(), "not built yet") {
				t.Skip("the walk isn't built yet")
			}

			if err != nil {
				t.Fatal(err)
			}

			cmp := graphtest.Compare(pair.Base, pair.Head, pair.Slices, oracle, sel.Selected, junctions)
			t.Logf("selected: %s", firstLine(cmp.String()))

			if !cmp.OK() || len(cmp.Explained) > 0 || len(cmp.Joined) > 0 || len(cmp.Sampled) > 0 || len(cmp.Kept) > 0 {
				t.Fatalf("the walk and the forward binding differ:\n%s", cmp)
			}

			for _, n := range c.p.Narrowings {
				left, err := graphtest.LeftOut(bind, pair.Base, pair.Head, n, oracle)
				if err != nil {
					t.Fatal(err)
				}

				lc := graphtest.Compare(graphtest.Wide(pair.Base, n), graphtest.Wide(pair.Head, n), pair.Slices, left, sel.LeftOut[n], junctions)
				t.Logf("left out by %s: %s", n, firstLine(lc.String()))

				if !lc.OK() || len(lc.Explained) > 0 || len(lc.Joined) > 0 || len(lc.Sampled) > 0 || len(lc.Kept) > 0 {
					t.Fatalf("what %s left out differs:\n%s", n, lc)
				}
			}
		})
	}
}

func modelFor(c equivCase) (graphtest.Model, graph.Options) {
	plugs := []graph.Plugin{
		{Name: graphtest.SBOMName, SBOM: true, Namespaces: []string{"syn"}},
		{Name: graphtest.CorrelatorName},
	}

	on := func(n string) bool {
		switch n {
		case "unreached-new":
			return c.unreachedNew
		case "referenced-new":
			return c.referencedNew
		}

		return slices.Contains(c.p.Narrowings, n)
	}

	return graphtest.Model{Plugins: plugs, UnreachedNew: c.unreachedNew, ReferencedNew: c.referencedNew}, graph.Options{Plugins: plugs, On: on}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}

func TestTheEquivalenceCasesTellSlicesApart(t *testing.T) {
	partial, leftOut := 0, 0

	for _, c := range equivCases() {
		pair := graphtest.Generate(c.p)
		model, _ := modelFor(c)
		bind := func(tr *deps.Tree) ([]deps.Slice, error) { return model.Bind(tr, pair.Slices), nil }

		oracle, _, err := graphtest.ForwardWith(bind, pair.Base, pair.Head)
		if err != nil {
			t.Fatal(err)
		}

		count := map[string]int{}
		for k := range oracle {
			count[k.Width]++
		}

		if n := count[graph.Executed]; n > 0 && n < c.p.Slices {
			partial++
		} else if n := count[graph.Dependents]; n > 0 && n < c.p.Slices {
			partial++
		}

		for _, n := range c.p.Narrowings {
			left, err := graphtest.LeftOut(bind, pair.Base, pair.Head, n, oracle)
			if err != nil {
				t.Fatal(err)
			}

			leftOut += len(left)
		}
	}

	if cases := len(equivCases()); partial < cases/3 || leftOut < 10 {
		t.Fatalf("%d of %d cases select some slices and not others, the narrowings leave out %d", partial, cases, leftOut)
	}
}

const (
	crossOp      = "api:createNote"
	crossHandler = "go:server/notes#method:Handler.CreateNote"
	crossHelper  = "go:server/notes#func:validate"
)

func crossTrees(change string) (*deps.Tree, *deps.Tree) {
	tree := func(handler, op string) *deps.Tree {
		return &deps.Tree{
			Units: map[string]deps.Unit{
				crossOp:      {ID: crossOp, Group: "api/openapi.yaml", Digest: op},
				crossHandler: {ID: crossHandler, Group: "server/notes", Digest: handler},
				crossHelper:  {ID: crossHelper, Group: "server/notes", Digest: "v"},
			},
			Links:      []plugin.Link{{From: crossHandler, To: crossOp, Kind: "implements"}},
			Typed:      []plugin.Link{{From: crossHandler, To: crossHelper, Kind: graph.KindDependentsOnly}},
			CannotTell: map[string]string{},
		}
	}

	switch change {
	case "handler":
		return tree("h1", "o"), tree("h2", "o")
	default:
		return tree("h", "o1"), tree("h", "o2")
	}
}

func TestABrowserTestIsSelectedThroughWhatItsRequestsReached(t *testing.T) {
	ss := []plugins.BindSlice{{Suite: "browser", Name: "web/notes.spec.ts", HasList: true, List: []string{crossOp, crossHandler, crossHelper}}}

	for _, change := range []string{"handler", "op"} {
		base, head := crossTrees(change)
		checkCross(t, base, head, ss, map[string]bool{graph.Executed: true, graph.Dependents: true})
	}
}

func TestAFrontendTestOfAMockedBackendNeedsTheWideSetting(t *testing.T) {
	ss := []plugins.BindSlice{{Suite: "browser", Name: "web/notes.spec.ts", HasList: true, List: []string{crossOp}}}
	base, head := crossTrees("handler")

	checkCross(t, base, head, ss, map[string]bool{graph.Executed: false, graph.Dependents: true})
}

func checkCross(t *testing.T, base, head *deps.Tree, ss []plugins.BindSlice, want map[string]bool) {
	t.Helper()

	model := graphtest.Model{UnreachedNew: true}

	oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) { return model.Bind(tr, ss), nil }, base, head)
	if err != nil {
		t.Fatal(err)
	}

	for width, selected := range want {
		if _, ok := oracle[graphtest.Key{Suite: ss[0].Suite, Name: ss[0].Name, Width: width}]; ok != selected {
			t.Fatalf("%s: selected %v, want %v: %v", width, ok, selected, oracle)
		}
	}

	sel, err := graph.Select(t.Context(), base, head, ss, []string{graph.Executed, graph.Dependents}, graph.Options{})
	if err != nil && strings.Contains(err.Error(), "not built yet") {
		t.Skip("the walk isn't built yet")
	}

	if err != nil {
		t.Fatal(err)
	}

	if c := graphtest.Compare(base, head, ss, oracle, sel.Selected, graphtest.Accept{}); !c.OK() || len(c.Explained) > 0 {
		t.Fatalf("the walk differs:\n%s", c)
	}
}

func TestALinkBetweenUnchangedUnitsIsTheOneAcceptedExtra(t *testing.T) {
	tree := func(links ...plugin.Link) *deps.Tree {
		units := map[string]deps.Unit{}
		for _, id := range []string{"u:listed", "u:source", "u:target"} {
			units[id] = deps.Unit{ID: id, Group: "g", Digest: "same"}
		}

		return &deps.Tree{Units: units, Links: links, CannotTell: map[string]string{}}
	}

	reach := []plugin.Link{{From: "u:listed", To: "u:source", Kind: "uses"}, {From: "u:listed", To: "u:target", Kind: "uses"}}
	base := tree(reach...)
	head := tree(append(slices.Clone(reach), plugin.Link{From: "u:source", To: "u:target", Kind: "uses"})...)
	ss := []plugins.BindSlice{{Suite: "s", Name: "x", HasList: true, List: []string{"u:listed"}}}

	oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
		return graphtest.Model{UnreachedNew: true}.Bind(tr, ss), nil
	}, base, head)
	if err != nil {
		t.Fatal(err)
	}

	if len(oracle) > 0 {
		t.Fatalf("binding forward selects: %v", oracle)
	}

	sel, err := graph.Select(t.Context(), base, head, ss, []string{graph.Executed}, graph.Options{})
	if err != nil && strings.Contains(err.Error(), "not built yet") {
		t.Skip("the walk isn't built yet")
	}

	if err != nil {
		t.Fatal(err)
	}

	c := graphtest.Compare(base, head, ss, oracle, sel.Selected, graphtest.Accept{})
	if !c.OK() || len(c.Explained) != 1 || !strings.Contains(c.String(), "u:source -> u:target") {
		t.Fatalf("want the slice explained by the new link:\n%s", c)
	}
}

func TestAnOwnFileWithoutUnitsSelectsTheSlicesWithoutAList(t *testing.T) {
	c := equivCase{p: graphtest.Params{Seed: 1, Units: 60, GroupSize: 6, Links: 1, Slices: 5, Listed: 2, Unlisted: 2, OwnFiles: 1}, unreachedNew: true}
	pair := graphtest.Generate(c.p)
	model, opts := modelFor(c)

	oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) { return model.Bind(tr, pair.Slices), nil }, pair.Base, pair.Head)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"s0000_test", "s0001_test", "s0002_test"} {
		if _, ok := oracle[graphtest.Key{Suite: "syn", Name: name, Width: graph.Executed}]; !ok {
			t.Fatalf("binding forward doesn't select %s: %v", name, oracle)
		}
	}

	sel, err := graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents}, opts)
	if err != nil {
		t.Fatal(err)
	}

	if cmp := graphtest.Compare(pair.Base, pair.Head, pair.Slices, oracle, sel.Selected, junctions); !cmp.OK() || len(cmp.Explained) > 0 {
		t.Fatalf("the walk differs:\n%s", cmp)
	}
}
