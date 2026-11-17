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
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	localRun  = "local"
	envConfig = "PIPELINEGEN_CONFIG"
)

var (
	errDirty      = errors.New("the tree has changes")
	errNotRecord  = errors.New("can't record")
	errTaskFailed = errors.New("the batch task failed")
)

// A declared run's steps read the repository's own config, so a generated pipeline's step, or any run
// but the local one, refuses PIPELINEGEN_CONFIG.
func configFromEnv() (string, bool, error) {
	p := os.Getenv(envConfig)
	if p == "" {
		return "", false, nil
	}

	if secret.InPipeline() || (os.Getenv(envRun) != "" && os.Getenv(envRun) != localRun) {
		return "", false, fmt.Errorf("%w: %s is for a local recording only; a declared run reads the repository's config", errUsage, envConfig)
	}

	return p, true, nil
}

// localStorePath is in the git directory, so no clone or push ever carries the local recordings.
func localStorePath(ctx context.Context, root string) (string, error) {
	out, err := gitx.Repo{Dir: root}.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("local store: %w", err)
	}

	return filepath.Join(strings.TrimSpace(out), "pipelinegen", "local.git"), nil
}

func localStoreURL(ctx context.Context, path string, create bool) (string, bool, error) {
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		if !create {
			return "", false, nil
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return "", false, fmt.Errorf("local store: %w", err)
		}

		args := [][]string{{"init", "--quiet", "--bare", path}}

		// a branch store reads by partial fetches.
		for _, key := range []string{"uploadpack.allowFilter", "uploadpack.allowAnySHA1InWant"} {
			args = append(args, []string{"-C", path, "config", key, "true"})
		}

		for _, args := range args {
			if _, err := (gitx.Repo{Dir: filepath.Dir(path)}).Git(ctx, args...); err != nil {
				return "", false, fmt.Errorf("local store: %w", err)
			}
		}
	}

	return "file://" + path, true, nil
}

type recordFlags struct {
	common

	suite, store, plugins, work string
	slices                      []string
	images                      imageFlags
}

func runRecord(ctx context.Context, args []string, stdout io.Writer) error {
	var f recordFlags

	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	f.register(fs)
	textVar(fs, &f.suite, "suite", "", "the suite to record (required)")
	fs.Func("slice", "record only this slice, by its name (repeatable; default every slice of the suite)", func(s string) error {
		f.slices = append(f.slices, s)

		return nil
	})
	pathVar(fs, &f.store, "store", "", "the local store (default <git directory>/pipelinegen/local.git)", reads|writes)
	pathVar(fs, &f.plugins, "plugins", "", "the directory holding the configured plugins (default tools/plugins under the root)", reads)
	pathVar(fs, &f.work, "work", "", "where the build's directories go (default a temporary directory, removed after)", writes)
	f.images.register(fs)

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if f.suite == "" {
		return fmt.Errorf("%w: --suite is needed", errUsage)
	}

	cfg, err := f.load()
	if err != nil {
		return err
	}

	root := absRoot(f.root)

	if err := recordable(cfg, f.suite); err != nil {
		return err
	}

	plugins := f.plugins
	if plugins == "" {
		plugins = filepath.Join(root, "tools", "plugins")
	}

	f.plugins = absOr(plugins)

	// the plugins and the build's own directory are tools, not the code
	// recorded: files of theirs in the tree don't count.
	work := ""
	if f.work != "" {
		work = absOr(f.work)
	}

	if err := cleanTree(ctx, root, f.plugins, work); err != nil {
		return err
	}

	return f.record(ctx, root, stdout)
}

