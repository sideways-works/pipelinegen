// Package affected decides which slices a change reaches, and why.
package affected

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

// Differ is bazel-diff: it hashes a workspace, compares two hash files and
// explains what made a target differ.
type Differ interface {
	// Hashes writes the hashes of the workspace in dir to hashes and its
	// dependency edges to edges.
	Hashes(ctx context.Context, bz *bazel.Client, dir, hashes, edges string) error
	// Impacted lists the labels of the targets that differ between the
	// workspaces two hash files were made from.
	Impacted(ctx context.Context, bz *bazel.Client, base, head string) ([]string, error)
	// Explain names the root causes of a target's difference: the targets
	// whose own hash changed, with the path from each to it.
	Explain(ctx context.Context, base, head, headEdges, target string) ([]RootCause, error)
}

// RootCause is a changed target and its path to the explained one.
type RootCause struct {
	Label string
	Path  []string
}

// Plan is what a run runs: per suite, the slices it selected, why, and
// those it left out.
type Plan struct {
	Base      string      `json:"base"`
	Head      string      `json:"head"`
	All       bool        `json:"all"`
	Reason    string      `json:"reason"`
	Suites    []SuitePlan `json:"suites"`
	Narrowing int         `json:"narrowing,omitempty"`
	// Narrowings are the narrowings whose level the config sets in place
	// of their declarer's.
	Narrowings   []plugins.Override `json:"narrowings,omitempty"`
	Plugins      *plugins.Report    `json:"plugins,omitempty"`
	Lists        *lists.Source      `json:"lists,omitempty"`
	Warnings     []string           `json:"warnings,omitempty"`
	Rebuild      *Rebuild           `json:"rebuild,omitempty"`
	Unobservable []Unobservable     `json:"unobservable,omitempty"`
	// LabelGroups is how many groups of recorded slices Bazel was asked
	// which of their impacted labels anything else that changed reaches.
	LabelGroups      int       `json:"label_groups,omitempty"`
	LabelHashSeconds []float64 `json:"label_hash_seconds,omitempty"`
	// Config is each part of the config the change edited and what it means
	// for the tests; ConfigUnderstood tells whether the planner understood
	// every part.
	Config           []config.Part `json:"config,omitempty"`
	ConfigUnderstood bool          `json:"config_understood,omitempty"`
}

// Rebuild is what a cold rebuild of the head's graph found: each answer that
// differs from what the cache serves, and the error that kept it from
// finishing. Check is the config's rebuild_check.
type Rebuild struct {
	Check       string               `json:"check"`
	Differences []plugins.Difference `json:"differences,omitempty"`
	Error       string               `json:"error,omitempty"`
}

// SuitePlan is one suite's part of a plan.
type SuitePlan struct {
	Suite      string              `json:"suite"`
	All        bool                `json:"all"`
	Reason     string              `json:"reason"`
	Selected   []bazel.Slice       `json:"selected"`
	Reasons    map[string][]Reason `json:"reasons"`
	Causes     []Cause             `json:"causes,omitempty"`
	Untouched  []string            `json:"untouched"`
	Removed    []string            `json:"removed"`
	WholeSuite []string            `json:"whole_suite"`
	Narrowed   []Narrowed          `json:"narrowed,omitempty"`
	ObservedBy []string            `json:"observed_by,omitempty"`
	// Unlisted names, in a recorded suite, the slices without a list yet,
	// each bound to every unit the plugins list.
	Unlisted []string `json:"unlisted,omitempty"`

	slices []bazel.Slice
}

// Narrowed is a selection made smaller than what the change reaches: sampled
// or narrowed. The nightly traces its failures to it.
type Narrowed struct {
	Kind      string   `json:"kind"`
	Narrowing string   `json:"narrowing"`
	OffAt     int      `json:"off_at"`
	Reason    string   `json:"reason"`
	Reached   int      `json:"reached"`
	Dropped   []string `json:"dropped"`
}

// The kinds of Narrowed.
const (
	NarrowedSampled = "sampled"
	NarrowedBy      = "narrowed"
)

// Reason is one cause of a slice's selection.
type Reason struct {
	Kind string `json:"kind"`
	// What changed: a file, a test, a declaration, a target, or why
	// everything runs.
	What    string   `json:"what"`
	Through []string `json:"through,omitempty"`
	Hops    []Hop    `json:"hops,omitempty"`
}

