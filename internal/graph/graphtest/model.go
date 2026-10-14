package graphtest

import (
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// Model binds slices forward as the graph's contract says
// (docs/internals/graph.md). Over plain links alone it binds exactly as
// plugins.Set.Bind does without bind correlators.
type Model struct {
	Plugins       []graph.Plugin
	UnreachedNew  bool
	ReferencedNew bool
	Observes      func(suite, correlator string) bool
}

// Bind binds each slice in both widths in one tree.
func (m Model) Bind(t *deps.Tree, ss []plugins.BindSlice) []deps.Slice {
	l := newModelLinks(t)
	out := make([]deps.Slice, 0, 2*len(ss))

	for _, sl := range ss {
		ex := m.slice(t, sl, plugins.Executed)
		dep := m.slice(t, sl, plugins.Dependents)

		if sl.HasList || sl.Graph {
			state := l.executed(t, &ex, sl)
			l.dependents(t, &dep, &ex, state, sl)
		}

		out = append(out, ex, dep)
	}

	if !m.UnreachedNew && !m.correlatorUntold(t) {
		m.unbound(t, ss, out)
	}

	for i := range out {
		sl := ss[i/2]
		d := &out[i]

		if sl.HasList {
			m.untold(t, d, sl)
		}

		m.pins(t, d, sl)
		sort.Strings(d.Units)
		sort.Strings(d.Groups)
		sort.Strings(d.Linked)
	}

	return out
}

func (m Model) slice(t *deps.Tree, sl plugins.BindSlice, width string) deps.Slice {
	return deps.Slice{
		Suite: sl.Suite, Name: sl.Name, Width: width, Why: map[string]deps.Why{},
		Everything: !sl.HasList && !sl.Graph, Data: dataOf(t, sl),
		Dropped: map[string][]string{}, DroppedGroups: map[string][]string{},
	}
}

func dataOf(t *deps.Tree, sl plugins.BindSlice) []string {
	var out []string

	for _, label := range sl.Data {
		path, ok := strings.CutPrefix(label, "//:")
		if _, hasUnits := t.FileUnits[path]; ok && hasUnits && sl.HasList {
			continue
		}

		out = append(out, label)
	}

	return out
}

type modelLinks struct {
	forward, reverse map[string][]string
	typed            map[string][]typedLink
}

type typedLink struct{ to, kind string }

func newModelLinks(t *deps.Tree) *modelLinks {
	l := &modelLinks{forward: map[string][]string{}, reverse: map[string][]string{}, typed: map[string][]typedLink{}}

	for _, ln := range t.Links {
		l.forward[ln.From] = append(l.forward[ln.From], ln.To)
		l.reverse[ln.To] = append(l.reverse[ln.To], ln.From)
	}

	for _, ln := range t.Typed {
		l.typed[ln.From] = append(l.typed[ln.From], typedLink{to: ln.To, kind: ln.Kind})
	}

	return l
}

func modelRoots(t *deps.Tree, sl plugins.BindSlice) map[string]deps.Why {
	out := map[string]deps.Why{}

	for _, id := range sl.List {
		out[id] = deps.Why{Kind: "executed"}
	}

	for _, id := range sl.Units {
		out[id] = deps.Why{Kind: "data"}
	}

	for _, id := range ownUnits(t, sl) {
		out[id] = deps.Why{Kind: whyOwn}
	}

	return out
}

func ownUnits(t *deps.Tree, sl plugins.BindSlice) []string {
	var out []string

	for _, f := range sl.Sources {
		out = append(out, deps.FileID(f))
		out = append(out, t.FileUnits[f]...)
	}

	for id, u := range t.Units {
		if slices.Contains(sl.Sources, u.Group) || (u.File != "" && slices.Contains(sl.Sources, u.File)) {
			out = append(out, id)
		}
	}

	return out
}

const (
	unbound = iota
	boundOnly
	expandedState
)

func (l *modelLinks) executed(t *deps.Tree, d *deps.Slice, sl plugins.BindSlice) map[string]int {
	state := map[string]int{}

	var queue []string

	visit := func(id string, s int, why deps.Why) {
		if state[id] >= s {
			return
		}

		if state[id] == unbound {
			d.Why[id] = why
			addUnit(t, d, id)
		}

		state[id] = s
		queue = append(queue, id)
	}

	roots := modelRoots(t, sl)
	if sl.Graph {
		// a graph-bound slice holds its own files' units, not walked from;
		// what they reach is the owner's graph
		for _, id := range ownUnits(t, sl) {
			if _, ok := d.Why[id]; !ok {
				d.Why[id] = deps.Why{Kind: whyOwn}
				addUnit(t, d, id)
			}

			state[id] = boundOnly
		}

		roots = map[string]deps.Why{}
		for _, id := range sl.Units {
			roots[id] = deps.Why{Kind: "data"}
		}
	}

	for _, id := range sortedKeys(roots) {
		visit(id, expandedState, roots[id])
	}

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]

		for _, to := range l.forward[id] {
			visit(to, expandedState, deps.Why{Kind: "closure", Via: id})
		}

		if state[id] != expandedState {
			continue
		}

		for _, tl := range l.typed[id] {
			switch tl.kind {
			case graph.KindBind:
				visit(tl.to, boundOnly, deps.Why{Kind: tl.kind, Via: id})
			case graph.KindDependentsOnly:
			default:
				visit(tl.to, expandedState, deps.Why{Kind: tl.kind, Via: id})
			}
		}
	}

	return state
}