func recordable(cfg *config.Config, suite string) error {
	s, ok := cfg.Suites[suite]

	switch {
	case !ok:
		return fmt.Errorf("%w: no suite %q", errUsage, suite)
	case s.Bind != config.BindRecorded:
		return fmt.Errorf("%w: suite %s isn't bound by recording (bind: %s)", errNotRecord, suite, config.BindRecorded)
	case len(cfg.CorrelatorsFor(suite)) == 0:
		return fmt.Errorf("%w: no correlator observes suite %s", errNotRecord, suite)
	case s.RunSlice == "" && !s.HasSteps():
		return fmt.Errorf("%w: suite %s has no run_slice task and no steps", errNotRecord, suite)
	}

	return nil
}

// The lists describe HEAD, so a recording of other code would bind its slices to the wrong things.
func cleanTree(ctx context.Context, root string, tools ...string) error {
	files, err := changedFiles(ctx, root, tools...)
	if err != nil || len(files) == 0 {
		return err
	}

	return fmt.Errorf("%w: a recording describes a commit; commit these first, then record:\n  %s", errDirty, fileList(files))
}

// changedFiles are the work tree's changes, tracked or new, outside the tools' directories.
func changedFiles(ctx context.Context, root string, tools ...string) ([]string, error) {
	out, err := gitx.Repo{Dir: root}.Git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}

	var files []string

	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if len(line) <= 3 || under(filepath.Join(root, line[3:]), tools) {
			continue
		}

		files = append(files, line[3:])
	}

	return files, nil
}

func fileList(files []string) string {
	shown := files[:min(len(files), 10)]
	more := ""

	if len(files) > len(shown) {
		more = fmt.Sprintf("\n  and %d more", len(files)-len(shown))
	}

	return strings.Join(shown, "\n  ") + more
}

func under(p string, dirs []string) bool {
	for _, d := range dirs {
		if d == "" {
			continue
		}

		if rel, err := filepath.Rel(d, p); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return true
		}
	}

	return false
}

func (f *recordFlags) record(ctx context.Context, root string, stdout io.Writer) (err error) {
	head, err := gitx.Repo{Dir: root}.Git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	commit := strings.TrimSpace(head)

	storePath := f.store
	if storePath == "" {
		if storePath, err = localStorePath(ctx, root); err != nil {
			return err
		}
	}

	remote, _, err := localStoreURL(ctx, absOr(storePath), true)
	if err != nil {
		return err
	}

	work := f.work
	if work == "" {
		if work, err = os.MkdirTemp("", "pipelinegen-record-"); err != nil {
			return fmt.Errorf("record: %w", err)
		}

		defer os.RemoveAll(work) //nolint:errcheck // a temporary directory
	}

	work = absOr(work)

	// Each recording is a run of its own, so its lists replace a commit's earlier ones.
	change := localChange(time.Now())
	ch := runs.Change{Run: localRun, ID: change, Commit: commit, Branch: change}

	derived, err := localConfig(f.configPath(), f.suite, remote, change, filepath.Join(work, config.FileName))
	if err != nil {
		return err
	}

	todo, err := recordSlices(ctx, derived, root, f.suite, f.slices)
	if err != nil {
		return err
	}

	b := &localBuild{
		cfg: derived, cfgPath: filepath.Join(work, config.FileName), root: root, dir: filepath.Join(work, "build"),
		suite: f.suite, ch: ch, plugins: f.plugins, box: &imageBox{flags: f.images},
	}

	if err := b.ready(ctx, &f.common, names(todo)); err != nil {
		return err
	}

	remove, err := b.checkout(ctx)
	if err != nil {
		return err
	}

	defer remove()

	if err := b.start(ctx, todo); err != nil {
		return err
	}

	if err := b.run(ctx); err != nil {
		return err
	}

	if err := b.finish(ctx); err != nil {
		return err
	}

	return say(stdout, "recorded %d slices of %s at %s; the lists are in the local store %s, which affected and why read unless the config names a remote (then: --local)\n",
		len(todo), f.suite, short12(commit), storePath)
}

func (c *common) configPath() string {
	if c.config != "" {
		return c.config
	}

	return filepath.Join(c.root, config.FileName)
}

