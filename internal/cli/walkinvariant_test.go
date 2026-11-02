package cli

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

var walkOffAt = map[string]int{"loose": 2, "unreached-new": 3, "method-sets": 4, plugins.NarrowPluginEntry: 4, "referenced-new": 5}

type walkSpec struct {
	units      map[string]deps.Unit
	plain      []plugin.Link
	typed      []plugin.Link
	wide       map[string][]plugin.Link
	ends       []string
	unobserved bool
}

const walkCorrelator = "corr"

func (s walkSpec) at(on func(string) bool) *deps.Tree {
	t := &deps.Tree{
		Units: maps.Clone(s.units), Links: slices.Clone(s.plain), Typed: slices.Clone(s.typed),
		CannotTell: map[string]string{}, Providers: map[string]string{}, RuledOut: map[string][]plugin.Link{},
		CorrelatorEnds: map[string][]string{walkCorrelator: s.ends},
	}

	for _, n := range sortedKeys(s.wide) {
		if on(n) {
			t.RuledOut[n] = s.wide[n]
		} else {
			t.Links = append(t.Links, s.wide[n]...)
		}
	}

	return t
}

func inForce(level int, off string) func(string) bool {
	return func(n string) bool { return n != off && level < walkOffAt[n] }
}

type walkPlan struct {
	selected map[string]bool
	narrowed []affected.Narrowed
	book     []affected.Narrowed
}

func walkAt(t *testing.T, b, h walkSpec, ss []plugins.BindSlice, width string, level int, off string) walkPlan {
	t.Helper()

	on := inForce(level, off)

	observes := func(string, string) bool { return !b.unobserved }

	sel, err := graph.Select(t.Context(), b.at(on), h.at(on), ss, []string{width}, graph.Options{On: on, Observes: observes})
	if err != nil {
		t.Fatal(err)
	}

	p := walkPlan{selected: map[string]bool{}}

	var selected, left []bazel.Slice

	for _, sl := range ss {
		if _, ok := sel.Selected[graph.Key{Suite: sl.Suite, Name: sl.Name, Width: width}]; ok {
			p.selected[sl.Name] = true
			selected = append(selected, bazel.Slice{Name: sl.Name})
		} else {
			left = append(left, bazel.Slice{Name: sl.Name})
		}
	}

	p.narrowed = walkNarrowed(sel, func(n string) int { return walkOffAt[n] }, "s", width, selected, left)
	p.book = bookNarrowed(t, b.at(on), h.at(on), ss, width, selected, left)

	return p
}

func bookNarrowed(t *testing.T, b, h *deps.Tree, ss []plugins.BindSlice, width string, selected, left []bazel.Slice) []affected.Narrowed {
	t.Helper()

	book := newPlanBook(nil)

	for commit, tr := range map[string]*deps.Tree{"base": b, "head": h} {
		bound, err := (&plugins.Set{}).Bind(t.Context(), t.TempDir(), tr, ss)
		if err != nil {
			t.Fatal(err)
		}

		book.keep(commit, tr, bound)
	}

	return namedLeftOut(leftOutBy(book.trees["base"], book.trees["head"], "s", width, left), func(n string) int { return walkOffAt[n] }, len(selected))
}

func (p walkPlan) named(level int) map[string]bool {
	out := map[string]bool{}

	for _, n := range p.narrowed {
		if n.OffAt <= level {
			for _, name := range n.Dropped {
				out[name] = true
			}
		}
	}

	return out
}