// Hop is one step of a reason's path: the unit it reaches, as its sbom
// names it, and the link that leads there, with the plugin that gave it.
type Hop struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	Plugin string `json:"plugin,omitempty"`
	Back   bool   `json:"back,omitempty"`
	Label  string `json:"label,omitempty"`
	File   string `json:"file,omitempty"`
}

// The kinds of Reason.
const (
	KindEverything  = "everything"
	KindNewSlice    = "new-slice"
	KindTestAdded   = "test-added"
	KindTestRemoved = "test-removed"
	KindTestChanged = "test-changed"
	KindExecuted    = "executed"
	KindDepends     = "depends"
	KindOpens       = "opens"
	KindTarget      = "target"
	// KindInputAdded, KindInputRemoved and KindInputUnknown are a Bazel
	// label of a slice's inputs (What) that the head's inputs gained, that
	// they lost, or that the head's hashes don't hold, so whether it
	// changed can't be told.
	KindInputAdded   = "input-added"
	KindInputRemoved = "input-removed"
	KindInputUnknown = "input-unknown"
	// KindPlugin is a changed entry of a plugin that binds the slice:
	// What is the plugin's name.
	KindPlugin = "plugin"
	// KindTextInput is a changed file the suite's text_inputs say the
	// slice reads as text (What): a change its sbom calls unobservable is
	// one such a test can observe, so the labels holding it select.
	KindTextInput = "text-input"
	// KindBaseConfig is a slice the base's configuration selects: a change
	// that edits the config can add selection but never remove it for itself.
	KindBaseConfig = "base-config"
)

// Options are one planning's inputs.
type Options struct {
	Config    *config.Config
	Repo      gitx.Repo
	Cache     string
	Base      string
	Head      string
	All       bool
	AllReason string
	Differ    Differ
	Bazel     func(dir string) *bazel.Client
	// Claimed are the head's files an sbom plugin claims, with the plugin:
	// such a change is carried by its units, so no Bazel target needs to own
	// it.
	Claimed map[string]string
	Written func(tree string) (string, error)
	Expect  func(commit string) int
	Warn    func(format string, args ...any)
	// ToolKey names the tools a snapshot depends on besides the commit and
	// the config (pipelinegen's build, bazel-diff): cached snapshots are
	// reused only under the same key.
	ToolKey string
	// Prepare, when set, writes what a work tree needs before Bazel reads it
	// (the deps repository) and returns a key for it: snapshots are cached
	// by commit and key.
	Prepare func(ctx context.Context, tree, commit string, tests map[string][]bazel.Slice) (string, error)
	Stub    func(tree string) error
	Reasons func(base, head, target string) ([]Reason, bool)
	// ExplainLimit bounds how many slices of a plan bazel-diff is asked to
	// explain, one process each; zero, no bound; negative, none is asked.
	ExplainLimit int
	explained    *explainBudget
	Why          func(base, head, target string, cause RootCause) ([]Reason, bool)
	Narrowed     func(base, head, suite, width string, selected, left []bazel.Slice) []Narrowed
	EntryPool    func(base, head, suite, width string) map[string]bool
	Meaning      *config.Meaning
	Unlisted     func(commit, suite string) []string
	// Sample, when set, says how a suite samples; unset, the size comes from
	// the config and only sample_on entries sample.
	Sample func(suite string) *SampleRule
	// Follow, when set, says how far a change is followed in a suite; unset
	// is executed.
	Follow func(suite string) string
	// Select, when set, is the walk over the plugins' graph that selects the
	// slices of recorded suites; unset, those suites are selected by their
	// selectors.
	Select *graph.Selection
	// Alone, when set, names a suite's slices in a tree that nothing but
	// their own files binds; each is selected whenever any file changed.
	Alone func(commit, suite string) []string
	// Inputs are, while Select is set, the Bazel labels a recorded slice
	// depends on whatever its list says; it fails for a tree or slice whose
	// inputs were never recorded.
	Inputs func(commit, suite, slice string) ([]string, error)
	// Through, when set, names the filegroups a slice's input label was
	// found through at a commit, innermost first.
	Through      func(commit, suite, slice, label string) []string
	Unobservable func(base, head string, changes []gitx.Change) UnobservableRule
	// Declared, when set, are the labels a recorded slice's test file names
	// in its own pipelinegen:data lines, which stay a reason whatever the
	// files behind them.
	Declared func(commit, suite, slice string) []string
	// Rebase, when set, compares each reached slice from a later pass of
	// its own.
	Rebase *Rebase
	quiet  *quiet
	pinned func(repo string) (changed bool, why string)
}

