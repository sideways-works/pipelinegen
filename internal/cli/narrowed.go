package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/depsrepo"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

// planBook keeps what each narrowing kept out of each slice, so the plan can say which slices a
// narrowing left out of a change.
type planBook struct {
	set *plugins.Set

	mu             sync.Mutex
	trees          map[string]*bookTree
	unlisted       map[string]map[string][]string
	unlistedSeen   map[string]bool
	alone          map[string]map[string][]string
	aloneSeen      map[string]bool
	unobservedSeen map[string]bool
	// workspace is the Bazel workspace's directory in the repository, where labels' paths start.
	workspace string
}

func (b *planBook) noList(commit, suite, name string) {
	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.unlisted, b.unlistedSeen = noteSlice(b.unlisted, b.unlistedSeen, commit, suite, name)
}

func (b *planBook) aloneSlice(commit, suite, name string) {
	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.alone, b.aloneSeen = noteSlice(b.alone, b.aloneSeen, commit, suite, name)
}

func noteSlice(m map[string]map[string][]string, seen map[string]bool, commit, suite, name string) (map[string]map[string][]string, map[string]bool) {
	if m == nil {
		m, seen = map[string]map[string][]string{}, map[string]bool{}
	}

	if m[commit] == nil {
		m[commit] = map[string][]string{}
	}

	if key := commit + "\x00" + suite + "\x00" + name; !seen[key] {
		seen[key] = true

		m[commit][suite] = append(m[commit][suite], name)
	}

	return m, seen
}

func (b *planBook) aloneHook() func(commit, suite string) []string {
	if b == nil {
		return nil
	}

	return func(commit, suite string) []string {
		b.mu.Lock()
		defer b.mu.Unlock()

		return slices.Clone(b.alone[commit][suite])
	}
}

func (b *planBook) unlistedHook() func(commit, suite string) []string {
	if b == nil {
		return nil
	}

	return func(commit, suite string) []string {
		b.mu.Lock()
		defer b.mu.Unlock()

		return slices.Clone(b.unlisted[commit][suite])
	}
}

func (b *planBook) markUnlisted(plan *affected.Plan) {
	if b == nil || plan == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for i := range plan.Suites {
		names := slices.Clone(b.unlisted[plan.Head][plan.Suites[i].Suite])
		slices.Sort(names)
		plan.Suites[i].Unlisted = names
	}
}

type bookTree struct {
	units      map[string]deps.Unit
	narrowings []string
	slices     map[string]bookSlice // by suite/name/width
	bound      map[string]bookBound

	unbound  *unboundBook
	leaves   map[string]*leafInputs
	declared map[string][]string
}

type bookSlice struct {
	dropped, groups map[string][]string
}

type bookBound struct {
	units []string
	why   map[string]deps.Why
	// everything marks a slice bound to every unit; its reason says so rather than naming a target.
	everything bool
	data       []string
}

func newPlanBook(set *plugins.Set) *planBook {
	return &planBook{set: set, trees: map[string]*bookTree{}}
}

func (b *planBook) keep(commit string, t *deps.Tree, ss []deps.Slice) {
	if b == nil {
		return
	}

	bt := &bookTree{units: t.Units, narrowings: t.Narrowings, slices: make(map[string]bookSlice, len(ss)), bound: make(map[string]bookBound, len(ss))}

	for _, d := range ss {
		bt.bound[depsrepo.Selector(d)] = bookBound{units: d.Units, why: d.Why, everything: d.Everything, data: d.Data}

		if len(d.Dropped) > 0 || len(d.DroppedGroups) > 0 {
			bt.slices[d.Suite+"/"+d.Name+"/"+d.Width] = bookSlice{dropped: d.Dropped, groups: d.DroppedGroups}
		}
	}

	bt.unbound = newUnboundBook(t, ss)

	b.mu.Lock()
	b.trees[commit] = bt
	b.mu.Unlock()
}

// A tree or slice the book never kept is an error: no labels and labels never recorded must not look alike.
func (b *planBook) inputsHook() func(commit, suite, slice string) ([]string, error) {
	if b == nil {
		return nil
	}

	return func(commit, suite, slice string) ([]string, error) { return b.inputs(commit, "", suite, slice) }
}

var errNoInputs = errors.New("the plan kept no Bazel inputs")

