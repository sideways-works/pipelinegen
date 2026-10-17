package affected

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/recording"
)

// walks tells whether a suite's slices are selected by the walk and the main
// repository's impacted targets rather than by selectors in the deps
// repository: a suite of test files while Select is set.
func (o Options) walks(suite string) bool {
	return o.Select != nil && o.Config.Suites[suite].Implicit()
}

type joined struct {
	seen    map[string]bool
	reasons map[string][]Reason
	quiet   map[string][]string
}

// join selects a recorded suite's slices without selectors: by the walk
// reaching a listed unit, by its own file changing or being new, or by one
// of its Bazel input labels being impacted or in one tree only. A label the
// head's hashes don't hold can't be told, so it selects its slice.
func (o Options) join(ctx context.Context, suite string, b, h *Snapshot, impacted map[string]bool, changes []gitx.Change, hashed func() (map[string]bool, error)) (joined, error) {
	width := o.width(suite)

	j := joined{seen: make(map[string]bool, len(impacted)), reasons: map[string][]Reason{}, quiet: map[string][]string{}}
	maps.Copy(j.seen, impacted)

	own := []string{recording.SetupTarget(suite), SampleTarget(suite), SampleBoundTarget(suite), EntryTarget(suite)}
	for _, name := range own {
		j.seen[bazel.SelectorLabel(name)] = false
	}

	for _, sl := range h.Slices[suite] {
		j.seen[sl.Label] = false
	}

	add := func(name string, rs ...Reason) {
		label := bazel.SelectorLabel(name)
		j.seen[label] = true
		j.reasons[label] = appendNew(j.reasons[label], rs...)
	}

	for _, hit := range o.Select.Hits(suite, width) {
		add(hit.Slice, pathReasons(hit.Paths)...)
	}

	main := mainLabels(impacted)

	before := map[string]bool{}
	for _, sl := range b.Slices[suite] {
		before[sl.Name] = true
	}

	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.Path] = true
	}

	inputs := map[string][]Reason{}

	for _, sl := range h.Slices[suite] {
		if !before[sl.Name] {
			add(sl.Name, Reason{Kind: KindNewSlice, What: sl.Label})

			continue
		}

		for _, src := range sl.Sources {
			if changed[src] {
				add(sl.Name, Reason{Kind: KindTestChanged, What: src})
			}
		}

		rs, err := o.inputReasons(suite, sl.Name, b.Commit, h.Commit, main, hashed, len(changes) == 0)
		if err != nil {
			return joined{}, err
		}

		inputs[sl.Name] = rs
	}

	o.joinInputs(ctx, suite, h.Slices[suite], inputs, &j, add)

	return j, nil
}

func (o Options) joinInputs(ctx context.Context, suite string, sls []bazel.Slice, inputs map[string][]Reason, j *joined, add func(string, ...Reason)) {
	o.quiet.prepare(ctx, o, suite, inputs)

	for _, sl := range sls {
		rs, gone := o.quiet.filter(ctx, o, suite, sl.Name, inputs[sl.Name])
		if len(gone) > 0 {
			j.quiet[sl.Label] = gone
		}

		if len(rs) > 0 {
			add(sl.Name, rs...)
		}
	}
}

