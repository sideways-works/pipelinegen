package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/driver"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/plugin"
)

var errSteps = errors.New("steps")

type drivenBatch struct {
	cfg                   *config.Config
	suite                 string
	tree, tools, plugins  string
	work, cache, prepared string
	// coverage is empty unless the run records the suite.
	result, coverage string
	record           bool
	// deadline is zero when there is no limit.
	deadline time.Time
	params   map[string]string
	secrets  []string
	// commit is empty on this machine, where the tree is no commit of a run.
	commit string
	log    io.Writer
}

func (b *drivenBatch) env() driver.Env {
	return driver.Env{Tools: b.tools, Secrets: b.secrets, Params: b.params, Cache: filepath.Join(b.cache, "tests"), Commit: b.commit}
}

func (b *drivenBatch) route(list []batchSlice) (map[int][]batchSlice, error) {
	s := b.cfg.Suites[b.suite]
	out := map[int][]batchSlice{}

	var bad []string

	for _, sl := range list {
		i, ok := s.RunStepFor(config.RoutedSlice{Name: sl.Name, Label: sl.Label, Kind: sl.Kind})
		if !ok {
			bad = append(bad, sl.Name)

			continue
		}

		out[i] = append(out[i], sl)
	}

	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: suite %s: %s taken by no run step or by more than one: %s",
			errSteps, b.suite, plural(len(bad), "slice"), strings.Join(firstFew(bad, 10), ", "))
	}

	return out, nil
}

func firstFew(names []string, n int) []string {
	if len(names) <= n {
		return names
	}

	return append(slices.Clone(names[:n]), fmt.Sprintf("and %d more", len(names)-n))
}

func (b *drivenBatch) driverFor(name string, env []string) (driver.Driver, error) { //nolint:ireturn // the built-in driver or a plugin's
	if name == config.BuiltinCommand {
		return &driver.Command{Env: env, Log: b.log}, nil
	}

	p, ok := b.cfg.Driver(name)
	if !ok {
		return nil, fmt.Errorf("%w: no driver %s", errSteps, name)
	}

	exe := filepath.Join(b.plugins, p.Name)

	// The bootstrap built a driver pinned by commit at that commit: there is no checksum to check it by.
	if p.Release != nil {
		var err error
		if exe, err = pinnedBinary(p, b.plugins); err != nil {
			return nil, fmt.Errorf("%w: driver %s in %s: %w; it is not run", errSteps, p.Name, b.plugins, err)
		}
	} else if _, err := os.Stat(exe); err != nil {
		return nil, fmt.Errorf("%w: driver %s: %w", errSteps, p.Name, err)
	}

	return &driver.Exec{Label: name, Path: exe, Env: env, Dir: b.tree, Log: b.log}, nil
}

func (b *drivenBatch) describe(ctx context.Context, st config.Step, d driver.Driver, env []string) (plugin.Description, error) {
	desc, err := d.Describe(ctx)
	if err != nil {
		return desc, err
	}

	if desc.Kind != plugin.KindDriver {
		return desc, fmt.Errorf("%w: %s says it is a %s, not a driver", errSteps, d.Name(), desc.Kind)
	}

	if !desc.HasVerb(st.Kind()) {
		return desc, fmt.Errorf("%w: suite %s: %s is no %s driver (its verbs: %s)", errSteps, b.suite, d.Name(), st.Kind(), strings.Join(desc.Verbs, ", "))
	}

	if missing := plugins.MissingNeeds(desc.Needs, env); len(missing) > 0 {
		return desc, fmt.Errorf("%w: suite %s: driver %s needs %s, which this machine's PATH lacks; the suite's image must hold it",
			errSteps, b.suite, d.Name(), strings.Join(missing, ", "))
	}

	return desc, nil
}

