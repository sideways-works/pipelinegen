// Package graph plans a change outward: it finds the units that changed
// between two trees and walks backwards from them to the slices that bind
// them. docs/internals/graph.md is the contract it keeps.
package graph

import (
	"context"
	"errors"
	"sort"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

// The link kinds the walk reads from deps.Tree.Typed. A link in
// deps.Tree.Links, or of an unknown typed kind, is a plain link, the widest
// reading.
const (
	// KindFollow binds its target and expands it.
	KindFollow = plugin.LinkFollow
	// KindBind binds its target without expanding it at the executed width.
	KindBind = plugin.LinkBind
	// KindDependentsOnly holds only at the dependents width.
	KindDependentsOnly = plugin.LinkDependentsOnly
)

// The namespaces the core owns besides file:, never dangling and without a
// digest of their own.
const (
	// GroupPrefix names a group as a link end: group:<group>.
	GroupPrefix = plugin.GroupPrefix
	// NodePrefix names a junction a plugin needs that is no unit:
	// node:<namespace>:<rest>.
	NodePrefix = plugin.NodePrefix
)

// The widths a slice is bound in.
const (
	Executed   = plugins.Executed
	Dependents = plugins.Dependents
)

// Key names one slice in one width.
type Key struct {
	Suite, Name, Width string
}

// Step is one hop of a path that runs from a slice's root towards a
// changed node.
type Step struct {
	From, To, Kind string
	Back           bool
	Plugin         string
}

// The kinds of the steps the core adds to a path.
const (
	KindPin        = "pin"
	KindUntold     = "untold"
	KindEverything = "everything"
	KindCannotTell = "cannot-tell"
	KindNames      = "names"
	KindSample     = "sample"
	// KindUnlisted is a unit a slice's list holds in a namespace no
	// configured sbom lists: whether it changed can't be known.
	KindUnlisted = "unlisted"
)

// Path is one reason a slice is selected: from Listed, a root of the
// slice, to Changed, a changed node.
type Path struct {
	Listed, Changed string
	Steps           []Step
	Why             string
	Nodes           map[string]Node
	Entry           string
}

// Node is a unit of a path as its sbom described it.
type Node struct {
	Label, File, Plugin string
}

// Plugin is what the walk needs to know of a configured plugin.
type Plugin struct {
	Name       string
	SBOM       bool
	Namespaces []string
}

// Options say what the walk walks under.
type Options struct {
	Plugins []Plugin
	// On tells whether a narrowing is in force at the plan's level.
	On func(narrowing string) bool
	// Paths is how many shortest paths a selected slice keeps (0 is 3).
	Paths int
	// Observes tells whether a correlator observes a suite's slices; nil
	// means every correlator observes every suite.
	Observes func(suite, correlator string) bool
}

// Selection is the walk's answer.
type Selection struct {
	// Selected are the selected slices, each with up to Options.Paths
	// shortest paths in a fixed order.
	Selected map[Key][]Path
	// LeftOut are, per narrowing in force, the slices only its ruled-out
	// links or its wide rule would select.
	LeftOut map[string]map[Key][]Path
	// Entries are the selected slices a changed plugin entry reaches.
	Entries map[Key]bool
	// EntryPool are the recorded slices a changed entry gives up to its
	// suite's sample while plugin-entry is on.
	EntryPool map[Key]bool
}

// Hit is one selected slice of a suite in a width.
type Hit struct {
	Slice string
	Paths []Path
}

// Hits are one suite's selected slices in one width, sorted by name.
func (s *Selection) Hits(suite, width string) []Hit {
	var out []Hit

	for k, paths := range s.Selected {
		if k.Suite == suite && k.Width == width {
			out = append(out, Hit{Slice: k.Name, Paths: paths})
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Slice < out[j].Slice })

	return out
}

// ErrGraph wraps what can't be loaded or walked.
var ErrGraph = errors.New("graph")

// Change is a unit or core node that differs between the trees, and how (a
// How constant).
type Change struct {
	ID  string
	How string
}

// Changed is the one definition of what changed between two trees in a
// width.
func Changed(b, h *deps.Tree, ss []plugins.BindSlice, width string, o Options) ([]Change, error) {
	l := load(b, h)
	ch := changesOf(l, o, width)

	for _, g := range namesChanged(l, ss, width, o) {
		if _, ok := ch.how[g]; !ok {
			ch.how[g] = HowNames
		}
	}

	return ch.list(l), nil
}

// Select walks backwards from what changed between base and head and answers
// which slices bind a changed node in each width, with one walk per width,
// linear in the links reached.
func Select(_ context.Context, b, h *deps.Tree, slices []plugins.BindSlice, widths []string, o Options) (*Selection, error) {
	l := load(b, h)
	sel := selectAll(l, slices, widths, o)
	leftOut(l, slices, widths, o, sel)

	return sel, nil
}
