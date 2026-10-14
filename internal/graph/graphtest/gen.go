package graphtest

import (
	"cmp"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Params shape a pair of synthetic trees and the slices planned on them.
type Params struct {
	Seed uint64
	// Units in the base tree, in groups of GroupSize (10 when 0).
	Units, GroupSize         int
	Links                    int
	Kinds                    []string
	Refs                     bool
	GroupNodes               bool
	Junctions                int
	Slices, Listed, Unlisted int
	Changed, Added, Removed  int
	CannotTell               int
	Narrowings               []string
	RuledOut                 int
	OwnFiles                 int
	Depth                    int
	Core                     bool
	Shared                   int
	ChangeShared             bool
	UntoldCorrelator         string
	Plugins                  bool
	PinChanged               string
	CorrelatorEnds           int
}

// The plugins a generated pair names.
const (
	SBOMName       = "syn-sbom"
	CorrelatorName = "syn-correlator"
)

// Pair is a generated base and head with the slices planned on both.
type Pair struct {
	Base, Head *deps.Tree
	Slices     []plugins.BindSlice
	Changed    []string
}

// Generate makes a pair of trees from p. Equal parameters make equal
// pairs.
func Generate(p Params) Pair {
	g := newGen(p)
	g.units()
	g.cannotTell()
	g.narrowings()

	if p.UntoldCorrelator != "" {
		g.head.Untold = map[string][]string{p.UntoldCorrelator: {"generated.txt"}}
	}

	linkedAbsent(g.base)
	linkedAbsent(g.head)

	if p.Plugins {
		g.pins()
		g.correlatorEnds()
	}

	pair := Pair{Base: g.base, Head: g.head, Slices: g.slices()}

	for k := range min(p.OwnFiles, len(pair.Slices)-p.Unlisted) {
		i := p.Unlisted + k
		src := fmt.Sprintf("syn/s%04d_test.sh", i)
		pair.Slices[i].Sources = append(pair.Slices[i].Sources, src)

		id := deps.FileID(src)
		for _, t := range []*deps.Tree{g.base, g.head} {
			t.Units[id] = deps.Unit{ID: id, Group: src, File: src, Digest: "content"}
		}

		if k == 0 {
			g.head.Units[id] = deps.Unit{ID: id, Group: src, File: src, Digest: "content, changed"}
			pair.Changed = append(pair.Changed, id)
		}
	}

	for n := range g.total {
		if !g.inHead(n) || n >= p.Units || g.changed[n] {
			pair.Changed = append(pair.Changed, g.id(n))
		}
	}

	return pair
}

type gen struct {
	p          Params
	r          *rand.Rand
	size       int
	kinds      []string
	total      int
	removed    map[int]bool
	changed    map[int]bool
	base, head *deps.Tree
}

func newGen(p Params) *gen {
	g := &gen{
		p: p, r: rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15)), //nolint:gosec // test data
		size: cmp.Or(p.GroupSize, 10), kinds: p.Kinds, total: p.Units + p.Added,
		removed: map[int]bool{}, changed: map[int]bool{}, base: newTree("base"), head: newTree("head"),
	}

	if len(g.kinds) == 0 {
		g.kinds = []string{plugin.LinkUses}
	}

	for len(g.removed) < min(p.Removed, p.Units) {
		g.removed[g.r.IntN(p.Units)] = true
	}

	if p.ChangeShared && p.Shared > 0 && !g.removed[0] {
		g.changed[0] = true
	}

	for want := min(p.Changed+len(g.changed), p.Units-len(g.removed)); len(g.changed) < want; {
		if n := g.r.IntN(p.Units); !g.removed[n] {
			g.changed[n] = true
		}
	}

	return g
}

func (g *gen) id(n int) string    { return fmt.Sprintf("syn:g%05d#u%07d", n/g.size, n) }
func (g *gen) group(n int) string { return fmt.Sprintf("g%05d", n/g.size) }
func (g *gen) inHead(n int) bool  { return n >= g.p.Units || !g.removed[n] }
func (g *gen) kind() string       { return g.kinds[g.r.IntN(len(g.kinds))] }