func (b *drivenBatch) request(st config.Step, exports map[string]string) (plugin.DriverRequest, error) {
	name := st.Driver()
	work, cache := filepath.Join(b.work, name), filepath.Join(b.cache, "drivers", name)

	for _, d := range []string{work, cache} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return plugin.DriverRequest{}, fmt.Errorf("%w: %w", errSteps, err)
		}
	}

	req := plugin.DriverRequest{
		Protocol: plugin.Protocol, Work: work, Cache: cache, Config: b.cfg.DriverConfig(st),
		Tree:      b.tree,
		Workspace: b.cfg.Bazel.Workspace,
		Tools: map[string]string{
			"pipelinegen": filepath.Join(b.tools, "pipelinegen"),
			"bazelisk":    filepath.Join(b.tools, "bazelisk"),
		},
		Suite:    b.suite,
		Record:   b.record,
		Stack:    b.cfg.Suites[b.suite].Stack,
		Env:      b.env().Vars(exports),
		Prepared: b.prepared,
	}

	if !b.deadline.IsZero() {
		req.Deadline = b.deadline.UTC().Format(time.RFC3339)
	}

	return req, nil
}

func (b *drivenBatch) prepare(ctx context.Context) error {
	s := b.cfg.Suites[b.suite]

	// What the prepare steps make starts empty, also in a --work directory an earlier run left behind.
	if len(s.StepsOf(config.StepPrepare)) > 0 {
		if err := emptyDir(b.prepared); err != nil {
			return fmt.Errorf("%w: %w", errSteps, err)
		}
	}

	for _, i := range s.StepsOf(config.StepPrepare) {
		rs, err := b.ready(ctx, s.Steps[i], nil)
		if err != nil {
			return err
		}

		if err := rs.d.Prepare(ctx, rs.req); err != nil {
			return err
		}
	}

	return nil
}

type readyStep struct {
	d    driver.Driver
	desc plugin.Description
	req  plugin.DriverRequest
}

func (b *drivenBatch) ready(ctx context.Context, st config.Step, exports map[string]string) (readyStep, error) {
	env := b.env().For(exports)

	d, err := b.driverFor(st.Driver(), env)
	if err != nil {
		return readyStep{}, err
	}

	desc, err := b.describe(ctx, st, d, env)
	if err != nil {
		return readyStep{}, err
	}

	req, err := b.request(st, exports)
	if err != nil {
		return readyStep{}, err
	}

	return readyStep{d: d, desc: desc, req: req}, nil
}

// setUp leaves the set-ups it started in running, also on a failure.
func (b *drivenBatch) setUp(ctx context.Context, running *[]driver.Running) (map[string]string, error) {
	s := b.cfg.Suites[b.suite]
	exports := map[string]string{}

	for _, i := range s.StepsOf(config.StepSetup) {
		rs, err := b.ready(ctx, s.Steps[i], exports)
		if err != nil {
			return nil, err
		}

		r, err := rs.d.Setup(ctx, rs.req)
		if err != nil {
			return nil, err
		}

		*running = append(*running, r)

		maps.Copy(exports, r.Env())
	}

	return exports, nil
}

func (b *drivenBatch) run(ctx context.Context, list []batchSlice, hooks driver.Hooks, setupEnd func() error) error {
	s := b.cfg.Suites[b.suite]
	if !s.HasSteps() {
		return fmt.Errorf("%w: suite %s has no steps; its batch task runs its run_slice file", errUsage, b.suite)
	}

	routed, err := b.route(list)
	if err != nil {
		return err
	}

	var running []driver.Running

	defer func() {
		for _, r := range slices.Backward(running) {
			if serr := r.Stop(ctx); serr != nil {
				warnf(b.log, "pipelinegen slice run: %v\n", serr)
			}
		}
	}()

	exports, err := b.setUp(ctx, &running)
	if err != nil {
		return err
	}

	if err := setupEnd(); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: %w", errSteps, err)
	}

	marks := plugin.Marks{Command: []string{exe, sliceCommand, "mark"}, Dir: filepath.Join(b.work, "marks")}
	// In a recording run the correlators' collect calls come at each slice's real start and end.
	collects := len(b.cfg.CorrelatorsFor(b.suite)) > 0
	runner := &driver.Runner{Hooks: hooks, Record: b.record, Stack: s.Stack, Collects: collects, Marks: marks, Log: b.log}

	for _, i := range s.StepsOf(config.StepRun) {
		if len(routed[i]) == 0 {
			continue
		}

		rs, err := b.ready(ctx, s.Steps[i], exports)
		if err != nil {
			return err
		}

		if err := runner.Run(ctx, driver.Step{Driver: rs.d, Desc: rs.desc, Batch: s.Steps[i].Bundles(), Base: rs.req}, b.driverSlices(routed[i])); err != nil {
			return err
		}
	}

	return nil
}