// inputReasons says why a slice's Bazel labels select it, unless no file
// changed. A tree whose inputs were never recorded fails the plan: comparing
// with nothing would take every label for a new one.
func (o Options) inputReasons(suite, slice, base, head string, impacted map[string]bool, hashed func() (map[string]bool, error), unchanged bool) ([]Reason, error) {
	if o.Inputs == nil {
		return nil, nil
	}

	inBase, err := o.Inputs(base, suite, slice)
	if err != nil {
		return nil, fmt.Errorf("the Bazel inputs of %s/%s at %s: %w", suite, slice, short(base), err)
	}

	inHead, err := o.Inputs(head, suite, slice)
	if err != nil {
		return nil, fmt.Errorf("the Bazel inputs of %s/%s at %s: %w", suite, slice, short(head), err)
	}

	was := set(mainLabelList(inBase))
	now := mainLabelList(inHead)

	var rs []Reason

	for _, l := range now {
		switch {
		case !was[l]:
			rs = append(rs, Reason{Kind: KindInputAdded, What: l})
		case impacted[l]:
			rs = append(rs, Reason{Kind: KindTarget, What: l})
		case unchanged:
			// the same files: whatever the label names is as it was
		case pinOfLabel(o, l) == pinSame:
			// another repository's file, under the same pin
		case pinOfLabel(o, l) == pinChanged:
			rs = append(rs, Reason{Kind: KindTarget, What: l, Through: []string{"the pin of its repository changed"}})
		case pinWhy(o, l) != "":
			o.warnf("%s/%s depends on %s, which can't be told unchanged: %s", suite, slice, l, pinWhy(o, l))
			rs = append(rs, Reason{Kind: KindInputUnknown, What: l, Through: []string{pinWhy(o, l)}})
		default:
			have, err := hashed()
			if err != nil {
				o.warnf("%s/%s depends on %s, and the head's hashes can't be read (%v): selected, since whether it changed can't be told", suite, slice, l, err)
				rs = append(rs, Reason{Kind: KindInputUnknown, What: l})
			} else if !have[l] {
				o.warnf("%s/%s depends on %s, which the head's hashes don't hold: selected, since whether it changed can't be told", suite, slice, l)
				rs = append(rs, Reason{Kind: KindInputUnknown, What: l})
			}
		}
	}

	is := set(now)

	for _, l := range slices.Sorted(maps.Keys(was)) {
		if !is[l] {
			rs = append(rs, Reason{Kind: KindInputRemoved, What: l})
		}
	}

	return rs, nil
}

// mainLabel writes a label of the main repository one way, //pkg:name,
// whether Bazel printed it with @@ or @, and whether it was written //pkg
// for the target named after its package.
func mainLabel(l string) string {
	for _, p := range []string{"@@//", "@//"} {
		if rest, ok := strings.CutPrefix(l, p); ok {
			l = "//" + rest

			break
		}
	}

	repo, pkg, ok := strings.Cut(l, "//")
	if !ok || pkg == "" || strings.Contains(pkg, ":") {
		return l
	}

	return repo + "//" + pkg + ":" + path.Base(pkg)
}

func mainLabelList(ls []string) []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, mainLabel(l))
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func mainLabels(impacted map[string]bool) map[string]bool {
	out := make(map[string]bool, len(impacted))

	for l, ok := range impacted {
		if ok {
			out[mainLabel(l)] = true
		}
	}

	return out
}

func appendNew(rs []Reason, more ...Reason) []Reason {
	for _, r := range more {
		if !slices.ContainsFunc(rs, func(have Reason) bool {
			return have.Kind == r.Kind && have.What == r.What && slices.Equal(have.Through, r.Through)
		}) {
			rs = append(rs, r)
		}
	}

	return rs
}

func hashedLabels(path string) func() (map[string]bool, error) {
	var (
		done bool
		have map[string]bool
		err  error
	)

	return func() (map[string]bool, error) {
		if !done {
			done = true
			have, err = readHashedLabels(path)
		}

		return have, err
	}
}

var errHashes = errors.New("not a hash file")

func readHashedLabels(path string) (map[string]bool, error) {
	f, err := os.Open(path) //nolint:gosec // pipelinegen's own snapshot
	if err != nil {
		return nil, fmt.Errorf("the head's hashes: %w", err)
	}
	defer f.Close() //nolint:errcheck // read only

	have := map[string]bool{}

	if err := hashKeys(json.NewDecoder(bufio.NewReaderSize(f, 1<<20)), have, true); err != nil {
		return nil, fmt.Errorf("the head's hashes %s: %w", path, err)
	}

	return have, nil
}

