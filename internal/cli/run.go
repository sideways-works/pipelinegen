package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/batch"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/durations"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

const (
	envTrigger = "PIPELINEGEN_TRIGGER"
	envRunFile = "PIPELINEGEN_RUN_FILE"
)

var (
	errMergeMoved = errors.New("the merge differs from the plan's")
	errNoPlan     = errors.New("no plan")
)

func changeFromBuild(ctx context.Context, cfg *config.Config, run string) (runs.Change, bool, error) {
	if f := os.Getenv(envRunFile); f != "" {
		ch, err := runs.ReadRunFile(f)

		return ch, err == nil, err
	}

	if dir := os.Getenv(envTrigger); dir != "" && run != "" {
		ch, err := runs.FromTrigger(ctx, cfg, run, dir)

		return ch, err == nil, err
	}

	return runs.Change{}, false, nil
}

var errUncovered = errors.New("uncovered files")

type buildFlags struct {
	common

	run, suite, trigger string
}

func (b *buildFlags) register(fs *flag.FlagSet) {
	b.common.register(fs)
	textVar(fs, &b.run, "run", "", "the run, by its name in the config")
	textVar(fs, &b.suite, "suite", "", "the suite")
	pathVar(fs, &b.trigger, "trigger", "trigger", "the trigger's checkout", reads)
}

func (b *buildFlags) open(ctx context.Context) (*config.Config, runs.Change, error) {
	if b.run == "" {
		return nil, runs.Change{}, fmt.Errorf("%w: -run is missing", errUsage)
	}

	// a step that reads its config and tree from a hand-off gets a
	// checkout of the change first, from the hand-off's trusted remote.
	handOff := b.trigger

	checkout, err := handOffCheckout(ctx, b.run, b.trigger, b.root == b.trigger)
	if err != nil {
		return nil, runs.Change{}, err
	}

	if checkout != "" {
		b.root, b.trigger = checkout, checkout
	}

	cfg, err := b.load()
	if err != nil {
		return nil, runs.Change{}, err
	}

	ch, err := runs.FromTrigger(ctx, cfg, b.run, handOff)
	if err != nil {
		return nil, runs.Change{}, err
	}

	if err := trustChange(cfg, ch); err != nil {
		return nil, runs.Change{}, err
	}

	return cfg, ch, nil
}

func reportID(ch runs.Change) report.ID {
	return report.ID{Run: ch.Run, Change: ch.ID, Commit: ch.Commit, PR: ch.PR, Proposed: ch.Proposed()}
}

func readPlan(ctx context.Context, store *gitstore.Store, id report.ID) (*affected.Plan, bool, error) {
	ref, ok, err := store.First(ctx, []string{id.PlanRef()})
	if err != nil || !ok {
		return nil, false, err
	}

	data, ok, err := store.File(ctx, ref.Commit, "plan.json")
	if err != nil || !ok {
		return nil, false, err
	}

	var p affected.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, false, fmt.Errorf("plan of %s: %w", id.Change, err)
	}

	return &p, true, nil
}

func suitePlan(p *affected.Plan, suite string) (affected.SuitePlan, bool) {
	for _, sp := range p.Suites {
		if sp.Suite == suite {
			return sp, true
		}
	}

	return affected.SuitePlan{}, false
}

func runPlan(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		b                              buildFlags
		cache, bdPath, out, from, gate string
		decide, begin                  bool
	)

	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	b.register(fs)
	b.spellings = true

	pathVar(fs, &cache, "cache", defaultCache(), "where work trees and hashes are kept", reads|writes)
	pathVar(fs, &bdPath, "bazel-diff", "", "a bazel-diff binary to run instead of the pinned release", readsFile)
	pathVar(fs, &out, "out", "", "plan into this directory, writing no record and no status (a proposed change's first step)", writes)
	pathVar(fs, &from, "from", "", "write the plan --out left in this directory and show the run (the second step)", reads)
	pathVar(fs, &gate, "gate", "", "write gate.json here, open only when this step hands the change on to its suite jobs: a branch's run when it wrote the plan, also writing version, the hand-off its suite jobs follow, and reading there the decision --decide left; a pull request's second step (-from) when it wrote the plan, or found it planned with none of its suites begun after missed_after, as the sweep would hand it on again", writes)
	fs.BoolVar(&begin, "begin", false, "a pull request's planning build, before its first step: record that it plans the head, so the run's sweep leaves the head to it until its plan's limit has passed; a refused write only warns")
	fs.BoolVar(&decide, "decide", false, "a continuous run's build, before its planner's prepare: decide whether it plans, waiting while a round of the run is in progress, and plan nothing; a build that won't plan writes the closed gate in --gate, one that will leaves its decision there for the plan step")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if err := planBegin(out, from, gate, decide, begin); err != nil {
		return err
	}

	b.root = b.trigger
	start := time.Now()

	cfg, ch, err := b.open(ctx)
	if errors.Is(err, runs.ErrSuperseded) {
		// the newer head is the sweep's to plan: nothing to write here.
		return say(stdout, "%s: %v\n", b.run, err)
	}

	if err != nil {
		return err
	}

	// after the store's scratch repository is back in the cache.
	defer trimAfter(cfg, cache, start)

	store := openStore(cfg)
	store.Seed = storeSeed(cache, store.Remote)
	store.Proposed = ch.Proposed() || !trustedRun(cfg, ch.Run, ch.ID)

	defer closeStore(store, &err)

	switch {
	case out != "":
		return planOut(ctx, cfg, ch, store, b.trigger, cache, bdPath, out, stdout)
	case from != "":
		return planFromGated(ctx, cfg, ch, store, b.trigger, from, gate, stdout)
	case begin:
		return beginPlanning(ctx, ch, store, stdout)
	case decide:
		return decideBeforePrepare(ctx, cfg, ch, store, b.trigger, gate, stdout)
	case gate == "":
		return planOneStep(ctx, cfg, ch, store, b.trigger, cache, bdPath, stdout)
	}

	return planBranchGated(ctx, cfg, ch, store, b.trigger, cache, bdPath, gate, stdout)
}