func (b *drivenBatch) driverSlices(list []batchSlice) []plugin.DriverSlice {
	out := make([]plugin.DriverSlice, 0, len(list))

	for _, sl := range list {
		ds := plugin.DriverSlice{
			Name: sl.Name, Label: sl.Label, Target: sl.Target, Kind: sl.Kind, Sources: sl.Sources, Tests: sl.Tests,
			Extras: filepath.Join(b.result, filepath.FromSlash(sl.Name)),
			Files:  report.FilesDir(b.result, sl.Name),
		}

		if b.record {
			ds.Recording = report.PartDir(b.coverage, sl.Name, 1)
		}

		out = append(out, ds)
	}

	return out
}

func writeResult(path string, ev *plugin.DriverEvent, why string) error {
	r := report.ResultFile{Failures: []report.Failure{}}

	if ev == nil {
		r.Error = why
	} else {
		r.Passed, r.Tests = ev.Passed, ev.Tests

		for _, f := range ev.Failures {
			r.Failures = append(r.Failures, report.Failure{Test: f.Test, Tail: f.Tail})
		}

		// the driver names them in the slice's files directory; the result
		// names them from the result directory, where the check of each
		// path refuses any that leaves it
		for _, f := range ev.Files {
			r.Files = append(r.Files, report.FailureFile{Path: report.FilesPrefix(ev.Slice) + f.Path, Label: f.Label})
		}
	}

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("result: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("result: %w", err)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("result: %w", err)
	}

	return nil
}

type journalHooks struct {
	r                        *report.Reporter
	j                        *report.Journal
	suite, root, result, rec string
	every                    time.Duration
	byName                   map[string]batchSlice
	out                      io.Writer
	notPassed                []string
}

func (h *journalHooks) event(sl plugin.DriverSlice) report.SliceEvent {
	return sliceEvent(h.suite, h.byName[sl.Name], h.root, h.result, h.rec)
}

func (h *journalHooks) Start(ctx context.Context, sl plugin.DriverSlice) error {
	return h.r.Started(ctx, h.j, h.event(sl), h.rec != "", h.every)
}

func (h *journalHooks) Stop(ctx context.Context, sl plugin.DriverSlice, unclosed string) error {
	e := h.event(sl)
	e.Unrecordable = unclosed

	return h.r.PartStopped(ctx, h.j, e)
}

func (h *journalHooks) End(ctx context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, apart bool) error {
	e := h.event(sl)

	if err := writeResult(e.Result, ev, why); err != nil {
		return err
	}

	if ev != nil {
		e.Seconds = ev.Seconds
		// An end with a reason still can't hold all of its recording (a replayed result in a marked bundle).
		e.Unrecordable = why
	}

	end := h.r.Ended
	if apart {
		end = h.r.EndedApart
	}

	got, err := end(ctx, h.j, e, h.every)
	if err != nil {
		return err
	}

	if got != report.Passed {
		h.notPassed = append(h.notPassed, sl.Name+" "+string(got))
	}

	return say(h.out, "slice %s: %s\n", sl.Name, got)
}

func sliceRun(ctx context.Context, args []string, stdout io.Writer) error {
	if ctx.Value(describeKey{}) != nil {
		return errors.Join(batchSliceRun(ctx, args, stdout), localSliceRun(ctx, args, stdout))
	}

	if os.Getenv(envBatch) == "" {
		return localSliceRun(ctx, args, stdout)
	}

	return batchSliceRun(ctx, args, stdout)
}

type drivenFlags struct {
	batches, result, coverage, prepared, work, cache string
}