// localRunYAML takes the recording's branch, then the suite.
const localRunYAML = `trigger: {branch: %s}
compare: none
select: all
record: %t
report: {status: false}
suites: {%s: {}}
`

// localChange names a run to the nanosecond: recordings started in the same second, such as batches
// recorded side by side, are runs of their own.
func localChange(t time.Time) string {
	t = t.UTC()

	return fmt.Sprintf("%s-%s-%09d", localRun, t.Format("20060102-150405"), t.Nanosecond())
}

func localConfig(src, suite, remote, change, out string) (*config.Config, error) {
	data, err := os.ReadFile(src) //nolint:gosec // the config the caller names
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	return localConfigOf(data, src, suite, remote, change, out, true)
}

// localConfigOf derives a local build's config from the config data src names: one run, local, of suite,
// recording it when record is set.
func localConfigOf(data []byte, src, suite, remote, change, out string, record bool) (*config.Config, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("record: %s: %w", src, err)
	}

	var run map[string]any
	if err := yaml.Unmarshal(fmt.Appendf(nil, localRunYAML, strconv.Quote(change), record, strconv.Quote(suite)), &run); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	delete(doc, "github")
	doc["git"] = map[string]any{"remote": remote}
	doc["runs"] = map[string]any{localRun: run}

	// The recording is of the user's own commit, so its change is this config's default branch and its
	// lists are a trusted run's.
	cc := map[string]any{}
	if own, ok := doc["concourse"].(map[string]any); ok {
		cc = maps.Clone(own)
	}

	cc["branch"] = change
	doc["concourse"] = cc

	// The local store is a file next to the repository: a branch store's flushes would only slow the batch.
	if store, ok := doc["store"].(map[string]any); ok {
		store = maps.Clone(store)
		delete(store, "backend")
		doc["store"] = store
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	if err := os.WriteFile(out, data, 0o600); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	derived, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("record: the local recording's config: %w", err)
	}

	return derived, nil
}

func recordSlices(ctx context.Context, cfg *config.Config, root, suite string, names []string) ([]bazel.Slice, error) {
	s := cfg.Suites[suite]

	var (
		all []bazel.Slice
		err error
	)

	if s.Implicit() {
		all, err = affected.TestFileSlices(ctx, filepath.Join(root, cfg.Bazel.Workspace), s)
	} else {
		all, err = (&bazel.Client{Bazelisk: bazeliskFor(cfg, root), Dir: filepath.Join(root, cfg.Bazel.Workspace), Env: childEnv(cfg), StartupFlags: startupFlags(cfg)}).Slices(ctx, s.Tag)
	}

	if err != nil {
		return nil, fmt.Errorf("suite %s: %w", suite, err)
	}

	if len(names) == 0 {
		if len(all) == 0 {
			return nil, fmt.Errorf("%w: suite %s has no slices", errNotRecord, suite)
		}

		return all, nil
	}

	var out []bazel.Slice

	for _, n := range names {
		i := slices.IndexFunc(all, func(sl bazel.Slice) bool { return sl.Name == n || sl.Label == n })
		if i < 0 {
			return nil, fmt.Errorf("%w: suite %s has no slice %s", errUsage, suite, n)
		}

		out = append(out, all[i])
	}

	return out, nil
}

type localBuild struct {
	cfg           *config.Config
	cfgPath, root string
	dir, suite    string
	plugins       string
	ch            runs.Change
	// box runs the steps in their images; nil runs them here.
	box *imageBox
	// hostChecked is set once the packages were looked for on this machine.
	hostChecked bool
	// out takes the tasks' output; nil is stderr.
	out io.Writer
	// storeGoes says the run's store is removed when it ends, so a failure is said in full.
	storeGoes bool
}

