package cli

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/forwardtag"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

// forward binds the recorded suites forward and hashes them in the deps repository instead of walking.
// Only a build with the pipelinegen_forward tag sets it, so the plugin proof can compare the two.
var forward atomic.Bool

func outward() bool { return !forward.Load() && !forwardtag.Tagged }

func walksSuite(cfg *config.Config, suite string) bool {
	return cfg.Suites[suite].Implicit()
}

// While planning walks, plugins load for any suite of test files, since the walk needs no deps repository.
func loadsPlugins(cfg *config.Config, deps bool) bool {
	if outward() {
		return slices.ContainsFunc(cfg.SuiteNames(), func(s string) bool { return walksSuite(cfg, s) })
	}

	return deps
}

// Prepare fills the trees in turn, base and then head; once both are there the selection is made,
// before affected selects.
type walkTrees struct {
	mu   sync.Mutex
	repo gitx.Repo
	// base and head are resolved to commits on the first tree.
	base, head string
	resolved   bool
	trees      map[string]walkTree
	set        *plugins.Set
	level      int
	// sel is filled in place once both trees are prepared.
	sel *graph.Selection
	// changed are the plan's changed paths, once the plan has read them.
	changed map[string]bool
}

func (w *walkTrees) changedPaths() map[string]bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.changed
}

type walkTree struct {
	tree   *deps.Tree
	slices []plugins.BindSlice
}

func newWalkTrees(repo gitx.Repo, base, head string, set *plugins.Set, level int) *walkTrees {
	if !outward() || set == nil {
		return nil
	}

	return walkTreesFor(repo, base, head, set, level)
}

func walkTreesFor(repo gitx.Repo, base, head string, set *plugins.Set, level int) *walkTrees {
	return &walkTrees{
		repo: repo, base: base, head: head, trees: map[string]walkTree{}, set: set, level: level,
		sel: &graph.Selection{Selected: map[graph.Key][]graph.Path{}, LeftOut: map[string]map[graph.Key][]graph.Path{}},
	}
}

func (w *walkTrees) selection() *graph.Selection {
	if w == nil {
		return nil
	}

	return w.sel
}

func (w *walkTrees) prepared(ctx context.Context, commit string, t *deps.Tree, bs []plugins.BindSlice) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.resolved {
		for _, rev := range []*string{&w.base, &w.head} {
			if *rev == "" {
				continue
			}

			c, ok, err := w.repo.Resolve(ctx, *rev)
			if err != nil {
				return fmt.Errorf("walk: %w", err)
			}

			if ok {
				*rev = c
			}
		}

		w.resolved = true
	}

	unwalkable(t, w.set)
	w.trees[commit] = walkTree{tree: t, slices: bs}

	b, okB := w.trees[w.base]
	h, okH := w.trees[w.head]

	if !okB || !okH || commit != w.head && commit != w.base {
		return nil
	}

	sel, err := graph.Select(ctx, b.tree, h.tree, h.slices, []string{plugins.Executed, plugins.Dependents}, w.options())
	if err != nil {
		return fmt.Errorf("walk: %w", err)
	}

	*w.sel = *sel

	return nil
}

func (w *walkTrees) options() graph.Options {
	ps := make([]graph.Plugin, 0, len(w.set.Plugins))
	for _, l := range w.set.Plugins {
		ps = append(ps, graph.Plugin{Name: l.Conf.Name, SBOM: l.Desc.Kind == plugin.KindSBOM, Namespaces: l.Desc.Namespaces})
	}

	return graph.Options{Plugins: ps, On: func(n string) bool { return w.set.On(n, w.level) }, Observes: w.set.Observes}
}

func splitWalked(cfg *config.Config, bs []plugins.BindSlice) (walked, bound []plugins.BindSlice) { //nolint:nonamedreturns // the two parts
	for _, sl := range bs {
		if walksSuite(cfg, sl.Suite) {
			walked = append(walked, sl)
		} else {
			bound = append(bound, sl)
		}
	}

	return walked, bound
}