func (f *drivenFlags) register(fs *flag.FlagSet) {
	pathVar(fs, &f.batches, "batches", "batches", "the directory of the run's batches", reads)
	// one process writes the batch's results and journal, and reads only
	// what it wrote.
	pathVar(fs, &f.result, "result", "result", "the batch's result directory", writes)
	pathVar(fs, &f.coverage, "coverage", "coverage", "the batch's coverage directory (recording runs)", writes)
	pathVar(fs, &f.prepared, "prepared", "", "what the suite's prepare steps made (default none: the suite has no prepare step)", reads)
	pathVar(fs, &f.work, "work", "work", "the batch's scratch directory for its drivers", writes)
	pathVar(fs, &f.cache, "cache", "cache", "the directory kept between builds, for the drivers and {cache}", reads|writes)
}

func batchSliceRun(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		s step
		f drivenFlags
	)

	s.setupEnd = true

	if err := s.open(ctx, sliceCommand, args, f.register); err != nil {
		return err
	}

	defer s.close(&err)

	if err := needEnv(envSuite, envBatch); err != nil {
		return err
	}

	suite, batch := os.Getenv(envSuite), os.Getenv(envBatch)
	cfg := s.reporter.Config

	list, err := readBatch(f.batches, batch)
	if err != nil {
		return err
	}

	// The batch's outputs start empty, as a CI system's do; otherwise an earlier batch's results, journal
	// and coverage would count as this one's.
	for _, d := range []string{f.result, f.coverage, f.work} {
		if err := emptyDir(d); err != nil {
			return fmt.Errorf("%w: %s holds what an earlier batch left and can't be emptied: %w", errSteps, d, err)
		}
	}

	if err := checkPlanOnce(ctx, s.reporter, f.result); err != nil {
		return err
	}

	rec := ""
	if cfg.Records(s.reporter.ID.Run, suite) {
		rec = f.coverage

		if err := errors.Join(needCollector(cfg, s.plugins, suite), needObservable(cfg, s.plugins, suite), needInterpreters(s.plugins, suite, "slice run")); err != nil {
			return err
		}
	} else {
		s.reporter.Collectors = nil
	}

	// the batch's ensure writes the journal out; nothing here waits on
	// the store.
	s.reporter.NoFlush = true

	b := &drivenBatch{
		cfg: cfg, suite: suite, tree: absOr(s.root), tools: absOr(s.tools), plugins: filepath.Join(absOr(s.tools), "plugins"),
		work: f.work, cache: f.cache, prepared: f.prepared, result: f.result, coverage: rec, record: rec != "",
		params: cfg.Params(s.reporter.ID.Run, suite), secrets: secretNames(cfg, s.reporter.ID.Run, suite), log: os.Stderr,
		deadline: taskDeadline(cfg, s.reporter.ID.Run, suite), commit: s.reporter.ID.Commit,
	}

	// a collect call also stops before the batch's own time limit.
	if b.record {
		s.reporter.Collectors = s.plugins.collectors(cfg, suite, b.deadline)
	}

	j := report.JournalIn(f.result)
	h := &journalHooks{
		r: s.reporter, j: j, suite: suite, root: s.root, result: f.result, rec: rec,
		every: flushEvery(cfg, s.reporter.ID.Run), byName: byName(list), out: stdout,
	}

	setupEnd := func() error {
		if err := s.reporter.SetupEnd(ctx, j, filepath.Join(f.coverage, "_setup")); err != nil {
			return err
		}

		return say(stdout, "setup ended\n")
	}

	if err := b.run(ctx, list, h, setupEnd); err != nil {
		return err
	}

	// Observe here, where the slices ran and within the task's limit, and keep it for the batch's end.
	if rec != "" && s.reporter.Observer != nil {
		s.plugins.observeAhead(ctx, j, suite, b.deadline.Add(-plugin.LimitMarginSeconds*time.Second), os.Stderr)
	}

	if len(h.notPassed) > 0 {
		return fmt.Errorf("batch %s: %s: %w", batch, strings.Join(firstFew(h.notPassed, 10), ", "), errNotPassed)
	}

	return nil
}

func taskDeadline(cfg *config.Config, run, suite string) time.Time {
	return time.Now().Add(time.Duration(cfg.SuiteMinutes(run, suite)) * time.Minute)
}

