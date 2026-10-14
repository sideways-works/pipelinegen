package graphtest

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// Comparison is the walk's selection set against the oracle's, in three
// classes.
type Comparison struct {
	Same        int
	Explained   map[Key]string
	Joined      map[Key]string
	Sampled     map[Key]string
	Kept        map[Key]string
	Missing     map[Key]string
	Unexplained map[Key]string
	BadPaths    map[Key]string
}

// OK tells whether nothing failed.
func (c Comparison) OK() bool {
	return len(c.Missing) == 0 && len(c.Unexplained) == 0 && len(c.BadPaths) == 0
}

// String lists every slice outside the first class, one a line, failures
// first.
func (c Comparison) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "%d the same, %d only the walk selects through a one-tree link, %d only through a junction, %d sample targets only the walk selects beside every slice, %d only through a sampled unit kept as a source, %d only the oracle selects, %d only the walk selects otherwise, %d bad paths\n",
		c.Same, len(c.Explained), len(c.Joined), len(c.Sampled), len(c.Kept), len(c.Missing), len(c.Unexplained), len(c.BadPaths))

	for _, part := range []struct {
		what string
		m    map[Key]string
	}{
		{"only the oracle selects", c.Missing},
		{"only the walk selects", c.Unexplained},
		{"bad path", c.BadPaths},
		{"only the walk selects, through a one-tree link", c.Explained},
		{"only the walk selects, through a junction", c.Joined},
		{"only the walk selects, a sample target beside every slice", c.Sampled},
		{"only the walk selects, through a sampled unit kept as a source", c.Kept},
	} {
		lines := make([]string, 0, len(part.m))
		for k, why := range part.m {
			lines = append(lines, "  "+part.what+": "+k.String()+": "+why)
		}

		slices.Sort(lines)

		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}

	return b.String()
}

// Accept says which selections the walk may make beyond the oracle's.
type Accept struct {
	Junctions   []string
	KeptSources bool
}

// Compare sets the walk's selection against the oracle's for the same
// trees and slices.
func Compare(base, head *deps.Tree, ss []plugins.BindSlice, oracle map[Key]string, walk map[graph.Key][]graph.Path, a Accept) Comparison {
	c := Comparison{
		Explained: map[Key]string{}, Joined: map[Key]string{}, Missing: map[Key]string{},
		Unexplained: map[Key]string{}, BadPaths: map[Key]string{}, Sampled: map[Key]string{}, Kept: map[Key]string{},
	}

	links := newLinkIndex(base, head)
	roots := rootsOf(base, head, ss)

	for gk, paths := range walk {
		k := Key(gk)

		for _, p := range paths {
			if bad := links.check(p, roots[Key{Suite: k.Suite, Name: k.Name}]); bad != "" {
				c.BadPaths[k] = bad
			}
		}

		if _, ok := oracle[k]; ok {
			c.Same++

			continue
		}

		if l, ok := links.oneTree(base, head, paths); ok {
			c.Explained[k] = l
		} else if j, ok := a.junction(k, paths); ok {
			c.Joined[k] = j
		} else if n, ok := everySliceSelected(ss, walk, k); ok {
			c.Sampled[k] = n
		} else if x, ok := links.kept(base, head, walk, k, paths); ok && a.KeptSources {
			c.Kept[k] = x
		} else {
			c.Unexplained[k] = describePaths(paths)
		}
	}

	for k, why := range oracle {
		if _, ok := walk[graph.Key(k)]; !ok {
			c.Missing[k] = why
		}
	}

	return c
}

func sampleTarget(suite, name string) bool {
	return name == graph.SampleTarget(suite) || name == graph.SampleBoundTarget(suite)
}

func everySliceSelected(ss []plugins.BindSlice, walk map[graph.Key][]graph.Path, k Key) (string, bool) {
	if !sampleTarget(k.Suite, k.Name) {
		return "", false
	}

	n := 0

	for _, sl := range ss {
		if sl.Suite != k.Suite || !sl.HasList || sl.Setup || strings.HasPrefix(sl.Name, "_") {
			continue
		}

		if _, ok := walk[graph.Key{Suite: k.Suite, Name: sl.Name, Width: k.Width}]; !ok {
			return "", false
		}

		n++
	}

	return fmt.Sprintf("all %d slices selected", n), n >= 2
}

func (ix linkIndex) kept(base, head *deps.Tree, walk map[graph.Key][]graph.Path, k Key, paths []graph.Path) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}

	x := paths[0].Changed

	for _, p := range paths {
		if p.Changed != x {
			return "", false
		}
	}

	if unchanged(base, head, x) || !ix.movedFrom(x) {
		return "", false
	}

	for _, p := range walk[graph.Key{Suite: k.Suite, Name: graph.SampleTarget(k.Suite), Width: k.Width}] {
		if p.Changed == x {
			return x, true
		}
	}

	return "", false
}

func (ix linkIndex) movedFrom(id string) bool {
	for _, lk := range ix.byFrom[id] {
		if ix.in[lk] != 3 {
			return true
		}
	}

	return false
}

func unchanged(base, head *deps.Tree, id string) bool {
	if strings.HasPrefix(id, graph.GroupPrefix) || strings.HasPrefix(id, graph.NodePrefix) {
		return true
	}

	b, inBase := base.Units[id]
	h, inHead := head.Units[id]

	return inBase && inHead && b.Digest == h.Digest
}