func planBranchGated(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, cache, bdPath, gate string, stdout io.Writer) error {
	prior, err := readDecided(gate)
	if err != nil {
		return err
	}

	return planGated(ctx, gate, ch, store, func() error {
		return planAfter(ctx, cfg, ch, store, trigger, cache, bdPath, stdout, prior)
	})
}

func planModes(out, from, gate string, decide, begin bool) error {
	switch {
	case begin && (decide || out != "" || from != "" || gate != ""):
		return fmt.Errorf("%w: --begin is a step of its own, before the plan: no --out, --from, --gate or --decide", errUsage)
	case out != "" && from != "":
		return fmt.Errorf("%w: -out and -from are two steps, never one", errUsage)
	case decide && (gate == "" || out != "" || from != ""):
		return fmt.Errorf("%w: --decide takes --gate, and neither --out nor --from", errUsage)
	case out != "" && gate != "":
		return fmt.Errorf("%w: -out hands nothing on: the gate is the second step's, -from", errUsage)
	}

	return nil
}

// planBegin writes the second step's gate shut before anything else, so every way the step ends
// without a hand-off leaves it shut.
func planBegin(out, from, gate string, decide, begin bool) error {
	if err := planModes(out, from, gate, decide, begin); err != nil {
		return err
	}

	if from == "" || gate == "" {
		return nil
	}

	if err := os.MkdirAll(gate, 0o750); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	return writeGate(filepath.Join(gate, "gate.json"), false)
}

func planFromGated(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, from, gate string, stdout io.Writer) error {
	handOn, err := planFrom(ctx, cfg, ch, store, trigger, from, stdout)
	if !handOn {
		endPlanning(ctx, ch, store, stdout)
	}

	if err != nil || !handOn || gate == "" {
		return err
	}

	return writeGate(filepath.Join(gate, "gate.json"), true)
}

func planGated(ctx context.Context, gate string, ch runs.Change, store *gitstore.Store, plan func() error) error {
	_, before, err := readPlan(ctx, store, reportID(ch))
	if err != nil {
		return err
	}

	if err := plan(); err != nil {
		return err
	}

	_, after, err := readPlan(ctx, store, reportID(ch))
	if err != nil {
		return err
	}

	return writeHandOffGate(gate, ch, !before && after)
}

// A build that exited or planned nothing hands nothing on, so no suite job starts a build for its commit.
func writeHandOffGate(dir string, ch runs.Change, planned bool) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	if err := writeGate(filepath.Join(dir, "gate.json"), planned); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, "version"), []byte(ch.Run+" "+ch.Commit+"\n"), 0o600); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	return nil
}

// planOneStep plans and writes the plan in one step: only for a branch's change, whose code is trusted.
func planOneStep(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, cache, bdPath string, stdout io.Writer) error {
	return planAfter(ctx, cfg, ch, store, trigger, cache, bdPath, stdout, nil)
}

func planAfter(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, cache, bdPath string, stdout io.Writer, prior *roundDecision) (err error) {
	// a connector's change runs its code while planning: never in a step
	// that also writes.
	if ch.Connector != "" {
		return fmt.Errorf("%w: %s %s is a proposed change: plan it in two steps, --out then --from", errUsage, ch.Run, ch.ID)
	}

	if done, err := ranAlready(ctx, cfg, ch, store, stdout); done || err != nil {
		return err
	}

	id := reportID(ch)

	if isContinuous(cfg, ch.Run) {
		sweepOldRounds(ctx, cfg, ch.Run, os.Stderr)

		if run, _, err := decideRound(ctx, cfg, ch, store, trigger, stdout, prior); err != nil || !run {
			return err
		}

		var cancel context.CancelFunc

		ctx, cancel = planWithin(ctx, cfg)
		defer cancel()

		defer func() { err = planTookTooLong(ctx, cfg, err) }()
	}

	if ch.PR != 0 {
		admitted, why, err := runs.Admit(ctx, newGitHubHost(cfg), cfg, ch, plannedSinceIn(ctx, store, id))
		if err != nil {
			return err
		}

		if !admitted {
			// no plan: every suite job's gate skips.
			return say(stdout, "%s %s: not planned: %s\n", ch.Run, ch.ID, why)
		}
	}

	r := &report.Reporter{
		Config: cfg, ID: id, Store: store, Warn: os.Stderr, URL: os.Getenv(envJobURL),
		GitHub: host(cfg),
	}

	// a proposed change planned in one step runs its code where the store
	// is written.
	lines, err := proposedWarning(cfg, store)
	if err != nil {
		return errors.Join(err, r.PlanFailed(context.WithoutCancel(ctx), err))
	}

	plan, err := planChange(ctx, cfg, ch, store, trigger, cache, bdPath)
	if err != nil {
		// no suite may wait on a run that will never start.
		return errors.Join(err, r.PlanFailed(context.WithoutCancel(ctx), err))
	}

	sayFirst(plan, firstWarnings(cfg, ch.Run, lines))

	// the nightly rebuilds the graph cold; what it finds is in the plan
	// before any answer it changes is written.
	rb := rebuildFor(ctx, cfg, ch, store, trigger, plan, cache)

	if err := startRun(ctx, store, r, plan, rb); err != nil {
		return plannedAlready(err, ch, stdout)
	}

	sweepStore(ctx, cfg, ch, store, trigger, plan)

	return say(stdout, "%s %s: %s\n", ch.Run, ch.ID, summary(plan))
}

// The plan another planner wrote first stands, and this planner writes nothing more: no status, no
// answers and no sweep.
func plannedAlready(err error, ch runs.Change, stdout io.Writer) error {
	if errors.Is(err, report.ErrPlanned) {
		return say(stdout, "%s %s: planned already\n", ch.Run, ch.ID)
	}

	return err
}

// Only a trusted run's plan sweeps, as it alone writes what later plans read.
func sweepStore(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger string, plan *affected.Plan) {
	if store.Proposed {
		return
	}

	keep := report.KeepFrom(cfg)
	keep.Lists.Mainline = mainline(ctx, cfg, ch, trigger)

	if _, err := report.Sweep(ctx, store, time.Now(), keep, readLists(plan)); err != nil {
		warnf(os.Stderr, "pipelinegen plan: sweep: %v\n", err)
	}

	if _, err := plugincache.Drop(ctx, store, time.Now()); err != nil {
		warnf(os.Stderr, "pipelinegen plan: sweep of cached answers: %v\n", err)
	}

	if err := sweepSuperseded(ctx, store, time.Now()); err != nil {
		warnf(os.Stderr, "pipelinegen plan: sweep of superseded builds: %v\n", err)
	}
}