func (l *modelLinks) dependents(t *deps.Tree, d, ex *deps.Slice, state map[string]int, sl plugins.BindSlice) {
	depth := sl.Depth
	steps := map[string]int{}

	var q stepQueue

	reach := func(id string, b int, why deps.Why, ahead bool) {
		if depth > 0 && b > depth {
			return
		}

		if have, ok := steps[id]; ok && have <= b {
			return
		}

		if _, ok := steps[id]; !ok {
			d.Why[id] = why
			addUnit(t, d, id)
		}

		steps[id] = b
		q.push(step{id: id, back: b}, ahead)
	}

	for _, id := range sortedKeys(ex.Why) {
		if sl.Graph && state[id] == boundOnly {
			d.Why[id] = ex.Why[id]
			addUnit(t, d, id)

			continue
		}

		reach(id, 0, ex.Why[id], false)
	}

	for it, ok := q.pop(); ok; it, ok = q.pop() {
		if steps[it.id] != it.back {
			continue
		}

		for _, to := range l.forward[it.id] {
			reach(to, it.back, deps.Why{Kind: "closure", Via: it.id}, true)
		}

		for _, tl := range l.typed[it.id] {
			if strings.HasPrefix(tl.to, graph.NodePrefix) {
				reach(tl.to, it.back, deps.Why{Kind: tl.kind, Via: it.id}, true)
			} else {
				reach(tl.to, it.back+1, deps.Why{Kind: tl.kind, Via: it.id}, false)
			}
		}

		for _, from := range l.reverse[it.id] {
			reach(from, it.back+1, deps.Why{Kind: "dependent", Via: it.id}, false)
		}
	}
}

func addUnit(t *deps.Tree, d *deps.Slice, id string) {
	u, ok := t.Units[id]
	if !ok {
		return
	}

	d.Units = append(d.Units, id)

	if _, wide := t.CannotTell[id]; wide && u.Group != "" && !slices.Contains(d.Groups, u.Group) {
		d.Groups = append(d.Groups, u.Group)
	}
}

func (m Model) untold(t *deps.Tree, d *deps.Slice, sl plugins.BindSlice) {
	for _, name := range sortedKeys(t.Untold) {
		p, ok := m.plugin(name)
		if !ok || !p.SBOM {
			d.Everything = true

			continue
		}

		if !reachesNamespaces(d, sl, p.Namespaces, t.Untold[name]) {
			continue
		}

		for _, f := range t.Untold[name] {
			id := deps.FileID(f)
			if !slices.Contains(d.Units, id) {
				d.Units = append(d.Units, id)
			}

			if _, ok := d.Why[id]; !ok {
				d.Why[id] = deps.Why{Kind: "cannot-tell", By: name}
			}
		}
	}
}

func (m Model) correlatorUntold(t *deps.Tree) bool {
	for name := range t.Untold {
		if p, ok := m.plugin(name); !ok || !p.SBOM {
			return true
		}
	}

	return false
}

func reachesNamespaces(d *deps.Slice, sl plugins.BindSlice, namespaces, files []string) bool {
	if len(namespaces) == 0 {
		return true
	}

	for _, src := range sl.Sources {
		if slices.Contains(files, src) {
			return true
		}
	}

	for id := range d.Why {
		for _, ns := range namespaces {
			if strings.HasPrefix(id, ns+":") {
				return true
			}
		}
	}

	return false
}

func (m Model) plugin(name string) (graph.Plugin, bool) {
	for _, p := range m.Plugins {
		if p.Name == name {
			return p, true
		}
	}

	return graph.Plugin{}, false
}

const whyOwn = "own"

func (m Model) pins(t *deps.Tree, d *deps.Slice, sl plugins.BindSlice) {
	if d.Everything {
		return
	}

	pin := func(name string) {
		id, ok := t.Pins[name]
		if !ok || slices.Contains(d.Units, id) {
			return
		}

		d.Units = append(d.Units, id)

		if _, ok := d.Why[id]; !ok {
			d.Why[id] = deps.Why{Kind: "plugin"}
		}
	}

	for _, id := range slices.Clone(d.Units) {
		if p, ok := t.Providers[id]; ok {
			pin(p)
		}
	}

	for id, p := range t.Providers {
		if slices.Contains(d.Groups, t.Units[id].Group) {
			pin(p)
		}
	}

	for _, p := range m.Plugins {
		if p.SBOM {
			continue
		}

		observed := m.Observes == nil || m.Observes(sl.Suite, p.Name)
		answers := plugins.AnswerUnits(t, p.Name)

		held := slices.ContainsFunc(d.Units, func(id string) bool { return answers[id] && (!sl.Graph || d.Why[id].Kind != whyOwn) })

		if sl.HasList && observed || held {
			pin(p.Name)
		}
	}
}