// SampleRule is how a suite picks its sample.
type SampleRule struct {
	// On tells whether the sample narrowings are in force; a sample_on entry
	// samples either way.
	On      bool
	N       int
	Group   func(slice string) string
	OffAt   map[string]int
	EntryOn bool
}

// The narrowings a sample stands for: a change to what every slice ran,
// to what every slice binds only through that, or to setup-only code.
const (
	SampledBySample      = "sample"
	SampledBySampleBound = "sample-bound"
	SampledBySetup       = "setup-split"
	SampledByPluginEntry = "plugin-entry"
)

// SampleTarget is the target in the deps repository bound to the units a
// suite's recorded slices all ran.
func SampleTarget(suite string) string { return "_sample." + suite }

// SampleBoundTarget is the target bound to the units a suite's recorded
// slices all bind only through what they all ran, or because they run at
// start-up.
func SampleBoundTarget(suite string) string { return "_sample-bound." + suite }

// EntryTarget is the target bound to the plugin entries a suite's
// recorded slices depend on, while plugin-entry is on.
func EntryTarget(suite string) string { return "_sample-entry." + suite }

func (o Options) warnf(format string, args ...any) {
	if o.Warn != nil {
		o.Warn(format, args...)
	}
}

const dependentsSuffix = ".dependents"

func (o Options) width(suite string) string {
	if o.Follow == nil {
		return config.FollowExecuted
	}

	return o.Follow(suite)
}

func selectorFor(label, width string) string {
	if width != config.FollowDependents || !strings.HasPrefix(label, bazel.SelectorLabel("")) {
		return label
	}

	return label + dependentsSuffix
}

func atWidth(impacted map[string]bool, suite, width string, all []bazel.Slice) map[string]bool {
	if width != config.FollowDependents {
		return impacted
	}

	out := make(map[string]bool, len(impacted))
	maps.Copy(out, impacted)

	own := []string{
		bazel.SelectorLabel(recording.SetupTarget(suite)),
		bazel.SelectorLabel(SampleTarget(suite)), bazel.SelectorLabel(SampleBoundTarget(suite)),
		bazel.SelectorLabel(EntryTarget(suite)),
	}

	for _, label := range append(own, labelsOf(all)...) {
		if sel := selectorFor(label, width); sel != label {
			out[label] = impacted[sel]
		}
	}

	return out
}

func labelsOf(all []bazel.Slice) []string {
	out := make([]string, 0, len(all))
	for _, sl := range all {
		out = append(out, sl.Label)
	}

	return out
}

// ErrHead wraps a head that can't be planned: without it there is nothing to
// run.
var ErrHead = errors.New("head")

const noSelectors = "no selectors for the test files: learn's stub did not run"

// explainBudget is how many targets bazel-diff may still explain in one
// plan; what it explained is kept by target, so every slice selected through
// the same label shares one answer.
type explainBudget struct {
	limited       bool
	left, skipped int
	inputs        map[string]explainedInput
	files         map[string][]string
	changed       map[string]bool
}

func withExplainBudget(o Options) (Options, func()) {
	o.explained = &explainBudget{
		limited: o.ExplainLimit != 0, left: max(o.ExplainLimit, 0),
		inputs: map[string]explainedInput{}, files: map[string][]string{},
	}

	return o, func() {
		if o.explained.skipped > 0 {
			o.warnf("%d selected slices were not explained (explain_limit %d): pipelinegen why --slice explains one", o.explained.skipped, max(o.ExplainLimit, 0))
		}
	}
}

const maxRootCauses = 3

// Compute plans a run.
func Compute(ctx context.Context, o Options) (*Plan, error) {
	// git runs in the repository, so a relative cache would land inside it
	cache, err := filepath.Abs(o.Cache)
	if err != nil {
		return nil, fmt.Errorf("cache: %w", err)
	}

	o.Cache = cache

	unlock, err := lockCache(cache)
	if err != nil {
		return nil, err
	}
	defer unlock()

	// with the lock held, no plan is hashing the head without some files:
	// a directory one left is a plan's that died
	sweepWithout(cache)

	var warnings []string

	if o.Warn == nil {
		o.Warn = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	}

	plan, err := compute(ctx, o)
	if plan != nil {
		plan.Warnings = append(plan.Warnings, warnings...)
	}

	return plan, err
}