func rebuildFor(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger string, plan *affected.Plan, cache string) *rebuild {
	if !cfg.Rebuilds(ch.Run) || ch.Proposed() {
		return nil
	}

	rb := rebuildGraph(ctx, cfg, store, trigger, plan.Head, cache, pluginsDir())
	plan.Rebuild = rb.Found
	rb.say(os.Stderr)

	return rb
}

// The rebuild's answers are written only once the plan holding what it found is stored.
func startRun(ctx context.Context, store *gitstore.Store, r *report.Reporter, plan *affected.Plan, rb *rebuild) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	if err := report.WritePlan(ctx, store, r.ID, data); err != nil {
		return err
	}

	if err := r.Start(ctx); err != nil {
		return err
	}

	if rb != nil {
		if err := rb.write(ctx, os.Stderr); err != nil {
			warnf(os.Stderr, "pipelinegen plan: the cold rebuild's answers stay local: %v\n", err)
		}
	}

	return nil
}

func compareWith(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger string) (head, base, note string, err error) {
	head = ch.Commit

	switch cfg.Runs[ch.Run].Compare {
	case config.CompareMergedTarget:
		tip, err := runs.FetchTip(ctx, trigger, ch.Base, readAuth(ctx, cfg, trigger))
		if err != nil {
			return "", "", "", err
		}

		if head, err = runs.Merge(ctx, trigger, ch.Commit, tip); err != nil {
			return "", "", "", err
		}

		return head, tip, "", nil
	case config.CompareLastPassed:
		base, note, err = branchBase(ctx, cfg, ch, store, trigger)

		return head, base, note, err
	}

	return head, "", "", nil
}

func planChange(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, dir, cache, bdPath string) (*affected.Plan, error) {
	run := cfg.Runs[ch.Run]

	trigger, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	progress.Stage(ctx, "the commit under test and its base")

	head, base, note, err := compareWith(ctx, cfg, ch, store, trigger)
	if err != nil {
		return nil, err
	}

	plan, err := planCommits(ctx, cfg, store, trigger, ch.Run, base, head, run.Select == config.SelectAll, cache, bdPath, pluginsDir())
	if err == nil && note != "" {
		plan.Warnings = append(plan.Warnings, note)
	}

	return plan, err
}

// Without a store there are no lists, which binds every recorded slice to everything.
func storeLists(ctx context.Context, store *gitstore.Store, trigger, base string, walk int) (recording.Lists, *lists.Source, error) {
	if store == nil {
		return recording.Lists{}, nil, nil
	}

	return planningLists(ctx, store, trigger, base, walk)
}

// why plans this way too, so it explains the plan the planning job would make.
func planCommits(ctx context.Context, cfg *config.Config, store *gitstore.Store, trigger, runName, base, head string, all bool, cache, bdPath, plugins string) (*affected.Plan, error) {
	// a named run whose store isn't a proposed change's: the default
	// branch's own run (run.go sets Proposed for every other)
	trusted := runName != "" && store != nil && !store.Proposed

	p, err := withBaseConfig(ctx, cfg, trigger, runName, base, head, trusted, func(c *config.Config, everything string, meaning *config.Meaning) (*affected.Plan, error) {
		return planWith(ctx, c, store, trigger, runName, base, head, all, everything, meaning, cache, bdPath, plugins)
	})
	if p != nil && runName != "" {
		ofRun(p, cfg, runName)
	}

	return p, err
}

// Planning binds every suite of the config, but a run runs, reports and waits for its own alone; the
// suite jobs read a plan the same way, by their tree's config.
func ofRun(p *affected.Plan, cfg *config.Config, runName string) {
	p.Suites = slices.DeleteFunc(p.Suites, func(sp affected.SuitePlan) bool {
		return !cfg.RunCovers(runName, sp.Suite)
	})
}

// A change never deselects its own tests by editing the config: it plans under its config and, when it
// edits it, under the base's too, uniting the two. A base config that can't plan never stops the plan:
// a trusted run plans under its own alone and says so, any other runs everything.
func withBaseConfig(ctx context.Context, cfg *config.Config, trigger, run, base, head string, trusted bool, plan planFunc) (*affected.Plan, error) {
	p, err := withBase(ctx, cfg, trigger, run, base, head, trusted, plan)
	if p != nil {
		p.Warnings = append(slices.Clone(cfg.Warnings), p.Warnings...)

		for i, sp := range p.Suites {
			if cfg.Suites[sp.Suite].Bind == config.BindRecorded {
				p.Suites[i].ObservedBy = cfg.CorrelatorsFor(sp.Suite)
			}
		}
	}

	return p, err
}

type planFunc func(c *config.Config, everything string, meaning *config.Meaning) (*affected.Plan, error)

