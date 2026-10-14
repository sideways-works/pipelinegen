package graph

import (
	"slices"
	"strings"
)

// The layers of the walk's states: needBound is any reach at the executed
// width, needExpanded a reach that expands the node, needDependents a reach
// at the dependents width.
const (
	needBound int32 = iota
	needExpanded
	needDependents
	layers
)

type state = int32

func stateOf(node, layer int32) state { return node*layers + layer }

func nodeOf(s state) int32 { return s / layers }

func layerOf(s state) int32 { return s % layers }

type hop struct {
	prev     state
	from, to int32
	name     uint16
	back     bool
	step     bool
}

type walk struct {
	l     *loaded
	trees uint8
	cost  []int32
	via   []hop
	// hops and end are, per state, its path's steps and the changed node it
	// ends at, so a root's reason is measured without walking it.
	hops, end []int32
}

func walkFrom(l *loaded, dependents bool, sources []source) *walk {
	return walkIn(l, 1<<base|1<<head, dependents, sources)
}

func walkIn(l *loaded, trees uint8, dependents bool, sources []source) *walk {
	n := len(l.ids) * int(layers)
	l.walks++
	l.steps += n

	w := &walk{l: l, trees: trees, cost: make([]int32, n), via: make([]hop, n), hops: make([]int32, n), end: make([]int32, n)}
	for i := range w.cost {
		w.cost[i] = -1
	}

	// a 0-1 breadth-first order, one depth at a time: a free step joins
	// the queue of the depth being walked, a step that costs depth the
	// next one's. Each state is settled at its fewest depth steps, and
	// within them at about its fewest hops, so reasons stay short.
	var free, costly []state

	push := func(s state, c int32, h hop) {
		if old := w.cost[s]; old >= 0 && old <= c {
			return
		}

		w.cost[s] = c
		w.via[s] = h

		// a state's own hop is final when the state is walked, so what
		// it hands on is too
		var step int32
		if h.step {
			step = 1
		}

		switch {
		case h.prev >= 0:
			w.hops[s], w.end[s] = w.hops[h.prev]+step, w.end[h.prev]
		case h.step:
			w.hops[s], w.end[s] = step, h.to
		default:
			w.hops[s], w.end[s] = 0, nodeOf(s)
		}

		if h.prev >= 0 && c > w.cost[h.prev] {
			costly = append(costly, s)
		} else {
			free = append(free, s)
		}
	}

	for _, src := range sources {
		h := hop{prev: -1}
		if src.kind != "" {
			h = hop{prev: -1, from: src.from, to: src.node, name: l.kindID(src.kind), step: true}
		}

		push(stateOf(src.at, needBound), 0, h)

		if dependents && !src.executed {
			push(stateOf(src.at, needDependents), 0, h)
		}
	}

	for depth := int32(0); len(free) > 0; depth++ {
		// the queue grows while it is walked
		for len(free) > 0 {
			s := free[0]
			free = free[1:]

			// a state settled at fewer steps after it was queued for this
			// depth was walked at those
			if w.cost[s] == depth {
				w.expand(s, push)
			}
		}

		free, costly = costly, nil
	}

	return w
}

