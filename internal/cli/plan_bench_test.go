package cli

import (
	"fmt"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/depsrepo"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func scaleTree(units, slices, links, listed int) (*deps.Tree, []plugins.BindSlice) {
	r := rand.New(rand.NewPCG(5, 6)) //nolint:gosec // test data
	tree := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}
	ids := make([]string, 0, units)

	for i := range units {
		group := fmt.Sprintf("internal/pkg%04d", i/10)
		id := fmt.Sprintf("go:%s#func:F%05d", group, i)
		tree.Units[id] = deps.Unit{ID: id, Group: group, Digest: fmt.Sprintf("%016x", r.Uint64())}
		ids = append(ids, id)
	}

	for _, id := range ids {
		for range links {
			tree.Links = append(tree.Links, plugin.Link{From: id, To: ids[r.IntN(len(ids))], Kind: plugin.LinkUses})
		}
	}

	bs := make([]plugins.BindSlice, 0, slices)

	for i := range slices {
		list := make([]string, 0, listed)
		for range listed {
			list = append(list, ids[r.IntN(len(ids))])
		}

		bs = append(bs, plugins.BindSlice{Suite: "backend", Name: fmt.Sprintf("internal/pkg%04d/x%04d_test.go", i%(units/10), i), List: list, HasList: true})
	}

	return tree, bs
}

// PIPELINEGEN_DEPS_SCALE=1 runs it at a large backend's size, =third at a third of it.
func BenchmarkThePlannersOwnWorkAtScale(b *testing.B) {
	units, slices, listed := 3700, 100, 130

	switch os.Getenv("PIPELINEGEN_DEPS_SCALE") {
	case "":
	case "third":
		units, slices, listed = 12300, 333, 430
	default:
		units, slices, listed = 37000, 1000, 1300
	}

	tree, bs := scaleTree(units, slices, 3, listed)

	set, err := plugins.Load(b.Context(), plugins.Options{Config: &config.Config{}})
	if err != nil {
		b.Fatal(err)
	}

	for b.Loop() {
		bound, err := set.Bind(b.Context(), b.TempDir(), tree, bs)
		if err != nil {
			b.Fatal(err)
		}

		book := newPlanBook(set)
		book.keep("c", tree, bound)

		bound = toSample(tree, bound, true, true)

		if _, err := (depsrepo.Writer{}).Write(b.Context(), b.TempDir(), tree, bound); err != nil {
			b.Fatal(err)
		}
	}
}