// findPlugins puts in b.plugins the drivers and correlators the suite's batch runs, fetching those
// the plugins directory lacks.
func (b *localBuild) findPlugins(ctx context.Context) error {
	f, err := pluginsFor(ctx, b.cfg, b.plugins, config.Platform(), suitePlugins(b.cfg, b.suite, b.cfg.Records(localRun, b.suite)), false)
	if err != nil {
		return err
	}

	b.plugins = f.dir

	return nil
}

func (b *localBuild) output() io.Writer {
	if b.out == nil {
		return os.Stderr
	}

	return b.out
}

func (b *localBuild) source() string { return filepath.Join(b.dir, "source") }

// source is a worktree inside the build's directory, as a CI checkout is, so a task writing ../coverage
// writes into the build; a link to the working tree would send it beside the working tree.
func (b *localBuild) checkout(ctx context.Context) (remove func(), err error) {
	if err := os.MkdirAll(b.dir, 0o750); err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	repo := gitx.Repo{Dir: b.root}

	// A stopped recording's checkout goes and the same path is checked out again, which keeps Bazel's
	// output base, keyed by the path, warm.
	if _, err := os.Lstat(b.source()); err == nil {
		_, _ = repo.Git(ctx, "worktree", "remove", "--force", b.source()) //nolint:errcheck // removed below either way

		if err := os.RemoveAll(b.source()); err != nil {
			return nil, fmt.Errorf("record: %w", err)
		}

		_, _ = repo.Git(ctx, "worktree", "prune") //nolint:errcheck // a stale entry only lists itself
	}

	if _, err := repo.Git(ctx, "worktree", "add", "--quiet", "--detach", b.source(), b.ch.Commit); err != nil {
		return nil, fmt.Errorf("record: a checkout of %s for the build: %w", short12(b.ch.Commit), err)
	}

	return func() {
		// A worktree left behind only lists itself until pruned.
		if _, err := repo.Git(context.WithoutCancel(ctx), "worktree", "remove", "--force", b.source()); err != nil {
			_, _ = repo.Git(context.WithoutCancel(ctx), "worktree", "prune") //nolint:errcheck // best effort, see above
		}
	}, nil
}

func (b *localBuild) start(ctx context.Context, todo []bazel.Slice) (err error) {
	store := openStore(b.cfg)
	defer closeStore(store, &err)

	id := reportID(b.ch)

	type suitePart struct {
		Suite      string        `json:"suite"`
		Selected   []bazel.Slice `json:"selected"`
		Untouched  []string      `json:"untouched"`
		WholeSuite []string      `json:"whole_suite"`
	}

	plan := struct {
		Head   string      `json:"head"`
		Suites []suitePart `json:"suites"`
	}{Head: b.ch.Commit, Suites: []suitePart{{Suite: b.suite, Selected: todo, Untouched: []string{}, WholeSuite: []string{}}}}

	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	if err := report.ReplacePlan(ctx, store, id, data); err != nil {
		return err
	}

	r := &report.Reporter{Config: b.cfg, ID: id, Store: store, GitHub: report.Nowhere{}, Warn: os.Stderr}
	if err := r.Start(ctx); err != nil {
		return err
	}

	if err := b.clear(); err != nil {
		return err
	}

	if err := b.linkTools(); err != nil {
		return err
	}

	for _, d := range []string{"result", "coverage"} {
		if err := os.MkdirAll(filepath.Join(b.dir, d), 0o750); err != nil {
			return fmt.Errorf("record: %w", err)
		}
	}

	return writeBatches(batchesOf(b.dir), b.ch, todo, [][]string{names(todo)})
}

