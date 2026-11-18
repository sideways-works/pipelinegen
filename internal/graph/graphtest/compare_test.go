package graphtest

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestCompareSortsIntoItsClasses(t *testing.T) {
	base, head := newTree("base"), newTree("head")
	for _, tr := range []*deps.Tree{base, head} {
		for _, id := range []string{"u:a", "u:c", "u:d"} {
			tr.Units[id] = deps.Unit{ID: id, Digest: "same"}
		}

		tr.Links = []plugin.Link{{From: "u:a", To: "u:b"}}
		tr.Typed = []plugin.Link{{From: "u:b", To: "node:go:impl:M()", Kind: graph.KindDependentsOnly}, {From: "node:go:impl:M()", To: "u:m", Kind: graph.KindDependentsOnly}}
	}

	head.Links = append(head.Links, plugin.Link{From: "u:c", To: "u:d"}, plugin.Link{From: "u:e", To: "u:d"})
	base.Units["u:e"] = deps.Unit{ID: "u:e", Digest: "1"}
	head.Units["u:e"] = deps.Unit{ID: "u:e", Digest: "2"}

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "same", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "link", HasList: true, List: []string{"u:c"}},
		{Suite: "s", Name: "impl", HasList: true, List: []string{"u:b"}},
		{Suite: "s", Name: "missing", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "other", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "bad", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "changed", HasList: true, List: []string{"u:e"}},
		{Suite: "s", Name: "pin", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "pinroot", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "untold", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "nowhere", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "unit", HasList: true, List: []string{"u:a"}},
	}

	key := func(name, width string) Key { return Key{Suite: "s", Name: name, Width: width} }
	gkey := func(name, width string) graph.Key { return graph.Key(key(name, width)) }
	path := func(listed, changed string, steps ...graph.Step) []graph.Path {
		return []graph.Path{{Listed: listed, Changed: changed, Steps: steps}}
	}

	oracle := map[Key]string{
		key("same", Executed):    "+ unit u:b",
		key("missing", Executed): "+ unit u:b",
		key("bad", Executed):     "+ unit u:b",
		key("pin", Executed):     "+ unit plugin:c",
		key("pinroot", Executed): "+ unit plugin:c",
		key("untold", Executed):  "+ everything",
		key("nowhere", Executed): "+ everything",
		key("unit", Executed):    "+ everything",
	}
	walk := map[graph.Key][]graph.Path{
		gkey("same", Executed): path("u:a", "u:b", graph.Step{From: "u:a", To: "u:b", Kind: plugin.LinkUses}),
		gkey("link", Executed): path("u:c", "u:c"),
		gkey("impl", Dependents): path("u:b", "u:m",
			graph.Step{From: "u:b", To: "node:go:impl:M()", Kind: graph.KindDependentsOnly},
			graph.Step{From: "node:go:impl:M()", To: "u:m", Kind: graph.KindDependentsOnly}),
		gkey("other", Executed):   path("u:a", "u:b", graph.Step{From: "u:a", To: "u:b", Kind: plugin.LinkUses}),
		gkey("bad", Executed):     path("u:a", "u:b", graph.Step{From: "u:a", To: "u:z", Kind: plugin.LinkUses}),
		gkey("changed", Executed): path("u:e", "u:e"),
		gkey("pin", Executed):     path("", "plugin:c", graph.Step{To: "plugin:c", Kind: graph.KindPin}),
		gkey("pinroot", Executed): path("u:zz", "plugin:c", graph.Step{To: "plugin:c", Kind: graph.KindPin}),
		gkey("untold", Executed):  path("", "plugin:c", graph.Step{To: "plugin:c", Kind: graph.KindEverything}),
		gkey("nowhere", Executed): path("", "", graph.Step{Kind: graph.KindEverything}),
		gkey("unit", Executed):    path("", "u:a", graph.Step{To: "u:a", Kind: graph.KindEverything}),
	}

	c := Compare(base, head, ss, oracle, walk, Accept{Junctions: []string{"node:go:impl:"}})

	for what, got := range map[string]map[Key]string{
		"explained": c.Explained, "joined": c.Joined, "missing": c.Missing, "unexplained": c.Unexplained, "bad": c.BadPaths,
	} {
		want := map[string][]Key{
			"explained": {key("link", Executed)}, "joined": {key("impl", Dependents)}, "missing": {key("missing", Executed)},
			"unexplained": {key("other", Executed), key("changed", Executed)}, "bad": {key("bad", Executed), key("pinroot", Executed), key("nowhere", Executed), key("unit", Executed)},
		}[what]

		for _, k := range want {
			if _, ok := got[k]; !ok || len(got) != len(want) {
				t.Errorf("%s: %v, want %v\n%s", what, got, want, c)
			}
		}
	}

	if c.Same != 7 || c.OK() {
		t.Errorf("same %d, ok %v:\n%s", c.Same, c.OK(), c)
	}

	if !strings.Contains(c.Explained[key("link", Executed)], "u:c -> u:d") {
		t.Errorf("the one-tree link isn't named: %s", c.Explained[key("link", Executed)])
	}
}