func narrowedHook(book *planBook, walk *walkTrees, set *plugins.Set) func(base, head, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
	if walk == nil {
		return book.narrowedHook()
	}

	walked := walkNarrowedHook(walk.selection(), set.OffAt)
	if book == nil {
		return walked
	}

	return func(base, head, suite, width string, selected, left []bazel.Slice) []affected.Narrowed {
		out := walked(base, head, suite, width, selected, left)

		return append(out, book.buildLeftOut(base, head, suite, selected, left, walk.changedPaths(), set.OffAt(plugins.NarrowBuildFiles))...)
	}
}

func entryPoolHook(book *planBook, walk *walkTrees) func(base, head, suite, width string) map[string]bool {
	if walk == nil {
		if book == nil {
			return nil
		}

		return book.entryPool
	}

	sel := walk.selection()

	return func(_, _, suite, width string) map[string]bool {
		out := map[string]bool{}

		for k := range sel.EntryPool {
			if k.Suite == suite && k.Width == width {
				out[k.Name] = true
			}
		}

		return out
	}
}

func inputsHook(book *planBook, walk *walkTrees) func(commit, suite, slice string) ([]string, error) {
	if walk == nil || book == nil {
		return nil
	}

	return func(commit, suite, slice string) ([]string, error) {
		return book.inputs(commit, walk.other(commit), true, suite, slice)
	}
}

func throughHook(book *planBook, walk *walkTrees) func(commit, suite, slice, label string) []string {
	if walk == nil || book == nil {
		return nil
	}

	return book.through
}

// other is the plan's tree beside commit, once both are resolved.
func (w *walkTrees) other(commit string) string {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch commit {
	case w.base:
		return w.head
	case w.head:
		return w.base
	}

	return ""
}

// untestedRead names each file an untested entry matches that a slice of the head demonstrably reads:
// such an entry is wrong, so the plan treats the file as uncovered.
func (w *walkTrees) untestedRead(cfg *config.Config) []string {
	if w == nil {
		return nil
	}

	h, ok := w.trees[w.head]
	if !ok || h.tree == nil {
		return nil
	}

	readers := map[string][]string{}

	for _, sl := range h.slices {
		for _, id := range slices.Concat(sl.List, sl.Units) {
			f := unitFile(h.tree, id)
			if f == "" {
				continue
			}

			if _, ok := cfg.UntestedFor(f); !ok {
				continue
			}

			name := sl.Suite + "/" + sl.Name
			if !slices.Contains(readers[f], name) {
				readers[f] = append(readers[f], name)
			}
		}
	}

	out := make([]string, 0, len(readers))

	for _, f := range slices.Sorted(maps.Keys(readers)) {
		u, _ := cfg.UntestedFor(f)
		rs := readers[f]

		more := ""
		if len(rs) > 1 {
			more = fmt.Sprintf(" and %d other slices", len(rs)-1)
		}

		out = append(out, fmt.Sprintf("%s is listed as untested (%s) but slice %s%s reads it: remove it from untested", f, u.Reason, rs[0], more))
	}

	return out
}

func unitFile(t *deps.Tree, id string) string {
	if f, ok := strings.CutPrefix(id, "file:"); ok {
		return f
	}

	return t.Units[id].File
}

// The walk follows links and never asks bind, so a correlator without links would select nothing where
// its slices must run; marked, they depend on everything.
func unwalkable(t *deps.Tree, set *plugins.Set) {
	if set == nil {
		return
	}

	for _, l := range set.Plugins {
		if !bindsWithoutLinks(l.Desc) {
			continue
		}

		if t.Untold == nil {
			t.Untold = map[string][]string{}
		}

		if _, ok := t.Untold[l.Conf.Name]; !ok {
			t.Untold[l.Conf.Name] = []string{}
		}
	}
}

func bindsWithoutLinks(d plugin.Description) bool {
	return d.Kind == plugin.KindCorrelator && slices.Contains(d.Capabilities, plugin.CallBind) && !slices.Contains(d.Capabilities, plugin.CallLinks)
}

func noLinksText(name, version string) string {
	return fmt.Sprintf("%s %s offers no links: this pipelinegen plans by links, so every recorded slice it could bind runs; pin a release of %s that offers links", name, version, name)
}