func compute(ctx context.Context, o Options) (*Plan, error) {
	head, ok, err := o.Repo.Resolve(ctx, o.Head)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, fmt.Errorf("%w: no commit %q", ErrHead, o.Head)
	}

	plan := &Plan{Head: head}

	o, explained := withExplainBudget(o)
	defer explained()

	reason, base, err := everythingReason(ctx, o, head)
	if err != nil {
		return nil, err
	}

	plan.Base = namedBase(ctx, o, base)

	if reason != "" {
		h, err := slicesOnly(ctx, o, head)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrHead, err)
		}

		return everything(plan, o.Config, h.Slices, reason), nil
	}

	b, err := snapshot(ctx, o, base)
	if errors.Is(err, ErrMoved) {
		// not the base's fault: planning again gives the honest answer
		return nil, err
	}

	if err != nil {
		h, herr := slicesOnly(ctx, o, head)
		if herr != nil {
			return nil, fmt.Errorf("%w: %w", ErrHead, herr)
		}

		return everything(plan, o.Config, h.Slices, fmt.Sprintf("the base %s could not be hashed: %v", short(base), err)), nil
	}

	h, err := headSnapshot(ctx, o, head)
	if err != nil {
		return nil, err
	}

	impacted, reason, err := impactedTargets(ctx, o, b, h)
	if err != nil {
		return nil, err
	}

	if reason != "" {
		return everything(plan, o.Config, h.Slices, reason), nil
	}

	changes, err := o.Repo.Diff(ctx, base, head)
	if err != nil {
		return nil, err
	}

	if err := o.withQuiet(plan, b, h, changes).selectInto(ctx, plan, b, h, impacted, changes); err != nil {
		return nil, err
	}

	return plan, nil
}

func (o Options) selectInto(ctx context.Context, plan *Plan, b, h *Snapshot, impacted map[string]bool, changes []gitx.Change) error {
	suites, err := selectSuites(ctx, o, b, h, impacted, changes)
	if err != nil {
		return err
	}

	plan.Suites, plan.LabelGroups = suites, o.quiet.count()
	if o.quiet != nil {
		plan.LabelHashSeconds = o.quiet.seconds
	}

	return nil
}

func selectSuites(ctx context.Context, o Options, b, h *Snapshot, impacted map[string]bool, changes []gitx.Change) ([]SuitePlan, error) {
	var out []SuitePlan

	hashed := hashedLabels(h.Hashes)
	o.pinned = o.pinJudge(ctx, b.Commit, h.Commit)

	if o.explained != nil {
		o.explained.changed = map[string]bool{}
		for _, c := range changes {
			o.explained.changed[c.Path] = true
		}
	}

	for _, name := range o.Config.SuiteNames() {
		// a test file's selector exists only once Prepare wrote it: without
		// one nothing could select the file, so every file runs (a walked
		// suite needs none)
		if o.Config.Suites[name].Implicit() && o.Prepare == nil && !o.walks(name) {
			for _, s := range everything(&Plan{}, o.Config, map[string][]bazel.Slice{name: h.Slices[name]}, noSelectors).Suites {
				if s.Suite == name {
					out = append(out, s)
				}
			}

			continue
		}

		var walked *joined

		seen := atWidth(impacted, name, o.width(name), h.Slices[name])
		if o.walks(name) {
			j, err := o.join(ctx, name, b, h, impacted, changes, hashed)
			if err != nil {
				return nil, err
			}

			walked, seen = &j, j.seen
		}

		// a slice nothing but its own file binds runs on every change
		alone, seen, forced := o.aloneRule(name, h, seen, changes)

		rule := o.sampleRule(name)
		on := sampleOnHit(o.Config, name, o.beyondConfig(changes))
		sp := selectSuite(name, o.Config.Suites[name], h.Slices[name], seen, rule.On)

		sp, seen, since := o.rebase(name, sp, h.Slices[name], seen, rule, on)
		sp.slices = h.Slices[name]

		if walked != nil {
			sp.Narrowed = append(sp.Narrowed, o.quiet.narrowed(name, sp, walked.quiet, h.Slices[name])...)
		}

		// the slices selected on their own have reasons; those a sample
		// added have the sample's
		own := sp.Selected
		sp.Reasons = o.suiteReasons(ctx, b, h, name, unforced(own, forced), walked, changes)

		o.sampleConfigured(&sp, name, b, h, seen, rule, on, changes)

		sp.Removed = removed(b.Slices[name], h.Slices[name])
		aloneReasons(&sp, alone)

		sp.Causes = causesOf(sp)
		passedSince(&sp, since)

		if o.Narrowed != nil && !sp.All {
			sp.Narrowed = append(sp.Narrowed, o.leftOut(b, h, name, sp)...)
		}

		out = append(out, sp)
	}

	return out, nil
}

