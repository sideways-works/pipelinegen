// Package deps is what planning learns from the plugins for the deps repository bazel-diff hashes: a tree's
// units with their digests, and per slice what its selector depends on.
package deps

import (
	"context"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Unit is a unit as planning hashes it.
type Unit struct {
	ID string
	// Group is where its digest file lives (a Go package's directory, a
	// file's path); a slice may depend on a whole group at once.
	Group string
	// Digest changes exactly when the unit's behaviour can change; with digest_from it already combines
	// its sources'.
	Digest  string
	File    string
	Entry   string
	NotRoot bool
	Label   string
}

// Tree is what the plugins say about one tree.
type Tree struct {
	Commit string
	Units  map[string]Unit
	// Links are the links in force at the plan's narrowing level, whatever their kind plain links:
	// followed at every width and walked backwards at the dependents width.
	Links []plugin.Link
	// Typed are the links in force whose kind the walk reads apart (internal/graph), kept out of Links so
	// the forward closure never follows them as plain links.
	Typed         []plugin.Link
	TypedRuledOut map[string][]plugin.Link
	CannotTell    map[string]string
	// CannotTellBy names, per unit in CannotTell, the plugin the reason
	// comes from, which is not always the sbom that lists the unit.
	CannotTellBy map[string]string
	FileUnits    map[string][]string
	Narrowings   []string
	RuledOut     map[string][]plugin.Link
	ByUnits      map[string]string
	Anchors      map[string]string
	Pins         map[string]string
	LinkBy       map[[2]string]string
	// CorrelatorEnds are per correlator the near ends of its links, sorted, CorrelatorFiles the files it
	// claims: a slice whose binding holds one rests on its answers, so a change of its entry reaches it.
	CorrelatorEnds  map[string][]string
	CorrelatorFiles map[string][]string
	Providers       map[string]string
	// Unbound are, per width and group, what bind correlators say nothing
	// binds there: a slice linking the group depends on that list of names.
	Unbound map[string]map[string]Unbound
	// Untold are, per plugin, the files it couldn't tell about in this tree;
	// each has a unit of its own (FileID).
	Untold   map[string][]string
	Complete map[string]string
	UnitsOf  map[string][]string
	// InputUnits are, sorted, the units a set names as inputs, keying its answer; those no file declares
	// (a Go package's API unit) are neither names of their group nor bound with it whole.
	InputUnits []string
}

// FileID is the id of the unit that stands for a whole file.
func FileID(path string) string { return "file:" + path }

// Unbound is one group's names nothing binds, in one width.
type Unbound struct {
	Names []string
	// Narrowed are, per narrowing in force, the names only that narrowing
	// counts as bound: with it off they are unbound too.
	Narrowed map[string][]string
}

// UnboundID is the id of the unit whose digest is a group's unbound names
// in one width: a slice that links the group depends on it.
func UnboundID(width, group string) string { return "unbound:" + width + ":" + group }

// Why says why a unit is in a slice's dependencies.
type Why struct {
	Kind string
	Via  string
	By   string
	Vias []string
}

// Slice is what one slice's selector depends on, in one width.
type Slice struct {
	Suite      string
	Name       string
	Width      string
	Everything bool
	Units      []string
	Groups     []string
	// Data are the repository's Bazel labels the slice depends on whatever the lists say (its owner, its
	// data, extra_deps, pipelinegen:data lines), after the file-with-units rule.
	Data   []string
	Why    map[string]Why
	Linked []string
	// Dropped are per narrowing in force the unit ids the wide rule would add, DroppedGroups the groups whose
	// unbound names it would; neither is written to the deps repository.
	Dropped       map[string][]string
	DroppedGroups map[string][]string
}

type Writer interface {
	// Write makes the deps repository of the tree at root and returns a key that changes whenever what it
	// wrote does (the plan's snapshots are cached by it).
	Write(ctx context.Context, root string, tree *Tree, slices []Slice) (key string, err error)
}
