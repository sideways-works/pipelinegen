package graph

import (
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

type kind uint8

const (
	plain kind = iota
	follow
	bind
	dependentsOnly
)

func (k kind) String() string {
	switch k {
	case follow:
		return KindFollow
	case bind:
		return KindBind
	case dependentsOnly:
		return KindDependentsOnly
	default:
		return "plain"
	}
}

func typedKind(s string) kind {
	switch s {
	case KindFollow:
		return follow
	case KindBind:
		return bind
	case KindDependentsOnly:
		return dependentsOnly
	default:
		return plain
	}
}

const (
	base = 0
	head = 1
)

type edge struct {
	other int32
	kind  kind
	trees uint8
	ref   bool
	name  uint16
}

// Unbound ids are what the forward closure adds for the deps repository;
// they are no units of the graph's.
const unboundPrefix = "unbound:"

type loaded struct {
	ids     []string
	ix      map[string]int32
	digest  [2]map[int32]string
	group   map[int32]string
	file    map[int32]string
	byGroup map[string][]int32
	byFile  map[string][]int32
	entry   map[int32]string
	notRoot map[int32]bool
	input   map[int32]bool
	out, in [][]edge
	kinds   []string
	kindIx  map[string]uint16
	trees   [2]*deps.Tree
	// walks and steps count the work of planning in measures a busy machine
	// doesn't change.
	walks int
	steps int
}

func load(b, h *deps.Tree) *loaded {
	l, links := newLoaded(b, h)

	type key struct {
		from, to int32
		kind     kind
	}

	// at is each link's place among its ends' out and in links, so finding
	// the head's copy of a base link never scans a popular unit's links.
	at := make(map[key][2]int, links)

	for tr, t := range []*deps.Tree{b, h} {
		l.digest[tr] = make(map[int32]string, cap(l.ids))

		if t == nil {
			continue
		}

		l.units(tr, t)

		add := func(ln plugin.Link, k kind) {
			from, to := l.id(ln.From), l.id(ln.To)
			kk := key{from, to, k}

			var name uint16

			switch {
			case k != plain:
				name = l.kindID(k.String())
			case ln.Kind != "":
				name = l.kindID(ln.Kind)
			}

			if n, ok := at[kk]; ok {
				o, i := &l.out[from][n[0]], &l.in[to][n[1]]
				o.trees |= 1 << tr
				i.trees |= 1 << tr
				o.ref = o.ref || ln.Ref

				// a plain link named in the head only keeps that name
				if o.name == 0 {
					o.name, i.name = name, name
				}

				return
			}

			at[kk] = [2]int{len(l.out[from]), len(l.in[to])}
			l.out[from] = append(l.out[from], edge{other: to, kind: k, trees: 1 << tr, ref: ln.Ref, name: name})
			l.in[to] = append(l.in[to], edge{other: from, kind: k, trees: 1 << tr, name: name})
		}

		for _, ln := range t.Links {
			add(ln, plain)
		}

		for _, ln := range t.Typed {
			add(ln, typedKind(ln.Kind))
		}
	}

	l.sameEndsRef()
	l.inputUnits()

	return l
}

func newLoaded(b, h *deps.Tree) (*loaded, int) {
	// about one node per unit, and the links' few other ends
	nodes := 0

	for _, t := range []*deps.Tree{b, h} {
		if t != nil {
			nodes = max(nodes, len(t.Units))
		}
	}

	l := &loaded{
		ids: make([]string, 0, nodes), out: make([][]edge, 0, nodes), in: make([][]edge, 0, nodes),
		ix: make(map[string]int32, nodes), group: map[int32]string{}, file: map[int32]string{},
		byGroup: map[string][]int32{}, byFile: map[string][]int32{}, entry: map[int32]string{}, notRoot: map[int32]bool{}, input: map[int32]bool{},
		kindIx: map[string]uint16{}, trees: [2]*deps.Tree{b, h},
	}

	l.kindID(plainName)

	links := 0

	for _, t := range []*deps.Tree{b, h} {
		if t != nil {
			links = max(links, len(t.Links)+len(t.Typed))
		}
	}

	return l, links
}

// inputUnits marks the summary units no file declares (such as a Go
// package's API unit) that either tree's sets name as inputs. Such a unit
// changes only with a declaration, which selects through itself, so it is no
// name of its group and not bound with it whole.
func (l *loaded) inputUnits() {
	for _, t := range l.trees {
		if t == nil {
			continue
		}

		for _, id := range t.InputUnits {
			if i, ok := l.ix[id]; ok && l.file[i] == "" {
				l.input[i] = true
			}
		}
	}
}

const plainName = "plain"

// sameEndsRef marks every link between two ends as a reference when one is:
// referenced-new asks whether the target is referenced, not by which link.
func (l *loaded) sameEndsRef() {
	for _, out := range l.out {
		for i := range out {
			if !out[i].ref {
				continue
			}

			for j := range out {
				if out[j].other == out[i].other {
					out[j].ref = true
				}
			}
		}
	}
}

func (l *loaded) kindID(s string) uint16 {
	if i, ok := l.kindIx[s]; ok {
		return i
	}

	i := uint16(len(l.kinds)) //nolint:gosec // kinds are a handful of words
	l.kinds = append(l.kinds, s)
	l.kindIx[s] = i

	return i
}

func (l *loaded) units(tr int, t *deps.Tree) {
	for _, id := range sortedUnitIDs(t.Units) {
		if strings.HasPrefix(id, unboundPrefix) {
			continue
		}

		u := t.Units[id]
		i := l.id(id)
		_, known := l.digest[base][i]
		l.digest[tr][i] = u.Digest

		if known {
			continue
		}

		if u.Group != "" {
			l.group[i] = u.Group
			l.byGroup[u.Group] = append(l.byGroup[u.Group], i)
		}

		if u.File != "" {
			l.file[i] = u.File
			l.byFile[u.File] = append(l.byFile[u.File], i)
		}

		if u.Entry != "" {
			l.entry[i] = u.Entry
		}

		if u.NotRoot {
			l.notRoot[i] = true
		}
	}
}

func (l *loaded) id(s string) int32 {
	if i, ok := l.ix[s]; ok {
		return i
	}

	i := int32(len(l.ids)) //nolint:gosec // a graph holds far fewer than 2^31 nodes
	l.ids = append(l.ids, s)
	l.ix[s] = i
	l.out = append(l.out, nil)
	l.in = append(l.in, nil)

	return i
}

func sortedUnitIDs(m map[string]deps.Unit) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}

	sort.Strings(out)

	return out
}

func (l *loaded) linkPlugin(from, to string) string {
	for _, tr := range []int{head, base} {
		if t := l.trees[tr]; t != nil {
			if name, ok := t.LinkBy[[2]string{from, to}]; ok {
				return name
			}
		}
	}

	return ""
}
