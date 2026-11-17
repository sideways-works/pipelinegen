package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

var errCantRun = errors.New("can't run on this machine")

type localRunFlags struct {
	common

	lp     localPlan
	images imageFlags

	base, head, work            string
	suites, slices              []string
	all, record, noLists, local bool
}

func (f *localRunFlags) register(fs *flag.FlagSet) {
	f.common.register(fs)
	textVar(fs, &f.base, "base", "", "the commit to compare with (required unless --all)")
	textVar(fs, &f.head, "head", "HEAD", "the commit to plan and run: uncommitted changes are neither planned nor run")
	fs.Func("suite", "only this suite (repeatable; default every suite)", func(s string) error {
		f.suites = append(f.suites, s)

		return nil
	})
	fs.Func("slice", "only this slice, by its name or label (repeatable); one the plan didn't select runs too", func(s string) error {
		f.slices = append(f.slices, s)

		return nil
	})
	f.lp.register(fs)
	fs.Lookup("plugins").Usage = "the directory holding the configured plugins, the drivers among them (default: tools/plugins under the root)"
	fs.BoolVar(&f.all, "all", false, "run every slice, not only those the change selects")
	fs.BoolVar(&f.record, "record", false, "record the suites bound by recording and attach their lists to the head in the local store, a suite's only when every slice of it passed")
	fs.BoolVar(&f.noLists, "no-lists", false, "bind recorded slices to everything instead of reading the lists")
	fs.BoolVar(&f.local, "local", false, "read the lists from the local store `pipelinegen record` writes, not the remote")
	pathVar(fs, &f.work, "work", "", "where the builds' directories go, one per suite (default a temporary directory, removed after)", writes)
	f.images.register(fs)
}

func runLocal(ctx context.Context, args []string, stdout io.Writer) error {
	var f localRunFlags

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	f.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	// Without a base affected plans every slice; here that has to be asked for.
	if f.base == "" && !f.all {
		return fmt.Errorf("%w: --base is needed, or --all to run every slice", errUsage)
	}

	ctx = withFill(ctx, "run", *f.lp.noFill)

	f.root = absRoot(f.root)
	f.lp.plugins = pluginsIn(f.root, f.lp.plugins)

	if f.work != "" {
		f.work = absOr(f.work)
	}

	cfg, plan, dirty, err := f.plan(ctx)
	if err != nil {
		return err
	}

	if err := writeAffected(stdout, plan, false); err != nil {
		return err
	}

	if err := f.only(plan); err != nil {
		return err
	}

	todo, err := f.runnable(cfg, plan)
	if err != nil {
		return err
	}

	if len(todo) == 0 {
		return say(stdout, "nothing selected: no slice to run\n%s", dirtyLine(dirty))
	}

	return f.run(ctx, cfg, plan.Head, todo, dirty, stdout)
}

func (f *localRunFlags) plan(ctx context.Context) (*config.Config, *affected.Plan, bool, error) {
	from, err := fromFlags(f.noLists, f.local)
	if err != nil {
		return nil, nil, false, err
	}

	base, head, err := resolveRevs(ctx, f.root, f.base, f.head)
	if err != nil {
		return nil, nil, false, err
	}

	cfg, err := affectedConfig(ctx, &f.common, f.root, head, false)
	if err != nil {
		return nil, nil, false, err
	}

	for _, s := range f.suites {
		if _, err := pickSuites(cfg, s); err != nil {
			return nil, nil, false, err
		}
	}

	dirty, err := f.dirty(ctx, head)
	if err != nil {
		return nil, nil, false, err
	}

	plan, err := affectedPlan(ctx, cfg, f.root, base, head, f.all, repoCache(ctx, f.lp.cache, f.root), f.lp.bazelDiff, from, f.lp.plugins)
	if err != nil {
		return nil, nil, false, err
	}

	if len(f.suites) > 0 {
		plan.Suites = slices.DeleteFunc(plan.Suites, func(sp affected.SuitePlan) bool { return !slices.Contains(f.suites, sp.Suite) })
	}

	planWarnings(os.Stderr, plan)

	return cfg, plan, dirty, nil
}

func (f *localRunFlags) run(ctx context.Context, cfg *config.Config, head string, todo []affected.SuitePlan, dirty bool, stdout io.Writer) error {
	data, src, err := runConfigData(ctx, &f.common, f.root, head)
	if err != nil {
		return err
	}

	work := f.work
	if work == "" {
		if work, err = os.MkdirTemp("", "pipelinegen-run-"); err != nil {
			return fmt.Errorf("run: %w", err)
		}

		defer os.RemoveAll(work) //nolint:errcheck // a temporary directory
	}

	r := &localSuiteRun{data: data, src: src, root: f.root, work: work, plugins: f.lp.plugins, head: head, out: stdout, images: f.images}

	var tallies []suiteTally

	for _, sp := range todo {
		t, err := r.suite(ctx, sp, f.record && recordable(cfg, sp.Suite) == nil)
		if err != nil {
			return fmt.Errorf("suite %s: %w", sp.Suite, err)
		}

		tallies = append(tallies, t)
	}

	return localSummary(stdout, head, tallies, dirty)
}

