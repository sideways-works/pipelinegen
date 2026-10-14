package graph

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

const (
	narrowUnreachedNew  = "unreached-new"
	narrowReferencedNew = "referenced-new"
)

// UnreachedNew is the name rule under which no slice depends on a group's
// unbound names.
const UnreachedNew = narrowUnreachedNew

// HowNames is a group whose unbound names differ between the trees: the
// slices that link the group are selected.
const HowNames = "names"

func namesChanged(l *loaded, ss []plugins.BindSlice, width string, o Options) []int32 {
	if on(o, narrowUnreachedNew) {
		return nil
	}

	var groups []int32

	for g := range l.byGroup {
		if i, ok := l.ix[GroupPrefix+g]; ok {
			groups = append(groups, i)
		}
	}

	if len(groups) == 0 {
		return nil
	}

	slices.Sort(groups)

	var names [2]map[string][]int32

	for _, tr := range []int{base, head} {
		bound := boundByAny(l, tr, ss, width == Dependents)

		if on(o, narrowReferencedNew) {
			referenced(l, tr, bound)
		}

		names[tr] = unboundNames(l, tr, bound)
	}

	var out []int32

	for _, gi := range groups {
		g := strings.TrimPrefix(l.ids[gi], GroupPrefix)
		if !slices.Equal(names[base][g], names[head][g]) {
			out = append(out, gi)
		}
	}

	return out
}

func on(o Options, narrowing string) bool { return o.On != nil && o.On(narrowing) }

func unboundNames(l *loaded, tr int, bound []bool) map[string][]int32 {
	out := map[string][]int32{}

	for g, units := range l.byGroup {
		for _, i := range units {
			if _, ok := l.digest[tr][i]; ok && !bound[i] && !l.input[i] && isName(l.ids[i]) {
				out[g] = append(out[g], i)
			}
		}

		slices.SortFunc(out[g], func(a, b int32) int { return strings.Compare(l.ids[a], l.ids[b]) })
	}

	return out
}

func isName(id string) bool {
	for _, p := range []string{"file:", GroupPrefix, NodePrefix, pinPrefix, unboundPrefix} {
		if strings.HasPrefix(id, p) {
			return false
		}
	}

	return true
}

func referenced(l *loaded, tr int, bound []bool) {
	var more []int32

	for from, out := range l.out {
		if !bound[from] {
			continue
		}

		for _, e := range out {
			if e.trees&(1<<tr) != 0 && e.ref {
				more = append(more, e.other)
			}
		}
	}

	for _, i := range more {
		bound[i] = true
	}
}

// boundByAny is what the recorded slices bind together in tree tr, in a
// width. At the dependents width each slice's depth bounds its own part, so
// slices are walked together per depth.
func boundByAny(l *loaded, tr int, ss []plugins.BindSlice, dependents bool) []bool {
	bound := make([]bool, len(l.ids))

	byDepth := map[int][]int32{}

	for _, sl := range ss {
		if !sl.HasList {
			continue
		}

		d := 0
		if dependents {
			d = sl.Depth
		}

		byDepth[d] = append(byDepth[d], rootsOf(l, sl)...)
	}

	for d, roots := range byDepth {
		forward(l, tr, roots, dependents, d, bound)
	}

	return bound
}

func forward(l *loaded, tr int, roots []int32, dependents bool, depth int, bound []bool) {
	state := executedForward(l, tr, roots)

	if !dependents {
		for i, s := range state {
			if s > 0 {
				bound[i] = true
			}
		}

		return
	}

	for i, c := range dependentsForward(l, tr, state) {
		if c >= 0 && (depth == 0 || int(c) <= depth) {
			bound[i] = true
		}
	}
}

// executedForward is, per node, 0 for unbound, 1 for bound and 2 for bound
// and expanded at the executed width.
func executedForward(l *loaded, tr int, roots []int32) []uint8 {
	state := make([]uint8, len(l.ids))
	queue := make([]int32, 0, len(roots))

	reach := func(i int32, s uint8) {
		if state[i] < s {
			state[i] = s
			queue = append(queue, i)
		}
	}

	for _, r := range roots {
		reach(r, 2)
	}

	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]

		for _, e := range l.out[x] {
			if e.trees&(1<<tr) == 0 {
				continue
			}

			switch {
			case e.kind == plain:
				reach(e.other, 2)
			case e.kind == follow && state[x] == 2:
				reach(e.other, 2)
			case e.kind == bind && state[x] == 2:
				reach(e.other, 1)
			}
		}
	}

	return state
}

// dependentsForward is, per node, the fewest depth steps at which the
// dependents width reaches it, or -1.
func dependentsForward(l *loaded, tr int, state []uint8) []int32 {
	cost := make([]int32, len(l.ids))
	for i := range cost {
		cost[i] = -1
	}

	var free, costly []int32

	relax := func(i, c int32, step bool) {
		if old := cost[i]; old >= 0 && old <= c {
			return
		}

		cost[i] = c

		if step {
			costly = append(costly, i)
		} else {
			free = append(free, i)
		}
	}

	for i := range int32(len(state)) { //nolint:gosec // see id
		if state[i] > 0 {
			relax(i, 0, false)
		}
	}

	for len(free) > 0 || len(costly) > 0 {
		var x int32

		if len(free) > 0 {
			x, free = free[len(free)-1], free[:len(free)-1]
		} else {
			x, costly = costly[0], costly[1:]
		}

		for _, e := range l.out[x] {
			if e.trees&(1<<tr) == 0 {
				continue
			}

			free := e.kind == plain || strings.HasPrefix(l.ids[e.other], NodePrefix)
			relax(e.other, cost[x]+b2i(!free), !free)
		}

		for _, e := range l.in[x] {
			if e.trees&(1<<tr) != 0 && e.kind == plain {
				relax(e.other, cost[x]+1, true)
			}
		}
	}

	return cost
}

func b2i(b bool) int32 {
	if b {
		return 1
	}

	return 0
}
