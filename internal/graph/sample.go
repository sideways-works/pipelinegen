package graph

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/plugins"
)

const (
	narrowSample      = plugins.NarrowSample
	narrowSampleBound = plugins.NarrowSampleBound
	narrowPluginEntry = plugins.NarrowPluginEntry
)

// SampleTarget and SampleBoundTarget are a suite's sample targets, as the
// planner names them.
func SampleTarget(suite string) string { return "_sample." + suite }

// SampleBoundTarget is the target of what sample-bound moved to a suite's
// sample.
func SampleBoundTarget(suite string) string { return "_sample-bound." + suite }

// EntryTarget is the target of the plugin entries plugin-entry moved to a
// suite's sample.
func EntryTarget(suite string) string { return "_sample-entry." + suite }

type sampled [2]map[int32]string

func (s sampled) both(l *loaded, i int32) bool {
	for _, tr := range []int{base, head} {
		if _, ok := l.digest[tr][i]; ok && s[tr][i] == "" {
			return false
		}
	}

	return true
}

// sampleSuites works out per suite which changed units go to its sample. A
// changed pin makes every unit its sbom lists a change, so each slice's
// binding is worked out once per tree and only a change every slice binds is
// looked at in its cone.
func sampleSuites(l *loaded, ss []plugins.BindSlice, ch changes, o Options) map[string]sampled {
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

		roots := make([]sliceRoots, len(suiteSlices))
		for i, sl := range suiteSlices {
			roots[i] = rootsByWhy(l, sl)
		}

		var s sampled

		for _, tr := range []int{base, head} {
			s[tr] = map[int32]string{}

			var changed []int32

			for c, how := range ch.how {
				if _, ok := l.digest[tr][c]; ok && how != HowLinks {
					changed = append(changed, c)
				}
			}

			if len(changed) == 0 {
				continue
			}

			sp := newSampler(l, tr, roots, on(o, narrowSampleBound), len(changed))

			for _, c := range changed {
				if n := sp.decide(c); n != "" {
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

// inSample tells whether a slice is one a suite's sample is drawn from; the
// setup slice keeps every unit it binds.
func inSample(sl plugins.BindSlice) bool {
	return sl.HasList && !sl.Setup && !strings.HasPrefix(sl.Name, "_")
}

func sampledSlices(ss []plugins.BindSlice) map[string][]plugins.BindSlice {
	out := map[string][]plugins.BindSlice{}

	for _, sl := range ss {
		if inSample(sl) {
			out[sl.Suite] = append(out[sl.Suite], sl)
		}
	}

	return out
}

type sliceRoots struct {
	listed, other map[int32]bool
	all           []int32
}

func rootsByWhy(l *loaded, sl plugins.BindSlice) sliceRoots {
	r := sliceRoots{listed: map[int32]bool{}, other: map[int32]bool{}, all: rootsOf(l, sl)}

	for _, id := range sl.List {
		if i, ok := l.ix[id]; ok {
			r.listed[i] = true
		}
	}

	for _, i := range r.all {
		if !r.listed[i] {
			r.other[i] = true
		}
	}

	// a unit the slice names as data or holds in its own files is that,
	// whatever its list says
	for i := range r.other {
		delete(r.listed, i)
	}

	for _, id := range sl.Units {
		if i, ok := l.ix[id]; ok {
			delete(r.listed, i)
			r.other[i] = true
		}
	}

	return r
}

// sampler decides which changed units of one suite's slices go to the sample
// in one tree. Each slice's closure (0 not, 1 bound, 2 bound and expanded)
// is worked out once, and only a change every slice binds is looked at in
// its cone.
type sampler struct {
	l     *loaded
	tr    int
	roots []sliceRoots
	n     int32
	bound bool

	listed, held []int32
	every        []int32
	counted      bool
	closures     [][]uint8
	rootedAt     map[int32][]int32
	bringable    []bool

	layer   []uint8
	cone    []int32
	queue   []int32
	coned   bool
	class   []uint8
	classed []int32
}

const (
	classNone uint8 = iota
	classSample
	classUnknown
	classKept
)

const (
	inBound uint8 = 1 << iota
	inExpanded
)

func newSampler(l *loaded, tr int, roots []sliceRoots, bound bool, changes int) *sampler {
	n := len(l.ids)
	s := &sampler{
		l: l, tr: tr, roots: roots, n: int32(len(roots)), bound: bound, //nolint:gosec // a suite has far fewer than 2^31 slices
		listed: make([]int32, n), held: make([]int32, n), layer: make([]uint8, n), class: make([]uint8, n),
	}

	for _, r := range roots {
		for i := range r.listed {
			s.listed[i]++
			s.held[i]++
		}

		for i := range r.other {
			s.held[i]++
		}
	}

	if changes >= len(roots) {
		s.count(false)
	}

	return s
}

func (s *sampler) count(keep bool) {
	n := len(s.l.ids)
	s.every, s.counted = make([]int32, n), true

	if keep {
		s.closures = make([][]uint8, len(s.roots))
	}

	closure := make([]uint8, n)

	for k, r := range s.roots {
		if keep {
			closure = make([]uint8, n)
			s.closures[k] = closure
		}

		s.close(r.all, closure)

		for i, st := range closure {
			if st > 0 {
				s.every[i]++
			}
		}
	}
}

func (s *sampler) bindsAll(c int32) bool {
	n := s.n

	if s.counted {
		return s.every[c] == n
	}

	if s.rootedAt == nil {
		s.rootedAt = map[int32][]int32{}

		for k, r := range s.roots {
			for _, i := range r.all {
				s.rootedAt[i] = append(s.rootedAt[i], int32(k))
			}
		}
	}

	s.coneOf(c)
	defer s.clearCone()

	seen := make([]bool, n)
	got := int32(0)

	for _, y := range s.cone {
		for _, k := range s.rootedAt[y] {
			if !seen[k] {
				seen[k] = true
				got++
			}
		}

		if got == n {
			return true
		}
	}

	return false
}

func (s *sampler) close(roots []int32, state []uint8) {
	s.l.walks++
	s.l.steps += len(state)

	clear(state)

	queue := s.queue[:0]

	mark := func(i int32, st uint8) {
		if state[i] < st {
			state[i] = st
			queue = append(queue, i)
		}
	}

	for _, r := range roots {
		mark(r, 2)
	}

	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]

		s.l.steps += len(s.l.out[x])

		for _, e := range s.l.out[x] {
			if e.trees&(1<<s.tr) == 0 {
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

	s.queue = queue[:0]
}

func (s *sampler) decide(c int32) string {
	n := s.n

	if !s.bindsAll(c) {
		return ""
	}

	if s.listed[c] == n {
		return narrowSample
	}

	// sample-bound takes only what no slice lists or holds otherwise
	if !s.bound || s.held[c] > 0 {
		return ""
	}

	if s.closures == nil {
		s.count(true)
	}

	if !s.mayBeBrought(c) {
		return ""
	}

	return s.bringBound(c)
}

func (s *sampler) bringBound(c int32) string {
	inSample, unknowns := s.boundThrough(c)
	defer s.clearClasses()

	if s.coned {
		defer s.clearCone()
	}

	// the fixed point, by a work list: a node is looked at again only
	// when one it is bound through went to the sample
	queue := slices.Clone(unknowns)
	queued := make(map[int32]bool, len(unknowns))

	for _, y := range unknowns {
		queued[y] = true
	}

	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		queued[y] = false

		if inSample[y] != "" || !s.throughSampleAll(y, inSample) {
			continue
		}

		if y == c {
			return narrowSampleBound
		}

		inSample[y] = narrowSampleBound

		for _, e := range s.l.out[y] {
			if z := e.other; e.trees&(1<<s.tr) != 0 && s.class[z] == classUnknown && inSample[z] == "" && !queued[z] {
				queued[z] = true
				queue = append(queue, z)
			}
		}
	}

	return ""
}

func (s *sampler) boundThrough(c int32) (map[int32]string, []int32) {
	n := s.n

	inSample := map[int32]string{}
	unknowns := []int32{c}
	s.class[c] = classUnknown
	s.classed = append(s.classed[:0], c)

	binds := false

	for k := 0; k < len(unknowns); k++ {
		for _, e := range s.l.in[unknowns[k]] {
			if e.trees&(1<<s.tr) == 0 {
				continue
			}

			p := e.other

			if e.kind == bind {
				binds = binds || s.expandedByAny(p)

				continue
			}

			if (e.kind != plain && e.kind != follow) || s.class[p] != classNone {
				continue
			}

			s.classed = append(s.classed, p)

			switch {
			case s.listed[p] == n:
				s.class[p] = classSample
				inSample[p] = narrowSample
			case s.held[p] == 0 && s.every[p] == n:
				s.class[p] = classUnknown
				unknowns = append(unknowns, p)
			default:
				s.class[p] = classKept
			}
		}
	}

	if binds {
		s.coneOf(c)
	}

	return inSample, unknowns
}

// mayBeBrought tells whether sample-bound may take c in any change's cone,
// as a least fixed point over the whole tree, so only a change in it needs
// its cone.
func (s *sampler) mayBeBrought(c int32) bool {
	if s.bringable == nil {
		s.bringable = s.bringableAll()
	}

	return s.bringable[c]
}

func (s *sampler) bringableAll() []bool {
	n := s.n
	in := make([]bool, len(s.l.ids))

	var queue []int32

	for i := range s.l.ids {
		if x := int32(i); s.held[x] == 0 && s.every[x] == n {
			queue = append(queue, x)
		}
	}

	queued := make([]bool, len(s.l.ids))
	for _, x := range queue {
		queued[x] = true
	}

	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]
		queued[x] = false

		if in[x] || !s.mayBring(x, in) {
			continue
		}

		in[x] = true

		for _, e := range s.l.out[x] {
			if z := e.other; e.trees&(1<<s.tr) != 0 && !in[z] && !queued[z] && s.held[z] == 0 && s.every[z] == n {
				queued[z] = true
				queue = append(queue, z)
			}
		}
	}

	return in
}

func (s *sampler) mayBring(x int32, in []bool) bool {
	n := s.n

	for i := range s.roots {
		closure := s.closures[i]
		brought := false

	edges:
		for _, e := range s.l.in[x] {
			if e.trees&(1<<s.tr) == 0 {
				continue
			}

			p := e.other

			switch {
			case e.kind == plain && closure[p] > 0:
			case e.kind == follow && closure[p] == 2:
				if strings.HasPrefix(s.l.ids[p], GroupPrefix) {
					brought = true

					break edges
				}
			default:
				continue
			}

			if s.listed[p] != n && !in[p] {
				return false
			}

			brought = true
		}

		if !brought {
			return false
		}
	}

	return true
}

func (s *sampler) coneOf(c int32) {
	type st struct {
		node  int32
		layer uint8
	}

	queue := []st{{c, inBound}}
	s.l.walks++
	s.coned = true
	s.layer[c] = inBound
	s.cone = append(s.cone[:0], c)

	push := func(i int32, layer uint8) {
		if s.layer[i]&layer != 0 {
			return
		}

		if s.layer[i] == 0 {
			s.cone = append(s.cone, i)
		}

		s.layer[i] |= layer
		queue = append(queue, st{i, layer})
	}

	for len(queue) > 0 {
		x := queue[0]
		queue = queue[1:]

		s.l.steps += len(s.l.in[x.node])

		for _, e := range s.l.in[x.node] {
			if e.trees&(1<<s.tr) == 0 {
				continue
			}

			switch {
			case e.kind == plain:
				push(e.other, inBound)
			case e.kind == follow:
				push(e.other, inExpanded)
			case e.kind == bind && x.layer == inBound:
				push(e.other, inExpanded)
			}
		}
	}
}

func (s *sampler) clearCone() {
	s.coned = false

	for _, y := range s.cone {
		s.layer[y] = 0
	}

	s.cone = s.cone[:0]
}

// reach is how slice i binds y inside c's cone, 0 outside it. Every link
// into a node the cone holds as bound comes from the cone, so there it is
// the slice's closure.
func (s *sampler) reach(i int, y int32) uint8 {
	if s.coned && s.layer[y] == 0 {
		return 0
	}

	return s.closures[i][y]
}

func (s *sampler) expandedByAny(y int32) bool {
	for _, closure := range s.closures {
		if closure[y] == 2 {
			return true
		}
	}

	return false
}

func (s *sampler) clearClasses() {
	for _, y := range s.classed {
		s.class[y] = classNone
	}

	s.classed = s.classed[:0]
}

func (s *sampler) throughSampleAll(y int32, inSample map[int32]string) bool {
	for i := range s.roots {
		if !s.throughSample(i, y, inSample) {
			return false
		}
	}

	return true
}

func (s *sampler) throughSample(i int, y int32, inSample map[int32]string) bool {
	brought := false

	for _, e := range s.l.in[y] {
		if e.trees&(1<<s.tr) == 0 {
			continue
		}

		p := e.other

		switch {
		case e.kind == plain && s.reach(i, p) > 0:
		case (e.kind == follow || e.kind == bind) && s.reach(i, p) == 2:
			if e.kind == follow && strings.HasPrefix(s.l.ids[p], GroupPrefix) {
				// it runs when a package the slice links starts
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

// withoutSampled are the walk's sources less the suite's changes the sample
// takes in every tree that has them. Where the sample takes a unit, the from
// ends of its one-tree links the width follows become sources in its place
// (movedCounts).
func withoutSampled(l *loaded, sources []source, s sampled, keepMoved bool, width string) []source {
	out := make([]source, 0, len(sources))

	for _, src := range sources {
		if sampledAway(l, src, s) && (!keepMoved || !hasMovedLink(l, src.node)) {
			out = append(out, movedInto(l, src.node, width)...)

			continue
		}

		out = append(out, src)
	}

	return out
}

func movedInto(l *loaded, x int32, width string) []source {
	var out []source

	for _, e := range l.in[x] {
		if e.trees != 1<<base|1<<head && e.other != x && !unitChanged(l, e.other) && (width == Dependents || e.kind != dependentsOnly) {
			out = append(out, source{at: e.other, node: e.other})
		}
	}

	return out
}

func sampledAway(l *loaded, src source, s sampled) bool {
	return src.kind == "" && src.at == src.node && !src.executed && s.both(l, src.node)
}

func hasMovedLink(l *loaded, x int32) bool {
	for _, e := range l.out[x] {
		if e.trees != 1<<base|1<<head {
			return true
		}
	}

	return false
}

func sampleTargets(l *loaded, suite string, s sampled) map[string][]Path {
	out := map[string][]Path{}

	for _, tr := range []int{base, head} {
		ids := make([]int32, 0, len(s[tr]))
		for c := range s[tr] {
			ids = append(ids, c)
		}

		slices.SortFunc(ids, func(a, b int32) int { return strings.Compare(l.ids[a], l.ids[b]) })

		for _, c := range ids {
			name := SampleTarget(suite)
			if s[tr][c] == narrowSampleBound {
				name = SampleBoundTarget(suite)
			}

			p := Path{Changed: l.ids[c], Steps: []Step{{To: l.ids[c], Kind: KindSample}}}
			if !slices.ContainsFunc(out[name], func(q Path) bool { return q.Changed == p.Changed }) {
				out[name] = append(out[name], p)
			}
		}
	}

	return out
}
