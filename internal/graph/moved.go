package graph

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// movedCap bounds how many moved-link ends the walk compares tree by tree; past it a sampled unit stays
// a source: more than the forward binding selects, never less.
const movedCap = 64

// movedTargets are the far ends of one-tree links leaving a unit the sample takes: the sample takes the
// unit out of every slice, but not what its links bind there.
func movedTargets(l *loaded, sources []source, s sampled) []int32 {
	var out []int32

	for _, src := range sources {
		if !sampledAway(l, src, s) {
			continue
		}

		for _, e := range l.out[src.node] {
			if e.trees != 1<<base|1<<head && e.other != src.node {
				out = append(out, e.other)
			}
		}
	}

	slices.SortFunc(out, func(a, b int32) int { return strings.Compare(l.ids[a], l.ids[b]) })

	return slices.Compact(out)
}

// movedClosure adds what a moved end binds only when expanded: a slice may
// bind the end in both trees but expand it in one only.
func movedClosure(l *loaded, targets []int32) []int32 {
	seen := map[int32]bool{}
	queue := slices.Clone(targets)

	for _, y := range targets {
		seen[y] = true
	}

	for len(queue) > 0 && len(seen) <= movedCap {
		y := queue[0]
		queue = queue[1:]

		for _, e := range l.out[y] {
			if (e.kind != follow && e.kind != bind) || seen[e.other] {
				continue
			}

			seen[e.other] = true

			if e.kind == follow {
				queue = append(queue, e.other)
			}
		}
	}

	out := make([]int32, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}

	slices.SortFunc(out, func(a, b int32) int { return strings.Compare(l.ids[a], l.ids[b]) })

	return out
}

type movedWalks struct {
	targets []int32
	walks   [][2]*walk
}

func newMovedWalks(l *loaded, targets []int32, dependents bool) *movedWalks {
	m := &movedWalks{}

	for _, y := range targets {
		// what a slice depends on is units; a junction or a group's node
		// bound in one tree only shows in the units it leads to
		if _, b := l.digest[base][y]; !b {
			if _, h := l.digest[head][y]; !h {
				continue
			}
		}

		m.targets = append(m.targets, y)
		m.walks = append(m.walks, [2]*walk{
			walkIn(l, 1<<base, dependents, []source{{at: y, node: y}}),
			walkIn(l, 1<<head, dependents, []source{{at: y, node: y}}),
		})
	}

	return m
}

func (m *movedWalks) selects(sl plugins.BindSlice, roots []int32, dependents bool) ([]Path, bool) {
	if m == nil {
		return nil, false
	}

	for i := range m.targets {
		var (
			bound [2]bool
			by    [2]Path
		)

		for _, tr := range []int{base, head} {
			w := m.walks[i][tr]

			for _, r := range roots {
				c, at := w.reached(r)
				if c >= 0 && (!dependents || sl.Depth == 0 || int(c) <= sl.Depth) {
					bound[tr], by[tr] = true, w.path(r, at)

					break
				}
			}
		}

		if bound[base] != bound[head] {
			p := by[base]
			if bound[head] {
				p = by[head]
			}

			return []Path{p}, true
		}
	}

	return nil, false
}