// hashKeys reads an object of target hashes into have. bazel-diff writes
// the hashes at the top, or under "hashes" beside other keys: at the top
// a key that is no label is read only if it holds the hashes.
func hashKeys(dec *json.Decoder, have map[string]bool, top bool) error {
	if _, err := dec.Token(); err != nil { // {
		return err //nolint:wrapcheck // wrapped by the caller
	}

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err //nolint:wrapcheck // wrapped by the caller
		}

		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("%w: a key that is no string", errHashes)
		}

		if top && key == "hashes" {
			if err := hashKeys(dec, have, false); err != nil {
				return err
			}

			continue
		}

		if !top || strings.HasPrefix(key, "/") || strings.HasPrefix(key, "@") {
			have[mainLabel(key)] = true
		}

		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err //nolint:wrapcheck // wrapped by the caller
		}
	}

	_, err := dec.Token() // }

	return err //nolint:wrapcheck // wrapped by the caller
}

const boundToEverything = "it is bound to every unit the plugins list: it has no list yet, or a plugin couldn't tell what it touched, or a plugin it depends on changed"

const noListYet = "it has no recording yet, so it is bound to every unit the plugins list"

func pathReasons(paths []graph.Path) []Reason {
	if len(paths) == 0 {
		return []Reason{{Kind: KindEverything, What: noListYet}}
	}

	out := make([]Reason, 0, len(paths))

	for _, p := range paths {
		out = appendNew(out, pathReason(p))
	}

	return out
}

func wideBecause(p graph.Path) string {
	st := p.Steps[0]
	plugin := strings.TrimPrefix(st.From, "plugin:")

	switch st.Kind {
	case graph.KindEverything:
		return strings.TrimPrefix(p.Changed, "plugin:") + " couldn't tell what the recorded slices touched in one of the trees (its warning says why), so every slice it binds runs"
	case graph.KindUntold:
		if plugin != "" {
			return plugin + " couldn't tell about " + strings.TrimPrefix(p.Changed, "file:") + ", which changed, and the slice reaches what it lists"
		}

		return "a plugin couldn't tell about " + strings.TrimPrefix(p.Changed, "file:") + ", which changed"
	case graph.KindCannotTell:
		why := ""
		if p.Why != "" {
			why = " (" + p.Why + ")"
		}

		return "it reaches " + st.From + ", which can't be told" + why + ", in the group of " + p.Changed + ", which changed"
	case graph.KindUnlisted:
		ns, _, _ := strings.Cut(strings.TrimPrefix(p.Changed, "dep:"), ":")

		return "it touched " + p.Changed + ", which no configured sbom lists: configure an sbom that lists " + ns + " units, or every change runs it"
	}

	return boundToEverything
}

func pathReason(p graph.Path) Reason {
	last := ""
	if len(p.Steps) > 0 {
		last = p.Steps[len(p.Steps)-1].Kind
	}

	if p.Entry != "" {
		return Reason{Kind: KindPlugin, What: p.Entry}
	}

	switch last {
	case graph.KindPin:
		return Reason{Kind: KindPlugin, What: strings.TrimPrefix(p.Changed, "plugin:")}
	case graph.KindNames:
		return Reason{Kind: KindDepends, What: "the names nothing binds in " + strings.TrimPrefix(p.Changed, graph.GroupPrefix)}
	case graph.KindEverything, graph.KindCannotTell, graph.KindUntold, graph.KindUnlisted:
		if len(p.Steps) == 1 {
			return Reason{Kind: KindEverything, What: wideBecause(p)}
		}
	}

	if p.Listed == p.Changed {
		return Reason{Kind: KindExecuted, What: p.Changed, Hops: hopsOf(p)}
	}

	// the path's nodes from the root on: each step ends where the path
	// goes next, the link's To, or its From for a step against it
	nodes := []string{p.Listed}

	for _, st := range p.Steps {
		next := st.To
		if st.Back {
			next = st.From
		}

		if next != nodes[len(nodes)-1] {
			nodes = append(nodes, next)
		}
	}

	through := make([]string, 0, len(nodes))

	for _, n := range slices.Backward(nodes) {
		if n != p.Changed {
			through = append(through, n)
		}
	}

	return Reason{Kind: KindDepends, What: p.Changed, Through: through, Hops: hopsOf(p)}
}