// inputs are a slice's labels at commit; other is the plan's other tree, whose leaves decide which
// filegroups go back whole and which files were added or removed. A suite's setup part takes the
// files whose adding or removing shows there.
func (b *planBook) inputs(commit, other, suite, slice string) ([]string, error) {
	b.mu.Lock()
	bt, ot := b.trees[commit], b.trees[other]
	b.mu.Unlock()

	if bt == nil {
		return nil, fmt.Errorf("%w for commit %s", errNoInputs, commit)
	}

	if slice == recording.SetupTarget(suite) {
		return setupInputs(bt, ot, suite), nil
	}

	if bt.leaves != nil {
		li, ok := bt.leaves[suite+"/"+slice]
		if !ok {
			return nil, fmt.Errorf("%w for %s/%s at commit %s", errNoInputs, suite, slice, commit)
		}

		return li.labels(otherLeaves(ot, suite, slice)), nil
	}

	rec, ok := bt.bound[depsrepo.Selector(deps.Slice{Suite: suite, Name: slice})]
	if !ok {
		return nil, fmt.Errorf("%w for %s/%s at commit %s", errNoInputs, suite, slice, commit)
	}

	return rec.data, nil
}

func otherLeaves(ot *bookTree, suite, slice string) *leafInputs {
	if ot == nil {
		return nil
	}

	return ot.leaves[suite+"/"+slice]
}

func setupInputs(bt, ot *bookTree, suite string) []string {
	out := []string{}

	if ot == nil {
		return out
	}

	for key, li := range bt.leaves {
		if s, slice, _ := strings.Cut(key, "/"); s == suite {
			_, setup := li.given(otherLeaves(ot, suite, slice))
			out = append(out, setup...)
		}
	}

	slices.Sort(out)

	return slices.Compact(out)
}

// through names the filegroups a slice's input was found through, innermost first.
func (b *planBook) through(commit, suite, slice, label string) []string {
	b.mu.Lock()
	bt := b.trees[commit]
	b.mu.Unlock()

	if bt == nil || bt.leaves[suite+"/"+slice] == nil {
		return nil
	}

	return bt.leaves[suite+"/"+slice].through(label)
}