// clear removes what an earlier recording in the same --work directory left for one run, so no collector
// reads it as this run's. Only the caches stay, the checkout among them, whose path keys Bazel's output base.
func (b *localBuild) clear() error {
	keep := map[string]bool{"source": true, "cache": true}

	for _, c := range b.taskCaches() {
		first, _, _ := strings.Cut(filepath.ToSlash(filepath.Clean(c)), "/")
		keep[first] = true
	}

	ents, err := os.ReadDir(b.dir)
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	var removed []string

	for _, e := range ents {
		if keep[e.Name()] {
			continue
		}

		if err := os.RemoveAll(filepath.Join(b.dir, e.Name())); err != nil {
			return fmt.Errorf("record: %s holds what an earlier recording left, and %w: nothing runs on what it would mix in", b.dir, err)
		}

		removed = append(removed, e.Name())
	}

	if len(removed) > 0 {
		warnf(os.Stderr, "pipelinegen record: removed what an earlier recording left in %s: %s\n", b.dir, strings.Join(removed, ", "))
	}

	return nil
}

func (b *localBuild) taskCaches() []string {
	s := b.cfg.Suites[b.suite]
	if s.HasSteps() {
		return nil
	}

	var out []string

	for _, file := range []string{s.Prepare, s.RunSlice} {
		if file == "" {
			continue
		}

		t, err := readTask(filepath.Join(b.root, file))
		if err != nil {
			continue
		}

		for _, c := range t.Caches {
			out = append(out, c.dir())
		}
	}

	return out
}

func batchesOf(dir string) string { return filepath.Join(dir, "batches") }

// Each link goes over an earlier recording's, which may name another binary.
func (b *localBuild) linkTools() error {
	if err := os.MkdirAll(filepath.Join(b.dir, "tools"), 0o750); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	links := map[string]string{
		"tools/pipelinegen": exe,
		"tools/plugins":     b.plugins,
		"tools/bazelisk":    "",
	}

	if bz, err := exec.LookPath(bazeliskFor(b.cfg, b.root)); err == nil {
		links["tools/bazelisk"] = absOr(bz)
	}

	for name, target := range links {
		if err := replaceLink(target, filepath.Join(b.dir, name)); err != nil {
			return fmt.Errorf("record: %w", err)
		}
	}

	return nil
}

func replaceLink(target, path string) error {
	if fi, err := os.Lstat(path); err == nil && fi.IsDir() {
		// a directory a rename can't go over.
		if err := os.RemoveAll(path); err != nil {
			return err //nolint:wrapcheck // the caller says whose
		}
	}

	if target == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err //nolint:wrapcheck // the caller says whose
		}

		return nil
	}

	tmp := fmt.Sprintf("%s.%d.new", path, os.Getpid())
	_ = os.Remove(tmp) //nolint:errcheck // one left by an earlier process of the same id

	if err := os.Symlink(target, tmp); err != nil {
		return err //nolint:wrapcheck // the caller says whose
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp) //nolint:errcheck // the rename's error says it

		return err //nolint:wrapcheck // the caller says whose
	}

	return nil
}

// The directory itself stays, as a CI system's mounted output can't be removed.
func emptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err //nolint:wrapcheck // the caller says whose
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		return err //nolint:wrapcheck // the caller says whose
	}

	for _, e := range ents {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err //nolint:wrapcheck // the caller says whose
		}
	}

	return nil
}

func names(todo []bazel.Slice) []string {
	out := make([]string, 0, len(todo))
	for _, sl := range todo {
		out = append(out, sl.Name)
	}

	return out
}

// ownEnv is the build's own PIPELINEGEN_ variables: never a job's address or the pipeline step's mark,
// which would make the tasks a pipeline's.
func (b *localBuild) ownEnv() map[string]string {
	return map[string]string{
		envRun: localRun, envSuite: b.suite, envBatch: "0", "PIPELINEGEN_RECORD": strconv.FormatBool(b.cfg.Records(localRun, b.suite)),
		"PIPELINEGEN_RUN_FILE": filepath.Join(batchesOf(b.dir), runs.RunFile), envConfig: b.cfgPath,
	}
}

