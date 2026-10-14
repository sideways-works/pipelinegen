package graph

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func specBound(t *deps.Tree, roots []string, dependents bool, depth int) map[string]bool {
	type edge struct {
		to   string
		kind kind
	}

	out, in := map[string][]edge{}, map[string][]string{}

	for _, ln := range t.Links {
		out[ln.From] = append(out[ln.From], edge{ln.To, plain})
		in[ln.To] = append(in[ln.To], ln.From)
	}

	for _, ln := range t.Typed {
		out[ln.From] = append(out[ln.From], edge{ln.To, typedKind(ln.Kind)})
	}

	state := map[string]int{} // 1 bound, 2 expanded
	queue := []string{}

	reach := func(id string, s int) {
		if state[id] < s {
			state[id] = s
			queue = append(queue, id)
		}
	}

	for _, r := range roots {
		reach(r, 2)
	}

	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]

		for _, e := range out[x] {
			switch {
			case e.kind == plain:
				reach(e.to, 2)
			case e.kind == follow && state[x] == 2:
				reach(e.to, 2)
			case e.kind == bind && state[x] == 2:
				reach(e.to, 1)
			}
		}
	}

	bound := map[string]bool{}
	for id := range state {
		bound[id] = true
	}

	if dependents {
		// fewest steps, by relaxing until nothing moves: small trees
		cost := map[string]int{}
		for id := range bound {
			cost[id] = 0
		}

		for moved := true; moved; {
			moved = false

			relax := func(id string, c int) {
				if old, ok := cost[id]; !ok || c < old {
					cost[id] = c
					moved = true
				}
			}

			for x, c := range cost {
				for _, e := range out[x] {
					step := 1
					if e.kind == plain || strings.HasPrefix(e.to, NodePrefix) {
						step = 0
					}

					relax(e.to, c+step)
				}

				for _, from := range in[x] {
					relax(from, c+1)
				}
			}
		}

		for id, c := range cost {
			if depth == 0 || c <= depth {
				bound[id] = true
			}
		}
	}

	for id := range bound {
		if _, wide := t.CannotTell[id]; wide {
			g := t.Units[id].Group
			for uid, u := range t.Units {
				if u.Group == g {
					bound[uid] = true
				}
			}
		}
	}

	units := map[string]bool{}

	for id := range bound {
		if _, ok := t.Units[id]; ok {
			units[id] = true
		}
	}

	return units
}

func specSelected(b, h *deps.Tree, ss []plugins.BindSlice, width string) map[Key]bool {
	out := map[Key]bool{}

	for _, sl := range ss {
		bb := specBound(b, sl.List, width == Dependents, sl.Depth)
		hb := specBound(h, sl.List, width == Dependents, sl.Depth)

		sel := len(bb) != len(hb)

		for id := range bb {
			ub, uh := b.Units[id], h.Units[id]
			_, inH := h.Units[id]
			sel = sel || !hb[id] || !inH || ub.Digest != uh.Digest
		}

		if sel {
			out[Key{Suite: sl.Suite, Name: sl.Name, Width: width}] = true
		}
	}

	return out
}

func randomTypedTrees(r *rand.Rand, units int) (*deps.Tree, *deps.Tree) {
	id := func(i int) string {
		if i >= units {
			return fmt.Sprintf("node:x:%d", i-units)
		}

		return fmt.Sprintf("u:%03d", i)
	}

	b := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}
	for i := range units {
		b.Units[id(i)] = deps.Unit{ID: id(i), Group: fmt.Sprintf("g%d", i%3), Digest: "1"}
	}

	kinds := []string{"", KindFollow, KindBind, KindDependentsOnly}

	type link struct {
		from, to int
		kind     string
	}

	nodes := units + 2
	all := map[link]bool{}

	for range r.IntN(3 * units) {
		ln := link{r.IntN(nodes), r.IntN(nodes), kinds[r.IntN(len(kinds))]}
		if ln.from != ln.to {
			all[ln] = true
		}
	}

	h := cloneTree(b)
	h.CannotTell = map[string]string{}

	if r.IntN(4) == 0 {
		x := id(r.IntN(units))
		b.CannotTell[x], h.CannotTell[x] = "test", "test"
	}

	headLinks := map[link]bool{}
	for ln := range all {
		headLinks[ln] = true
	}

	for range 1 + r.IntN(2) {
		u := r.IntN(units)
		x := h.Units[id(u)]
		x.Digest = "2"
		h.Units[id(u)] = x

		for ln := range headLinks {
			if ln.from == u && r.IntN(2) == 0 {
				delete(headLinks, ln)
			}
		}

		if r.IntN(2) == 0 {
			headLinks[link{u, r.IntN(nodes), kinds[r.IntN(len(kinds))]}] = true
		}
	}

	set := func(t *deps.Tree, m map[link]bool) {
		for ln := range m {
			pl := plugin.Link{From: id(ln.from), To: id(ln.to), Kind: ln.kind}
			if ln.kind == "" {
				pl.Kind = plugin.LinkUses
				t.Links = append(t.Links, pl)
			} else {
				t.Typed = append(t.Typed, pl)
			}
		}

		for _, ls := range [][]plugin.Link{t.Links, t.Typed} {
			sort.Slice(ls, func(i, j int) bool {
				if ls[i].From != ls[j].From {
					return ls[i].From < ls[j].From
				}

				return ls[i].To < ls[j].To
			})
		}
	}

	set(b, all)
	set(h, headLinks)

	return b, h
}

