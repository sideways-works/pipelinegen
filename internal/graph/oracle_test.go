package graph

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func forwardSelected(t *testing.T, b, h *deps.Tree, ss []plugins.BindSlice) map[Key]bool {
	t.Helper()

	set := &plugins.Set{}

	bind := func(tr *deps.Tree) map[Key]deps.Slice {
		got, err := set.Bind(context.Background(), t.TempDir(), cloneTree(tr), ss)
		if err != nil {
			t.Fatal(err)
		}

		out := map[Key]deps.Slice{}
		for _, d := range got {
			out[Key{Suite: d.Suite, Name: d.Name, Width: d.Width}] = d
		}

		return out
	}

	bb, hb := bind(b), bind(h)

	changed := func(id string) bool {
		ub, inB := b.Units[id]
		uh, inH := h.Units[id]

		return inB != inH || ub.Digest != uh.Digest
	}

	anyChanged := false

	for id := range b.Units {
		anyChanged = anyChanged || changed(id)
	}

	for id := range h.Units {
		anyChanged = anyChanged || changed(id)
	}

	groupChanged := func(g string) bool {
		for _, tr := range []*deps.Tree{b, h} {
			for id, u := range tr.Units {
				if u.Group == g && changed(id) {
					return true
				}
			}
		}

		return false
	}

	out := map[Key]bool{}

	for k, db := range bb {
		dh := hb[k]

		sel := db.Everything != dh.Everything || (db.Everything && anyChanged) ||
			!sameSet(db.Units, dh.Units) || !sameSet(db.Groups, dh.Groups)

		for _, id := range append(slices.Clone(db.Units), dh.Units...) {
			sel = sel || changed(id)
		}

		for _, g := range append(slices.Clone(db.Groups), dh.Groups...) {
			sel = sel || groupChanged(g)
		}

		if sel {
			out[k] = true
		}
	}

	return out
}

func cloneTree(t *deps.Tree) *deps.Tree {
	c := *t
	c.Units = map[string]deps.Unit{}

	maps.Copy(c.Units, t.Units)

	return &c
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)

	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

func randomTrees(r *rand.Rand, units, groups, links int) (*deps.Tree, *deps.Tree) {
	b := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}

	id := func(i int) string { return fmt.Sprintf("u:%03d", i) }

	for i := range units {
		b.Units[id(i)] = deps.Unit{ID: id(i), Group: fmt.Sprintf("g%d", i%groups), Digest: "1"}
	}

	type pair struct{ from, to int }

	have := map[pair]bool{}

	for range links {
		p := pair{r.IntN(units), r.IntN(units)}
		if p.from != p.to {
			have[p] = true
		}
	}

	h := cloneTree(b)
	h.CannotTell = map[string]string{}

	if r.IntN(3) == 0 {
		x := id(r.IntN(units))
		b.CannotTell[x] = "test"
		h.CannotTell[x] = "test"
	}

	headLinks := map[pair]bool{}
	for p := range have {
		headLinks[p] = true
	}

	for range 1 + r.IntN(3) {
		u := r.IntN(units)

		switch r.IntN(4) {
		case 0:
			delete(h.Units, id(u))
		case 1:
			nu := units + r.IntN(5)
			h.Units[id(nu)] = deps.Unit{ID: id(nu), Group: "g0", Digest: "1"}
		default:
			x := h.Units[id(u)]
			x.Digest = "2"
			h.Units[id(u)] = x

			for p := range headLinks {
				if p.from == u && r.IntN(2) == 0 {
					delete(headLinks, p)
				}
			}

			if r.IntN(2) == 0 {
				headLinks[pair{u, r.IntN(units)}] = true
			}
		}
	}

	toLinks := func(m map[pair]bool) []plugin.Link {
		var out []plugin.Link

		for p := range m {
			if p.from != p.to {
				out = append(out, plugin.Link{From: id(p.from), To: id(p.to), Kind: plugin.LinkUses})
			}
		}

		sort.Slice(out, func(i, j int) bool {
			if out[i].From != out[j].From {
				return out[i].From < out[j].From
			}

			return out[i].To < out[j].To
		})

		return out
	}

	b.Links, h.Links = toLinks(have), toLinks(headLinks)

	return b, h
}

func randomSlices(r *rand.Rand, units, n int) []plugins.BindSlice {
	out := make([]plugins.BindSlice, 0, n)

	for i := range n {
		sl := plugins.BindSlice{Suite: "s", Name: fmt.Sprintf("t%02d", i), HasList: r.IntN(8) != 0}

		if sl.HasList {
			for range 1 + r.IntN(4) {
				sl.List = append(sl.List, fmt.Sprintf("u:%03d", r.IntN(units+3)))
			}
		}

		out = append(out, sl)
	}

	return out
}

func TestTheWalkSelectsWhatTheForwardClosureSelects(t *testing.T) {
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 7)) //nolint:gosec // a test's repeatable trees
		units := 5 + r.IntN(40)
		b, h := randomTrees(r, units, 1+r.IntN(6), r.IntN(3*units))
		ss := randomSlices(r, units, 1+r.IntN(8))

		want := forwardSelected(t, b, h, ss)

		got, err := Select(context.Background(), b, h, ss, []string{Executed, Dependents}, Options{})
		if err != nil {
			t.Fatal(err)
		}

		for _, k := range allKeys(ss) {
			if _, sel := got.Selected[k]; sel != want[k] {
				t.Fatalf("seed %d: %v selected = %v, forward closure %v\nbase %v\nhead %v\nslices %+v",
					seed, k, sel, want[k], b.Links, h.Links, ss)
			}
		}
	}
}

func allKeys(ss []plugins.BindSlice) []Key {
	var out []Key

	for _, sl := range ss {
		for _, w := range []string{Executed, Dependents} {
			out = append(out, Key{Suite: sl.Suite, Name: sl.Name, Width: w})
		}
	}

	return out
}