// dirty refuses changes under --record, as record does, and otherwise names them: the run is of a commit,
// so what isn't committed is neither planned nor run.
func (f *localRunFlags) dirty(ctx context.Context, head string) (bool, error) {
	root, tools := f.root, []string{f.lp.plugins, f.work}

	if at, err := resolveRev(ctx, root, "HEAD"); err != nil || at != head {
		return false, err
	}

	if f.record {
		return false, cleanTree(ctx, root, tools...)
	}

	files, err := changedFiles(ctx, root, tools...)
	if err != nil || len(files) == 0 {
		return false, err
	}

	warnf(os.Stderr, "pipelinegen run: uncommitted changes are neither planned nor run; this tests %s as committed, without:\n  %s\n", short12(head), fileList(files))

	return true, nil
}

// only keeps the slices --slice names, by name or label, in every suite that has them. One the plan
// didn't select runs too, as asked, and a line says so.
func (f *localRunFlags) only(plan *affected.Plan) error {
	if len(f.slices) == 0 {
		return nil
	}

	named := func(sl bazel.Slice) bool {
		return slices.Contains(f.slices, sl.Name) || slices.Contains(f.slices, sl.Label)
	}
	found := map[string]bool{}

	var kept []affected.SuitePlan

	for _, sp := range plan.Suites {
		var keep []bazel.Slice

		for _, sl := range slices.Concat(sp.Selected, sp.Slices()) {
			if !named(sl) || slices.ContainsFunc(keep, func(k bazel.Slice) bool { return k.Label == sl.Label }) {
				continue
			}

			found[sl.Name], found[sl.Label] = true, true

			if !slices.ContainsFunc(sp.Selected, func(s bazel.Slice) bool { return s.Label == sl.Label }) {
				warnf(os.Stderr, "pipelinegen run: suite %s's slice %s wasn't selected by the plan; it runs as --slice names it\n", sp.Suite, sl.Name)
			}

			keep = append(keep, sl)
		}

		if len(keep) > 0 {
			sp.Selected, sp.WholeSuite = keep, nil
			kept = append(kept, sp)
		}
	}

	if missing := slices.DeleteFunc(slices.Clone(f.slices), func(n string) bool { return found[n] }); len(missing) > 0 {
		where := "no suite has"
		if len(f.suites) > 0 {
			where = "no suite of --suite " + strings.Join(f.suites, ", ") + " has"
		}

		what := "a slice"
		if len(missing) > 1 {
			what = "the slices"
		}

		return fmt.Errorf("%w: %s %s %s at %s", errUsage, where, what, strings.Join(missing, ", "), short12(plan.Head))
	}

	plan.Suites = kept

	return nil
}

func (f *localRunFlags) runnable(cfg *config.Config, plan *affected.Plan) ([]affected.SuitePlan, error) {
	var todo []affected.SuitePlan

	for _, sp := range plan.Suites {
		if len(sp.WholeSuite) > 0 {
			warnf(os.Stderr, "pipelinegen run: suite %s's whole-suite checks don't run on this machine: %s\n", sp.Suite, strings.Join(sp.WholeSuite, ", "))
		}

		if len(sp.Selected) == 0 {
			continue
		}

		s := cfg.Suites[sp.Suite]
		if s.RunSlice == "" && !s.HasSteps() {
			return nil, fmt.Errorf("%w: suite %s has no run_slice task and no steps", errCantRun, sp.Suite)
		}

		if f.record {
			if err := recordable(cfg, sp.Suite); err != nil {
				warnf(os.Stderr, "pipelinegen run: suite %s runs without recording: %v\n", sp.Suite, err)
			}
		}

		todo = append(todo, sp)
	}

	return todo, nil
}

// runConfigData is the config the plan was made under, which the builds run under too.
func runConfigData(ctx context.Context, c *common, root, head string) ([]byte, string, error) {
	p := c.config
	if p == "" {
		env, ok, err := configFromEnv()
		if err != nil {
			return nil, "", err
		}

		if ok {
			p = env
		}
	}

	if p != "" {
		data, err := os.ReadFile(p) //nolint:gosec // the config the caller names
		if err != nil {
			return nil, "", fmt.Errorf("run: %w", err)
		}

		return data, p, nil
	}

	data, ok, err := gitx.Repo{Dir: root}.Show(ctx, head, "./"+config.FileName)
	if err != nil {
		return nil, "", err
	}

	if !ok {
		return nil, "", fmt.Errorf("%w: %s has no %s", config.ErrUnreadable, short12(head), config.FileName)
	}

	return data, short12(head) + ":" + config.FileName, nil
}

