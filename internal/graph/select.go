package graph

import (
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

const defaultPaths = 3

func selectAll(l *loaded, ss []plugins.BindSlice, widths []string, o Options) *Selection {
	n := o.Paths
	if n <= 0 {
		n = defaultPaths
	}

	p := &planner{
		l: l, ss: ss, o: o, n: n,
		sel:   &Selection{Selected: map[Key][]Path{}, LeftOut: map[string]map[Key][]Path{}, Entries: map[Key]bool{}, EntryPool: map[Key]bool{}},
		split: correlatorsUntold(l, o), undescribed: undescribedBy(o), roots: make([][]int32, len(ss)),
	}

	// a slice's roots are the same in every width
	for i, sl := range ss {
		p.roots[i] = rootsOf(l, sl)
	}

	for _, width := range widths {
		p.width(width)
	}

	return p.sel
}

type planner struct {
	l           *loaded
	ss          []plugins.BindSlice
	o           Options
	n           int
	sel         *Selection
	split       [2][]string
	undescribed func(string) bool
	roots       [][]int32
	samples     map[string]sampled
	sampled     bool
}

func (p *planner) width(width string) {
	l, ss, o := p.l, p.ss, p.o
	dependents := width == Dependents

	// what changed depends on the width: a link counts only where the
	// width follows it
	ch := changesOf(l, o, width)
	untoldWalks := untoldSBOMs(l, ch, o)
	everything := somethingChanged(ch)
	entryOn := on(o, narrowPluginEntry)
	sources, entrySources := p.sources(width, ch, entryOn)

	if !p.sampled {
		p.samples, p.sampled = sampleSuites(l, ss, ch, o), true
	}

	w := walkFrom(l, dependents, sources)

	untold := make([]*untoldWalk, 0, len(untoldWalks))

	for _, u := range untoldWalks {
		untold = append(untold, u.in(l, dependents))
	}

	bySuite, moved := suiteWalks(l, sources, p.samples, width)

	e := entries{paths: map[string][]Path{}}
	entryChanged := len(ch.entryUnits) > 0 || len(ch.correlators) > 0

	if entryChanged {
		e.walk = walkFrom(l, dependents, append(sourcesOf(l, changes{how: entryHow(ch)}), entrySources...))
	}

	for _, suite := range sortedKeys(p.samples) {
		for name, paths := range sampleTargets(l, suite, p.samples[suite]) {
			p.sel.Selected[Key{Suite: suite, Name: name, Width: width}] = paths
		}
	}

	for i, sl := range ss {
		k := Key{Suite: sl.Suite, Name: sl.Name, Width: width}

		sw, mw := w, (*movedWalks)(nil)
		if v, ok := bySuite[sl.Suite]; ok && inSample(sl) {
			sw, mw = v, moved[sl.Suite]
		}

		observed := observedEntry(o, sl, ch)
		sampledEntry := entryOn && entrySampled(sl)

		if sampledEntry {
			observed = ""
		}

		selected := true

		if paths, ok := selectOne(l, sw, untold, sl, p.roots[i], dependents, observed, everything, p.split, p.n, p.undescribed); ok {
			p.sel.Selected[k] = paths
		} else if paths, ok := mw.selects(sl, p.roots[i], dependents); ok {
			p.sel.Selected[k] = paths
		} else {
			selected = false
		}

		if entryOn && (!selected || sampledEntry) {
			p.entry(&e, k, sl, p.roots[i], observedEntry(o, sl, ch), selected, sampledEntry)
		}

		// what an entry reached is the change's own, whatever samples
		if _, ok := p.sel.Selected[k]; ok && entryChanged && p.reaches(&e, k, sl, p.roots[i], observedEntry(o, sl, ch)) {
			p.sel.Entries[k] = true
		}
	}

	for suite, paths := range e.paths {
		p.sel.Selected[Key{Suite: suite, Name: EntryTarget(suite), Width: width}] = paths
	}

	p.finish(width, ch)
}

func (p *planner) finish(width string, ch changes) {
	allSampled(p.ss, width, p.o, p.sel)
	p.nameEntries(width, ch)
}

func (p *planner) nameEntries(width string, ch changes) {
	if len(ch.pinned) == 0 {
		return
	}

	mark := func(paths []Path) {
		for i := range paths {
			if j, ok := p.l.ix[paths[i].Changed]; ok {
				if name, ok := ch.pinned[j]; ok {
					paths[i].Entry = name
				}
			}
		}
	}

	for k, paths := range p.sel.Selected {
		if k.Width == width {
			mark(paths)
		}
	}

	for _, left := range p.sel.LeftOut {
		for k, paths := range left {
			if k.Width == width {
				mark(paths)
			}
		}
	}
}

func (p *planner) sources(width string, ch changes, entryOn bool) ([]source, []source) {
	sources := sourcesOf(p.l, ch)

	entrySources := correlatorSources(p.l, ch)

	if !entryOn {
		sources = append(slices.Clone(sources), entrySources...)
	}

	for _, g := range namesChanged(p.l, p.ss, width, p.o) {
		if _, ok := ch.how[g]; !ok {
			sources = append(slices.Clone(sources), source{at: g, node: g, executed: true})
		}
	}

	return sources, entrySources
}

type entries struct {
	walk  *walk
	paths map[string][]Path
}

func (p *planner) reaches(e *entries, k Key, sl plugins.BindSlice, roots []int32, observed string) bool {
	if !sl.HasList && !sl.Graph {
		return true
	}

	_, ok := entryReach(p.l, e.walk, sl, roots, k.Width == Dependents, observed, p.n, p.undescribed)

	return ok
}

func (p *planner) entry(e *entries, k Key, sl plugins.BindSlice, roots []int32, observed string, selected, sampledEntry bool) {
	paths, ok := entryReach(p.l, e.walk, sl, roots, k.Width == Dependents, observed, p.n, p.undescribed)

	switch {
	case !ok:
	case !sampledEntry:
		p.sel.Selected[k] = paths
	default:
		// the suite's sample runs and is drawn from the slices the entry
		// reaches
		p.sel.EntryPool[k] = true

		if _, ok := e.paths[sl.Suite]; !ok {
			e.paths[sl.Suite] = paths[:1]
		}

		if selected {
			return
		}

		if p.sel.LeftOut[narrowPluginEntry] == nil {
			p.sel.LeftOut[narrowPluginEntry] = map[Key][]Path{}
		}

		p.sel.LeftOut[narrowPluginEntry][k] = paths
	}
}

func suiteWalks(l *loaded, sources []source, samples map[string]sampled, width string) (map[string]*walk, map[string]*movedWalks) {
	dependents := width == Dependents

	bySuite := map[string]*walk{}
	moved := map[string]*movedWalks{}

	for _, suite := range sortedKeys(samples) {
		targets := movedTargets(l, sources, samples[suite])
		if !dependents {
			targets = movedClosure(l, targets)
		}

		keep := len(targets) > movedCap

		bySuite[suite] = walkFrom(l, dependents, withoutSampled(l, sources, samples[suite], keep, width))

		if !keep && len(targets) > 0 {
			moved[suite] = newMovedWalks(l, targets, dependents)
		}
	}

	return bySuite, moved
}

// allSampled selects a suite's sample targets where every slice the sample draws from is selected: it then
// costs nothing, and the forward sample at the dependents width rests on an order the walk can't replay.
func allSampled(ss []plugins.BindSlice, width string, o Options, sel *Selection) {
	if !on(o, narrowSample) {
		return
	}

	targets := []func(string) string{SampleTarget}
	if on(o, narrowSampleBound) {
		targets = append(targets, SampleBoundTarget)
	}

	for suite, sls := range sampledSlices(ss) {
		if len(sls) < 2 {
			continue
		}

		var first []Path

		for _, sl := range sls {
			paths, ok := sel.Selected[Key{Suite: suite, Name: sl.Name, Width: width}]
			if !ok {
				first = nil

				break
			}

			if first == nil {
				first = paths[:min(1, len(paths))]
			}
		}

		if first == nil {
			continue
		}

		for _, target := range targets {
			k := Key{Suite: suite, Name: target(suite), Width: width}
			if _, ok := sel.Selected[k]; !ok {
				sel.Selected[k] = first
			}
		}
	}
}

func namedUnits(l *loaded, sl plugins.BindSlice) map[int32]bool {
	if !sl.Graph || len(sl.Units) == 0 {
		return nil
	}

	out := make(map[int32]bool, len(sl.Units))

	for _, id := range sl.Units {
		if i, ok := l.ix[id]; ok {
			out[i] = true
		}
	}

	return out
}

func selectOne(l *loaded, w *walk, untold []*untoldWalk, sl plugins.BindSlice, roots []int32, dependents bool, observed string, everything bool, split [2][]string, n int, undescribed func(string) bool) ([]Path, bool) {
	listed := sl.HasList

	// a unit no configured sbom lists can't be told unchanged: the slice
	// that touched one depends on every change, and says which unit
	if listed && everything {
		if id, ok := unlistedIn(l, sl, undescribed); ok {
			return []Path{{Listed: id, Changed: id, Steps: []Step{{To: id, Kind: KindUnlisted}}}}, true
		}
	}

	switch {
	case !listed && !sl.Graph:
		// a slice without a list depends on every unit
		return nil, everything
	case listed && observed != "":
		// a correlator records the slices of the suites it observes
		return pinPath(observed), true
	case listed && (len(split[base]) > 0) != (len(split[head]) > 0):
		// a correlator couldn't tell in one tree: there the slice depends
		// on everything, in the other on what it binds
		return everythingBy(slices.Concat(split[base], split[head])[0]), true
	case listed && len(split[base]) > 0 && everything:
		return everythingBy(split[base][0]), true
	}

	var cands []candidate

	named := namedUnits(l, sl)

	for _, r := range roots {
		c, at := w.reached(r)
		if c < 0 || (dependents && sl.Depth > 0 && int(c) > sl.Depth) {
			continue
		}

		cand := w.candidate(r, at, c)

		// a graph-bound slice follows none of its own files' links: what its
		// file reaches is its owner's Bazel graph, which bazel-diff hashes
		if sl.Graph && !named[r] && cand.hops > 0 && !w.cannotTellOnly(at, cand.hops) {
			continue
		}

		cands = append(cands, cand)
	}

	if listed {
		for _, u := range untold {
			if p, ok := u.selects(l, sl, roots); ok {
				cands = append(cands, candidate{path: &p, hops: int32(len(p.Steps))}) //nolint:gosec // a path has few steps
			}
		}
	}

	if len(cands) == 0 {
		return nil, false
	}

	return withNodes(l, cannotTellWhy(l, bestPaths(w, cands, n))), true
}

func sourcesOf(l *loaded, ch changes) []source {
	ids := make([]int32, 0, len(ch.how))
	for i := range ch.how {
		ids = append(ids, i)
	}

	slices.SortFunc(ids, func(a, b int32) int { return strings.Compare(l.ids[a], l.ids[b]) })

	wide := cannotTellByGroup(l)

	out := make([]source, 0, len(ids))

	for _, i := range ids {
		out = append(out, source{at: i, node: i})

		// a set's input unit is not bound with its group whole
		g, ok := l.group[i]
		if !ok || l.input[i] {
			continue
		}

		for _, x := range wide[g] {
			if x != i {
				out = append(out, source{at: x, node: i, from: x, kind: KindCannotTell})
			}
		}
	}

	return out
}

func cannotTellByGroup(l *loaded) map[string][]int32 {
	seen := map[int32]bool{}
	out := map[string][]int32{}

	for _, t := range l.trees {
		if t == nil {
			continue
		}

		for _, id := range sortedKeys(t.CannotTell) {
			i, ok := l.ix[id]
			if !ok || seen[i] {
				continue
			}

			if g, ok := l.group[i]; ok {
				seen[i] = true
				out[g] = append(out[g], i)
			}
		}
	}

	return out
}

func somethingChanged(ch changes) bool {
	if len(ch.correlators) > 0 || len(ch.entryUnits) > 0 {
		return true
	}

	for _, how := range ch.how {
		if how != HowLinks && how != HowCannotTell {
			return true
		}
	}

	return false
}

func correlatorsUntold(l *loaded, o Options) [2][]string {
	var out [2][]string

	for tr, t := range l.trees {
		if t == nil {
			continue
		}

		for _, name := range sortedKeys(t.Untold) {
			if p, ok := pluginNamed(o.Plugins, name); !ok || !p.SBOM {
				out[tr] = append(out[tr], name)
			}
		}
	}

	return out
}

func everythingBy(correlator string) []Path {
	id := pinPrefix + correlator

	return []Path{{Changed: id, Steps: []Step{{To: id, Kind: KindEverything}}}}
}

func rootsOf(l *loaded, sl plugins.BindSlice) []int32 {
	out := make([]int32, 0, len(sl.List)+len(sl.Units))

	add := func(id string) {
		if i, ok := l.ix[id]; ok {
			out = append(out, i)
		}
	}

	if sl.HasList {
		for _, id := range sl.List {
			add(id)
		}
	}

	for _, id := range sl.Units {
		add(id)
	}

	for _, f := range sl.Sources {
		add(deps.FileID(f))

		for _, t := range l.trees {
			if t != nil {
				for _, id := range t.FileUnits[f] {
					add(id)
				}
			}
		}
	}

	for _, f := range sl.Sources {
		out = append(out, l.byGroup[f]...)

		for _, i := range l.byFile[f] {
			if e := l.entry[i]; !l.notRoot[i] && (e == "" || len(sl.Tests) == 0 || slices.Contains(sl.Tests, e)) {
				out = append(out, i)
			}
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func undescribedBy(o Options) func(id string) bool {
	var (
		described []string
		sboms     bool
	)

	for _, p := range o.Plugins {
		if !p.SBOM {
			continue
		}

		if len(p.Namespaces) == 0 {
			return func(string) bool { return false }
		}

		sboms = true

		described = append(described, p.Namespaces...)
	}

	// without an sbom configured, what is described isn't known here
	if !sboms {
		return func(string) bool { return false }
	}

	return func(id string) bool {
		first, _, ok := strings.Cut(id, ":")

		// the core's own ids name files and plugins, never an sbom's unit
		switch first {
		case "file", "plugin", "group", "node", "unbound":
			return false
		}

		// a namespace may have parts of its own (dep:npm)
		return ok && !slices.ContainsFunc(described, func(ns string) bool { return strings.HasPrefix(id, ns+":") })
	}
}

func unlistedIn(l *loaded, sl plugins.BindSlice, undescribed func(string) bool) (string, bool) {
	for _, id := range sl.List {
		if _, held := l.ix[id]; !held && undescribed(id) {
			return id, true
		}
	}

	return "", false
}

func cannotTellWhy(l *loaded, paths []Path) []Path {
	for i, p := range paths {
		if len(p.Steps) != 1 || p.Steps[0].Kind != KindCannotTell {
			continue
		}

		id := p.Steps[0].From

		for _, t := range l.trees {
			if t == nil {
				continue
			}

			if why, ok := t.CannotTell[id]; ok {
				paths[i].Why = why
				if by := t.CannotTellBy[id]; by != "" {
					paths[i].Why = by + ": " + why
				}

				break
			}
		}
	}

	return paths
}

func withNodes(l *loaded, paths []Path) []Path {
	for i, p := range paths {
		ids := make([]string, 0, 2+2*len(p.Steps))
		ids = append(ids, p.Listed, p.Changed)

		for _, st := range p.Steps {
			ids = append(ids, st.From, st.To)
		}

		nodes := map[string]Node{}

		for _, id := range ids {
			if _, done := nodes[id]; done || id == "" {
				continue
			}

			for _, tr := range []int{head, base} {
				t := l.trees[tr]
				if t == nil {
					continue
				}

				if u, ok := t.Units[id]; ok {
					nodes[id] = Node{Label: u.Label, File: u.File, Plugin: t.Providers[id]}

					break
				}
			}
		}

		paths[i].Nodes = nodes
	}

	return paths
}