func hopsOf(p graph.Path) []Hop {
	node := func(id string) graph.Node { return p.Nodes[id] }

	root := node(p.Listed)
	out := []Hop{{To: p.Listed, Kind: "listed", Plugin: root.Plugin, Label: root.Label, File: root.File}}
	at := p.Listed

	for _, st := range p.Steps {
		next := st.To
		if st.Back {
			next = st.From
		}

		if next == at {
			continue
		}

		n := node(next)
		out = append(out, Hop{From: at, To: next, Kind: st.Kind, Plugin: st.Plugin, Back: st.Back, Label: n.Label, File: n.File})
		at = next
	}

	return out
}

// suiteReasons are the reasons of a suite's selected slices: the walk's
// where the suite was walked, else what Prepare wrote and bazel-diff.
func (o Options) suiteReasons(ctx context.Context, b, h *Snapshot, suite string, selected []bazel.Slice, walked *joined, changes []gitx.Change) map[string][]Reason {
	if walked != nil {
		return o.walkedReasons(ctx, b, h, selected, walked.reasons)
	}

	return reasons(ctx, o, b, h, suite, selected, changes)
}

func (o Options) walkedReasons(ctx context.Context, b, h *Snapshot, selected []bazel.Slice, by map[string][]Reason) map[string][]Reason {
	out := make(map[string][]Reason, len(selected))

	for _, sl := range selected {
		rs := by[sl.Label]
		if len(rs) == 0 {
			rs = []Reason{{Kind: KindTarget, What: sl.Label}}
		}

		out[sl.Label] = o.explainInputs(ctx, b, h, rs)
	}

	return out
}

func (o Options) explainInputs(ctx context.Context, b, h *Snapshot, rs []Reason) []Reason {
	i := -1

	for j, r := range rs {
		switch r.Kind {
		case KindTarget:
			if i < 0 && len(r.Through) == 0 {
				i = j
			}
		case KindInputAdded, KindInputRemoved, KindInputUnknown, KindTextInput:
			// say themselves what happened to the label
		default:
			return rs
		}
	}

	if i < 0 || o.Differ == nil {
		return rs
	}

	got, ok := o.explainedInput(rs[i].What)
	if !ok {
		budget := o.explained
		if budget != nil && budget.limited {
			if budget.left <= 0 {
				budget.skipped++
				rs[i].Through = []string{unexplained}

				return rs
			}

			budget.left--
		}

		got = o.explainInput(ctx, b, h, rs[i].What)
		if budget != nil && budget.inputs != nil {
			budget.inputs[rs[i].What] = got
		}
	}

	if got.err != nil {
		return append(rs, Reason{Kind: KindTarget, What: "bazel-diff could not explain: " + got.err.Error()})
	}

	if len(got.causes) == 0 {
		return rs
	}

	return slices.Concat(rs[:i], got.causes, rs[i+1:])
}

type explainedInput struct {
	causes []Reason
	err    error
}

// explainedInput is the plan's answer for an input label explained before:
// the label's root causes don't depend on the slice.
func (o Options) explainedInput(label string) (explainedInput, bool) {
	if o.explained == nil {
		return explainedInput{}, false
	}

	got, ok := o.explained.inputs[label]

	return got, ok
}

func (o Options) explainInput(ctx context.Context, b, h *Snapshot, label string) explainedInput {
	causes, err := o.Differ.Explain(ctx, b.Hashes, h.Hashes, h.Edges, label)
	if err != nil {
		return explainedInput{err: err}
	}

	var more []Reason

	for n, c := range causes {
		if n == maxRootCauses {
			break
		}

		// the label itself says no more than the reason it replaces
		if c.Label == label && len(o.filesBehind(ctx, c.Label)) == 0 {
			continue
		}

		more = appendNew(more, o.causeTargets(ctx, c)...)
	}

	return explainedInput{causes: o.quiet.observableCauses(o.Config, more)}
}