// out are some links from unit n, mostly to units nearby: code mostly uses
// what sits close to it, so a slice binds part of the tree.
func (g *gen) out(n int) []plugin.Link {
	ls := make([]plugin.Link, 0, 2*g.p.Links+1)

	for range g.r.IntN(2*g.p.Links + 1) {
		to := g.r.IntN(g.total)
		if g.r.IntN(10) > 0 {
			to = min(max(n+g.r.IntN(4*g.size+1)-2*g.size, 0), g.total-1)
		}

		l := plugin.Link{From: g.id(n), To: g.id(to), Kind: g.kind()}
		if g.p.Refs {
			l.Ref = g.r.IntN(3) == 0
		}

		ls = append(ls, l)
	}

	return ls
}

func (g *gen) units() {
	for n := range g.total {
		u := deps.Unit{ID: g.id(n), Group: g.group(n), Digest: fmt.Sprintf("d%d", n)}
		links := g.out(n)

		if n < g.p.Units {
			g.base.Units[u.ID] = u
			g.addLinks(g.base, links)
		}

		if !g.inHead(n) {
			continue
		}

		if g.changed[n] {
			u.Digest += "'"
			links = slices.DeleteFunc(links, func(plugin.Link) bool { return g.r.IntN(3) == 0 })
			links = append(links, g.out(n)...)
		}

		g.head.Units[u.ID] = u
		g.addLinks(g.head, links)
	}

	g.nodes()
}

var typedKinds = []string{graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}

func (g *gen) addLinks(t *deps.Tree, links []plugin.Link) {
	for _, l := range links {
		if slices.Contains(typedKinds, l.Kind) {
			t.Typed = append(t.Typed, l)
		} else {
			t.Links = append(t.Links, l)
		}
	}
}

func (g *gen) nodes() {
	groups := (g.total + g.size - 1) / g.size
	junctions := make([]string, g.p.Junctions)

	for j := range junctions {
		junctions[j] = fmt.Sprintf("%ssyn:impl:%d", graph.NodePrefix, j)
	}

	var shared []plugin.Link

	if g.p.GroupNodes {
		for k := range groups {
			node := graph.GroupPrefix + fmt.Sprintf("g%05d", k)
			shared = append(shared, plugin.Link{From: node, To: g.id(k * g.size), Kind: graph.KindFollow})

			// a package imports a few of those before it, so the imports
			// make no chain through the whole tree
			if k > 0 && g.r.IntN(3) == 0 {
				shared = append(shared, plugin.Link{From: node, To: graph.GroupPrefix + fmt.Sprintf("g%05d", g.r.IntN(k)), Kind: graph.KindFollow})
			}
		}
	}

	for _, j := range junctions {
		for range 3 {
			shared = append(shared, plugin.Link{From: j, To: g.id(g.r.IntN(g.total)), Kind: graph.KindDependentsOnly})
		}
	}

	for n := range g.total {
		var own []plugin.Link

		if g.p.GroupNodes {
			own = append(own, plugin.Link{From: g.id(n), To: graph.GroupPrefix + g.group(n), Kind: graph.KindFollow})
		}

		if len(junctions) > 0 && g.r.IntN(4) == 0 {
			own = append(own, plugin.Link{From: g.id(n), To: junctions[g.r.IntN(len(junctions))], Kind: graph.KindDependentsOnly})
		}

		for _, t := range []*deps.Tree{g.base, g.head} {
			if _, ok := t.Units[g.id(n)]; ok {
				t.Typed = append(t.Typed, own...)
			}
		}
	}

	for _, t := range []*deps.Tree{g.base, g.head} {
		for _, l := range shared {
			if _, ok := t.Units[l.To]; ok || strings.HasPrefix(l.To, graph.GroupPrefix) {
				t.Typed = append(t.Typed, l)
			}
		}
	}
}

func (g *gen) pins() {
	for _, t := range []*deps.Tree{g.base, g.head} {
		t.Pins, t.Providers = map[string]string{}, map[string]string{}

		for id := range t.Units {
			t.Providers[id] = SBOMName
		}

		for _, name := range []string{SBOMName, CorrelatorName} {
			id := plugins.PinID(name)
			t.Pins[name] = id

			digest := "v1"
			if t == g.head && name == g.p.PinChanged {
				digest = "v2"
			}

			t.Units[id] = deps.Unit{ID: id, Digest: digest}
		}
	}
}