func (m Model) unbound(t *deps.Tree, ss []plugins.BindSlice, out []deps.Slice) {
	boundBy := m.boundByAny(t, ss, out)

	names := map[string][]string{}

	// a set's input unit no file declares (a package's API) keys answers:
	// it is no name
	for id, u := range t.Units {
		if u.Group != "" && !strings.HasPrefix(id, "file:") && !strings.HasPrefix(id, graph.NodePrefix) && !summaryInput(t, id) {
			names[u.Group] = append(names[u.Group], id)
		}
	}

	for i := range out {
		d := &out[i]
		if !ss[i/2].HasList {
			continue
		}

		// a slice's binaries link the same packages whatever the width, so
		// the groups it links are those of its executed binding
		linked := out[i-i%2].Why

		for _, id := range sortedKeys(linked) {
			g, ok := strings.CutPrefix(id, graph.GroupPrefix)
			if !ok {
				continue
			}

			var free []string

			for _, n := range names[g] {
				if !boundBy[d.Width][n] {
					free = append(free, n)
				}
			}

			d.Linked = append(d.Linked, g)

			uid := deps.UnboundID(d.Width, g)
			t.Units[uid] = deps.Unit{ID: uid, Digest: digestOf(free)}
			d.Units = append(d.Units, uid)
			d.Why[uid] = deps.Why{Kind: "unbound"}
		}
	}
}

func (m Model) boundByAny(t *deps.Tree, ss []plugins.BindSlice, out []deps.Slice) map[string]map[string]bool {
	boundBy := map[string]map[string]bool{}

	for i, d := range out {
		if !ss[i/2].HasList {
			continue
		}

		if boundBy[d.Width] == nil {
			boundBy[d.Width] = map[string]bool{}
		}

		for _, id := range d.Units {
			boundBy[d.Width][id] = true
		}
	}

	if !m.ReferencedNew {
		return boundBy
	}

	for _, bound := range boundBy {
		var more []string

		for _, l := range slices.Concat(t.Links, t.Typed) {
			if l.Ref && bound[l.From] {
				more = append(more, l.To)
			}
		}

		for _, id := range more {
			bound[id] = true
		}
	}

	return boundBy
}

func digestOf(names []string) string {
	s := slices.Clone(names)
	slices.Sort(s)

	return strings.Join(s, ",")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}

type step struct {
	id   string
	back int
}

// stepQueue is a deque: a free step goes to the head and a costly one to the
// tail, so steps leave in order of cost.
type stepQueue struct {
	head []step // its last element is next
	tail []step
}

func (q *stepQueue) push(s step, ahead bool) {
	if ahead {
		q.head = append(q.head, s)
	} else {
		q.tail = append(q.tail, s)
	}
}

func (q *stepQueue) pop() (step, bool) {
	if n := len(q.head); n > 0 {
		s := q.head[n-1]
		q.head = q.head[:n-1]

		return s, true
	}

	if len(q.tail) == 0 {
		return step{}, false
	}

	s := q.tail[0]
	q.tail = q.tail[1:]

	return s, true
}

// AsBinder rewrites the reasons of what typed links bound as a bind
// correlator gives them, so planning's sample split reads them as it reads
// the Go correlator's.
func AsBinder(t *deps.Tree) After {
	into := map[string][]string{}

	for _, l := range slices.Concat(t.Links, t.Typed) {
		if l.Kind == graph.KindBind || l.Kind == graph.KindDependentsOnly {
			continue
		}

		into[l.To] = append(into[l.To], l.From)
	}

	return func(_ *deps.Tree, bound []deps.Slice) []deps.Slice {
		for _, d := range bound {
			for id, w := range d.Why {
				switch {
				case w.Kind == graph.KindFollow && strings.HasPrefix(w.Via, graph.GroupPrefix):
					d.Why[id] = deps.Why{Kind: "init", By: modelName, Via: w.Via}
				case w.Kind == graph.KindFollow:
					var vias []string

					for _, from := range into[id] {
						if _, ok := d.Why[from]; ok {
							vias = append(vias, from)
						}
					}

					d.Why[id] = deps.Why{Kind: "references", By: modelName, Via: w.Via, Vias: vias}
				case w.Kind == graph.KindBind || w.Kind == graph.KindDependentsOnly:
					d.Why[id] = deps.Why{Kind: w.Kind, By: modelName, Via: w.Via}
				}
			}
		}

		return bound
	}
}

const modelName = "model"

func summaryInput(t *deps.Tree, id string) bool {
	return t.Units[id].File == "" && slices.Contains(t.InputUnits, id)
}