// Everything says, before any tree is checked out, why a plan with these
// options runs every slice; empty when the plan has to compare the trees.
func Everything(ctx context.Context, o Options) (string, error) {
	head, ok, err := o.Repo.Resolve(ctx, o.Head)
	if err != nil {
		return "", err
	}

	if !ok {
		return "", fmt.Errorf("%w: no commit %q", ErrHead, o.Head)
	}

	reason, _, err := everythingReason(ctx, o, head)

	return reason, err
}

// namedBase is the plan's base: a plan of everything still names it, so
// the step that writes the plan can tell it is this change's.
func namedBase(ctx context.Context, o Options, base string) string {
	if base != "" || o.Base == "" {
		return base
	}

	if resolved, ok, err := o.Repo.Resolve(ctx, o.Base); err == nil && ok {
		return resolved
	}

	return ""
}

func everythingReason(ctx context.Context, o Options, head string) (reason, base string, err error) {
	if o.All && o.AllReason != "" {
		return o.AllReason, "", nil
	}

	if o.All {
		return "every slice runs in this run", "", nil
	}

	if o.Base == "" {
		return "nothing to compare with", "", nil
	}

	base, ok, err := o.Repo.Resolve(ctx, o.Base)
	if err != nil {
		return "", "", err
	}

	if !ok {
		return fmt.Sprintf("the base %s is unknown", o.Base), "", nil
	}

	if yes, err := o.Repo.IsAncestor(ctx, base, head); err != nil {
		return "", "", err
	} else if !yes {
		return fmt.Sprintf("the base %s is no ancestor of the head", short(base)), base, nil
	}

	changes, err := o.Repo.Diff(ctx, base, head)
	if err != nil {
		return "", "", err
	}

	for _, c := range changes {
		if p, ok := o.Config.MatchesEverythingOn(c.Path); ok {
			return fmt.Sprintf("%s changed (everything_on %s)", c.Path, p), base, nil
		}
	}

	return "", base, nil
}

func impactedTargets(ctx context.Context, o Options, b, h *Snapshot) (map[string]bool, string, error) {
	changes, err := o.Repo.Diff(ctx, b.Commit, h.Commit)
	if err != nil {
		return nil, "", err
	}

	for _, c := range changes {
		// a sample_on file needs no target: its change samples; the
		// config, when what its change means is understood, needs none
		// either: it selects by meaning
		if h.Owned[c.Path] || b.Owned[c.Path] || o.Config.MatchesSampleOn(c.Path) || c.Path == config.FileName && o.understood() {
			continue
		}

		if _, ok := o.Claimed[c.Path]; ok {
			continue
		}

		// the owner declared that no test needs to run for it: it selects
		// nothing by itself, and the plan says so
		if u, ok := o.Config.UntestedFor(c.Path); ok {
			o.warnf("%s: untested by declaration: %s", c.Path, u.Reason)

			continue
		}

		return nil, unownedReason(c.Path), nil
	}

	labels, err := o.Differ.Impacted(ctx, o.Bazel(workspaceDir(o)), b.Hashes, h.Hashes)
	if err != nil {
		return nil, "bazel-diff failed: " + err.Error(), nil //nolint:nilerr // a failed query selects everything
	}

	impacted := make(map[string]bool, len(labels))
	for _, l := range labels {
		impacted[bazel.NormalLabel(l)] = true
	}

	return impacted, "", nil
}