// env is the steps' environment on this machine: its own, less what ownEnv leaves out.
func (b *localBuild) env(params map[string]string) []string {
	own := b.ownEnv()

	var out []string

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if _, mine := own[name]; mine || name == envJobURL || name == envPipelineStep || name == envChange || name == envCommit {
			continue
		}

		out = append(out, kv)
	}

	for k, v := range b.cfg.Params(localRun, b.suite) {
		out = append(out, k+"="+v)
	}

	for k, v := range params {
		out = append(out, k+"="+v)
	}

	for k, v := range own {
		out = append(out, k+"="+v)
	}

	return out
}

func (b *localBuild) run(ctx context.Context) error {
	if b.cfg.Suites[b.suite].HasSteps() {
		return b.driven(ctx)
	}

	if p := b.cfg.Suites[b.suite].Prepare; p != "" {
		if err := b.task(ctx, "prepare", p); err != nil {
			return err
		}
	}

	return b.task(ctx, "batch", b.cfg.Suites[b.suite].RunSlice)
}

func (b *localBuild) task(ctx context.Context, kind, file string) error {
	t, err := readTask(filepath.Join(b.root, file))
	if err != nil {
		return err
	}

	for _, o := range t.Outputs {
		if err := os.MkdirAll(filepath.Join(b.dir, o.dir()), 0o750); err != nil {
			return fmt.Errorf("record: %w", err)
		}
	}

	for _, in := range t.Inputs {
		if _, err := os.Stat(filepath.Join(b.dir, in.dir())); err != nil && !in.Optional {
			return fmt.Errorf("%w: %s task %s takes the input %s, which a local recording doesn't make", errNotRecord, kind, file, in.Name)
		}
	}

	img, err := b.boxImage(ctx, kind, file)
	if err != nil {
		return err
	}

	if img.ref != "" {
		path := t.Run.Path
		if strings.Contains(path, "/") && !filepath.IsAbs(path) {
			path = filepath.Join(b.dir, t.Run.Dir, path)
		}

		if err := b.inImage(ctx, img, kind, filepath.Join(b.dir, t.Run.Dir), append([]string{path}, t.Run.Args...), t.params()); err != nil {
			if errors.Is(err, errImage) {
				return err
			}

			return fmt.Errorf("%w: %s task %s: %w; it ran in its image, %s, as on CI", errTaskFailed, kind, file, err, img.ref)
		}

		return nil
	}

	b.hostPackages(ctx)

	cmd := exec.CommandContext(ctx, t.Run.Path, t.Run.Args...) //nolint:gosec // the repository's own task, which the user asked to run
	if !strings.Contains(t.Run.Path, "/") {
		if p, err := exec.LookPath(t.Run.Path); err == nil {
			cmd.Path = p
		}
	} else if !filepath.IsAbs(t.Run.Path) {
		cmd.Path = filepath.Join(b.dir, t.Run.Dir, t.Run.Path)
	}

	cmd.Dir = filepath.Join(b.dir, t.Run.Dir)
	cmd.Env = b.env(t.params())
	cmd.Stdout, cmd.Stderr = b.output(), os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s task %s: %w; it ran on this machine, not in its image %s, so the machine needs what that image has; "+
			"source is a fresh checkout of HEAD: what your working tree has installed or built is not in it",
			errTaskFailed, kind, file, err, t.image())
	}

	return nil
}

func (b *localBuild) driven(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	// prepared and work start empty (start), cache is kept.
	if err := os.MkdirAll(filepath.Join(b.dir, "cache"), 0o750); err != nil {
		return fmt.Errorf("record: %w", err)
	}

	s := b.cfg.Suites[b.suite]

	for _, verb := range []string{plugin.CallPrepare, plugin.CallRun} {
		args := []string{sliceCommand, verb}
		if verb == plugin.CallRun && len(s.StepsOf(config.StepPrepare)) > 0 {
			args = append(args, "--prepared", "prepared")
		}

		kind := "batch"
		if verb == plugin.CallPrepare {
			kind = kindPrepare
		}

		img, err := b.boxImage(ctx, kind, "")
		if err != nil {
			return err
		}

		if img.ref != "" {
			if err := b.inImage(ctx, img, kind, b.dir, append([]string{filepath.Join(b.dir, "tools", "pipelinegen")}, args...), nil); err != nil {
				if errors.Is(err, errImage) {
					return err
				}

				return fmt.Errorf("%w: slice %s in %s: %w", errTaskFailed, verb, img.ref, err)
			}

			continue
		}

		b.hostPackages(ctx)

		cmd := exec.CommandContext(ctx, exe, args...) //nolint:gosec // pipelinegen itself
		cmd.Dir = b.dir
		cmd.Env = b.env(nil)
		cmd.Stdout, cmd.Stderr = b.output(), os.Stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w: slice %s: %w", errTaskFailed, verb, err)
		}
	}

	return nil
}

