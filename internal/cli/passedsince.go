package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

// passedSinceBases is how many commits after the base a plan compares slices from, each one more plan of
// what changed from there to the head.
const passedSinceBases = 2

func (x planExtra) explainLimit(cfg *config.Config) int {
	if x.reach {
		return -1
	}

	return cfg.ExplainLimitOrDefault()
}

// What a change from a commit to the head reaches in one suite.
type reached struct {
	all, sampled bool
	slices       map[string]bool
}

// passedSinceBase compares each slice from the newer of the run's base and the newest commit at which it
// passed since; anything it can't tell keeps the run's base, with a note for the plan's warnings.
func passedSinceBase(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, base, head, cache, bdPath string) (*affected.Rebase, []string) {
	run := cfg.Runs[ch.Run]

	if run.Compare != config.CompareLastPassed && run.Compare != config.CompareMergedTarget || run.Select == config.SelectAll ||
		store == nil || base == "" || base == head {
		return nil, nil
	}

	offAt, on := passedSinceOn(ctx, cfg, trigger, base, head)

	switch {
	case !on:
		return nil, nil
	case run.Compare == config.CompareMergedTarget:
		return carriedOver(ctx, cfg, ch, store, trigger, base, head, cache, bdPath, offAt)
	case store.Proposed:
		return nil, nil
	}

	return branchPassedSince(ctx, cfg, ch, store, trigger, base, head, cache, bdPath, offAt)
}

// branchPassedSince is passedSinceBase for a branch's last_passed run, the narrowing on, off from offAt.
func branchPassedSince(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, base, head, cache, bdPath string, offAt int) (*affected.Rebase, []string) {
	if everything, err := affected.Everything(ctx, affected.Options{Config: cfg, Repo: gitx.Repo{Dir: trigger}, Base: base, Head: head}); err != nil || everything != "" {
		return nil, nil
	}

	progress.Stage(ctx, "the slices that passed since the base")

	commits, err := runs.FirstParents(ctx, trigger, head, 50)
	if err != nil {
		return nil, []string{fmt.Sprintf("every slice compares from the base %s: its first parents: %v", short12(base), err)}
	}

	at := slices.Index(commits, base)
	if at <= 0 {
		return nil, nil
	}

	passes, err := report.Passes(ctx, store, ch.Run, ch.ID, commits[:at])
	if err != nil {
		return nil, []string{fmt.Sprintf("every slice compares from the base %s: the runs' records: %v", short12(base), err)}
	}

	var notes []string

	reaches := map[string]map[string]reached{}

	for _, c := range newestPassed(commits[:at], passes) {
		r, err := reachFrom(ctx, cfg, store, trigger, ch.Run, c, head, cache, bdPath, false)
		if err != nil && !errors.Is(err, errReachesAll) {
			notes = append(notes, fmt.Sprintf("the slices that passed at %s compare from the base %s: what changed since could not be planned: %v", short12(c), short12(base), err))
		}

		if r != nil {
			reaches[c] = r
		}
	}

	if len(reaches) == 0 {
		return nil, notes
	}

	return &affected.Rebase{OffAt: offAt, Since: sinceOf(ch.Run, passes, reaches)}, notes
}

// sinceOf says of a slice where it last passed in the run, when nothing from there to the head
// reaches it.
func sinceOf(run string, passes map[string]report.Pass, reaches map[string]map[string]reached) func(string, bazel.Slice) (affected.Since, bool) {
	return func(suite string, sl bazel.Slice) (affected.Since, bool) {
		p, ok := passes[suite+"/"+sl.Name]
		if !ok || !passedAs(p.Label, sl) {
			return affected.Since{}, false
		}

		r, ok := reaches[p.Commit][suite]
		if !ok || r.all || r.slices[sl.Name] {
			return affected.Since{}, false
		}

		return affected.Since{
			Why:     fmt.Sprintf("compared from %s, where it last passed in run %s: nothing from there to the head reaches it", short12(p.Commit), run),
			Sampled: r.sampled,
		}, true
	}
}

// fullPasses is how many first parents back a run with passed_within looks for its slices' passes.
const fullPasses = 50