// keepInputs records each walked slice's data as its selector would hold it, judged leaf by leaf
// (plugins.Leaves): a file whose units the slice touched and a BUILD file are given up, everything else
// stays whole. The inputs are made once both trees are kept.
func (b *planBook) keepInputs(cfg *config.Config, commit string, t *deps.Tree, bs []plugins.BindSlice) {
	if b == nil {
		return
	}

	f := plugins.NewLeaves(cfg, t, bs)
	in := make(map[string]*leafInputs, len(bs))
	declared := map[string][]string{}

	for _, sl := range bs {
		if sl.Setup {
			continue
		}

		in[sl.Suite+"/"+sl.Name] = newLeafInputs(f, sl, func(p string) { b.unobserved(sl.Suite, t.ByUnits[p], p) })

		if len(sl.Declared) > 0 {
			declared[sl.Suite+"/"+sl.Name] = sl.Declared
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	bt := b.trees[commit]
	if bt == nil {
		bt = &bookTree{units: t.Units, slices: map[string]bookSlice{}, bound: map[string]bookBound{}}
		b.trees[commit] = bt
	}

	bt.leaves, bt.declared = in, declared

	if cfg != nil {
		b.workspace = cfg.Bazel.Workspace
	}
}

func (b *planBook) declaredHook() func(commit, suite, slice string) []string {
	if b == nil {
		return nil
	}

	return func(commit, suite, slice string) []string {
		b.mu.Lock()
		defer b.mu.Unlock()

		if bt := b.trees[commit]; bt != nil {
			return bt.declared[suite+"/"+slice]
		}

		return nil
	}
}

func (b *planBook) unobserved(suite, narrowing, path string) {
	if b.set == nil {
		return
	}

	key := suite + "\x00" + narrowing

	b.mu.Lock()
	seen := b.unobservedSeen[key]

	if b.unobservedSeen == nil {
		b.unobservedSeen = map[string]bool{}
	}

	b.unobservedSeen[key] = true
	b.mu.Unlock()

	if !seen {
		b.set.Warnf("by-units-unobserved: suite %s: no recording of it holds units of the files %s narrows (%s, say), so they bind its slices whole; "+
			"list a correlator that records them in the suite's correlators to narrow them", suite, narrowing, path)
	}
}

func writtenHook(b *planBook) func(tree string) (string, error) {
	if b == nil {
		return nil
	}

	return depsrepo.ReadKey
}

// A deps repository holds a digest for every unit of its tree.
func (b *planBook) expectHook() func(commit string) int {
	if b == nil {
		return nil
	}

	return func(commit string) int {
		b.mu.Lock()
		defer b.mu.Unlock()

		if bt := b.trees[commit]; bt != nil {
			return len(bt.units)
		}

		return 0
	}
}

func (b *planBook) whyHook() func(base, head, target string, cause affected.RootCause) ([]affected.Reason, bool) {
	if b == nil {
		return nil
	}

	return b.why
}

const maxUnitReasons = 3

func (b *planBook) why(base, head, target string, cause affected.RootCause) ([]affected.Reason, bool) {
	if !strings.HasPrefix(cause.Label, depsLabelPrefix) {
		return nil, false
	}

	return b.direct(base, head, target)
}

// A recorded slice's reasons come from its bindings, with no bazel-diff process.
func (b *planBook) reasonsHook() func(base, head, target string) ([]affected.Reason, bool) {
	if b == nil {
		return nil
	}

	return b.direct
}

// direct is false for a selector that isn't a recorded slice's, or one none of whose units changed:
// bazel-diff explains those.
func (b *planBook) direct(base, head, target string) ([]affected.Reason, bool) {
	if !strings.HasPrefix(target, depsLabelPrefix+"slices:") {
		return nil, false
	}

	b.mu.Lock()
	tb, th := b.trees[base], b.trees[head]
	b.mu.Unlock()

	if tb == nil || th == nil {
		return nil, false
	}

	sel := strings.TrimPrefix(target, depsLabelPrefix+"slices:")

	rec, ok := th.bound[sel]
	if !ok {
		if rec, ok = tb.bound[sel]; !ok {
			return nil, false
		}
	}

	if rec.everything {
		return []affected.Reason{{Kind: affected.KindEverything, What: boundToEverything}}, true
	}

	changed := changedUnits(tb.units, th.units)

	var out []affected.Reason

	for _, id := range slices.Sorted(slices.Values(rec.units)) {
		// its own files' units are the test reasons' to name.
		if !changed[id] || rec.why[id].Kind == whyOwnFile {
			continue
		}

		out = append(out, unitReason(id, rec.why))
		if len(out) == maxUnitReasons {
			break
		}
	}

	return out, len(out) > 0
}

const boundToEverything = "it is bound to every unit the plugins list: it has no list yet, or a plugin couldn't tell what it touched"

const whyPinned = "plugin"

const whyOwnFile = "own"

const depsLabelPrefix = "@pipelinegen_deps//"

func unitReason(id string, why map[string]deps.Why) affected.Reason {
	w := why[id]

	if name, ok := strings.CutPrefix(id, "plugin:"); ok && w.Kind == whyPinned {
		return affected.Reason{Kind: affected.KindPlugin, What: name}
	}

	if group, ok := unboundGroup(id); ok {
		return affected.Reason{Kind: affected.KindDepends, What: "the names nothing binds in " + group}
	}

	if w.Kind == whyExecuted {
		return affected.Reason{Kind: affected.KindExecuted, What: id}
	}

	r := affected.Reason{Kind: affected.KindDepends, What: id}

	for via, seen := w.Via, map[string]bool{id: true}; via != "" && !seen[via]; via = why[via].Via {
		seen[via] = true
		r.Through = append(r.Through, via)
	}

	return r
}

func unboundGroup(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, "unbound:")
	if !ok {
		return "", false
	}

	_, group, ok := strings.Cut(rest, ":")

	return group, ok
}

func (b *planBook) narrowedHook() func(base, head, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
	if b == nil {
		return nil
	}

	return b.narrowed
}

// narrowed names, per narrowing, the slices the change left out that its wide rule would have selected.
func (b *planBook) narrowed(base, head, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
	b.mu.Lock()
	tb, th := b.trees[base], b.trees[head]
	b.mu.Unlock()

	if tb == nil || th == nil {
		return nil
	}

	return namedLeftOut(leftOutBy(tb, th, suite, width, left), b.set.OffAt, len(selected))
}

func (b *planBook) entryPool(base, head, suite, width string) map[string]bool {
	b.mu.Lock()
	tb, th := b.trees[base], b.trees[head]
	b.mu.Unlock()

	if tb == nil || th == nil {
		return nil
	}

	changed := changedUnits(tb.units, th.units)
	out := map[string]bool{}

	for _, bt := range []*bookTree{tb, th} {
		for key, rec := range bt.slices {
			s, rest, ok := strings.Cut(key, "/")
			if !ok || s != suite || !strings.HasSuffix(rest, "/"+width) {
				continue
			}

			if _, ok := firstIn(rec.dropped[plugins.NarrowPluginEntry], changed); ok {
				out[strings.TrimSuffix(rest, "/"+width)] = true
			}
		}
	}

	return out
}

type narrowHit struct {
	narrowing, what string
}

// A slice several narrowings left out is named under each: it runs from each one's level up.
func namedLeftOut(hits map[string][]narrowHit, offAt func(string) int, selected int) []affected.Narrowed {
	type entry struct {
		slices []string
		what   string
	}

	by := map[string]*entry{}

	for _, slice := range sortedKeys(hits) {
		for _, h := range hits[slice] {
			e := by[h.narrowing]
			if e == nil {
				e = &entry{what: h.what}
				by[h.narrowing] = e
			}

			e.slices = append(e.slices, slice)
		}
	}

	out := make([]affected.Narrowed, 0, len(by))

	for _, n := range sortedKeys(by) {
		e := by[n]

		reason := e.what + " changed, which only the wide rule binds to these slices"
		if n == plugins.NarrowPluginEntry {
			reason = e.what + " changed: each suite it reaches runs its sample of the recorded slices it reaches instead of all of them, and the nightly runs the rest"
		}

		out = append(out, affected.Narrowed{
			Kind: affected.NarrowedBy, Narrowing: n, OffAt: offAt(n),
			Reason:  reason,
			Reached: selected + len(e.slices), Dropped: e.slices,
		})
	}

	return out
}

func leftOutBy(tb, th *bookTree, suite, width string, left []bazel.Slice) map[string][]narrowHit {
	changed := changedUnits(tb.units, th.units)
	hits := map[string][]narrowHit{}

	note := func(n, slice, what string) {
		if !slices.ContainsFunc(hits[slice], func(h narrowHit) bool { return h.narrowing == n }) {
			hits[slice] = append(hits[slice], narrowHit{n, what})
		}
	}

	for _, sl := range left {
		key := suite + "/" + sl.Name + "/" + width

		for _, bt := range []*bookTree{tb, th} {
			rec := bt.slices[key]

			for _, n := range sortedKeys(rec.dropped) {
				if id, ok := firstIn(rec.dropped[n], changed); ok {
					if name, pin := strings.CutPrefix(id, pinPrefix); pin {
						id = entryWhat(name)
					}

					note(n, sl.Name, id)
				}
			}

			for _, n := range sortedKeys(rec.groups) {
				// While unreached-new is on no slice depends on a group's unbound names, so another name rule
				// switched off alone selects nothing through them.
				if n != graph.UnreachedNew && slices.Contains(bt.narrowings, graph.UnreachedNew) {
					continue
				}

				i := slices.IndexFunc(rec.groups[n], func(g string) bool {
					return tb.unbound.names(n, width, g) != th.unbound.names(n, width, g)
				})
				if i >= 0 {
					note(n, sl.Name, "what nothing binds in "+rec.groups[n][i])
				}
			}
		}
	}

	return hits
}

func changedUnits(base, head map[string]deps.Unit) map[string]bool {
	ids := map[string]bool{}

	for id, h := range head {
		if bu, ok := base[id]; !ok || bu.Digest != h.Digest {
			ids[id] = true
		}
	}

	for id := range base {
		if _, ok := head[id]; !ok {
			ids[id] = true
		}
	}

	return ids
}

// Where no correlator listed a group (one lists names only while unreached-new is off), the core counts
// the group's units no slice binds itself.
type unboundBook struct {
	given map[string]map[string]deps.Unbound

	groups map[string][]string        // unit ids by group
	bound  map[string]map[string]bool // by width
	whole  map[string]map[string]bool // groups held whole, by width

	mu   sync.Mutex
	memo map[string]string
}

// The sample moves units out of the slices later, but they are bound all the same.
func newUnboundBook(t *deps.Tree, ss []deps.Slice) *unboundBook {
	u := &unboundBook{
		given: t.Unbound, memo: map[string]string{}, groups: groupsOf(t.Units),
		bound: map[string]map[string]bool{}, whole: map[string]map[string]bool{},
	}

	for _, d := range ss {
		if d.Everything {
			continue
		}

		if u.bound[d.Width] == nil {
			u.bound[d.Width], u.whole[d.Width] = map[string]bool{}, map[string]bool{}
		}

		for _, id := range d.Units {
			u.bound[d.Width][id] = true
		}

		for _, g := range d.Groups {
			u.whole[d.Width][g] = true
		}
	}

	return u
}

func groupsOf(units map[string]deps.Unit) map[string][]string {
	out := map[string][]string{}

	for id, u := range units {
		if u.Group != "" {
			out[u.Group] = append(out[u.Group], id)
		}
	}

	return out
}

func (u *unboundBook) names(n, width, group string) string {
	u.mu.Lock()
	defer u.mu.Unlock()

	key := n + "\x00" + width + "\x00" + group
	if v, ok := u.memo[key]; ok {
		return v
	}

	var names []string

	if ub, ok := u.given[width][group]; ok {
		names = append(slices.Clone(ub.Names), ub.Narrowed[n]...)
	} else {
		names = u.counted(width, group)
	}

	slices.Sort(names)
	v := strings.Join(slices.Compact(names), "\n")
	u.memo[key] = v

	return v
}

func (u *unboundBook) counted(width, group string) []string {
	if u.whole[width][group] {
		return nil
	}

	var out []string

	for _, id := range u.groups[group] {
		if !u.bound[width][id] {
			out = append(out, id)
		}
	}

	return out
}

func firstIn(list []string, in map[string]bool) (string, bool) {
	for _, v := range list {
		if in[v] {
			return v, true
		}
	}

	return "", false
}