func withBase(ctx context.Context, cfg *config.Config, trigger, run, base, head string, trusted bool, plan planFunc) (*affected.Plan, error) {
	if base == "" {
		return plan(cfg, "", nil)
	}

	// the base's config first: what the change means for the tests is
	// planned under both configs.
	baseCfg, changed, baseErr := baseConfig(ctx, trigger, base, head)

	var meaning *config.Meaning
	if baseErr == nil && changed {
		meaning = config.MeaningOf(baseCfg, cfg, run)
	}

	own, err := plan(cfg, "", meaning)
	if err != nil {
		return own, err
	}

	// A trusted run's own config is the branch's, so it plans alone and says so loudly; a proposed change
	// could otherwise deselect its own tests by breaking the base's reading.
	unfit := func(why string) (*affected.Plan, error) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plan: %w", err)
		}

		if trusted {
			own.Warnings = append(own.Warnings, fmt.Sprintf("the base's configuration (%s) can't plan, so this plan is made under this commit's configuration alone: "+
				"a slice only the base's configuration would have selected may be left out (%s)", short12(base), why))

			return own, nil
		}

		return plan(cfg, "the base's configuration can't plan, so what it selected can't be known: "+why, nil)
	}

	if errors.Is(baseErr, config.ErrUnreadable) {
		if trusted {
			return unfit(baseErr.Error())
		}

		return plan(cfg, "the base's configuration can't be read, so what it selected can't be known: "+baseErr.Error(), nil)
	}

	if baseErr != nil || !changed {
		return own, baseErr
	}

	defer func() {
		if own != nil {
			for _, w := range baseCfg.Warnings {
				own.Warnings = append(own.Warnings, "the base's configuration: "+w)
			}
		}
	}()

	// how Bazel runs is the change's to say; what is selected is not.
	baseCfg.Bazel = cfg.Bazel

	byBase, err := plan(baseCfg, "", meaning)
	if err != nil {
		return unfit(err.Error())
	}

	own = affected.Union(own, byBase)
	if meaning != nil {
		own.Config, own.ConfigUnderstood = meaning.Parts, meaning.Understood()
	}

	return own, nil
}

func baseConfig(ctx context.Context, repo, base, head string) (*config.Config, bool, error) {
	r := gitx.Repo{Dir: repo}

	before, ok, err := r.Show(ctx, base, config.FileName)
	if err != nil || !ok {
		return nil, false, err
	}

	after, _, err := r.Show(ctx, head, config.FileName)
	if err != nil {
		return nil, false, err
	}

	if bytes.Equal(before, after) {
		return nil, false, nil
	}

	cfg, err := config.ParseHistory(before)
	if err != nil {
		return nil, false, fmt.Errorf("the base's configuration: %w", err)
	}

	return cfg, true, nil
}

// Without the first parents the sweep keeps lists by their age alone rather than take some still in use.
func mainline(ctx context.Context, cfg *config.Config, ch runs.Change, trigger string) []string {
	repo := gitx.Repo{Dir: trigger}

	if out, err := repo.Git(ctx, "rev-parse", "--is-shallow-repository"); err != nil || strings.TrimSpace(out) != "false" {
		return nil
	}

	tip := ""

	for _, ref := range []string{"refs/remotes/origin/" + cfg.Concourse.Branch, "refs/heads/" + cfg.Concourse.Branch} {
		if c, ok, err := repo.Resolve(ctx, ref); err == nil && ok {
			tip = c

			break
		}
	}

	if tip == "" && !ch.Proposed() && ch.Branch == cfg.Concourse.Branch {
		tip = ch.Commit
	}

	if tip == "" {
		return nil
	}

	out, err := repo.Git(ctx, "rev-list", "--first-parent", tip)
	if err != nil {
		return nil
	}

	return strings.Fields(out)
}

// The sweep keeps the lists a plan read whatever their age.
func readLists(plan *affected.Plan) []string {
	if plan.Lists == nil || plan.Lists.From == "" {
		return nil
	}

	return []string{plan.Lists.From}
}

func setLists(plan *affected.Plan, source *lists.Source) {
	plan.Lists = source

	if source != nil && source.Gone {
		plan.Warnings = append(plan.Warnings, lists.GoneText(*source))
	}
}

func checkCommit(ctx context.Context, trigger, head string) (failed []check.Finding, warnings []string, err error) { //nolint:nonamedreturns // the two kinds of finding
	findings, err := check.Commit(ctx, trigger, head, checkOptions())
	if err != nil {
		return nil, nil, err
	}

	for _, f := range findings {
		switch {
		// a config entry that can't be right fails the check, not the
		// plan: it says nothing about what a change reaches.
		case f.Warning || f.Kind == check.KindBadConfig:
			warnings = append(warnings, f.Path+": "+f.Problem+" ("+f.Fix+")")
		default:
			failed = append(failed, f)
		}
	}

	return failed, warnings, nil
}

// A file an sbom plugin claims is covered: its units carry a change. Claims are asked whenever the check
// finds an uncovered file, even in a plan that runs everything already, so a claimed file is never
// uncovered for want of asking.
func coverage(ctx context.Context, cfg *config.Config, runName, trigger, head string, all bool, allReason string, claims func() map[string]string) (bool, string, []string, error) {
	findings, warnings, err := checkCommit(ctx, trigger, head)
	if err != nil {
		return false, "", nil, err
	}

	if slices.ContainsFunc(findings, func(f check.Finding) bool { return f.Kind == check.KindUncovered }) {
		claimed := claims()

		findings = slices.DeleteFunc(findings, func(f check.Finding) bool {
			_, ok := claimed[f.Path]

			return ok && f.Kind == check.KindUncovered
		})
	}

	if len(findings) > 0 {
		if cfg.UncoveredFor(runName) == config.UncoveredFail {
			return false, "", nil, fmt.Errorf("%w: %s", errUncovered, describeFindings(findings))
		}

		if !all {
			return true, "files no slice covers: " + describeFindings(findings), warnings, nil
		}

		warnings = append(warnings, "files no slice covers: "+describeFindings(findings))
	}

	return all, allReason, warnings, nil
}

type planCheck struct {
	all      bool
	reason   string
	warnings []string
	claimed  map[string]string
}

// A reason to run every slice known before any tree is read stays the plan's reason; the check never
// takes its place.
func checkPlanned(ctx context.Context, cfg *config.Config, runName, trigger, base, head string, all bool, everything, cache, pluginsAt string) (planCheck, error) {
	before, err := affected.Everything(ctx, affected.Options{Config: cfg, Repo: gitx.Repo{Dir: trigger}, Base: base, Head: head, All: all, AllReason: everything})
	if err != nil {
		return planCheck{}, err
	}

	var (
		pc    planCheck
		asked bool
	)

	claims := func() map[string]string {
		if !asked {
			progress.Stage(ctx, "the plugins' claims")

			pc.claimed, asked = claimedOrNone(ctx, cfg, cache, pluginsAt, trigger, head), true
		}

		return pc.claimed
	}

	if before == "" {
		claims()
	}

	progress.Stage(ctx, "the check")

	pc.all, pc.reason, pc.warnings, err = coverage(ctx, cfg, runName, trigger, head, before != "", before, claims)

	return pc, err
}

