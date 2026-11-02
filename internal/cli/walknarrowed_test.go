package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestWalkNarrowedNamesEverySliceUnderEachNarrowing(t *testing.T) {
	key := func(name, width string) graph.Key { return graph.Key{Suite: "s", Name: name, Width: width} }
	path := func(changed string) []graph.Path { return []graph.Path{{Listed: "go:a#F", Changed: changed}} }
	names := []graph.Path{{Listed: "go:a#F", Changed: "group:x", Steps: []graph.Step{{From: "go:a#F", To: "group:x", Kind: graph.KindNames}}}}

	sel := &graph.Selection{LeftOut: map[string]map[graph.Key][]graph.Path{
		"method-sets":   {key("a", "executed"): path("go:x#method:T.M"), key("b", "executed"): path("go:x#method:T.N")},
		"unreached-new": {key("a", "executed"): names, key("c", "executed"): names, key("d", "dependents"): names},
	}}
	offAt := func(n string) int { return map[string]int{"method-sets": 5, "unreached-new": 3}[n] }

	left := []bazel.Slice{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	got := walkNarrowedHook(sel, offAt)("", "", "s", "executed", []bazel.Slice{{Name: "z"}}, left)

	want := []affected.Narrowed{
		{Kind: affected.NarrowedBy, Narrowing: "method-sets", OffAt: 5, Reason: "go:x#method:T.M changed, which only the wide rule binds to these slices", Reached: 3, Dropped: []string{"a", "b"}},
		{Kind: affected.NarrowedBy, Narrowing: "unreached-new", OffAt: 3, Reason: "what nothing binds in x changed, which only the wide rule binds to these slices", Reached: 3, Dropped: []string{"a", "c"}},
	}

	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}

	for i := range want {
		g, w := got[i], want[i]
		if g.Kind != w.Kind || g.Narrowing != w.Narrowing || g.OffAt != w.OffAt || g.Reason != w.Reason || g.Reached != w.Reached || !slices.Equal(g.Dropped, w.Dropped) {
			t.Errorf("%d: %+v, want %+v", i, g, w)
		}
	}

	if walkNarrowedHook(nil, offAt) != nil {
		t.Fatal("no walk, no hook")
	}
}

func TestALeftOutEntryIsNamedAsTheEntry(t *testing.T) {
	key := func(name string) graph.Key { return graph.Key{Suite: "s", Name: name, Width: "executed"} }

	for _, c := range []struct {
		name string
		path graph.Path
	}{
		{"an sbom", graph.Path{Listed: "t:a", Changed: "t:a", Entry: "texts"}},
		{"a correlator", graph.Path{Changed: "plugin:texts", Steps: []graph.Step{{To: "plugin:texts", Kind: graph.KindPin}}}},
	} {
		sel := &graph.Selection{LeftOut: map[string]map[graph.Key][]graph.Path{
			plugins.NarrowPluginEntry: {key("a"): {c.path}},
		}}

		got := walkNarrowed(sel, func(string) int { return 4 }, "s", "executed", nil, []bazel.Slice{{Name: "a"}})
		if len(got) != 1 || !strings.HasPrefix(got[0].Reason, "the entry of plugin texts changed: each suite it reaches runs its sample") {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}