func (w *walk) expand(s state, push func(state, int32, hop)) {
	x, layer, c := nodeOf(s), layerOf(s), w.cost[s]
	l := w.l

	link := func(from, to int32, e edge, back bool) hop {
		return hop{prev: s, from: from, to: to, name: e.name, back: back, step: true}
	}

	switch layer {
	case needBound, needExpanded:
		l.steps += len(l.in[x])

		for _, e := range l.in[x] {
			if e.trees&w.trees == 0 {
				continue
			}

			var to int32

			switch {
			case e.kind == plain:
				// a plain link leaves a bound node, and expands its target
				to = needBound
			case e.kind == follow:
				to = needExpanded
			case e.kind == bind && layer == needBound:
				to = needExpanded
			default:
				continue
			}

			push(stateOf(e.other, to), c, link(e.other, x, e, false))
		}
	case needDependents:
		// anything the executed width binds is expanded at depth 0
		push(stateOf(x, needBound), c, hop{prev: s})

		l.steps += len(l.in[x]) + len(l.out[x])

		step := int32(1)
		if strings.HasPrefix(l.ids[x], NodePrefix) {
			step = 0
		}

		for _, e := range l.in[x] {
			if e.trees&w.trees == 0 {
				continue
			}

			if e.kind == plain {
				push(stateOf(e.other, needDependents), c, link(e.other, x, e, false))
			} else {
				push(stateOf(e.other, needDependents), c+step, link(e.other, x, e, false))
			}
		}

		// a plain link walked against its direction at this width: a
		// node x links to reaches x at one step
		for _, e := range l.out[x] {
			if e.kind == plain && e.trees&w.trees != 0 {
				push(stateOf(e.other, needDependents), c+1, link(x, e.other, e, true))
			}
		}
	}
}

type source struct {
	at, node int32
	kind     string
	from     int32
	executed bool
}

func (w *walk) reached(node int32) (int32, state) {
	best, at := int32(-1), state(-1)

	for _, layer := range []int32{needBound, needExpanded} {
		s := stateOf(node, layer)
		if c := w.cost[s]; c >= 0 && (best < 0 || c < best) {
			best, at = c, s
		}
	}

	return best, at
}

func (w *walk) path(root int32, s state) Path {
	p := Path{Listed: w.l.ids[root]}

	for {
		h := w.via[s]

		if h.step {
			from, to := w.l.ids[h.from], w.l.ids[h.to]
			p.Steps = append(p.Steps, Step{From: from, To: to, Kind: w.l.kinds[h.name], Back: h.back, Plugin: w.l.linkPlugin(from, to)})
		}

		if h.prev < 0 {
			p.Changed = w.l.ids[nodeOf(s)]
			if h.step {
				p.Changed = w.l.ids[h.to]
			}

			return p
		}

		s = h.prev
	}
}

// candidate is one root's reason a slice is selected; only the few best are
// built into paths.
type candidate struct {
	root      int32
	at        state
	cost      int32
	hops, end int32
	path      *Path
}

func (w *walk) cannotTellOnly(at state, hops int32) bool {
	if hops != 1 {
		return false
	}

	id, ok := w.l.kindIx[KindCannotTell]
	if !ok {
		return false
	}

	// a change of layer adds no step: the step is further back
	for s := at; s >= 0; s = w.via[s].prev {
		if h := w.via[s]; h.step {
			return h.name == id
		}
	}

	return false
}

func (w *walk) candidate(root int32, at state, cost int32) candidate {
	return candidate{root: root, at: at, cost: cost, hops: w.hops[at], end: w.end[at]}
}

// less orders two reasons by depth steps, hops, then their ends' ids, so a
// plan's reasons don't move between runs.
func (w *walk) less(a, b *candidate) bool {
	switch {
	case a.cost != b.cost:
		return a.cost < b.cost
	case a.hops != b.hops:
		return a.hops < b.hops
	}

	al, ac := w.ends(a)
	bl, bc := w.ends(b)

	if al != bl {
		return al < bl
	}

	return ac < bc
}

func (w *walk) ends(c *candidate) (string, string) {
	if c.path != nil {
		return c.path.Listed, c.path.Changed
	}

	return w.l.ids[c.root], w.l.ids[c.end]
}

func bestPaths(w *walk, cands []candidate, n int) []Path {
	best := make([]*candidate, 0, n+1)

	for i := range cands {
		c := &cands[i]

		at := len(best)
		for at > 0 && w.less(c, best[at-1]) {
			at--
		}

		if at < n {
			best = slices.Insert(best, at, c)
			best = best[:min(len(best), n)]
		}
	}

	out := make([]Path, 0, len(best))

	for _, c := range best {
		if c.path != nil {
			out = append(out, *c.path)
		} else {
			out = append(out, w.path(c.root, c.at))
		}
	}

	return out
}