func planWith(ctx context.Context, cfg *config.Config, store *gitstore.Store, trigger, runName, base, head string, all bool, everything string, meaning *config.Meaning, cache, bdPath string, pluginsAt string) (*affected.Plan, error) {
	pinPlanningToolchains(trigger, cache)

	pc, err := checkPlanned(ctx, cfg, runName, trigger, base, head, all || everything != "", everything, cache, pluginsAt)
	if err != nil {
		return nil, err
	}

	all, allReason, setupWarnings, claimed := pc.all, pc.reason, pc.warnings, pc.claimed

	differ, err := differFor(ctx, cfg, cache, bdPath)
	if err != nil {
		return nil, err
	}

	// without the deps repository in MODULE.bazel no selector can be seen:
	// then every test file runs, as affected does without Prepare.
	var (
		p      planning
		source *lists.Source
	)

	// a plan that runs everything for a reason known before any tree is
	// read binds nothing: it asks no plugin and reads no lists.
	known, err := affected.Everything(ctx, affected.Options{Config: cfg, Repo: gitx.Repo{Dir: trigger}, Base: base, Head: head, All: all, AllReason: allReason})
	if err != nil {
		return nil, err
	}

	deps := hasDepsRepo(filepath.Join(trigger, cfg.Bazel.Workspace))

	if loadsPlugins(cfg, deps) && known == "" {
		// both trees bind with the base's lists: lists that differ alone
		// select nothing.
		progress.Stage(ctx, "reading the lists")

		l, src, err := storeLists(ctx, store, trigger, base, cfg.ListsCommits())
		if err != nil {
			return nil, err
		}

		source = src

		var done func()
		if p, done, err = withPlugins(ctx, cfg, store, cache, l, marksOf(src), trigger, pluginsAt, base, head); err != nil {
			return nil, err
		}

		defer done()
	}

	progress.Stage(ctx, "selecting the slices")

	plan, err := affected.Compute(ctx, affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: trigger}, Cache: cache,
		Base: base, Head: head, All: all, AllReason: allReason, Differ: differ, ToolKey: toolKey(differ),
		Bazel:        bazelFor(cfg, trigger, cache),
		Prepare:      p.prepare,
		Stub:         stubFor(deps),
		Follow:       followFor(cfg, runName),
		Narrowed:     narrowedHook(p.book, p.walk, p.set),
		EntryPool:    entryPoolHook(p.book, p.walk),
		Meaning:      meaning,
		Unlisted:     p.book.unlistedHook(),
		Why:          p.book.whyHook(),
		Reasons:      p.book.reasonsHook(),
		ExplainLimit: cfg.ExplainLimitOrDefault(),
		Sample:       sampleRule(cfg, p.set),
		Written:      writtenHook(p.book),
		Expect:       p.book.expectHook(),
		Select:       p.walk.selection(),
		Inputs:       inputsHook(p.book, p.walk),
		Unobservable: unobservableHook(p.walk),
		Declared:     p.book.declaredHook(),
		Alone:        p.book.aloneHook(),
		Claimed:      claimed,
	})
	if err != nil {
		return nil, err
	}

	if p.set != nil {
		// first, so the plan's warnings name every stored answer that
		// differs from this plan's.
		if err := conflictWarnings(p.set, p.set.Flush(ctx)); err != nil {
			warnf(os.Stderr, "pipelinegen: the plugins' answers stay local: %v\n", err)
		}
	}

	setPlugins(plan, p.set)

	if err := finishPlan(plan, p, cfg, runName, source, setupWarnings); err != nil {
		return nil, err
	}

	return plan, nil
}

func finishPlan(plan *affected.Plan, p planning, cfg *config.Config, runName string, source *lists.Source, setupWarnings []string) error {
	if err := untestedRead(plan, p.walk, cfg, runName); err != nil {
		return err
	}

	setLists(plan, source)
	plan.Warnings = append(plan.Warnings, setupWarnings...)
	plan.Warnings = append(plan.Warnings, noLinksWarnings(p)...)

	// only a suite the run covers can lose its recordings in this run.
	covered := slices.DeleteFunc(cfg.SuiteNames(), func(s string) bool { return !cfg.RunCovers(runName, s) })

	lines, records := observeReadsProblems(p, cfg, runName, covered)
	if records {
		return observeReadsError(lines)
	}

	plan.Warnings = append(plan.Warnings, lines...)

	p.book.markUnlisted(plan)
	plan.Narrowing = narrowingLevel(cfg)

	return nil
}

func untestedRead(plan *affected.Plan, walk *walkTrees, cfg *config.Config, runName string) error {
	read := walk.untestedRead(cfg)
	if len(read) == 0 {
		return nil
	}

	if cfg.UncoveredFor(runName) == config.UncoveredFail {
		return fmt.Errorf("%w: %s", errUncovered, strings.Join(read, "; "))
	}

	plan.Warnings = append(plan.Warnings, read...)

	return nil
}

func followFor(cfg *config.Config, run string) func(suite string) string {
	return func(suite string) string {
		// the top level narrows nothing, and follows dependents everywhere.
		if narrowingLevel(cfg) >= config.MaxNarrowing {
			return config.FollowDependents
		}

		follow, _ := cfg.FollowFor(run, suite)

		return follow
	}
}

func bazelFor(cfg *config.Config, trigger, cache string) func(dir string) *bazel.Client {
	return func(dir string) *bazel.Client {
		return &bazel.Client{Bazelisk: bazeliskFor(cfg, trigger), Dir: dir, StartupFlags: cachedStartupFlags(cfg, cache), Env: cachedBazelEnv(cfg, cache)}
	}
}

// bazeliskFor tries PIPELINEGEN_BAZELISK (relative to the working directory), the config's
// bazel.bazelisk (relative to the root, or a name on PATH), then tools/bazelisk. Bazel runs in other
// directories, so the path is made absolute.
func bazeliskFor(cfg *config.Config, root string) string {
	if env := os.Getenv("PIPELINEGEN_BAZELISK"); env != "" {
		return absOr(env)
	}

	bin := cfg.Bazel.Bazelisk
	if strings.Contains(bin, "/") {
		if !filepath.IsAbs(bin) {
			bin = filepath.Join(root, bin)
		}

		return bin
	}

	if _, err := exec.LookPath(bin); err == nil {
		return bin
	}

	if fileExists(filepath.Join("tools", "bazelisk")) {
		return absOr(filepath.Join("tools", "bazelisk"))
	}

	return bin
}