// A slice that runs only with two narrowings off together is named under neither, and is returned.
func checkWalkLadder(t *testing.T, what string, b, h walkSpec, ss []plugins.BindSlice) (named, pairOnly []string) {
	t.Helper()

	for _, width := range []string{graph.Executed, graph.Dependents} {
		plans := map[int]walkPlan{}
		for level := 1; level <= 5; level++ {
			plans[level] = walkAt(t, b, h, ss, width, level, "")
		}

		for low := 1; low <= 5; low++ {
			p := plans[low]

			for _, n := range p.narrowed {
				if n.Reached != len(p.selected)+len(n.Dropped) || len(slices.Compact(slices.Sorted(slices.Values(n.Dropped)))) != len(n.Dropped) {
					t.Fatalf("%s, %s, level %d: %s's count %d for %v with %d selected", what, width, low, n.Narrowing, n.Reached, n.Dropped, len(p.selected))
				}

				for _, name := range n.Dropped {
					if p.selected[name] {
						t.Fatalf("%s, %s, level %d: %s names %s, which the level selects", what, width, low, n.Narrowing, name)
					}
				}
			}

			for _, n := range sortedKeys(walkOffAt) {
				if !inForce(low, "")(n) {
					continue
				}

				alone := walkAt(t, b, h, ss, width, low, n)

				var want, got []string

				for _, sl := range ss {
					if alone.selected[sl.Name] && !p.selected[sl.Name] {
						want = append(want, sl.Name)
					}
				}

				for _, e := range p.narrowed {
					if e.Narrowing == n {
						got = append(got, e.Dropped...)
					}
				}

				slices.Sort(got)

				if !slices.Equal(got, want) {
					t.Fatalf("%s, %s, level %d: %s names %v, its plan alone off adds %v", what, width, low, n, got, want)
				}

				named = append(named, got...)

				// Binding forward names a link narrowing's slices as the walk does only at the executed width while
				// unreached-new is on; other tests hold the book's name rules.
				if _, link := b.wide[n]; link && width == graph.Executed && inForce(low, "")("unreached-new") {
					var book []string

					for _, e := range p.book {
						if e.Narrowing == n {
							book = append(book, e.Dropped...)
						}
					}

					slices.Sort(book)

					if !slices.Equal(book, want) {
						t.Fatalf("%s, %s, level %d: binding forward names %v under %s, the walk %v", what, width, low, book, n, want)
					}
				}
			}

			for high := low + 1; high <= 5; high++ {
				byLadder := p.named(high)

				for _, sl := range ss {
					if !plans[high].selected[sl.Name] || p.selected[sl.Name] || byLadder[sl.Name] {
						continue
					}

					pairOnly = append(pairOnly, fmt.Sprintf("%s %s %d>%d", sl.Name, width, low, high))
				}
			}
		}
	}

	return named, pairOnly
}

func TestTheWalksNarrowedRecordHoldsOnTheLadder(t *testing.T) {
	named, pairs, entry := 0, 0, 0

	for seed := range uint64(150) {
		r := rand.New(rand.NewPCG(seed, 29)) //nolint:gosec // a test's repeatable trees
		b, h, ss := randomWalkTrees(r)

		n, p := checkWalkLadder(t, fmt.Sprintf("seed %d", seed), b, h, ss)
		named, pairs = named+len(n), pairs+len(p)

		for _, e := range walkAt(t, b, h, ss, graph.Executed, 1, "").narrowed {
			if e.Narrowing == plugins.NarrowPluginEntry {
				entry += len(e.Dropped)
			}
		}
	}

	if named == 0 || entry == 0 {
		t.Fatalf("%d slices named, %d of them under plugin-entry: the test proves nothing", named, entry)
	}

	t.Logf("%d slices named, %d under plugin-entry at level 1, %d run only with two narrowings off", named, entry, pairs)
}