func byName(list []batchSlice) map[string]batchSlice {
	out := make(map[string]batchSlice, len(list))
	for _, sl := range list {
		out[sl.Name] = sl
	}

	return out
}

func secretNames(cfg *config.Config, run, suite string) []string {
	return slices.Sorted(maps.Keys(cfg.SecretsFor(run, suite).Batch))
}

type printHooks struct {
	result    string
	out       io.Writer
	notPassed []string
}

func (h *printHooks) Start(context.Context, plugin.DriverSlice) error        { return nil }
func (h *printHooks) Stop(context.Context, plugin.DriverSlice, string) error { return nil }

func (h *printHooks) End(_ context.Context, sl plugin.DriverSlice, ev *plugin.DriverEvent, why string, _ bool) error {
	path := filepath.Join(h.result, filepath.FromSlash(sl.Name)+".json")
	if err := writeResult(path, ev, why); err != nil {
		return err
	}

	got, failures := report.ReadResult(path)
	if got != report.Passed {
		h.notPassed = append(h.notPassed, sl.Name)
	}

	if err := say(h.out, "slice %s: %s\n", sl.Name, got); err != nil {
		return err
	}

	for _, f := range failures {
		if err := say(h.out, "  %s\n%s\n", f.Test, indent(f.Tail)); err != nil {
			return err
		}
	}

	return nil
}