func absOr(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}

	return p
}

func fileExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}

// Without a passed commit within the walk, the newest one whose run is still going is the base: it plans
// every slice and makes the lists, so only it runs everything. The host's statuses and the store's
// records can differ: the store forgets runs past keep.runs, which a status outlives.
func lastPassed(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, repo string) (base string, inFlight bool, err error) {
	commits, err := runs.FirstParents(ctx, repo, ch.Commit, 50)
	if err != nil {
		return "", false, err
	}

	if !cfg.Reports() {
		// one listing says which commits were planned at all.
		planned, err := store.List(ctx, report.PlansPrefix(ch.Run, ch.ID))
		if err != nil {
			return "", false, err
		}

		return baseAmong(commits, func(c string) (string, error) {
			id := report.ID{Run: ch.Run, Change: ch.ID, Commit: c}
			if _, ok := planned[id.PlanRef()]; !ok {
				return "", nil
			}

			return report.RunState(ctx, store, cfg, id)
		})
	}

	gh := host(cfg)

	return baseAmong(commits, func(c string) (string, error) {
		all, err := statusesOf(ctx, gh, c)
		if err != nil {
			return "", err
		}

		return runStateWithBaseline(cfg, ch.Run, all), nil
	})
}

// pipelinegen baseline posts in a context of its own, so no status of the run erases it.
func runStateWithBaseline(cfg *config.Config, run string, all map[string]runs.Status) string {
	own := all[report.RunContext(cfg, run)].State

	switch own {
	case statusSuccess, "failure":
	default:
		if all[baselineContext(cfg, run)].State == statusSuccess {
			return statusSuccess
		}
	}

	return own
}

// A trusted run's commit carrying a baseline is its own base and selects nothing; an ordinary passed
// status never is, so a rerun of a commit runs again. With no earlier pass, a baselined first parent
// comes before a run still in flight.
func branchBase(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, repo string) (base, note string, err error) {
	trusted := store != nil && trustedRun(cfg, ch.Run, ch.ID)

	if trusted {
		own, err := ownBaseline(ctx, cfg, ch, store)
		if err != nil {
			return "", "", err
		}

		if own {
			return ch.Commit, short12(ch.Commit) + " carries a baseline set by hand, so it is its own base", nil
		}
	}

	base, inFlight, err := lastPassed(ctx, cfg, ch, store, repo)
	if err != nil {
		return "", "", err
	}

	if (base == "" || inFlight) && trusted {
		commits, err := runs.FirstParents(ctx, repo, ch.Commit, 50)
		if err != nil {
			return "", "", err
		}

		seeded, err := lists.SeededBase(ctx, store, commits, ch.Run)
		if err != nil {
			return "", "", err
		}

		if seeded != "" {
			return seeded, fmt.Sprintf("no earlier run of %s passed; compared with %s, which carries a baseline", ch.Run, short12(seeded)), nil
		}
	}

	if inFlight {
		note = fmt.Sprintf("no earlier run of %s passed; compared with %s, whose run is still going", ch.Run, short12(base))
	}

	return base, note, nil
}

func ownBaseline(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store) (bool, error) {
	seeded, err := lists.SeededBase(ctx, store, []string{ch.Commit}, ch.Run)
	if err != nil || seeded != "" || !cfg.Reports() {
		return seeded != "", err
	}

	all, err := statusesOf(ctx, host(cfg), ch.Commit)
	if err != nil {
		return false, err
	}

	// A baseline is in its own context, or in the run's context described as a baseline.
	if all[baselineContext(cfg, ch.Run)].State == statusSuccess {
		return true, nil
	}

	own := all[report.RunContext(cfg, ch.Run)]

	return own.State == statusSuccess && isBaselineStatus(own.Description), nil
}

func baseAmong(commits []string, state func(commit string) (string, error)) (string, bool, error) {
	going := ""

	for _, c := range commits {
		s, err := state(c)
		if err != nil {
			return "", false, err
		}

		switch s {
		case statusSuccess:
			return c, false, nil
		case "pending":
			if going == "" {
				going = c
			}
		}
	}

	return going, going != "", nil
}

func describeFindings(fs []check.Finding) string {
	parts := make([]string, 0, min(len(fs), 5))
	for i, f := range fs {
		if i == 5 {
			parts = append(parts, fmt.Sprintf("and %d more", len(fs)-5))

			break
		}

		parts = append(parts, f.Path+": "+f.Problem)
	}

	return strings.Join(parts, "; ")
}

func summary(p *affected.Plan) string {
	if p.All {
		return "every slice: " + p.Reason
	}

	parts := make([]string, 0, len(p.Suites))
	for _, sp := range p.Suites {
		parts = append(parts, fmt.Sprintf("%s %d of %d%s%s", sp.Suite, len(sp.Selected), len(sp.Selected)+len(sp.Untouched), causesText(sp), narrowedText(sp)))
	}

	out := strings.Join(parts, ", ")

	// how often bazel-diff hashed the head again to tell what else the
	// change reaches, and how long each took.
	if len(p.LabelHashSeconds) > 0 {
		times := make([]string, 0, len(p.LabelHashSeconds))
		for _, s := range p.LabelHashSeconds {
			times = append(times, fmt.Sprintf("%.1f s", s))
		}

		out += fmt.Sprintf("; label groups %d, hashed again in %s", p.LabelGroups, strings.Join(times, ", "))
	}

	switch n := len(p.Unobservable); n {
	case 0:
	case 1:
		out += "; 1 file changed without an observable change"
	default:
		out += fmt.Sprintf("; %d files changed without an observable change", n)
	}

	return out
}