func (a Accept) junction(k Key, paths []graph.Path) (string, bool) {
	if k.Width != graph.Dependents || len(paths) == 0 {
		return "", false
	}

	var first string

	for _, p := range paths {
		j := ""

		for _, s := range p.Steps {
			for _, id := range []string{s.From, s.To} {
				for _, prefix := range a.Junctions {
					if j == "" && strings.HasPrefix(id, prefix) {
						j = id
					}
				}
			}
		}

		if j == "" {
			return "", false
		}

		if first == "" {
			first = j
		}
	}

	return first, true
}

type linkIndex struct {
	in     map[linkKey]int // 1 base, 2 head, 3 both
	byFrom map[string][]linkKey
}

type linkKey struct {
	from, to, kind string
	typed          bool
}

func newLinkIndex(base, head *deps.Tree) linkIndex {
	ix := linkIndex{in: map[linkKey]int{}, byFrom: map[string][]linkKey{}}

	for bit, t := range map[int]*deps.Tree{1: base, 2: head} {
		for _, l := range t.Links {
			ix.in[linkKey{from: l.From, to: l.To}] |= bit
		}

		for _, l := range t.Typed {
			ix.in[linkKey{from: l.From, to: l.To, kind: l.Kind, typed: true}] |= bit
		}
	}

	for k := range ix.in {
		ix.byFrom[k.from] = append(ix.byFrom[k.from], k)
	}

	return ix
}

func (ix linkIndex) has(s graph.Step) bool {
	if ix.in[linkKey{from: s.From, to: s.To}] != 0 {
		return true
	}

	return !s.Back && ix.in[linkKey{from: s.From, to: s.To, kind: s.Kind, typed: true}] != 0
}

var coreSteps = []string{graph.KindPin, graph.KindUntold, graph.KindEverything, graph.KindCannotTell, graph.KindNames, graph.KindSample}

func (ix linkIndex) check(p graph.Path, roots map[string]bool) string {
	// a slice selected through a correlator has one single-step path to the
	// plugin and no root
	if wholesale(p) {
		return ""
	}

	// a suite's own targets (setup, sample) have no roots of their own
	if roots != nil && !roots[p.Listed] {
		return fmt.Sprintf("starts at %s, which is no root of the slice: %s", p.Listed, describePath(p))
	}

	at := p.Listed

	for i, s := range p.Steps {
		from, to := s.From, s.To
		if s.Back {
			from, to = to, from
		}

		if !slices.Contains(coreSteps, s.Kind) && !ix.has(s) {
			return fmt.Sprintf("step %d, %s, is no link of either tree: %s", i, stepString(s), describePath(p))
		}

		if from != at && !slices.Contains(coreSteps, s.Kind) {
			return fmt.Sprintf("step %d starts at %s, the path is at %s: %s", i, from, at, describePath(p))
		}

		at = to
	}

	if at != p.Changed {
		return fmt.Sprintf("ends at %s, not at the changed %s: %s", at, p.Changed, describePath(p))
	}

	return ""
}

const pluginPrefix = "plugin:"

func wholesale(p graph.Path) bool {
	if p.Listed != "" || len(p.Steps) != 1 {
		return false
	}

	s := p.Steps[0]

	return (s.Kind == graph.KindPin || s.Kind == graph.KindEverything) && s.From == "" && !s.Back &&
		len(s.To) > len(pluginPrefix) && strings.HasPrefix(s.To, pluginPrefix) && s.To == p.Changed
}

func (ix linkIndex) oneTree(base, head *deps.Tree, paths []graph.Path) (string, bool) {
	var found []string

	for _, p := range paths {
		if !unchanged(base, head, p.Changed) {
			continue
		}

		for _, k := range ix.byFrom[p.Changed] {
			if in := ix.in[k]; in != 3 {
				where := "base"
				if in == 2 {
					where = "head"
				}

				found = append(found, fmt.Sprintf("%s -> %s %s, in the %s only", k.from, k.to, k.kind, where))
			}
		}
	}

	if len(found) == 0 {
		return "", false
	}

	slices.Sort(found)

	return found[0], true
}

func rootsOf(base, head *deps.Tree, ss []plugins.BindSlice) map[Key]map[string]bool {
	out := make(map[Key]map[string]bool, len(ss))

	for _, sl := range ss {
		r := map[string]bool{}

		for _, id := range sl.List {
			r[id] = true
		}

		for _, id := range sl.Units {
			r[id] = true
		}

		for _, t := range []*deps.Tree{base, head} {
			for _, f := range sl.Sources {
				r[deps.FileID(f)] = true

				for _, id := range t.FileUnits[f] {
					r[id] = true
				}
			}

			for id, u := range t.Units {
				if slices.Contains(sl.Sources, u.Group) || (u.File != "" && slices.Contains(sl.Sources, u.File)) {
					r[id] = true
				}
			}
		}

		out[Key{Suite: sl.Suite, Name: sl.Name}] = r
	}

	return out
}

func stepString(s graph.Step) string {
	if s.Back {
		return s.To + " <-" + s.Kind + "- " + s.From
	}

	return s.From + " -" + s.Kind + "-> " + s.To
}

func describePath(p graph.Path) string {
	parts := make([]string, 0, len(p.Steps)+1)
	parts = append(parts, p.Listed)

	for _, s := range p.Steps {
		parts = append(parts, stepString(s))
	}

	return strings.Join(parts, ", ")
}

func describePaths(paths []graph.Path) string {
	if len(paths) == 0 {
		return "no path"
	}

	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, describePath(p))
	}

	return strings.Join(out, " | ")
}