func randomWalkTrees(r *rand.Rand) (walkSpec, walkSpec, []plugins.BindSlice) {
	units := 4 + r.IntN(14)
	id := func(i int) string { return fmt.Sprintf("u:%03d", i) }
	link := func() (string, string, bool) {
		from, to := r.IntN(units), r.IntN(units)

		return id(from), id(to), from != to
	}

	b := walkSpec{units: map[string]deps.Unit{}, wide: map[string][]plugin.Link{}}

	for i := range units {
		g := fmt.Sprintf("g%d", i%3)
		b.units[id(i)] = deps.Unit{ID: id(i), Group: g, Digest: "1"}
		b.typed = append(b.typed, plugin.Link{From: id(i), To: graph.GroupPrefix + g, Kind: graph.KindFollow})
	}

	for range r.IntN(units) {
		if from, to, ok := link(); ok {
			if r.IntN(3) == 0 {
				b.typed = append(b.typed, plugin.Link{From: from, To: to, Kind: graph.KindDependentsOnly, Ref: true})
			} else {
				b.plain = append(b.plain, plugin.Link{From: from, To: to, Kind: plugin.LinkUses})
			}
		}
	}

	for _, n := range []string{"loose", "method-sets"} {
		for range r.IntN(units / 2) {
			if from, to, ok := link(); ok {
				b.wide[n] = append(b.wide[n], plugin.Link{From: from, To: to, Kind: plugin.LinkUses, Narrowing: n})
			}
		}
	}

	h := walkSpec{units: maps.Clone(b.units), plain: slices.Clone(b.plain), typed: slices.Clone(b.typed), wide: b.wide}

	for range 1 + r.IntN(2) {
		u := h.units[id(r.IntN(units))]
		u.Digest = "2"
		h.units[u.ID] = u
	}

	if r.IntN(2) == 0 {
		added := id(units)
		g := fmt.Sprintf("g%d", r.IntN(3))
		h.units[added] = deps.Unit{ID: added, Group: g, Digest: "1"}
		h.typed = append(h.typed, plugin.Link{From: added, To: graph.GroupPrefix + g, Kind: graph.KindFollow})

		if r.IntN(2) == 0 {
			h.typed = append(h.typed, plugin.Link{From: id(r.IntN(units)), To: added, Kind: graph.KindDependentsOnly, Ref: true})
		}
	}

	var ss []plugins.BindSlice

	for i := range 1 + r.IntN(5) {
		sl := plugins.BindSlice{Suite: "s", Name: fmt.Sprintf("t%d", i), HasList: true}
		for range 1 + r.IntN(2) {
			sl.List = append(sl.List, id(r.IntN(units)))
		}

		ss = append(ss, sl)
	}

	if r.IntN(3) == 0 {
		pin := plugins.PinID(walkCorrelator)
		b.units[pin] = deps.Unit{ID: pin, Digest: "1"}
		h.units[pin] = deps.Unit{ID: pin, Digest: "2"}
		b.unobserved = r.IntN(2) == 0

		for range 1 + r.IntN(2) {
			b.ends = append(b.ends, id(r.IntN(units)))
		}

		h.ends, h.unobserved = b.ends, b.unobserved
	}

	return b, h, ss
}

func TestTheWalkNamesNoNarrowingForASliceOnlyTwoSelect(t *testing.T) {
	spec := func(head bool) walkSpec {
		s := walkSpec{units: map[string]deps.Unit{}, wide: map[string][]plugin.Link{}}

		ids := []string{"u:f", "u:g"}
		if head {
			ids = append(ids, "u:helper")
			s.typed = []plugin.Link{{From: "u:g", To: "u:helper", Kind: graph.KindDependentsOnly, Ref: true}}
		}

		for _, id := range ids {
			s.units[id] = deps.Unit{ID: id, Group: "p", Digest: "1"}
			if head && id == "u:g" {
				s.units[id] = deps.Unit{ID: id, Group: "p", Digest: "2"}
			}

			s.typed = append(s.typed, plugin.Link{From: id, To: graph.GroupPrefix + "p", Kind: graph.KindFollow})
		}

		return s
	}

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "checkout", HasList: true, List: []string{"u:f"}},
		{Suite: "s", Name: "orders", HasList: true, List: []string{"u:g"}},
	}

	b, h := spec(false), spec(true)

	for level := 1; level <= 5; level++ {
		p := walkAt(t, b, h, ss, graph.Executed, level, "")
		if !p.selected["orders"] || p.selected["checkout"] != (level == 5) {
			t.Fatalf("level %d selects %v, want orders, and checkout at 5 only", level, p.selected)
		}

		var named []string
		for _, n := range p.narrowed {
			named = append(named, n.Narrowing+":"+fmt.Sprint(n.Dropped))
		}

		want := []string(nil)
		if level == 3 || level == 4 {
			want = []string{"referenced-new:[checkout]"}
		}

		if !slices.Equal(named, want) {
			t.Errorf("level %d names %v, want %v", level, named, want)
		}
	}

	if _, pairs := checkWalkLadder(t, "two name rules", b, h, ss); !slices.Equal(pairs, []string{"checkout executed 1>5", "checkout executed 2>5"}) {
		t.Errorf("the ladder check finds %v run only with two narrowings off, want checkout from levels 1 and 2", pairs)
	}
}