type localSuiteRun struct {
	data                     []byte
	src, root, work, plugins string
	head                     string
	out                      io.Writer
	images                   imageFlags
}

type suiteTally struct {
	suite               string
	slices              []string
	states              map[string]report.State
	recording, attached bool
}

// suite runs one batch of the suite's selected slices in a build of its own. Without recording its
// records go to a store in the build's directory, so the local store holds recordings only.
func (r *localSuiteRun) suite(ctx context.Context, sp affected.SuitePlan, record bool) (suiteTally, error) {
	dir := filepath.Join(r.work, sp.Suite)

	storePath := filepath.Join(dir, "store.git")

	if record {
		p, err := localStorePath(ctx, r.root)
		if err != nil {
			return suiteTally{}, err
		}

		storePath = p
	}

	remote, _, err := localStoreURL(ctx, storePath, true)
	if err != nil {
		return suiteTally{}, err
	}

	change := localChange(time.Now())
	ch := runs.Change{Run: localRun, ID: change, Commit: r.head, Branch: change}
	cfgPath := filepath.Join(dir, config.FileName)

	derived, err := localConfigOf(r.data, r.src, sp.Suite, remote, change, cfgPath, record)
	if err != nil {
		return suiteTally{}, err
	}

	b := &localBuild{
		cfg: derived, cfgPath: cfgPath, root: r.root, dir: filepath.Join(dir, "build"),
		suite: sp.Suite, ch: ch, plugins: r.plugins, out: r.out, box: &imageBox{flags: r.images},
	}

	if err := b.findPlugins(ctx); err != nil {
		return suiteTally{}, err
	}

	remove, err := b.checkout(ctx)
	if err != nil {
		return suiteTally{}, err
	}

	defer remove()

	if err := b.start(ctx, sp.Selected); err != nil {
		return suiteTally{}, err
	}

	// A batch with a slice that did not pass fails its task, so its end runs either way and the states say
	// what happened; the end stops before the lists are attached unless every slice passed.
	ranErr := b.run(ctx)
	endErr := b.finish(ctx)

	t := suiteTally{suite: sp.Suite, slices: names(sp.Selected), recording: record}

	if t.states, err = localStates(ctx, derived, ch, sp.Suite); err != nil {
		return t, errors.Join(err, ranErr, endErr)
	}

	t.attached = record && endErr == nil && t.passed()

	if ranErr != nil && !t.onlyFailed() {
		warnf(os.Stderr, "pipelinegen run: suite %s: %v\n", sp.Suite, ranErr)
	}

	if endErr != nil && t.passed() {
		return t, endErr
	}

	return t, nil
}

func localStates(ctx context.Context, cfg *config.Config, ch runs.Change, suite string) (map[string]report.State, error) {
	store := openStore(cfg)

	states, err := report.States(ctx, store, reportID(ch), suite)
	if cerr := store.Close(); err == nil {
		err = cerr
	}

	return states, err
}

func (t suiteTally) count(st report.State) int {
	n := 0

	for _, name := range t.slices {
		if t.states[name] == st {
			n++
		}
	}

	return n
}

func (t suiteTally) passed() bool { return t.count(report.Passed) == len(t.slices) }

// onlyFailed says every slice that did not pass failed its tests, which is why a batch task fails.
func (t suiteTally) onlyFailed() bool {
	return t.count(report.Passed)+t.count(report.Failed) == len(t.slices) && t.count(report.Failed) > 0
}

func localSummary(w io.Writer, head string, tallies []suiteTally, dirty bool) error {
	var (
		b   bytes.Buffer
		bad bool
	)

	fmt.Fprintf(&b, "ran at %s:\n", short12(head))

	for _, t := range tallies {
		passed, failed, errored := t.count(report.Passed), t.count(report.Failed), t.count(report.Errored)
		fmt.Fprintf(&b, "  %s: %d passed, %d failed, %d errored", t.suite, passed, failed, errored)

		if other := len(t.slices) - passed - failed - errored; other > 0 {
			fmt.Fprintf(&b, ", %d not run", other)
		}

		switch {
		case t.attached:
			b.WriteString("; its lists are attached to it in the local store")
		case t.recording:
			b.WriteString("; no lists attached, as not every slice passed")
		}

		b.WriteString("\n")

		for _, name := range t.slices {
			if st := t.states[name]; st != report.Passed {
				bad = true

				fmt.Fprintf(&b, "    %s %s\n", stateOrNotRun(st), name)
			}
		}
	}

	b.WriteString(dirtyLine(dirty))

	if err := emit(w, &b); err != nil {
		return err
	}

	if bad {
		return exitCodeError(1)
	}

	return nil
}

func stateOrNotRun(st report.State) report.State {
	if st == "" || st == report.Pending || st == report.Running {
		return report.NotRun
	}

	return st
}

func dirtyLine(dirty bool) string {
	if !dirty {
		return ""
	}

	return "the work tree's uncommitted changes were neither planned nor run\n"
}
