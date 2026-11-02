package cli

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	toUnchanged = iota
	toChanged
	toAdded
	toRemoved
	toGroup
	toJunction
	endKinds
)

func movedLinks(r *rand.Rand, pair graphtest.Pair, kinds []string, n int) {
	changed := map[string]bool{}
	for _, id := range pair.Changed {
		changed[id] = true
	}

	var unchanged, digest, added, removed, groups []string

	for id, u := range pair.Base.Units {
		h, ok := pair.Head.Units[id]

		switch {
		case !strings.HasPrefix(id, "syn:"):
		case !ok:
			removed = append(removed, id)
		case h.Digest != u.Digest:
			digest = append(digest, id)
		default:
			unchanged = append(unchanged, id)
			groups = append(groups, graph.GroupPrefix+u.Group)
		}
	}

	for id := range pair.Head.Units {
		if _, ok := pair.Base.Units[id]; !ok && strings.HasPrefix(id, "syn:") {
			added = append(added, id)
		}
	}

	for _, s := range [][]string{unchanged, digest, added, removed, groups} {
		slices.Sort(s)
	}

	pick := func(s []string) string {
		if len(s) == 0 {
			return ""
		}

		return s[r.IntN(len(s))]
	}

	for i := range n {
		from := pick(unchanged)
		if from == "" {
			return
		}

		kind := kinds[r.IntN(len(kinds))]
		inHead := r.IntN(2) == 0

		var to string

		switch i % endKinds {
		case toUnchanged:
			to = pick(unchanged)
		case toChanged:
			to = pick(digest)
		case toAdded:
			to, inHead = pick(added), true
		case toRemoved:
			to, inHead = pick(removed), false
		case toGroup:
			to = pick(groups)
		case toJunction:
			to = graph.NodePrefix + "syn:impl:moved"
			for _, t := range []*deps.Tree{pair.Base, pair.Head} {
				t.Typed = append(t.Typed, plugin.Link{From: to, To: pick(unchanged), Kind: graph.KindFollow})
			}
		}

		if to == "" || to == from {
			continue
		}

		t := pair.Base
		if inHead {
			t = pair.Head
		}

		ln := plugin.Link{From: from, To: to, Kind: kind}
		if kind == graph.KindFollow || kind == graph.KindBind || kind == graph.KindDependentsOnly {
			t.Typed = append(t.Typed, ln)
		} else {
			t.Links = append(t.Links, ln)
		}
	}
}

// A moved link can move an unchanged unit between a slice and its sample-bound target; that is the one
// difference accepted, when sampleMove shows it.
func TestALinkMovedFromAnUnchangedUnitMissesNoSlice(t *testing.T) {
	kinds := []string{"uses", graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}

	for seed := range uint64(60) {
		for _, mode := range []string{"", "sample", "sample-bound"} {
			sample, bound := mode != "", mode == "sample-bound"

			t.Run(fmt.Sprintf("seed%d-%s", seed, mode), func(t *testing.T) {
				p := graphtest.Params{
					Seed: seed, Units: 100, GroupSize: 5, Links: 1, Kinds: kinds, Slices: 8, Listed: 3, Core: seed%2 == 0,
					Changed: 2, Added: 2, Removed: 2, GroupNodes: seed%3 != 0, Depth: int(seed % 3), Shared: int(seed % 3), ChangeShared: seed%2 == 1,
				}
				pair := graphtest.Generate(p)
				movedLinks(rand.New(rand.NewPCG(seed, 11)), pair, kinds, 12) //nolint:gosec // test data

				model := graphtest.Model{UnreachedNew: true}

				var after []graphtest.After
				if sample {
					after = append(after, func(t *deps.Tree, ss []deps.Slice) []deps.Slice { return toSample(t, ss, true, bound) })
				}

				oracle, bound2, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
					return graphtest.AsBinder(tr)(tr, model.Bind(tr, pair.Slices)), nil
				}, pair.Base, pair.Head, after...)
				if err != nil {
					t.Fatal(err)
				}

				opts := graph.Options{On: func(n string) bool {
					return n == "unreached-new" || (sample && n == "sample") || (bound && n == "sample-bound")
				}}

				sel, err := graph.Select(t.Context(), pair.Base, pair.Head, pair.Slices, []string{graph.Executed, graph.Dependents}, opts)
				if err != nil {
					t.Fatal(err)
				}

				c := graphtest.Compare(pair.Base, pair.Head, pair.Slices, oracle, sel.Selected, graphtest.Accept{KeptSources: true})
				line, _, _ := strings.Cut(c.String(), "\n")
				t.Log(line)

				if len(c.BadPaths) > 0 {
					t.Fatalf("the walk gives paths the trees don't have:\n%s", c)
				}

				for k := range c.Missing {
					if why := sampleMove(pair, bound2, k, opts); !bound || why != "" {
						t.Fatalf("the walk misses %s: %s\n%s", k, why, c)
					}
				}
			})
		}
	}
}

