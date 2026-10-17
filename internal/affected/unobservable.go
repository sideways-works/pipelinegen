package affected

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// Unobservable is a changed file that changed nothing a test can observe
// when it runs as code: the sbom that lists its units says they cover it
// whole, in both trees, under the same pin, and every one of them is as
// it was.
type Unobservable struct {
	Path   string `json:"path"`
	Plugin string `json:"plugin"`
	Units  int    `json:"units"`
}

// UnobservableRule is, while the narrowing is on, the plan's changed files
// that changed nothing observable, and the level that switches it off.
type UnobservableRule struct {
	Files []Unobservable
	OffAt int
	// Anchored are the changed files an sbom claims by their units with an
	// anchor: a slice whose list holds the anchor is bound to the units it
	// touched, so a label holding the file is no reason of its own for it.
	Anchored []Anchored
	Holds    func(suite, slice, unit string) bool
}

// Anchored is a changed file claimed by its units with an anchor.
type Anchored struct {
	Path, Anchor, Narrowing, Plugin string
	OffAt                           int
}

type quiet struct {
	rule               UnobservableRule
	files              map[string]Unobservable
	anchored           map[string]Anchored
	changed            []string
	baseSnap, headSnap *Snapshot
	without            map[string]map[string]bool
	kept               map[string]map[string]bool
	absorbed           map[string][]string
	queries            int
	seconds            []float64
	head               string
}

func newQuiet(rule UnobservableRule, changes []gitx.Change) *quiet {
	if len(rule.Files) == 0 && len(rule.Anchored) == 0 {
		return nil
	}

	named := map[string]Unobservable{}
	for _, f := range rule.Files {
		named[f.Path] = f
	}

	anchored := map[string]Anchored{}
	for _, a := range rule.Anchored {
		anchored[a.Path] = a
	}

	q := &quiet{
		rule: rule, files: map[string]Unobservable{}, anchored: map[string]Anchored{},
		kept: map[string]map[string]bool{}, absorbed: map[string][]string{}, without: map[string]map[string]bool{},
	}

	for _, c := range changes {
		q.changed = append(q.changed, c.Path)

		// only a file the change modified in place counts
		if f, ok := named[c.Path]; ok && c.Status == "M" {
			q.files[c.Path] = f

			continue
		}

		if a, ok := anchored[c.Path]; ok && c.Status == "M" && rule.Holds != nil {
			q.anchored[c.Path] = a
		}
	}

	if len(q.files) == 0 && len(q.anchored) == 0 {
		return nil
	}

	return q
}

// observable are the file labels less those of files that changed nothing
// observable, unless that leaves none.
func (q *quiet) observable(cfg *config.Config, labels []string) []string {
	if q == nil || cfg == nil {
		return labels
	}

	out := slices.DeleteFunc(slices.Clone(labels), func(l string) bool {
		p, ok := bazel.LabelPath(l)
		_, quiet := q.files[path.Join(cfg.Bazel.Workspace, p)]

		return ok && quiet
	})

	if len(out) == 0 {
		return labels
	}

	return out
}

func (o Options) withQuiet(plan *Plan, b, h *Snapshot, changes []gitx.Change) Options {
	if o.Unobservable == nil {
		return o
	}

	if o.quiet = newQuiet(o.Unobservable(b.Commit, h.Commit, changes), changes); o.quiet != nil {
		o.quiet.head, o.quiet.baseSnap, o.quiet.headSnap = h.Commit, b, h
		plan.Unobservable = o.quiet.list()
	}

	return o
}

func (q *quiet) observableCauses(cfg *config.Config, rs []Reason) []Reason {
	if q == nil || cfg == nil {
		return rs
	}

	out := slices.DeleteFunc(slices.Clone(rs), func(r Reason) bool {
		p, ok := bazel.LabelPath(r.What)
		_, quiet := q.files[path.Join(cfg.Bazel.Workspace, p)]

		return r.Kind == KindTarget && ok && quiet
	})

	if len(out) == 0 {
		return rs
	}

	return out
}

func (q *quiet) count() int {
	if q == nil {
		return 0
	}

	return q.queries
}

func (q *quiet) list() []Unobservable {
	out := make([]Unobservable, 0, len(q.files))
	for _, p := range sortedKeysOf(q.files) {
		out = append(out, q.files[p])
	}

	return out
}

func (q *quiet) textRead(s config.Suite, test string) []string {
	var out []string

	for _, p := range slices.Concat(sortedKeysOf(q.files), sortedKeysOf(q.anchored)) {
		if s.ReadsText(test, p) {
			out = append(out, p)
		}
	}

	slices.Sort(out)

	return out
}

