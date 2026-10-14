package graphtest

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func syntheticSet() (*plugins.Set, Model) {
	set := &plugins.Set{Plugins: []*plugins.Loaded{
		{Conf: config.Plugin{Name: SBOMName}, Desc: plugin.Description{Kind: plugin.KindSBOM, Namespaces: []string{"syn"}}},
		{Conf: config.Plugin{Name: CorrelatorName}, Desc: plugin.Description{Kind: plugin.KindCorrelator}},
	}}

	return set, Model{UnreachedNew: true, Plugins: []graph.Plugin{
		{Name: SBOMName, SBOM: true, Namespaces: []string{"syn"}},
		{Name: CorrelatorName},
	}}
}

func TestTheModelBindsAsBindOverPlainLinks(t *testing.T) {
	for seed := range uint64(60) {
		p := Params{
			Seed: seed, Units: 150 + int(seed)*5, GroupSize: 6, Links: 1 + int(seed%3), Slices: 15, Listed: 3, Unlisted: 2,
			Changed: 3, Added: 2, Removed: 2, CannotTell: 4, Depth: int(seed % 4), Core: seed%3 == 0,
			Narrowings: []string{"n1"}, RuledOut: 5, Plugins: true, PinChanged: []string{"", SBOMName, CorrelatorName}[seed%3],
		}
		if seed%5 == 0 {
			p.UntoldCorrelator = CorrelatorName
		}

		pair := Generate(p)
		set, model := syntheticSet()

		for _, tree := range []*deps.Tree{pair.Base, pair.Head} {
			want, err := set.Bind(t.Context(), t.TempDir(), tree, pair.Slices)
			if err != nil {
				t.Fatal(err)
			}

			got := model.Bind(tree, pair.Slices)

			for i := range want {
				w, g := want[i], got[i]
				if KeyOf(w) != KeyOf(g) || w.Everything != g.Everything || !slices.Equal(w.Units, g.Units) || !slices.Equal(w.Groups, g.Groups) {
					t.Fatalf("seed %d, %s, %v:\nbind  %v %v %v\nmodel %v %v %v", seed, tree.Commit, KeyOf(w),
						w.Everything, w.Units, w.Groups, g.Everything, g.Units, g.Groups)
				}
			}
		}
	}
}

func TestOneDepthCounterOverBothKindsOfStep(t *testing.T) {
	tree := newTree("t")
	for _, id := range []string{"go:a#func:Listed", "go:a#func:Called", "sql:table"} {
		tree.Units[id] = deps.Unit{ID: id, Group: "a", Digest: "d"}
	}

	tree.Typed = []plugin.Link{{From: "go:a#func:Listed", To: "go:a#func:Called", Kind: graph.KindDependentsOnly}}
	tree.Links = []plugin.Link{{From: "sql:table", To: "go:a#func:Called", Kind: plugin.LinkUses}}

	for depth, want := range map[int]bool{1: false, 2: true, 0: true} {
		out := Model{UnreachedNew: true}.Bind(tree, []plugins.BindSlice{{Suite: "s", Name: "x", HasList: true, List: []string{"go:a#func:Listed"}, Depth: depth}})

		dep := out[1]
		if !slices.Contains(dep.Units, "go:a#func:Called") || slices.Contains(dep.Units, "sql:table") != want {
			t.Errorf("depth %d: %v, want the table bound: %v", depth, dep.Units, want)
		}

		if slices.Contains(out[0].Units, "go:a#func:Called") {
			t.Errorf("the executed width binds a call: %v", out[0].Units)
		}
	}
}

