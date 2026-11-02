package cli

import (
	"fmt"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestNarrowedNamesASliceUnderEveryNarrowingWhoseWideRuleSelectsIt(t *testing.T) {
	units := func(digests map[string]string) map[string]deps.Unit {
		out := map[string]deps.Unit{}
		for id, d := range digests {
			out[id] = deps.Unit{ID: id, Digest: d}
		}

		return out
	}

	record := map[string]bookSlice{
		"s/both/executed":      {dropped: map[string][]string{"low": {"go:x#C"}, "high": {"go:x#A"}}},
		"s/only-high/executed": {dropped: map[string][]string{"low": {"go:x#B"}, "high": {"go:x#A"}}},
		"s/none/executed":      {dropped: map[string][]string{"low": {"go:x#B"}}},
	}

	base := &bookTree{units: units(map[string]string{"go:x#A": "1", "go:x#B": "1", "go:x#C": "1"}), slices: record}
	head := &bookTree{units: units(map[string]string{"go:x#A": "2", "go:x#B": "1", "go:x#C": "2"}), slices: record}
	offAt := func(n string) int { return map[string]int{"low": 3, "high": 5}[n] }

	left := []bazel.Slice{{Name: "both"}, {Name: "only-high"}, {Name: "none"}}
	got := namedLeftOut(leftOutBy(base, head, "s", "executed", left), offAt, 1)

	want := []affected.Narrowed{
		{Kind: affected.NarrowedBy, Narrowing: "high", OffAt: 5, Reason: "go:x#A changed, which only the wide rule binds to these slices", Reached: 3, Dropped: []string{"both", "only-high"}},
		{Kind: affected.NarrowedBy, Narrowing: "low", OffAt: 3, Reason: "go:x#C changed, which only the wide rule binds to these slices", Reached: 2, Dropped: []string{"both"}},
	}

	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	for i := range want {
		g, w := got[i], want[i]
		if g.Kind != w.Kind || g.Narrowing != w.Narrowing || g.OffAt != w.OffAt || g.Reason != w.Reason || g.Reached != w.Reached || !slices.Equal(g.Dropped, w.Dropped) {
			t.Errorf("%d: %+v, want %+v", i, g, w)
		}
	}
}

func TestANameRuleAloneNamesNothingWhileUnreachedNewIsOn(t *testing.T) {
	names := func(ids ...string) *unboundBook {
		return newUnboundBook(&deps.Tree{Unbound: map[string]map[string]deps.Unbound{"executed": {"p": {Names: ids}}}}, nil)
	}

	offAt := func(n string) int { return map[string]int{"unreached-new": 3, "referenced-new": 5}[n] }
	left := []bazel.Slice{{Name: "links-p"}}

	for _, c := range []struct {
		inForce []string
		want    string
	}{
		{[]string{"unreached-new", "referenced-new"}, "unreached-new:[links-p]"},
		{[]string{"referenced-new"}, "referenced-new:[links-p]"},
	} {
		groups := map[string][]string{}
		for _, n := range c.inForce {
			groups[n] = []string{"p"}
		}

		record := map[string]bookSlice{"s/links-p/executed": {groups: groups}}
		base := &bookTree{narrowings: c.inForce, slices: record, unbound: names("u:old")}
		head := &bookTree{narrowings: c.inForce, slices: record, unbound: names("u:new", "u:old")}

		var got []string
		for _, n := range namedLeftOut(leftOutBy(base, head, "s", "executed", left), offAt, 0) {
			got = append(got, n.Narrowing+":"+fmt.Sprint(n.Dropped))
		}

		if !slices.Equal(got, []string{c.want}) {
			t.Errorf("in force %v: named %v, want %s", c.inForce, got, c.want)
		}
	}
}

func TestABookEntryLeftOutIsNamedAsTheEntry(t *testing.T) {
	record := map[string]bookSlice{"s/a/executed": {dropped: map[string][]string{plugins.NarrowPluginEntry: {"plugin:texts"}}}}
	base := &bookTree{units: map[string]deps.Unit{"plugin:texts": {ID: "plugin:texts", Digest: "1"}}, slices: record}
	head := &bookTree{units: map[string]deps.Unit{"plugin:texts": {ID: "plugin:texts", Digest: "2"}}, slices: record}

	got := namedLeftOut(leftOutBy(base, head, "s", "executed", []bazel.Slice{{Name: "a"}}), func(string) int { return 4 }, 0)
	if len(got) != 1 || got[0].Reason != "the entry of plugin texts changed: each suite it reaches runs its sample of the recorded slices it reaches instead of all of them, and the nightly runs the rest" {
		t.Errorf("%+v", got)
	}
}