const (
	Executed   = plugins.Executed
	Dependents = plugins.Dependents
)

func TestCompareAcceptsASampleTargetOnlyBesideEverySlice(t *testing.T) {
	base, head := newTree("base"), newTree("head")
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"u:a"}},
		{Suite: "s", Name: "_setup.s", HasList: true, Setup: true, List: []string{"u:a"}},
	}

	for _, tr := range []*deps.Tree{base, head} {
		tr.Units["u:a"] = deps.Unit{ID: "u:a", Digest: tr.Commit}
	}

	path := []graph.Path{{Listed: "u:a", Changed: "u:a"}}
	oracle := map[Key]string{{Suite: "s", Name: "one", Width: Executed}: "- unit u:a", {Suite: "s", Name: "two", Width: Executed}: "- unit u:a"}
	walk := map[graph.Key][]graph.Path{
		{Suite: "s", Name: "one", Width: Executed}:                        path,
		{Suite: "s", Name: "two", Width: Executed}:                        path,
		{Suite: "s", Name: graph.SampleBoundTarget("s"), Width: Executed}: path,
	}

	c := Compare(base, head, ss, oracle, walk, Accept{})
	if !c.OK() || len(c.Sampled) != 1 || !strings.Contains(c.String(), "1 sample targets only the walk selects") {
		t.Fatalf("want the target accepted beside every slice:\n%s", c)
	}

	delete(walk, graph.Key{Suite: "s", Name: "two", Width: Executed})
	delete(oracle, Key{Suite: "s", Name: "two", Width: Executed})

	if c := Compare(base, head, ss, oracle, walk, Accept{}); c.OK() || len(c.Unexplained) != 1 {
		t.Fatalf("a target beside some slices only must fail:\n%s", c)
	}
}

func TestCompareKeepsASampledSourceOnlyWhenAccepted(t *testing.T) {
	base, head := newTree("base"), newTree("head")
	for _, tr := range []*deps.Tree{base, head} {
		for _, id := range []string{"u:x", "u:a", "u:b", "u:z"} {
			tr.Units[id] = deps.Unit{ID: id, Digest: "same"}
		}

		tr.Units["u:x"] = deps.Unit{ID: "u:x", Digest: tr.Commit}
		tr.Units["u:z"] = deps.Unit{ID: "u:z", Digest: tr.Commit}
	}

	base.Links = []plugin.Link{{From: "u:x", To: "u:a"}}
	head.Links = []plugin.Link{{From: "u:x", To: "u:b"}}

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"u:x"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"u:x", "u:a", "u:b", "u:z"}},
	}
	key := graph.Key{Suite: "s", Name: "two", Width: Executed}
	oracle := map[Key]string{{Suite: "s", Name: "one", Width: Executed}: "- unit u:a"}
	walk := map[graph.Key][]graph.Path{
		{Suite: "s", Name: "one", Width: Executed}: {{Listed: "u:x", Changed: "u:x"}},
		key: {{Listed: "u:x", Changed: "u:x"}},
		{Suite: "s", Name: graph.SampleTarget("s"), Width: Executed}: {{Listed: "u:x", Changed: "u:x", Steps: []graph.Step{{From: "u:x", To: "u:x", Kind: graph.KindSample}}}},
	}

	if c := Compare(base, head, ss, oracle, walk, Accept{KeptSources: true}); !c.OK() || c.Kept[Key(key)] != "u:x" {
		t.Fatalf("want two kept through u:x:\n%s", c)
	}

	if c := Compare(base, head, ss, oracle, walk, Accept{}); c.OK() || len(c.Kept) > 0 {
		t.Fatalf("the fallback must fail unless accepted:\n%s", c)
	}

	walk[key] = []graph.Path{{Listed: "u:z", Changed: "u:z"}}
	if c := Compare(base, head, ss, oracle, walk, Accept{KeptSources: true}); c.OK() {
		t.Fatalf("a changed unit with no moved link is no fallback:\n%s", c)
	}
}