func noLinksWarnings(p planning) []string {
	if p.walk == nil || p.set == nil {
		return nil
	}

	var out []string

	for _, l := range p.set.Plugins {
		if bindsWithoutLinks(l.Desc) {
			out = append(out, noLinksText(l.Conf.Name, l.Desc.Version))
		}
	}

	return out
}

func unobservableHook(walk *walkTrees) func(base, head string, changes []gitx.Change) affected.UnobservableRule {
	if walk == nil {
		return nil
	}

	return walk.unobservable
}

// unobservable are the changed files in which nothing a test can observe changed, while that narrowing is on.
func (w *walkTrees) unobservable(base, head string, changes []gitx.Change) affected.UnobservableRule {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.changed = make(map[string]bool, len(changes))
	for _, c := range changes {
		w.changed[c.Path] = true
	}

	offAt := w.set.OffAt(plugins.NarrowUnobservable)
	rule := w.anchoredRule(affected.UnobservableRule{OffAt: offAt}, base, head, changes)

	if !w.set.On(plugins.NarrowUnobservable, w.level) {
		return rule
	}

	b, okB := w.trees[base]
	h, okH := w.trees[head]

	if !okB || !okH {
		return rule
	}

	for _, c := range changes {
		if c.Status != "M" {
			continue
		}

		if n, ok := unchangedWhole(b.tree, h.tree, c.Path); ok {
			rule.Files = append(rule.Files, affected.Unobservable{Path: c.Path, Plugin: b.tree.Complete[c.Path], Units: n})
		}
	}

	return rule
}

func (w *walkTrees) anchoredRule(rule affected.UnobservableRule, base, head string, changes []gitx.Change) affected.UnobservableRule {
	b, okB := w.trees[base]
	h, okH := w.trees[head]

	if !okB || !okH {
		return rule
	}

	for _, c := range changes {
		anchor, ok := h.tree.Anchors[c.Path]
		if c.Status != "M" || !ok || b.tree.Anchors[c.Path] != anchor || b.tree.ByUnits[c.Path] != h.tree.ByUnits[c.Path] {
			continue
		}

		n := h.tree.ByUnits[c.Path]
		rule.Anchored = append(rule.Anchored, affected.Anchored{
			Path: c.Path, Anchor: anchor, Narrowing: n, OffAt: w.set.OffAt(n), Plugin: plugins.ProviderOf(h.tree, h.tree.FileUnits[c.Path]),
		})
	}

	rule.Holds = holds(h.slices)

	return rule
}

func holds(bs []plugins.BindSlice) func(suite, slice, unit string) bool {
	lists := map[string][]string{}
	setups := map[string][]string{}

	for _, sl := range bs {
		if sl.Setup {
			setups[sl.Suite] = append(setups[sl.Suite], sl.List...)
		} else {
			lists[sl.Suite+"/"+sl.Name] = sl.List
		}
	}

	return func(suite, slice, unit string) bool {
		return slices.Contains(lists[suite+"/"+slice], unit) || slices.Contains(setups[suite], unit)
	}
}

func unchangedWhole(bt, ht *deps.Tree, path string) (int, bool) {
	by := bt.Complete[path]
	if by == "" || ht.Complete[path] != by {
		return 0, false
	}

	pin := plugins.PinID(by)
	if bp, hp := bt.Units[pin], ht.Units[pin]; bp.Digest == "" || bp.Digest != hp.Digest {
		return 0, false
	}

	for _, t := range []*deps.Tree{bt, ht} {
		for _, files := range t.Untold {
			if slices.Contains(files, path) {
				return 0, false
			}
		}

		if _, ok := t.CannotTell[deps.FileID(path)]; ok {
			return 0, false
		}
	}

	was, now := slices.Sorted(slices.Values(bt.UnitsOf[path])), slices.Sorted(slices.Values(ht.UnitsOf[path]))
	if !slices.Equal(slices.Compact(was), slices.Compact(now)) {
		return 0, false
	}

	for _, id := range now {
		bu, okB := bt.Units[id]
		hu, okH := ht.Units[id]

		if !okB || !okH || bu.Digest == "" || bu.Digest != hu.Digest {
			return 0, false
		}

		_, cb := bt.CannotTell[id]
		_, ch := ht.CannotTell[id]

		if cb || ch {
			return 0, false
		}
	}

	return len(slices.Compact(now)), true
}
