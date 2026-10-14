package graphtest

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// After changes a tree's bindings before they are compared.
type After func(t *deps.Tree, bound []deps.Slice) []deps.Slice

// Forward is the selection the walk must reproduce: each slice bound in each
// tree by the set's Bind, then compared between the trees by Content.
func Forward(ctx context.Context, set *plugins.Set, base, head *deps.Tree, ss []plugins.BindSlice, after ...After) (map[Key]string, [2][]deps.Slice, error) {
	return ForwardWith(func(t *deps.Tree) ([]deps.Slice, error) { return bindIn(ctx, set, t, ss) }, base, head, after...)
}

// Binder binds the slices in one tree.
type Binder func(t *deps.Tree) ([]deps.Slice, error)

// ForwardWith is Forward with another binding, such as the Model's.
func ForwardWith(bind Binder, base, head *deps.Tree, after ...After) (map[Key]string, [2][]deps.Slice, error) {
	var bound [2][]deps.Slice

	// Bind adds units of its own to a tree, so it binds copies.
	base, head = clone(base), clone(head)

	for i, t := range []*deps.Tree{base, head} {
		var err error
		if bound[i], err = bind(t); err != nil {
			return nil, bound, err
		}

		for _, a := range after {
			bound[i] = a(t, bound[i])
		}
	}

	return Selected(base, head, bound[0], bound[1]), bound, nil
}

func clone(t *deps.Tree) *deps.Tree {
	c := *t
	c.Units = maps.Clone(t.Units)
	c.CannotTell = maps.Clone(t.CannotTell)
	c.CannotTellBy = maps.Clone(t.CannotTellBy)
	c.Links = slices.Clone(t.Links)
	c.Typed = slices.Clone(t.Typed)

	return &c
}

func bindIn(ctx context.Context, set *plugins.Set, t *deps.Tree, ss []plugins.BindSlice) ([]deps.Slice, error) {
	dir, err := os.MkdirTemp("", "forward")
	if err != nil {
		return nil, fmt.Errorf("forward: %w", err)
	}

	out, err := set.Bind(ctx, dir, t, ss)
	if rmErr := os.RemoveAll(dir); err == nil && rmErr != nil {
		err = rmErr
	}

	if err != nil {
		return nil, fmt.Errorf("forward: %w", err)
	}

	return out, nil
}

// Wide is a copy of the tree with a narrowing's ruled-out links put back and
// the narrowing off.
func Wide(t *deps.Tree, narrowing string) *deps.Tree {
	w := *t
	w.Units = maps.Clone(t.Units)
	w.Links = slices.Concat(t.Links, t.RuledOut[narrowing])
	w.Typed = slices.Concat(t.Typed, t.TypedRuledOut[narrowing])
	w.Narrowings = slices.DeleteFunc(slices.Clone(t.Narrowings), func(n string) bool { return n == narrowing })
	w.RuledOut = maps.Clone(t.RuledOut)
	delete(w.RuledOut, narrowing)
	w.TypedRuledOut = maps.Clone(t.TypedRuledOut)
	delete(w.TypedRuledOut, narrowing)

	return &w
}

// LeftOut are the slices a narrowing left out: those the trees with its
// ruled-out links put back select and the trees as they are don't.
func LeftOut(bind Binder, base, head *deps.Tree, narrowing string, selected map[Key]string) (map[Key]string, error) {
	wide, _, err := ForwardWith(bind, Wide(base, narrowing), Wide(head, narrowing))
	if err != nil {
		return nil, err
	}

	out := map[Key]string{}

	for k, why := range wide {
		if _, ok := selected[k]; !ok {
			out[k] = why
		}
	}

	return out, nil
}
