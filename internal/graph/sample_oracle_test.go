package graph

import (
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// The oracle the sampler is held to: a backwards walk from each changed
// unit, with each slice's binding inside that cone.

func sampleSuitesByWalks(l *loaded, ss []plugins.BindSlice, ch changes, o Options) map[string]sampled {
	if !on(o, narrowSample) {
		return nil
	}

	bySuite := sampledSlices(ss)
	out := map[string]sampled{}

	for _, suite := range sortedKeys(bySuite) {
		suiteSlices := bySuite[suite]
		if len(suiteSlices) < 2 {
			continue
		}

		var s sampled

		for _, tr := range []int{base, head} {
			s[tr] = map[int32]string{}

			for c, how := range ch.how {
				if _, ok := l.digest[tr][c]; !ok || how == HowLinks {
					continue
				}

				if n := sampleOneByWalk(l, tr, suiteSlices, c, on(o, narrowSampleBound)); n != "" {
					s[tr][c] = n
				}
			}
		}

		if len(s[base])+len(s[head]) > 0 {
			out[suite] = s
		}
	}

	return out
}

func sampleOneByWalk(l *loaded, tr int, ss []plugins.BindSlice, c int32, bound bool) string {
	cone := walkIn(l, 1<<tr, false, []source{{at: c, node: c}})

	nodes := coneNodesByWalk(cone)
	roots := make([]sliceRoots, len(ss))
	reach := make([][]uint8, len(ss))

	for i, sl := range ss {
		roots[i] = rootsByWhy(l, sl)
		reach[i] = reachInCone(l, tr, roots[i].all, nodes)

		if reach[i][c] == 0 {
			return ""
		}
	}

	inSample := map[int32]string{}

	for y := range nodes {
		if ranByAllByWalk(roots, reach, y) {
			inSample[y] = narrowSample
		}
	}

	if n := inSample[c]; n != "" || !bound {
		return n
	}

	for grew := true; grew; {
		grew = false

		for y := range nodes {
			if inSample[y] == "" && boundByAllThroughSampleByWalk(l, tr, roots, reach, y, inSample) {
				inSample[y] = narrowSampleBound
				grew = true
			}
		}
	}

	return inSample[c]
}

func coneNodesByWalk(w *walk) map[int32]bool {
	out := map[int32]bool{}

	for s, c := range w.cost {
		if c >= 0 {
			out[nodeOf(state(s))] = true
		}
	}

	return out
}

func reachInCone(l *loaded, tr int, roots []int32, nodes map[int32]bool) []uint8 {
	l.walks++
	state := make([]uint8, len(l.ids))

	var queue []int32

	mark := func(i int32, s uint8) {
		if nodes[i] && state[i] < s {
			state[i] = s
			queue = append(queue, i)
		}
	}

	for _, r := range roots {
		mark(r, 2)
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
				mark(e.other, 2)
			case e.kind == follow && state[x] == 2:
				mark(e.other, 2)
			case e.kind == bind && state[x] == 2:
				mark(e.other, 1)
			}
		}
	}

	return state
}

func ranByAllByWalk(roots []sliceRoots, reach [][]uint8, y int32) bool {
	for i, r := range roots {
		if !r.listed[y] || reach[i][y] == 0 {
			return false
		}
	}

	return true
}

func boundByAllThroughSampleByWalk(l *loaded, tr int, roots []sliceRoots, reach [][]uint8, y int32, inSample map[int32]string) bool {
	for i, r := range roots {
		if reach[i][y] == 0 || r.listed[y] || r.other[y] {
			return false
		}

		if !throughSampleByWalk(l, tr, reach[i], y, inSample) {
			return false
		}
	}

	return true
}

func throughSampleByWalk(l *loaded, tr int, reach []uint8, y int32, inSample map[int32]string) bool {
	brought := false

	for _, e := range l.in[y] {
		if e.trees&(1<<tr) == 0 {
			continue
		}

		p := e.other

		switch {
		case e.kind == plain && reach[p] > 0:
		case (e.kind == follow || e.kind == bind) && reach[p] == 2:
			if e.kind == follow && strings.HasPrefix(l.ids[p], GroupPrefix) {
				return true
			}

			if e.kind == bind {
				return false
			}
		default:
			continue
		}

		if inSample[p] == "" {
			return false
		}

		brought = true
	}

	return brought
}
