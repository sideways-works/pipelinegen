package plugins

import (
	"slices"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/deps"
)

type wideGraphs struct {
	t       *deps.Tree
	forward map[string]map[string][]string
	reverse map[string]map[string][]string
}

func newWideGraphs(t *deps.Tree) *wideGraphs {
	w := &wideGraphs{t: t, forward: map[string]map[string][]string{}, reverse: map[string]map[string][]string{}}

	for n, links := range t.RuledOut {
		w.forward[n], w.reverse[n] = map[string][]string{}, map[string][]string{}

		for _, ln := range links {
			w.forward[n][ln.From] = append(w.forward[n][ln.From], ln.To)
			w.reverse[n][ln.To] = append(w.reverse[n][ln.To], ln.From)
		}
	}

	return w
}

func (w *wideGraphs) dropped(g *linkGraph, d *deps.Slice, sl BindSlice, dependents bool) {
	for _, n := range sortedKeys(w.forward) {
		var extra []string
		if within(dependents, sl.Depth) {
			extra = w.extraWithin(g, d, n, sl.Depth)
		} else {
			extra = w.extra(g, d, n, dependents)
		}

		if len(extra) > 0 {
			d.Dropped[n] = mergeStrings(d.Dropped[n], extra)
		}
	}

	for _, label := range sl.Data {
		path, ok := bazel.LabelPath(label)
		n, byUnits := w.t.ByUnits[path]

		if !ok || !byUnits {
			continue
		}

		var unbound []string

		for _, id := range w.t.FileUnits[path] {
			if _, bound := d.Why[id]; !bound {
				unbound = append(unbound, id)
			}
		}

		if len(unbound) > 0 {
			d.Dropped[n] = mergeStrings(d.Dropped[n], unbound)
		}
	}
}

func (w *wideGraphs) extra(g *linkGraph, d *deps.Slice, n string, dependents bool) []string {
	seen := make(map[string]bool, len(d.Why))
	queue := make([]string, 0, len(d.Why))

	for _, id := range sortedKeys(d.Why) {
		seen[id] = true
		queue = append(queue, id)
	}

	var out []string

	visit := func(id string) {
		if seen[id] {
			return
		}

		seen[id] = true
		queue = append(queue, id)

		if _, ok := w.t.Units[id]; ok {
			out = append(out, id)
		}
	}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]

		for _, to := range g.forward[id] {
			visit(to)
		}

		for _, to := range w.forward[n][id] {
			visit(to)
		}

		if !dependents {
			continue
		}

		for _, from := range g.reverse[id] {
			visit(from)
		}

		for _, from := range w.reverse[n][id] {
			visit(from)
		}
	}

	return out
}

func (w *wideGraphs) extraWithin(g *linkGraph, d *deps.Slice, n string, depth int) []string {
	backs := make(map[string]int, len(d.Why))

	var q stepQueue

	// the units d holds start in order of their steps back
	held := sortedKeys(d.Why)
	slices.SortStableFunc(held, func(a, b string) int { return g.backs[d][a] - g.backs[d][b] })

	for _, id := range held {
		if b, ok := g.backs[d][id]; ok {
			backs[id] = b
			q.push(step{id: id, back: b}, false)
		}
	}

	push := func(id string, b int, ahead bool) {
		if have, ok := backs[id]; !ok || b < have {
			backs[id] = b
			q.push(step{id: id, back: b}, ahead)
		}
	}

	for it, ok := q.pop(); ok; it, ok = q.pop() {
		if backs[it.id] != it.back {
			continue
		}

		// both lists are walked in turn: appending one to the other could write into the link
		// graph's own arrays
		for _, next := range [][]string{g.forward[it.id], w.forward[n][it.id]} {
			for _, to := range next {
				push(to, it.back, true)
			}
		}

		if it.back >= depth {
			continue
		}

		for _, next := range [][]string{g.reverse[it.id], w.reverse[n][it.id]} {
			for _, from := range next {
				push(from, it.back+1, false)
			}
		}
	}

	var out []string

	for _, id := range sortedKeys(backs) {
		if _, held := d.Why[id]; held {
			continue
		}

		if _, ok := w.t.Units[id]; ok {
			out = append(out, id)
		}
	}

	return out
}