func indent(s string) string {
	if s == "" {
		return ""
	}

	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

type localFlags struct {
	common

	suite, plugins, tools, result, work, cache string
	names                                      []string
}

func (f *localFlags) register(fs *flag.FlagSet) {
	f.common.register(fs)
	textVar(fs, &f.suite, "suite", "", "the suite to run (on this machine; in a batch task, PIPELINEGEN_SUITE)")
	fs.Func("slice", "run only this slice, by its name (repeatable; default every slice of the suite)", func(s string) error {
		f.names = append(f.names, s)

		return nil
	})
	pathVar(fs, &f.plugins, "plugins", "", "the directory holding the configured drivers (default tools/plugins under the root)", reads)
	pathVar(fs, &f.tools, "tools", "", "a directory put first on the drivers' PATH (default none)", reads)
	pathVar(fs, &f.result, "result", "", "where the slices' result files go (default a temporary directory, removed after)", writes)
	pathVar(fs, &f.work, "work", "", "the drivers' scratch directory (default a temporary directory, removed after)", writes)
	pathVar(fs, &f.cache, "cache", "", "the directory kept between runs for the drivers (default the user's cache directory)", reads|writes)
}

func (f *localFlags) defaults(root string) (remove func(), err error) {
	var temps []string

	remove = func() {
		for _, d := range temps {
			_ = os.RemoveAll(d) //nolint:errcheck // a temporary directory
		}
	}

	if f.plugins == "" {
		f.plugins = filepath.Join(root, "tools", "plugins")
	}

	for _, d := range []*string{&f.result, &f.work} {
		if *d != "" {
			continue
		}

		if *d, err = os.MkdirTemp("", "pipelinegen-slice-run-"); err != nil {
			return remove, fmt.Errorf("slice run: %w", err)
		}

		temps = append(temps, *d)
	}

	if f.cache == "" {
		uc, err := os.UserCacheDir()
		if err != nil {
			return remove, fmt.Errorf("slice run: %w", err)
		}

		f.cache = filepath.Join(uc, "pipelinegen", "slice-run")
	}

	return remove, nil
}

func localSliceRun(ctx context.Context, args []string, stdout io.Writer) error {
	var f localFlags

	fs := flag.NewFlagSet("slice run", flag.ContinueOnError)
	f.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if f.suite == "" {
		return fmt.Errorf("%w: --suite is needed outside a batch task", errUsage)
	}

	cfg, err := f.load()
	if err != nil {
		return err
	}

	if _, ok := cfg.Suites[f.suite]; !ok {
		return fmt.Errorf("%w: no suite %q", errUsage, f.suite)
	}

	root := absRoot(f.root)

	remove, err := f.defaults(root)
	defer remove()

	if err != nil {
		return err
	}

	todo, err := recordSlices(ctx, cfg, root, f.suite, f.names)
	if err != nil {
		return err
	}

	pf, err := pluginsFor(ctx, cfg, absOr(f.plugins), config.Platform(), suitePlugins(cfg, f.suite, false), false)
	if err != nil {
		return err
	}

	b := &drivenBatch{
		cfg: cfg, suite: f.suite, tree: root, tools: f.tools, plugins: pf.dir,
		work: f.work, cache: f.cache, prepared: filepath.Join(f.work, "prepared"), result: f.result,
		params: cfg.Suites[f.suite].Params, log: os.Stderr,
	}

	if err := b.prepare(ctx); err != nil {
		return err
	}

	h := &printHooks{result: f.result, out: stdout}

	if err := b.run(ctx, fromBazel(todo), h, func() error { return nil }); err != nil {
		return err
	}

	if len(h.notPassed) > 0 {
		return fmt.Errorf("%s of %d: %w", plural(len(h.notPassed), "slice"), len(todo), errNotPassed)
	}

	return nil
}

func fromBazel(todo []bazel.Slice) []batchSlice {
	out := make([]batchSlice, 0, len(todo))

	for _, sl := range todo {
		bs := batchSlice{Name: sl.Name, Kind: sl.Kind, Sources: sl.Sources, Tests: sl.Tests}
		if !strings.HasPrefix(sl.Label, "@pipelinegen_deps//") {
			bs.Label, bs.Target = sl.Label, runTarget(sl)
		}

		out = append(out, bs)
	}

	return out
}

func sliceMark(ctx context.Context, args []string, stdout io.Writer) error {
	what, args := takeName(args)
	name, args := takeName(args)

	var (
		dir     string
		timeout time.Duration
	)

	fs := flag.NewFlagSet("slice mark", flag.ContinueOnError)
	pathVar(fs, &dir, "dir", "", "the bundle's marks directory (default "+plugin.EnvMarks+")", reads|writes)
	fs.DurationVar(&timeout, "timeout", 2*time.Minute, "how long to wait for the running batch to take the mark")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if dir == "" {
		dir = os.Getenv(plugin.EnvMarks)
	}

	if what == "" || name == "" || dir == "" {
		return fmt.Errorf("%w: slice mark start|stop <name>, with %s or --dir naming the bundle's marks directory", errUsage, plugin.EnvMarks)
	}

	if err := driver.MarkAndWait(ctx, dir, driver.Mark{Mark: what, Slice: name}, timeout); err != nil {
		return err
	}

	return say(stdout, "marked %s %s\n", what, name)
}

func slicePrepare(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		s step
		f drivenFlags
	)

	if err := s.open(ctx, sliceCommand, args, func(fs *flag.FlagSet) {
		pathVar(fs, &f.prepared, "prepared", "prepared", "the directory the prepare steps fill", writes)
		pathVar(fs, &f.work, "work", "work", "the prepare steps' scratch directory", writes)
		pathVar(fs, &f.cache, "cache", "cache", "the directory kept between builds, for the drivers", reads|writes)
	}); err != nil {
		return err
	}

	defer s.close(&err)

	if err := needEnv(envSuite); err != nil {
		return err
	}

	suite := os.Getenv(envSuite)
	cfg := s.reporter.Config

	b := &drivenBatch{
		cfg: cfg, suite: suite, tree: absOr(s.root), tools: absOr(s.tools), plugins: filepath.Join(absOr(s.tools), "plugins"),
		work: f.work, cache: f.cache, prepared: f.prepared, record: cfg.Records(s.reporter.ID.Run, suite),
		params: cfg.Params(s.reporter.ID.Run, suite), secrets: prepareSecretNames(cfg, s.reporter.ID.Run, suite), log: os.Stderr,
		deadline: taskDeadline(cfg, s.reporter.ID.Run, suite), commit: s.reporter.ID.Commit,
	}

	if err := b.prepare(ctx); err != nil {
		return err
	}

	return say(stdout, "prepared %s\n", suite)
}

func prepareSecretNames(cfg *config.Config, run, suite string) []string {
	return slices.Sorted(maps.Keys(cfg.SecretsFor(run, suite).Prepare))
}