func narrowedText(sp affected.SuitePlan) string {
	if len(sp.Narrowed) == 0 {
		return ""
	}

	parts := make([]string, 0, len(sp.Narrowed))
	for _, n := range sp.Narrowed {
		parts = append(parts, fmt.Sprintf("%s left out %d", n.Narrowing, len(n.Dropped)))
	}

	return " [" + strings.Join(parts, ", ") + "]"
}

const summaryCauses = 2

func causesText(sp affected.SuitePlan) string {
	if len(sp.Causes) == 0 {
		return ""
	}

	shown := sp.Causes[:min(len(sp.Causes), summaryCauses)]
	parts := make([]string, 0, len(shown)+1)

	for _, c := range shown {
		parts = append(parts, fmt.Sprintf("%d: %s", c.Slices, c.Text()))
	}

	rest := 0

	for _, sl := range sp.Selected {
		rs := sp.Reasons[sl.Label]
		if len(rs) > 0 && !slices.ContainsFunc(rs, func(r affected.Reason) bool {
			return slices.ContainsFunc(shown, func(c affected.Cause) bool { return c.Of(r) })
		}) {
			rest++
		}
	}

	if rest > 0 {
		parts = append(parts, fmt.Sprintf("%d for other reasons", rest))
	}

	return " (" + strings.Join(parts, "; ") + ")"
}

func say(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// The suite's gate is [1] when the plan selected something of it and [] otherwise, so the job runs or
// skips its work without failing.
func runTree(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		b         buildFlags
		out, gate string
	)

	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	b.register(fs)
	pathVar(fs, &out, "out", "source", "where the tree under test goes", writes)
	pathVar(fs, &gate, "gate", "gate", "where the gates go", writes)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	b.root = b.trigger
	b.history = true

	cfg, ch, err := b.open(ctx)
	if closed, cerr := closeEarly(gate, b.run, err, stdout); closed {
		return cerr
	}

	if err != nil {
		return err
	}

	s, ok := cfg.Suites[b.suite]
	if !ok {
		return fmt.Errorf("%w: no suite %q", errUsage, b.suite)
	}

	store := openStore(cfg)
	defer closeStore(store, &err)

	// a run ended by hand runs nothing more.
	if ended, err := endedEarly(ctx, store, ch, b.suite, gate, stdout); ended || err != nil {
		return err
	}

	plan, ok, err := readPlan(ctx, store, reportID(ch))
	if err != nil {
		return err
	}

	sp, inPlan := affected.SuitePlan{}, false
	if ok {
		sp, inPlan = suitePlan(plan, b.suite)
	}

	run := inPlan && (len(sp.Selected) > 0 || len(sp.WholeSuite) > 0)

	if err := writeGates(gate, s, sp, run); err != nil {
		return err
	}

	if !run {
		if err := os.MkdirAll(out, 0o750); err != nil {
			return fmt.Errorf("tree: %w", err)
		}

		return say(stdout, "%s %s: nothing to run for %s\n", ch.Run, ch.ID, b.suite)
	}

	if err := recordBegan(ctx, store, reportID(ch), b.suite, stdout); err != nil {
		return err
	}

	return checkoutTested(ctx, cfg, ch, plan, b, out, stdout)
}

func checkoutTested(ctx context.Context, cfg *config.Config, ch runs.Change, plan *affected.Plan, b buildFlags, out string, stdout io.Writer) error {
	head, err := testedCommit(ctx, cfg, ch, plan, b.trigger)
	if err != nil {
		return err
	}

	if err := runs.Checkout(ctx, b.trigger, head, out); err != nil {
		return err
	}

	return say(stdout, "%s %s: %s at %s\n", ch.Run, ch.ID, b.suite, head)
}

// recordBegan's record is a convenience: a store that can't be written only costs a warning, and the
// build goes on.
func recordBegan(ctx context.Context, store *gitstore.Store, id report.ID, suite string, stdout io.Writer) error {
	if err := report.RecordBegan(ctx, store, id, suite, os.Getenv(envJobURL)); err != nil {
		warnf(os.Stderr, "pipelinegen tree: %s %s: the start record of %s isn't written, so the run's sweep may hand the head on again: %v\n", id.Run, id.Change, suite, err)

		return nil
	}

	return say(stdout, "%s %s: %s began\n", id.Run, id.Change, suite)
}

// A pull request is tested as its head merged into the target's tip the plan used; a branch's commit as itself.
func testedCommit(ctx context.Context, cfg *config.Config, ch runs.Change, plan *affected.Plan, repo string) (string, error) {
	if cfg.Runs[ch.Run].Compare != config.CompareMergedTarget {
		return ch.Commit, nil
	}

	if err := runs.FetchCommit(ctx, repo, plan.Base, readAuth(ctx, cfg, repo)); err != nil {
		return "", err
	}

	head, err := runs.Merge(ctx, repo, ch.Commit, plan.Base)
	if err != nil {
		return "", err
	}

	if head != plan.Head {
		return "", fmt.Errorf("tree: %w: %s, the plan's %s", errMergeMoved, head, plan.Head)
	}

	return head, nil
}

func closeEarly(gate, run string, err error, stdout io.Writer) (bool, error) {
	switch {
	case errors.Is(err, config.ErrUnreadable):
		return true, closeUnreadable(gate, err)
	case errors.Is(err, runs.ErrSuperseded):
		return true, closeSuperseded(gate, run, err, stdout)
	}

	return false, nil
}

func closeSuperseded(gate, run string, err error, stdout io.Writer) error {
	if err := os.MkdirAll(gate, 0o750); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	if err := writeGate(filepath.Join(gate, "gate.json"), false); err != nil {
		return err
	}

	return say(stdout, "%s: %v\n", run, err)
}

func closeUnreadable(gate string, err error) error {
	warnf(os.Stderr, "pipelinegen tree: not run: this commit's config can't be read by the pinned pipelinegen: %v\n", err)

	if err := os.MkdirAll(gate, 0o750); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	return writeGate(filepath.Join(gate, "gate.json"), false)
}

func writeGates(dir string, s config.Suite, sp affected.SuitePlan, run bool) error {
	if err := writeGate(filepath.Join(dir, "gate.json"), run); err != nil {
		return err
	}

	for _, w := range s.WholeSuite {
		if err := writeGate(filepath.Join(dir, "whole-"+w.Name+".json"), run && slices.Contains(sp.WholeSuite, w.Name)); err != nil {
			return err
		}
	}

	return nil
}