func TestTheModelKeepsTheKindsTable(t *testing.T) {
	tree := newTree("t")
	for _, id := range []string{"u:root", "u:type", "u:typeRef", "u:empty", "u:emptyRef", "u:emptyPlain", "u:called", "u:calledRef", "u:impl"} {
		tree.Units[id] = deps.Unit{ID: id, Group: "g", Digest: "d"}
	}

	tree.Typed = []plugin.Link{
		{From: "u:root", To: "u:type", Kind: graph.KindFollow},
		{From: "u:type", To: "u:typeRef", Kind: graph.KindFollow},
		{From: "u:root", To: "u:empty", Kind: graph.KindBind},
		{From: "u:empty", To: "u:emptyRef", Kind: graph.KindFollow},
		{From: "u:root", To: "u:called", Kind: graph.KindDependentsOnly},
		{From: "u:called", To: "u:calledRef", Kind: graph.KindFollow},
		{From: "u:called", To: "node:syn:impl:M", Kind: graph.KindDependentsOnly},
		{From: "node:syn:impl:M", To: "u:impl", Kind: graph.KindDependentsOnly},
	}
	tree.Links = []plugin.Link{{From: "u:empty", To: "u:emptyPlain", Kind: plugin.LinkUses}}

	for _, c := range []struct {
		depth  int
		wantEx []string
		dep    []string
	}{
		{depth: 0, wantEx: []string{"u:empty", "u:emptyPlain", "u:root", "u:type", "u:typeRef"},
			dep: []string{"u:called", "u:calledRef", "u:empty", "u:emptyPlain", "u:emptyRef", "u:impl", "u:root", "u:type", "u:typeRef"}},
		{depth: 1, wantEx: []string{"u:empty", "u:emptyPlain", "u:root", "u:type", "u:typeRef"},
			dep: []string{"u:called", "u:empty", "u:emptyPlain", "u:emptyRef", "u:root", "u:type", "u:typeRef"}},
	} {
		out := Model{UnreachedNew: true}.Bind(tree, []plugins.BindSlice{{Suite: "s", Name: "x", HasList: true, List: []string{"u:root"}, Depth: c.depth}})

		if !slices.Equal(out[0].Units, c.wantEx) {
			t.Errorf("depth %d, executed %v, want %v", c.depth, out[0].Units, c.wantEx)
		}

		if !slices.Equal(out[1].Units, c.dep) {
			t.Errorf("depth %d, dependents %v, want %v", c.depth, out[1].Units, c.dep)
		}
	}
}

func TestALinkedGroupIsTheExecutedBindings(t *testing.T) {
	tree := newTree("t")
	for id, g := range map[string]string{"s:r#r": "r", "s:m#m": "m", "s:g#a": "g", "s:g#x": "g"} {
		tree.Units[id] = deps.Unit{ID: id, Group: g, Digest: "d"}
	}

	tree.Typed = []plugin.Link{{From: "s:r#r", To: "s:m#m", Kind: graph.KindBind}, {From: "s:m#m", To: graph.GroupPrefix + "g", Kind: graph.KindFollow}}
	ss := []plugins.BindSlice{{Suite: "s", Name: "one", HasList: true, List: []string{"s:r#r"}}}

	for _, d := range (Model{}).Bind(tree, ss) {
		if slices.Contains(d.Linked, "g") {
			t.Errorf("%s: links g, which only the dependents width reaches: %v", d.Width, d.Linked)
		}
	}

	tree.Typed[0].Kind = graph.KindFollow

	for _, d := range (Model{}).Bind(tree, ss) {
		if !slices.Contains(d.Linked, "g") {
			t.Errorf("%s: doesn't link g: %v", d.Width, d.Linked)
		}
	}
}

func TestAReferencedNameIsBoundUnderReferencedNew(t *testing.T) {
	tree := newTree("t")
	for _, id := range []string{"s:g#caller", "s:g#called"} {
		tree.Units[id] = deps.Unit{ID: id, Group: "g", Digest: "d"}
	}

	tree.Typed = []plugin.Link{
		{From: "s:g#caller", To: "s:g#called", Kind: graph.KindDependentsOnly, Ref: true},
		{From: "s:g#caller", To: graph.GroupPrefix + "g", Kind: graph.KindFollow},
	}
	ss := []plugins.BindSlice{{Suite: "s", Name: "one", HasList: true, List: []string{"s:g#caller"}}}

	for on, want := range map[bool]string{false: "s:g#called", true: ""} {
		tr := *tree
		tr.Units = maps.Clone(tree.Units)
		(Model{ReferencedNew: on}).Bind(&tr, ss)

		if got := tr.Units[deps.UnboundID(Executed, "g")].Digest; got != want {
			t.Errorf("referenced-new %v: unbound names %q, want %q", on, got, want)
		}
	}
}

func TestAnInputUnitIsNoNameAndNotBoundWhole(t *testing.T) {
	tree := newTree("t")
	for _, id := range []string{"s:g#caller", "s:g#free", "s:g#api"} {
		tree.Units[id] = deps.Unit{ID: id, Group: "g", Digest: "d"}
	}

	tree.InputUnits = []string{"s:g#api"}
	tree.Typed = []plugin.Link{{From: "s:g#caller", To: graph.GroupPrefix + "g", Kind: graph.KindFollow}}
	ss := []plugins.BindSlice{{Suite: "s", Name: "one", HasList: true, List: []string{"s:g#caller"}}}

	tr := *tree
	tr.Units = maps.Clone(tree.Units)
	(Model{}).Bind(&tr, ss)

	if got := tr.Units[deps.UnboundID(Executed, "g")].Digest; got != "s:g#free" {
		t.Errorf("unbound names %q, want s:g#free alone", got)
	}

	whole := deps.Slice{Groups: []string{"g"}}
	for _, line := range Content(tree, whole) {
		if strings.Contains(line, "s:g#api") {
			t.Errorf("a whole group binds its input unit: %s", line)
		}
	}
}