func everything(plan *Plan, cfg *config.Config, all map[string][]bazel.Slice, reason string) *Plan {
	plan.All = true
	plan.Reason = reason
	plan.Suites = nil

	for _, name := range cfg.SuiteNames() {
		sp := SuitePlan{
			Suite: name, All: true, Reason: reason, Selected: all[name],
			Reasons: map[string][]Reason{}, Untouched: []string{}, Removed: []string{}, WholeSuite: []string{},
			slices: all[name],
		}

		if sp.Selected == nil {
			sp.Selected = []bazel.Slice{}
		}

		for _, sl := range sp.Selected {
			sp.Reasons[sl.Label] = []Reason{{Kind: KindEverything, What: reason}}
		}

		for _, w := range cfg.Suites[name].WholeSuite {
			sp.WholeSuite = append(sp.WholeSuite, w.Name)
		}

		plan.Suites = append(plan.Suites, sp)
	}

	return plan
}

// ReachesEverySlice is a suite's reason when each of its slices was
// selected for reasons of its own.
const ReachesEverySlice = "the change reaches every slice"

func selectSuite(name string, s config.Suite, all []bazel.Slice, impacted map[string]bool, sampled bool) SuitePlan {
	sp := SuitePlan{Suite: name, Selected: []bazel.Slice{}, Untouched: []string{}, Removed: []string{}, WholeSuite: []string{}}

	for _, sl := range all {
		if impacted[sl.Label] {
			sp.Selected = append(sp.Selected, sl)
		} else {
			sp.Untouched = append(sp.Untouched, sl.Label)
		}
	}

	if len(all) > 0 && len(sp.Untouched) == 0 {
		sp.All = true
		sp.Reason = ReachesEverySlice
	}

	// code only the suite's setup ran changed: it can break any slice
	if len(all) > 0 && !sampled && impacted[bazel.SelectorLabel(recording.SetupTarget(name))] {
		sp.Selected, sp.Untouched = slices.Clone(all), []string{}
		sp.All = true
		sp.Reason = "code only the suite's setup runs changed"
	}

	for _, w := range s.WholeSuite {
		if sp.All || slices.ContainsFunc(w.AlsoOn, func(l string) bool { return impacted[l] }) {
			sp.WholeSuite = append(sp.WholeSuite, w.Name)
		}
	}

	sort.Strings(sp.Untouched)

	return sp
}

// leftOut are the Narrowed hook's entries for the slices of a suite the plan
// left out. A slice the sample left out is the sample's: the change reached
// it, so no narrowing left it out.
func (o Options) leftOut(b, h *Snapshot, name string, sp SuitePlan) []Narrowed {
	rest := left(h.Slices[name], sp.Selected)
	for _, n := range sp.Narrowed {
		if n.Kind == NarrowedSampled {
			rest = slices.DeleteFunc(rest, func(sl bazel.Slice) bool { return slices.Contains(n.Dropped, sl.Name) })
		}
	}

	// a slice left out for its later pass was reached: no narrowing left it out
	passed := passedOut(sp)
	rest = slices.DeleteFunc(rest, func(sl bazel.Slice) bool { return passed[sl.Name] })

	return unnamed(sp.Narrowed, o.Narrowed(b.Commit, h.Commit, name, o.width(name), sp.Selected, rest), len(sp.Selected))
}

func unnamed(named, got []Narrowed, selected int) []Narrowed {
	var out []Narrowed

	for _, n := range got {
		n.Dropped = slices.DeleteFunc(slices.Clone(n.Dropped), func(slice string) bool {
			return slices.ContainsFunc(named, func(m Narrowed) bool { return m.Narrowing == n.Narrowing && slices.Contains(m.Dropped, slice) })
		})

		if len(n.Dropped) > 0 {
			n.Reached = selected + len(n.Dropped)
			out = append(out, n)
		}
	}

	return out
}

func left(all, selected []bazel.Slice) []bazel.Slice {
	in := map[string]bool{}
	for _, sl := range selected {
		in[sl.Label] = true
	}

	var out []bazel.Slice

	for _, sl := range all {
		if !in[sl.Label] {
			out = append(out, sl)
		}
	}

	return out
}

func removed(base, head []bazel.Slice) []string {
	have := map[string]bool{}
	for _, sl := range head {
		have[sl.Label] = true
	}

	out := []string{}

	for _, sl := range base {
		if !have[sl.Label] {
			out = append(out, sl.Label)
		}
	}

	sort.Strings(out)

	return out
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}

	return commit
}

func unownedReason(p string) string {
	reason := p + " changed and no Bazel target owns it"
	if strings.HasSuffix(p, ".go") {
		reason += ": it is in no target yet (run gazelle)"
	}

	return reason
}