// Values are strings: Concourse puts a var into string fields as its JSON value, and refuses a number there.
func writeGate(path string, open bool) error {
	body := "[]\n"
	if open {
		body = "[\"1\"]\n"
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("gate: %w", err)
	}

	return nil
}

type batchSliceOut struct {
	Name    string   `json:"name"`
	Label   string   `json:"label,omitempty"`
	Target  string   `json:"target,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Tests   []string `json:"tests,omitempty"`
}

// A rerun leaves out the slices that already passed for this change.
func runBatches(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		b   buildFlags
		out string
	)

	fs := flag.NewFlagSet("batches", flag.ContinueOnError)
	b.register(fs)
	pathVar(fs, &out, "out", "batches", "where the batches go", writes)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	b.history = true

	cfg, ch, err := b.open(ctx)
	if err != nil {
		return err
	}

	s, ok := cfg.Suites[b.suite]
	if !ok {
		return fmt.Errorf("%w: no suite %q", errUsage, b.suite)
	}

	store := openStore(cfg)
	defer closeStore(store, &err)

	id := reportID(ch)

	plan, ok, err := readPlan(ctx, store, id)
	if err != nil {
		return err
	}

	if !ok {
		return fmt.Errorf("batches: %s %s: %w", ch.Run, ch.ID, errNoPlan)
	}

	sp, _ := suitePlan(plan, b.suite)

	states, err := report.States(ctx, store, id, b.suite)
	if err != nil {
		return err
	}

	todo := make([]bazel.Slice, 0, len(sp.Selected))
	for _, sl := range sp.Selected {
		if states[sl.Name] != report.Passed {
			todo = append(todo, sl)
		}
	}

	dealt := deal(b.root, s, todo)

	if err := writeBatches(out, ch, todo, dealt); err != nil {
		return err
	}

	return say(stdout, "%s %s: %d slices of %s in %d batches\n", ch.Run, ch.ID, len(todo), b.suite, len(dealt))
}

func deal(root string, s config.Suite, todo []bazel.Slice) [][]string {
	var file *durations.File

	if s.Durations != "" {
		if f, err := durations.Read(filepath.Join(root, s.Durations)); err == nil {
			file = f
		}
	}

	in := make([]batch.Slice, 0, len(todo))

	for _, sl := range todo {
		bs := batch.Slice{Name: sl.Name}

		for _, src := range sl.Sources {
			if file == nil {
				break
			}

			if v, ok := file.Seconds(src, nil); ok {
				bs.Secs += v
				bs.Recorded = true
			}
		}

		in = append(in, bs)
	}

	n := s.BatchCount()
	if s.BatchMinutes > 0 {
		n = batch.Count(in, s.BatchMinutes, s.ParallelOrDefault(), s.Batches)
	}

	return batch.Deal(in, n)
}

func writeBatches(out string, ch runs.Change, todo []bazel.Slice, dealt [][]string) error {
	byName := map[string]bazel.Slice{}
	for _, sl := range todo {
		byName[sl.Name] = sl
	}

	// the batch numbers as strings: the across step puts each into a
	// task's args and params, which Concourse types as strings.
	index := make([]string, 0, len(dealt))

	for i, names := range dealt {
		list := make([]batchSliceOut, 0, len(names))

		for _, n := range names {
			sl := byName[n]
			item := batchSliceOut{Name: sl.Name, Kind: sl.Kind, Sources: sl.Sources, Tests: sl.Tests}

			if !strings.HasPrefix(sl.Label, "@pipelinegen_deps//") {
				item.Label, item.Target = sl.Label, runTarget(sl)
			}

			list = append(list, item)
		}

		data, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return fmt.Errorf("batches: %w", err)
		}

		dir := filepath.Join(out, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("batches: %w", err)
		}

		if err := os.WriteFile(filepath.Join(dir, "slices.json"), append(data, '\n'), 0o600); err != nil {
			return fmt.Errorf("batches: %w", err)
		}

		index = append(index, strconv.Itoa(i))
	}

	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("batches: %w", err)
	}

	if err := os.MkdirAll(out, 0o750); err != nil {
		return fmt.Errorf("batches: %w", err)
	}

	if err := os.WriteFile(filepath.Join(out, "batches.json"), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("batches: %w", err)
	}

	// the suite's work begins now: its end judges a time limit by it.
	if err := writeStarted(out, time.Now()); err != nil {
		return err
	}

	return runs.WriteRunFile(out, ch)
}

// runTarget is a tagged slice's runnable target: its pipelinegen-run: tag,
// else itself.
func runTarget(sl bazel.Slice) string {
	for _, t := range sl.Tags {
		if target, ok := strings.CutPrefix(t, "pipelinegen-run:"); ok {
			return target
		}
	}

	return sl.Label
}

// sliceList prints one slice per line, tab-separated, "-" for an empty field.
func sliceList(batches, batchN string, stdout io.Writer) error {
	data, err := os.ReadFile(filepath.Join(batches, batchN, "slices.json")) //nolint:gosec // the batch's own file
	if err != nil {
		return fmt.Errorf("slice list: %w", err)
	}

	var list []batchSliceOut
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("slice list: %w", err)
	}

	var b strings.Builder

	for _, sl := range list {
		fields := []string{sl.Name, sl.Label, sl.Target, strings.Join(sl.Sources, ","), strings.Join(sl.Tests, ",")}
		for i, f := range fields {
			if f == "" {
				fields[i] = "-"
			}
		}

		b.WriteString(strings.Join(fields, "\t") + "\n")
	}

	return say(stdout, "%s", b.String())
}

func runSliceList(ctx context.Context, args []string, stdout io.Writer) error {
	var batches string

	fs := flag.NewFlagSet("slice list", flag.ContinueOnError)
	pathVar(fs, &batches, "batches", "batches", "the directory of the run's batches", reads)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	n := os.Getenv(envBatch)
	if n == "" {
		return fmt.Errorf("%w: %s must be set", errUsage, envBatch)
	}

	return sliceList(batches, n, stdout)
}