func (q *quiet) takes(o Options, suite, test string) []string {
	text := q.textRead(o.Config.Suites[suite], test)

	var out []string

	for _, p := range sortedKeysOf(q.files) {
		if !slices.Contains(text, p) {
			out = append(out, p)
		}
	}

	for _, p := range sortedKeysOf(q.anchored) {
		if !slices.Contains(text, p) && q.rule.Holds(suite, test, q.anchored[p].Anchor) {
			out = append(out, p)
		}
	}

	slices.Sort(out)

	return out
}

// decide makes sure the labels are decided for slices that take the given
// files: a label stays when the change impacts it with those files as the
// base has them. When bazel-diff can't say, every label stays, with a
// warning.
func (q *quiet) decide(ctx context.Context, o Options, labels, takes []string) {
	key := strings.Join(takes, "\x00")

	got := q.kept[key]
	if got == nil {
		got = map[string]bool{}
		q.kept[key] = got
	}

	var todo []string

	for _, l := range labels {
		if _, ok := got[l]; !ok {
			todo = append(todo, l)
		}
	}

	if len(todo) == 0 {
		return
	}

	impacted, err := q.impactedWithout(ctx, o, takes)
	if err != nil {
		o.warnf("what the change impacts besides %s can't be told, so no label is left out: %v", strings.Join(takes, ", "), err)
	}

	for _, l := range todo {
		got[l] = err != nil || impacted[l]
	}
}

// impactedWithout are the main repository's targets the change impacts with
// the given files as the base has them: bazel-diff hashes the head's work
// tree again with the base's copies in place, in the same output base, and
// compares that with the base's hashes.
func (q *quiet) impactedWithout(ctx context.Context, o Options, takes []string) (map[string]bool, error) {
	key := strings.Join(takes, "\x00")
	if got, ok := q.without[key]; ok {
		return got, nil
	}

	others := slices.DeleteFunc(slices.Clone(q.changed), func(p string) bool { return slices.Contains(takes, p) })
	if len(others) == 0 {
		q.without[key] = map[string]bool{}

		return q.without[key], nil
	}

	if o.Differ == nil || o.Bazel == nil || q.baseSnap == nil || q.headSnap == nil {
		return nil, errNoDiffer
	}

	q.queries++

	start := time.Now()

	impacted, err := hashWithout(ctx, o, q.baseSnap, q.headSnap, takes)
	if err != nil {
		return nil, err
	}

	q.seconds = append(q.seconds, math.Round(time.Since(start).Seconds()*10)/10)

	q.without[key] = impacted

	return impacted, nil
}

const withoutPrefix = "without-"

// sweepWithout removes such directories a plan left when it died; the caller
// holds the cache's lock, so no running plan owns one.
func sweepWithout(cache string) {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), withoutPrefix) {
			os.RemoveAll(filepath.Join(cache, e.Name())) //nolint:errcheck // the next plan tries again
		}
	}
}

var errNoDiffer = errors.New("no bazel-diff to ask")

func hashWithout(ctx context.Context, o Options, b, h *Snapshot, files []string) (out map[string]bool, err error) { //nolint:nonamedreturns // the restore's error joins the result's
	tree := gitx.Repo{Dir: filepath.Join(o.Cache, "tree")}

	if _, err := tree.Git(ctx, slices.Concat([]string{"checkout", b.Commit, "--"}, files)...); err != nil {
		return nil, fmt.Errorf("put the base's copies in place: %w", err)
	}

	defer func() {
		if _, rerr := tree.Git(ctx, slices.Concat([]string{"checkout", h.Commit, "--"}, files)...); rerr != nil {
			out, err = nil, errors.Join(err, fmt.Errorf("put the head's copies back: %w", rerr))
		}
	}()

	dir, err := os.MkdirTemp(o.Cache, withoutPrefix)
	if err != nil {
		return nil, fmt.Errorf("hashes without: %w", err)
	}

	defer os.RemoveAll(dir) //nolint:errcheck // scratch files

	bz := o.Bazel(workspaceDir(o))
	hashes := filepath.Join(dir, "hashes.json")

	if err := o.Differ.Hashes(ctx, bz, bz.Dir, hashes, filepath.Join(dir, "edges.json")); err != nil {
		return nil, err
	}

	labels, err := o.Differ.Impacted(ctx, bz, b.Hashes, hashes)
	if err != nil {
		return nil, err
	}

	out = make(map[string]bool, len(labels))
	for _, l := range labels {
		out[mainLabel(bazel.NormalLabel(l))] = true
	}

	return out, nil
}

func candidates(rs []Reason) []string {
	var out []string

	for _, r := range rs {
		if r.Kind == KindTarget && len(r.Through) == 0 {
			out = append(out, r.What)
		}
	}

	return out
}

func (o Options) candidatesOf(suite, test string, rs []Reason) []string {
	labels := candidates(rs)
	if o.Declared == nil || o.quiet == nil {
		return labels
	}

	declared := mainLabelList(o.Declared(o.quiet.head, suite, test))

	return slices.DeleteFunc(labels, func(l string) bool { return slices.Contains(declared, l) })
}