func TestAPlainLinkToASampledUnitStillSelects(t *testing.T) {
	tree := func(digest string, links ...plugin.Link) *deps.Tree {
		units := map[string]deps.Unit{
			"u:t": {ID: "u:t", Group: "gt", Digest: digest},
			"u:x": {ID: "u:x", Group: "gx", Digest: "1"},
		}

		return &deps.Tree{Units: units, Links: links, CannotTell: map[string]string{}}
	}

	base, head := tree("1"), tree("2", plugin.Link{From: "u:x", To: "u:t", Kind: "uses"})
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"u:t"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"u:t"}},
	}

	model := graphtest.Model{UnreachedNew: true}
	split := func(t *deps.Tree, ss []deps.Slice) []deps.Slice { return toSample(t, ss, true, false) }

	oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) {
		return graphtest.AsBinder(tr)(tr, model.Bind(tr, ss)), nil
	}, base, head, split)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("oracle: %v", oracle)

	on := func(n string) bool { return n == "unreached-new" || n == "sample" }

	sel, err := graph.Select(t.Context(), base, head, ss, []string{graph.Executed, graph.Dependents}, graph.Options{On: on})
	if err != nil {
		t.Fatal(err)
	}

	if c := graphtest.Compare(base, head, ss, oracle, sel.Selected, graphtest.Accept{}); len(c.Missing) > 0 {
		t.Fatalf("the walk misses slices:\n%s", c)
	}
}

// sampleMove tells why a slice the oracle selects and the walk doesn't is a missed test, or "" when it is none.
func sampleMove(pair graphtest.Pair, bound [2][]deps.Slice, k graphtest.Key, o graph.Options) string {
	changes, err := graph.Changed(pair.Base, pair.Head, pair.Slices, k.Width, o)
	if err != nil {
		return err.Error()
	}

	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.ID] = true
	}

	var d [2]deps.Slice

	partners := [2][]deps.Slice{}
	target := strings.HasPrefix(k.Name, "_sample")

	for i := range bound {
		for _, x := range bound[i] {
			switch {
			case graphtest.KeyOf(x) == k:
				d[i] = x
			case x.Suite == k.Suite && x.Width == k.Width && (target || strings.HasPrefix(x.Name, "_sample")):
				partners[i] = append(partners[i], x)
			}
		}
	}

	if d[0].Everything != d[1].Everything || !slices.Equal(d[0].Groups, d[1].Groups) || !slices.Equal(d[0].Data, d[1].Data) {
		return "its groups, data or everything-mark differ"
	}

	for i := range d {
		for _, id := range d[i].Units {
			if changed[id] {
				return "it binds " + id + ", which changed"
			}

			if slices.Contains(d[1-i].Units, id) {
				continue
			}

			if !slices.ContainsFunc(partners[1-i], func(x deps.Slice) bool { return slices.Contains(x.Units, id) }) {
				return id + " is bound to it in one tree only, and to no sample target in the other"
			}
		}
	}

	return ""
}
