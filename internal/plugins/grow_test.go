package plugins

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAddingToASliceKeepsTheOrderOfAScan(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test data

	for range 200 {
		var got, want []string

		ix := unitIndex{}

		for range r.IntN(300) {
			v := strconv.Itoa(r.IntN(60))

			switch r.IntN(10) {
			case 0:
				got = append(got, "x"+v)
				want = append(want, "x"+v)
			case 1:
				var more []string
				for range r.IntN(40) {
					more = append(more, strconv.Itoa(r.IntN(60)))
				}

				got = mergeStrings(got, more)

				for _, m := range more {
					want = appendNew(want, m)
				}
			default:
				ix.add(&got, v)
				want = appendNew(want, v)
			}
		}

		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBindingGrowsWithTheClosureNotItsSquare(t *testing.T) {
	if testing.Short() {
		t.Skip("times binding")
	}

	set, err := Load(t.Context(), Options{Config: &config.Config{}})
	if err != nil {
		t.Fatal(err)
	}

	took := func(n int) time.Duration {
		tree := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}

		for i := range n {
			id := fmt.Sprintf("go:p#func:F%06d", i)
			tree.Units[id] = deps.Unit{ID: id, Group: "p"}

			if i > 0 {
				tree.Links = append(tree.Links, plugin.Link{From: fmt.Sprintf("go:p#func:F%06d", i-1), To: id, Kind: plugin.LinkUses})
			}
		}

		sl := []BindSlice{{Suite: "s", Name: "a_test.go", List: []string{"go:p#func:F000000"}, HasList: true}}
		best := time.Duration(1<<63 - 1)

		for range 3 {
			start := time.Now()

			if _, err := set.Bind(t.Context(), t.TempDir(), tree, sl); err != nil {
				t.Fatal(err)
			}

			best = min(best, time.Since(start))
		}

		return best
	}

	// a ratio of two sizes measured back to back, a few times over: a busy
	// machine slows one measurement more than the other now and then, a
	// square in the code slows the large one every time
	var small, large time.Duration

	for range 3 {
		small, large = took(5000), took(20000)
		if large <= 8*small {
			return
		}
	}

	t.Fatalf("a closure of 20000 units took %v, of 5000 %v: %.1f times, want about 4", large, small, float64(large)/float64(small))
}

func TestABoundSliceHoldsEachUnitOnce(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10)) //nolint:gosec // test data
	tree := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}

	var ids []string

	for i := range 400 {
		id := fmt.Sprintf("go:p%02d#func:F%04d", i/10, i)
		tree.Units[id] = deps.Unit{ID: id, Group: fmt.Sprintf("p%02d", i/10)}
		ids = append(ids, id)

		if i%7 == 0 {
			tree.CannotTell[id] = "test"
		}
	}

	for range 1200 {
		tree.Links = append(tree.Links, plugin.Link{From: ids[r.IntN(400)], To: ids[r.IntN(400)], Kind: plugin.LinkUses})
	}

	var sl []BindSlice

	for i := range 20 {
		var list []string
		for range 30 {
			list = append(list, ids[r.IntN(400)], "go:gone#func:X")
		}

		sl = append(sl, BindSlice{Suite: "s", Name: fmt.Sprintf("t%02d_test.go", i), List: list, HasList: true, Depth: i % 3})
	}

	set, err := Load(t.Context(), Options{Config: &config.Config{}})
	if err != nil {
		t.Fatal(err)
	}

	bound, err := set.Bind(t.Context(), t.TempDir(), tree, sl)
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range bound {
		seen := map[string]bool{}

		for _, id := range d.Units {
			if seen[id] {
				t.Fatalf("%s %s holds %s twice", d.Name, d.Width, id)
			}

			seen[id] = true

			if _, ok := d.Why[id]; !ok {
				t.Fatalf("%s %s holds %s without a reason", d.Name, d.Width, id)
			}
		}

		if len(seen) < 2 {
			t.Fatalf("%s %s bound %d units: the test binds too little", d.Name, d.Width, len(seen))
		}
	}
}
