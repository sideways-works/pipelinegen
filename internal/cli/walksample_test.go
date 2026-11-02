package cli

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func sampledSelection(t *testing.T, b, h *deps.Tree, ss []plugins.BindSlice, bound bool) map[graph.Key]bool {
	t.Helper()

	bind := func(tr *deps.Tree) map[graph.Key]deps.Slice {
		c := *tr
		c.Units = maps.Clone(tr.Units)

		got, err := (&plugins.Set{}).Bind(context.Background(), t.TempDir(), &c, ss)
		if err != nil {
			t.Fatal(err)
		}

		out := map[graph.Key]deps.Slice{}
		for _, d := range toSample(&c, got, true, bound) {
			out[graph.Key{Suite: d.Suite, Name: d.Name, Width: d.Width}] = d
		}

		return out
	}

	bb, hb := bind(b), bind(h)

	changed := func(id string) bool {
		ub, inB := b.Units[id]
		uh, inH := h.Units[id]

		return inB != inH || ub.Digest != uh.Digest
	}

	out := map[graph.Key]bool{}

	for _, k := range slices.Collect(maps.Keys(mergeKeys(bb, hb))) {
		db, dh := bb[k], hb[k]

		sel := !sameUnits(db.Units, dh.Units)

		for _, id := range append(slices.Clone(db.Units), dh.Units...) {
			sel = sel || changed(id)
		}

		if sel {
			out[k] = true
		}
	}

	return out
}

func mergeKeys(a, b map[graph.Key]deps.Slice) map[graph.Key]bool {
	out := map[graph.Key]bool{}
	for k := range a {
		out[k] = true
	}

	for k := range b {
		out[k] = true
	}

	return out
}

func sameUnits(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	sort.Strings(a)
	sort.Strings(b)

	return slices.Equal(a, b)
}

func TestTheWalksSampleIsTodaysSample(t *testing.T) {
	sampledAny := 0

	for seed := range uint64(400) {
		r := rand.New(rand.NewPCG(seed, 19)) //nolint:gosec // a test's repeatable trees
		units := 6 + r.IntN(20)

		id := func(i int) string { return fmt.Sprintf("u:%03d", i) }

		b := &deps.Tree{Units: map[string]deps.Unit{}}
		for i := range units {
			b.Units[id(i)] = deps.Unit{ID: id(i), Group: "g", Digest: "1"}
		}

		for range r.IntN(2 * units) {
			from, to := r.IntN(units), r.IntN(units)
			if from != to {
				b.Links = append(b.Links, plugin.Link{From: id(from), To: id(to), Kind: plugin.LinkUses})
			}
		}

		h := &deps.Tree{Units: maps.Clone(b.Units), Links: b.Links}

		for range 1 + r.IntN(2) {
			u := id(r.IntN(units))
			x := h.Units[u]
			x.Digest = "2"
			h.Units[u] = x
		}

		shared := []string{id(0), id(1)}

		var ss []plugins.BindSlice

		for i := range 2 + r.IntN(4) {
			sl := plugins.BindSlice{Suite: "s", Name: fmt.Sprintf("t%d", i), HasList: true, List: slices.Clone(shared)}
			for range r.IntN(3) {
				sl.List = append(sl.List, id(r.IntN(units)))
			}

			ss = append(ss, sl)
		}

		for _, bound := range []bool{false, true} {
			want := sampledSelection(t, b, h, ss, bound)

			on := func(n string) bool { return n == plugins.NarrowSample || (bound && n == plugins.NarrowSampleBound) }

			got, err := graph.Select(context.Background(), b, h, ss, []string{plugins.Executed}, graph.Options{On: on})
			if err != nil {
				t.Fatal(err)
			}

			// the one class the walk may add: a sample target selected
			// while every slice of its suite is, which costs nothing.
			oracle := map[graphtest.Key]string{}

			for k := range want {
				if k.Width == plugins.Executed {
					oracle[graphtest.Key(k)] = "today"
				}
			}

			c := graphtest.Compare(b, h, ss, oracle, got.Selected, graphtest.Accept{})
			if !c.OK() || len(c.Explained)+len(c.Joined)+len(c.Kept) > 0 {
				t.Fatalf("seed %d, sample-bound %v: the walk differs from today:\n%s\nlinks %v\nhead %v\nslices %+v",
					seed, bound, c, b.Links, h.Units, ss)
			}

			for k := range got.Selected {
				if want[k] && (k.Name == graph.SampleTarget("s") || k.Name == graph.SampleBoundTarget("s")) {
					sampledAny++
				}
			}
		}
	}

	if sampledAny == 0 {
		t.Fatal("nothing went to the sample: the test proves nothing")
	}
}