// boxImage is the image a task runs in, none when the build runs its steps on this machine.
func (b *localBuild) boxImage(ctx context.Context, kind, file string) (taskImage, error) {
	if b.box == nil || b.box.flags.noImage {
		return taskImage{}, nil
	}

	return b.image(ctx, kind, file)
}

// A batch whose end fails stops before the suite's end, and attaches nothing.
func (b *localBuild) finish(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	steps := [][]string{
		{"batch-end", "--batch", "0", "--result", filepath.Join(b.dir, "result")},
		{"suite"},
		{"finish"},
	}

	for i, s := range steps {
		s = append(append([]string{"report"}, s...), "--root", b.source())

		cmd := exec.CommandContext(ctx, exe, s...) //nolint:gosec // pipelinegen itself
		cmd.Dir = b.dir
		cmd.Env = b.env(nil)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr

		if b.storeGoes {
			cmd.Env = append(cmd.Env, envInFull+"=true")
		}

		if err := cmd.Run(); err != nil {
			if !b.cfg.Records(localRun, b.suite) {
				return fmt.Errorf("%w: %s: %w", errNotPassed, strings.Join(s[:2], " "), err)
			}

			// the batch's end stops it before the suite's end attaches.
			attached := "no lists were attached"
			if i > 0 {
				attached = "only what the slices that passed recorded went into the lists"
			}

			return fmt.Errorf("%w: %s: %w; %s", errNotPassed, strings.Join(s[:2], " "), err, attached)
		}
	}

	return nil
}

type task struct {
	ImageResource struct {
		Source  map[string]any `yaml:"source"`
		Version map[string]any `yaml:"version"`
	} `yaml:"image_resource"`
	Inputs  []taskDir      `yaml:"inputs"`
	Outputs []taskDir      `yaml:"outputs"`
	Caches  []taskDir      `yaml:"caches"`
	Params  map[string]any `yaml:"params"`
	Run     struct {
		Path string   `yaml:"path"`
		Args []string `yaml:"args"`
		Dir  string   `yaml:"dir"`
	} `yaml:"run"`
}

type taskDir struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	Optional bool   `yaml:"optional"`
}

func (d taskDir) dir() string {
	if d.Path != "" {
		return d.Path
	}

	return d.Name
}

func readTask(p string) (*task, error) {
	data, err := os.ReadFile(p) //nolint:gosec // the suite's own task file
	if err != nil {
		return nil, fmt.Errorf("record: %w", err)
	}

	var t task
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("record: %s: %w", p, err)
	}

	if t.Run.Path == "" {
		return nil, fmt.Errorf("%w: %s has no run.path", errNotRecord, p)
	}

	return &t, nil
}

func (t *task) params() map[string]string {
	out := map[string]string{}

	for k, v := range t.Params {
		if v != nil {
			out[k] = fmt.Sprint(v)
		}
	}

	return out
}

func (t *task) image() string {
	src := t.ImageResource.Source
	repo, ok := src["repository"].(string)

	if !ok || repo == "" {
		return "(the task file names none; the suite's image)"
	}

	if tag, ok := src["tag"].(string); ok && tag != "" {
		return repo + ":" + tag
	}

	return repo
}