func (q *quiet) prepare(ctx context.Context, o Options, suite string, inputs map[string][]Reason) {
	if q == nil || o.Config.Suites[suite].Bind != config.BindRecorded {
		return
	}

	by := map[string][]string{}
	takes := map[string][]string{}

	for _, name := range sortedKeysOf(inputs) {
		labels := o.candidatesOf(suite, name, inputs[name])
		if len(labels) == 0 {
			continue
		}

		t := q.takes(o, suite, name)
		q.absorbed[suite+"/"+name] = t

		if len(t) == 0 {
			continue
		}

		key := strings.Join(t, "\x00")
		by[key] = append(by[key], labels...)
		takes[key] = t
	}

	for _, key := range sortedKeysOf(by) {
		labels := slices.Sorted(slices.Values(by[key]))
		q.decide(ctx, o, slices.Compact(labels), takes[key])
	}
}

func (q *quiet) filter(ctx context.Context, o Options, suite, test string, rs []Reason) ([]Reason, []string) {
	if q == nil || o.Config.Suites[suite].Bind != config.BindRecorded {
		return rs, nil
	}

	labels := o.candidatesOf(suite, test, rs)
	if len(labels) == 0 {
		return rs, nil
	}

	takes, ok := q.absorbed[suite+"/"+test]
	if !ok {
		takes = q.takes(o, suite, test)
		q.absorbed[suite+"/"+test] = takes
	}

	text := q.textRead(o.Config.Suites[suite], test)

	if len(takes) == 0 {
		return withText(rs, text), nil
	}

	q.decide(ctx, o, labels, takes)

	kept := q.kept[strings.Join(takes, "\x00")]

	var gone []string

	out := slices.DeleteFunc(slices.Clone(rs), func(r Reason) bool {
		if r.Kind != KindTarget || len(r.Through) > 0 || kept[r.What] {
			return false
		}

		gone = append(gone, r.What)

		return true
	})

	if len(gone) < len(labels) {
		out = withText(out, text)
	}

	return out, gone
}

func withText(rs []Reason, text []string) []Reason {
	for _, f := range text {
		rs = append(rs, Reason{Kind: KindTextInput, What: f})
	}

	return rs
}

func (q *quiet) narrowed(suite string, sp SuitePlan, dropped map[string][]string, all []bazel.Slice) []Narrowed {
	if q == nil || len(dropped) == 0 {
		return nil
	}

	selected := map[string]bool{}
	for _, sl := range sp.Selected {
		selected[sl.Label] = true
	}

	type group struct {
		narrowing string
		offAt     int
		why       string
		names     []string
	}

	by := map[string]*group{}

	for _, sl := range all {
		labels := dropped[sl.Label]
		if len(labels) == 0 || selected[sl.Label] {
			continue
		}

		g := q.groupOf(q.absorbed[suite+"/"+sl.Name], strings.Join(labels, ", "))
		key := g.narrowing + "\x00" + g.why

		if by[key] == nil {
			by[key] = &group{narrowing: g.narrowing, offAt: g.offAt, why: g.why}
		}

		by[key].names = append(by[key].names, sl.Name)
	}

	var out []Narrowed

	for _, key := range sortedKeysOf(by) {
		g := by[key]
		out = append(out, Narrowed{
			Kind: NarrowedBy, Narrowing: g.narrowing, OffAt: g.offAt, Reason: g.why,
			Reached: len(sp.Selected) + len(g.names), Dropped: g.names,
		})
	}

	return out
}

type leftOutBy struct {
	narrowing string
	offAt     int
	why       string
}

func (q *quiet) groupOf(takes []string, labels string) leftOutBy {
	out := leftOutBy{narrowing: plugins.NarrowUnobservable, offAt: q.rule.OffAt}

	var quiet, covered []string

	for _, p := range takes {
		if f, ok := q.files[p]; ok {
			units := fmt.Sprintf("its %d units as they were", f.Units)
			if f.Units == 1 {
				units = "its one unit as it was"
			}

			quiet = append(quiet, fmt.Sprintf("%s (%s: %s)", f.Path, f.Plugin, units))

			continue
		}

		a := q.anchored[p]
		covered = append(covered, fmt.Sprintf("%s (%s, by %s)", a.Path, a.Anchor, a.Plugin))

		if out.narrowing == plugins.NarrowUnobservable || a.OffAt < out.offAt {
			out.narrowing, out.offAt = a.Narrowing, a.OffAt
		}
	}

	var parts []string

	if len(quiet) > 0 {
		parts = append(parts, "nothing a test can observe changed in "+strings.Join(quiet, ", "))
	}

	if len(covered) > 0 {
		parts = append(parts, "its recording covers "+strings.Join(covered, ", ")+" and touched none of the changed units")
	}

	out.why = strings.Join(parts, "; ") + ", the only change behind " + labels

	return out
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	slices.Sort(out)

	return out
}