func TestTheWalkKeepsTheContractsReachRules(t *testing.T) {
	selected := 0

	for seed := range uint64(1500) {
		r := rand.New(rand.NewPCG(seed, 11)) //nolint:gosec // a test's repeatable trees
		units := 4 + r.IntN(25)
		b, h := randomTypedTrees(r, units)

		var ss []plugins.BindSlice

		for i := range 1 + r.IntN(6) {
			sl := plugins.BindSlice{Suite: "s", Name: fmt.Sprintf("t%d", i), HasList: true, Depth: r.IntN(4)}
			for range 1 + r.IntN(3) {
				sl.List = append(sl.List, fmt.Sprintf("u:%03d", r.IntN(units)))
			}

			ss = append(ss, sl)
		}

		got, err := Select(context.Background(), b, h, ss, []string{Executed, Dependents}, Options{})
		if err != nil {
			t.Fatal(err)
		}

		for _, width := range []string{Executed, Dependents} {
			want := specSelected(b, h, ss, width)

			for _, sl := range ss {
				k := Key{Suite: sl.Suite, Name: sl.Name, Width: width}
				if _, sel := got.Selected[k]; sel != want[k] {
					t.Fatalf("seed %d: %v (depth %d) selected = %v, the rules say %v\nbase %v %v\nhead %v %v\nlist %v",
						seed, k, sl.Depth, sel, want[k], b.Links, b.Typed, h.Links, h.Typed, sl.List)
				}

				if want[k] {
					selected++

					checkPaths(t, seed, b, h, sl, got.Selected[k])
				}
			}
		}
	}

	if selected == 0 {
		t.Fatal("no tree selected anything: the test proves nothing")
	}
}

func checkPaths(t *testing.T, seed uint64, b, h *deps.Tree, sl plugins.BindSlice, paths []Path) {
	t.Helper()

	if len(paths) == 0 {
		t.Fatalf("seed %d: %s selected without a path", seed, sl.Name)
	}

	has := func(st Step) bool {
		for _, tr := range []*deps.Tree{b, h} {
			for _, ls := range [][]plugin.Link{tr.Links, tr.Typed} {
				for _, ln := range ls {
					if ln.From == st.From && ln.To == st.To && (ln.Kind == st.Kind || st.Kind == "plain") {
						return true
					}
				}
			}
		}

		return st.Kind == KindCannotTell
	}

	changed, err := Changed(b, h, nil, Executed, Options{})
	if err != nil {
		t.Fatal(err)
	}

	isChanged := map[string]bool{}
	for _, c := range changed {
		isChanged[c.ID] = true
	}

	for _, p := range paths {
		at := p.Listed
		if !slices.Contains(sl.List, at) {
			t.Fatalf("seed %d: %s's path starts at %s, no root of it", seed, sl.Name, at)
		}

		for _, st := range p.Steps {
			if !has(st) {
				t.Fatalf("seed %d: %s's path takes %+v, which no tree has", seed, sl.Name, st)
			}

			switch {
			case !st.Back && st.From == at:
				at = st.To
			case st.Back && st.To == at:
				at = st.From
			default:
				t.Fatalf("seed %d: %s's path breaks at %+v (at %s): %+v", seed, sl.Name, st, at, p)
			}
		}

		if at != p.Changed || !isChanged[at] {
			t.Fatalf("seed %d: %s's path ends at %s, changed %s: %+v", seed, sl.Name, at, p.Changed, p)
		}
	}
}