// keepPassed leaves out of a plan of every slice, for a run with passed_within, the slices that passed
// in the run within the period with nothing from there to the head reaching them; anything it can't
// tell keeps the slice, with a note for the plan's warnings.
func keepPassed(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, head, cache, bdPath string, plan *affected.Plan) []string {
	run := cfg.Runs[ch.Run]

	within, ok := run.PassedWithin.Duration()
	if !ok || run.Select != config.SelectAll || !plan.All || store == nil || store.Proposed ||
		narrowingLevel(cfg) >= passedSinceOffAt(cfg) {
		return nil
	}

	progress.Stage(ctx, "the slices that passed within passed_within")

	commits, err := runs.FirstParents(ctx, trigger, head, fullPasses)
	if err != nil {
		return []string{fmt.Sprintf("every slice runs: the first parents of %s: %v", short12(head), err)}
	}

	all, err := report.Passes(ctx, store, ch.Run, ch.ID, commits)
	if err != nil {
		return []string{fmt.Sprintf("every slice runs: the run's records: %v", err)}
	}

	cut := time.Now().Add(-within)
	passes := map[string]report.Pass{}

	for k, p := range all {
		if p.At.After(cut) && !strings.HasPrefix(head, p.Commit) {
			passes[k] = p
		}
	}

	var notes []string

	reaches := map[string]map[string]reached{}

	for _, c := range newestPassed(commits, passes) {
		r, err := reachFrom(ctx, cfg, store, trigger, ch.Run, c, head, cache, bdPath, false)
		if err != nil && !errors.Is(err, errReachesAll) {
			notes = append(notes, fmt.Sprintf("the slices that passed at %s run again: what changed since could not be planned: %v", short12(c), err))
		}

		if r != nil {
			reaches[c] = r
		}
	}

	if len(reaches) > 0 {
		affected.KeepPassed(plan, cfg, &affected.Rebase{OffAt: passedSinceOffAt(cfg), Since: sinceOf(ch.Run, passes, reaches)})
	}

	return notes
}

// passedAs tells whether a pass recorded under label is one of sl. A test file's slice is recorded
// under its name, since its batch carries no label, while its plan names it by the deps repository's
// selector; a slice of a target is recorded under the target's label.
func passedAs(label string, sl bazel.Slice) bool {
	return label == "" || label == sl.Label || label == sl.Name && bazel.NormalLabel(sl.Label) == bazel.SelectorLabel(sl.Name)
}

// passedSinceOn is the level passed-since is off from and whether it is on, for a branch's run and a
// pull request's alike: under the change's config and, when the change edits it, under the base's
// too, as the plan unites the two.
func passedSinceOn(ctx context.Context, cfg *config.Config, trigger, base, head string) (int, bool) {
	offAt := passedSinceOffAt(cfg)
	on := narrowingLevel(cfg) < offAt

	byBase, changed, err := baseConfig(ctx, trigger, base, head)
	if err != nil {
		return offAt, false
	}

	if changed {
		offAt = min(offAt, passedSinceOffAt(byBase))
		on = on && narrowingLevel(byBase) < offAt
	}

	return offAt, on
}

// passedSinceOffAt is the level passed-since is off from: the config's own (narrowings.passed-since),
// else the core's.
func passedSinceOffAt(cfg *config.Config) int {
	if n, ok := cfg.Narrowings[plugins.NarrowPassedSince]; ok {
		return n.OffAt
	}

	return plugins.CoreOffAt(plugins.NarrowPassedSince)
}

// newestPassed are the newest commits, at most passedSinceBases, at which some slice last passed.
func newestPassed(commits []string, passes map[string]report.Pass) []string {
	at := map[string]bool{}
	for _, p := range passes {
		at[p.Commit] = true
	}

	var out []string

	for _, c := range commits {
		if at[c] && len(out) < passedSinceBases {
			out = append(out, c)
		}
	}

	return out
}

// errReachesAll is a change from a commit to the head that reaches every slice, or that no plan
// compares: a changed config, a plan of everything, a plan cut short with its build.
var errReachesAll = errors.New("the change reaches every slice")

// reachFrom plans what the change from c to the head reaches, suite by suite. A change to the config in
// between and a plan of everything reach everything (errReachesAll); any other error says why the
// change couldn't be planned, and reaches everything too.
//
// With trees, c need not be the head's ancestor: the plan compares the two trees as they are.
func reachFrom(ctx context.Context, cfg *config.Config, store *gitstore.Store, trigger, run, c, head, cache, bdPath string, trees bool) (map[string]reached, error) {
	if _, changed, err := baseConfig(ctx, trigger, c, head); err != nil || changed {
		return nil, errReachesAll
	}

	// the run's own plan keeps most of what limits.plan leaves
	sub, cancel := ctx, func() {}
	if deadline, ok := ctx.Deadline(); ok {
		sub, cancel = context.WithTimeout(ctx, time.Until(deadline)/3)
	}

	defer cancel()

	p, err := planCommitsBy(sub, cfg, store, trigger, run, c, head, false, cache, bdPath, pluginsDir(), planExtra{reach: true, trees: trees})
	if err != nil {
		if ctx.Err() != nil {
			return nil, errReachesAll
		}

		if sub.Err() != nil {
			err = fmt.Errorf("it took longer than a third of what limits.plan left: %w", err)
		}

		return nil, err
	}

	if p.All {
		return nil, errReachesAll
	}

	out := map[string]reached{}

	for _, sp := range p.Suites {
		r := reached{all: sp.All, slices: map[string]bool{}}

		for _, sl := range sp.Selected {
			r.slices[sl.Name] = true
		}

		r.sampled = slices.ContainsFunc(sp.Narrowed, func(n affected.Narrowed) bool { return n.Kind == affected.NarrowedSampled })
		out[sp.Suite] = r
	}

	return out, nil
}