func (g *gen) correlatorEnds() {
	ends := map[string]bool{}

	for range g.p.CorrelatorEnds {
		ends[g.id(g.r.IntN(g.total))] = true
	}

	for _, t := range []*deps.Tree{g.base, g.head} {
		var in []string

		for id := range ends {
			if _, ok := t.Units[id]; ok {
				in = append(in, id)
			}
		}

		slices.Sort(in)
		t.CorrelatorEnds = map[string][]string{CorrelatorName: in}
	}
}

func (g *gen) cannotTell() {
	for range g.p.CannotTell {
		id := g.id(g.r.IntN(g.total))

		for _, t := range []*deps.Tree{g.base, g.head} {
			if _, ok := t.Units[id]; ok {
				t.CannotTell[id] = "generated"
			}
		}
	}
}

func (g *gen) narrowings() {
	for _, name := range g.p.Narrowings {
		changed := slices.Sorted(maps.Keys(g.changed))
		ruled := make([]plugin.Link, 0, g.p.RuledOut)

		for range g.p.RuledOut {
			// half of them lead to a changed unit, so what a narrowing
			// leaves out shows
			to := g.r.IntN(g.p.Units)
			if len(changed) > 0 && g.r.IntN(2) == 0 {
				to = changed[g.r.IntN(len(changed))]
			}

			ruled = append(ruled, plugin.Link{From: g.id(g.r.IntN(g.p.Units)), To: g.id(to), Kind: g.kind()})
		}

		var plain, typed []plugin.Link

		for _, l := range ruled {
			if slices.Contains(typedKinds, l.Kind) {
				typed = append(typed, l)
			} else {
				plain = append(plain, l)
			}
		}

		for _, t := range []*deps.Tree{g.base, g.head} {
			t.Narrowings = append(t.Narrowings, name)
			t.RuledOut[name] = plain
			t.TypedRuledOut[name] = typed
		}
	}
}

func (g *gen) slices() []plugins.BindSlice {
	out := make([]plugins.BindSlice, 0, g.p.Slices)

	for i := range g.p.Slices {
		sl := plugins.BindSlice{Suite: "syn", Name: fmt.Sprintf("s%04d_test", i)}

		if i >= g.p.Unlisted {
			sl.HasList = true
			for range g.p.Listed {
				sl.List = append(sl.List, g.id(g.r.IntN(g.p.Units)))
			}

			if g.p.Core {
				sl.List = append(sl.List, g.id(g.r.IntN(min(g.size, g.p.Units))))
			}

			for n := range min(g.p.Shared, g.p.Units) {
				sl.List = append(sl.List, g.id(n))
			}
		}

		if g.p.Depth > 0 {
			sl.Depth = g.r.IntN(g.p.Depth + 1)
		}

		out = append(out, sl)
	}

	return out
}

func newTree(commit string) *deps.Tree {
	return &deps.Tree{
		Commit: commit, Units: map[string]deps.Unit{}, CannotTell: map[string]string{},
		FileUnits: map[string][]string{}, RuledOut: map[string][]plugin.Link{}, TypedRuledOut: map[string][]plugin.Link{},
	}
}

func linkedAbsent(t *deps.Tree) {
	for _, l := range slices.Concat(t.Links, t.Typed) {
		_, ok := t.Units[l.To]
		if ok || strings.HasPrefix(l.To, "file:") || strings.HasPrefix(l.To, graph.GroupPrefix) || strings.HasPrefix(l.To, graph.NodePrefix) {
			continue
		}

		if _, unit := t.Units[l.From]; unit {
			t.CannotTell[l.From] = "links to " + l.To + ", which no plugin describes"
		}
	}

	order := func(a, b plugin.Link) int {
		return strings.Compare(a.From+" "+a.To+" "+a.Kind, b.From+" "+b.To+" "+b.Kind)
	}
	slices.SortFunc(t.Links, order)
	slices.SortFunc(t.Typed, order)
}
