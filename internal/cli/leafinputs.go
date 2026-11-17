package cli

import (
	"path"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// leafInputs is one walked slice's data in one tree: its labels as named, the filegroups among them
// opened, and the leaves the slice gives up there.
type leafInputs struct {
	tops     []string
	groups   *bazel.Groups
	declared []string
	leaves   map[string]bool
	judged   map[string]plugins.Leaf
	// setup are the given-up leaves whose suite's setup list holds units their sbom lists for that
	// directory: added or removed, they reach the setup part instead.
	setup map[string]bool
}

func newLeafInputs(f *plugins.Leaves, sl plugins.BindSlice, unobserved func(path string)) *leafInputs {
	li := &leafInputs{
		tops: sl.Tops, groups: sl.Groups, declared: sl.Declared,
		leaves: make(map[string]bool, len(sl.Data)), judged: map[string]plugins.Leaf{}, setup: map[string]bool{},
	}

	if li.tops == nil {
		li.tops = sl.Data
	}

	for _, l := range sl.Data {
		li.leaves[l] = true

		switch f.Judge(sl, l, sl.InGroup(l)) {
		case plugins.LeafUnits:
			li.judged[l] = plugins.LeafUnits

			if p, ok := bazel.LabelPath(l); ok && f.SetupTakes(sl.Suite, p) {
				li.setup[l] = true
			}
		case plugins.LeafBuild:
			li.judged[l] = plugins.LeafBuild
		case plugins.LeafUnobserved:
			if p, ok := bazel.LabelPath(l); ok {
				unobserved(p)
			}
		case plugins.LeafKept:
		}
	}

	return li
}

// given are the leaves the slice gives up in this tree, beside other, the same slice in the other tree
// of the plan (nil when there is none). A file whose units replace it but which only one tree holds
// was added or removed: that shows in the setup part where the setup takes such files, and the file
// stays whole otherwise. Added and removed beside each other in one directory, it may be a rename, and
// order and names are what units don't carry: both stay whole. Without the other tree (unpaired),
// whether a file inside a filegroup was added or removed can't be told, so it stays whole.
func (li *leafInputs) given(other *leafInputs, unpaired bool) (out map[string]bool, setup []string) { //nolint:nonamedreturns // the two lists
	out = map[string]bool{}

	for l, j := range li.judged {
		if other == nil && unpaired && j == plugins.LeafUnits && !slices.Contains(li.tops, l) {
			continue
		}

		if j == plugins.LeafBuild || other == nil || other.leaves[l] {
			out[l] = true

			continue
		}

		switch {
		case renamed(l, li, other):
		case li.setup[l]:
			out[l] = true
			setup = append(setup, l)
		}
	}

	slices.Sort(setup)

	return out, setup
}

func renamed(l string, li, other *leafInputs) bool {
	p, ok := bazel.LabelPath(l)
	if !ok {
		return false
	}

	for m, j := range other.judged {
		if q, ok := bazel.LabelPath(m); ok && j == plugins.LeafUnits && !li.leaves[m] && path.Dir(q) == path.Dir(p) {
			return true
		}
	}

	return false
}

// labels are the slice's inputs in this tree. A filegroup goes back in place of what it holds where
// the slice gives up nothing below it in either tree, so both trees name it alike and the inputs stay
// short.
func (li *leafInputs) labels(other *leafInputs, unpaired bool) []string {
	gone, _ := li.given(other, unpaired)

	var otherGone map[string]bool
	if other != nil {
		otherGone, _ = other.given(li, unpaired)
	}

	whole := func(g string) bool {
		return !givesUp(li.groups, g, gone, map[string]bool{}) && (other == nil || !givesUp(other.groups, g, otherGone, map[string]bool{}))
	}

	var out []string

	var emit func(l string)

	emit = func(l string) {
		if !li.groups.IsGroup(l) {
			if leaf := li.groups.Leaf(l); !gone[leaf] {
				out = append(out, leaf)
			}

			return
		}

		if whole(l) {
			out = append(out, l)

			return
		}

		for _, m := range li.groups.MembersOf(l) {
			emit(m)
		}
	}

	for _, t := range li.tops {
		if slices.Contains(li.declared, t) || !li.groups.IsGroup(t) {
			if !gone[t] {
				out = append(out, t)
			}

			continue
		}

		emit(t)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func givesUp(g *bazel.Groups, label string, gone, seen map[string]bool) bool {
	if !g.IsGroup(label) {
		return gone[g.Leaf(label)]
	}

	if seen[label] {
		return false
	}

	seen[label] = true

	for _, m := range g.MembersOf(label) {
		if givesUp(g, m, gone, seen) {
			return true
		}
	}

	return false
}

// through are the filegroups an input was found through, innermost first.
func (li *leafInputs) through(label string) []string {
	for _, t := range li.tops {
		if slices.Contains(li.declared, t) {
			continue
		}

		if chain, ok := li.groups.Through(t, label); ok {
			return chain
		}
	}

	return nil
}

// buildLeftOut names the slices left out that hold a changed BUILD or .bzl file in their data: with the
// narrowing off, that file would have selected them.
func (b *planBook) buildLeftOut(base, head, suite string, selected, left []bazel.Slice, changed map[string]bool, offAt int) []affected.Narrowed {
	b.mu.Lock()
	trees, ws := []*bookTree{b.trees[base], b.trees[head]}, b.workspace
	b.mu.Unlock()

	var (
		names []string
		what  string
	)

	for _, sl := range left {
		hit := ""

		for _, bt := range trees {
			if bt == nil || bt.leaves[suite+"/"+sl.Name] == nil {
				continue
			}

			for l, j := range bt.leaves[suite+"/"+sl.Name].judged {
				if p, ok := bazel.LabelPath(l); ok && j == plugins.LeafBuild && changed[path.Join(ws, p)] && (hit == "" || p < hit) {
					hit = p
				}
			}
		}

		if hit != "" {
			names = append(names, sl.Name)

			if what == "" || hit < what {
				what = hit
			}
		}
	}

	if len(names) == 0 {
		return nil
	}

	slices.Sort(names)

	return []affected.Narrowed{{
		Kind: affected.NarrowedBy, Narrowing: plugins.NarrowBuildFiles, OffAt: offAt,
		Reason:  what + " changed, which reaches these slices only as a data file of their filegroups, and none of the rules they take from it changed",
		Reached: len(selected) + len(names), Dropped: names,
	}}
}
