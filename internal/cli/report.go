package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/osproc"
	"github.com/sideways-works/pipelinegen/internal/pluginstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
	"github.com/sideways-works/pipelinegen/plugin"
)

var errNotPassed = errors.New("did not pass")

const (
	envRun     = "PIPELINEGEN_RUN"
	envChange  = "PIPELINEGEN_CHANGE"
	envCommit  = "PIPELINEGEN_COMMIT"
	envPR      = "PIPELINEGEN_PR"
	envAlsoTo  = "PIPELINEGEN_ALSO_TO"
	envSuite   = "PIPELINEGEN_SUITE"
	envSlice   = "PIPELINEGEN_SLICE"
	envLabel   = "PIPELINEGEN_SLICE_LABEL"
	envBatch   = "PIPELINEGEN_BATCH"
	envJobURL  = "PIPELINEGEN_JOB_URL"
	envToken   = "GITHUB_TOKEN" //nolint:gosec // the variable's name, not a token
	envGitURL  = "PIPELINEGEN_GIT_URL"
	envAPIURL  = "PIPELINEGEN_GITHUB_API"
	defaultGit = "https://github.com"
)

const envPipelineStep = secret.PipelineStepVar

func takeName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}

	return "", args
}

type sub struct {
	name, summary string
	run           func(ctx context.Context, args []string) error
}

type subDocsKey struct{}

// Asked for flags, every subcommand hands its own to the describing context's collector.
func subcommands(ctx context.Context, args []string, subs []sub) error {
	if seen, ok := ctx.Value(describeKey{}).(*[]*flag.FlagSet); ok {
		docs, hasDocs := ctx.Value(subDocsKey{}).(*[]CommandDoc)

		for _, s := range subs {
			var own []*flag.FlagSet

			if err := s.run(context.WithValue(ctx, describeKey{}, &own), nil); err != nil && !errors.Is(err, flag.ErrHelp) {
				return err
			}

			*seen = append(*seen, own...)

			if hasDocs {
				*docs = append(*docs, CommandDoc{Name: s.name, Summary: s.summary, Flags: flagDocs(own)})
			}
		}

		return flag.ErrHelp
	}

	name, rest := takeName(args)

	i := slices.IndexFunc(subs, func(s sub) bool { return s.name == name })
	if i < 0 {
		names := make([]string, 0, len(subs))
		for _, s := range subs {
			names = append(names, s.name)
		}

		slices.Sort(names)

		return fmt.Errorf("%w: one of %s", errUsage, strings.Join(names, ", "))
	}

	return subs[i].run(ctx, rest)
}

func ints(s string) ([]int, error) {
	var out []int

	for f := range strings.SplitSeq(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}

		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is no number", errUsage, f)
		}

		out = append(out, n)
	}

	return out, nil
}

func buildChange(ctx context.Context, cfg *config.Config, id report.ID) runs.Change {
	if ch, ok, err := changeFromBuild(ctx, cfg, id.Run); err == nil && ok {
		return ch
	}

	return runs.Change{Run: id.Run, ID: id.Change, Commit: id.Commit, PR: id.PR}
}

// In the generated pipeline the run comes from the run file (PIPELINEGEN_RUN_FILE) or the trigger's
// checkout (PIPELINEGEN_TRIGGER).
func runIDFromEnv(ctx context.Context, cfg *config.Config) (report.ID, error) {
	id := report.ID{Run: os.Getenv(envRun), Change: os.Getenv(envChange), Commit: os.Getenv(envCommit)}

	if id.Change == "" || id.Commit == "" {
		if ch, ok, err := changeFromBuild(ctx, cfg, id.Run); err != nil {
			return id, err
		} else if ok {
			id = reportID(ch)
			id.AlsoTo = nil

			return id, id.Check()
		}
	}

	if id.Run == "" || id.Change == "" || id.Commit == "" {
		return id, fmt.Errorf("%w: %s, %s and %s must be set", errUsage, envRun, envChange, envCommit)
	}

	if pr := os.Getenv(envPR); pr != "" {
		n, err := strconv.Atoi(pr)
		if err != nil {
			return id, fmt.Errorf("%w: %s=%q", errUsage, envPR, pr)
		}

		id.PR = n
	}

	also, err := ints(os.Getenv(envAlsoTo))
	if err != nil {
		return id, err
	}

	id.AlsoTo = also

	return id, id.Check()
}

func needEnv(names ...string) error {
	for _, n := range names {
		if os.Getenv(n) == "" {
			return fmt.Errorf("%w: %s must be set", errUsage, n)
		}
	}

	return nil
}

// With tokens by purpose tokenEnv never reads GITHUB_TOKEN, which a task of the repository's may then
// hold with less right.
func tokenEnv(cfg *config.Config, purpose string) string {
	if cfg.GitHub.SplitTokens() == nil {
		return envToken
	}

	return toolcalls.PurposeEnv(purpose)
}

func tokenFor(cfg *config.Config, purpose string) string {
	return os.Getenv(tokenEnv(cfg, purpose))
}

func readAuth(ctx context.Context, cfg *config.Config, repo string) runs.Auth {
	scope := runs.GitHubScope
	if !cfg.Reports() {
		scope = runs.OriginScope(ctx, repo)
	}

	return runs.Auth{Scope: scope, Token: tokenFor(cfg, plugin.PurposeRead), Env: sshEnv(cfg, plugin.PurposeRead)}
}

func openStore(cfg *config.Config) *gitstore.Store {
	return openStoreAt(cfg, codeRemote(cfg))
}

func openStoreAt(cfg *config.Config, remote string) *gitstore.Store {
	s := &gitstore.Store{
		Remote: remote, Token: remoteToken(cfg, plugin.PurposeStore, remote), Env: sshEnv(cfg, plugin.PurposeStore),
		Retries: cfg.WriteRetries(), Backoff: cfg.WriteBackoff(), RetryFor: cfg.WriteDeadline(),
	}
	if cfg.OnBranch() {
		s.Branch = branchStore(cfg, remote, cfg.StoreBranchName())
	}

	if p, ok := cfg.StorePlugin(); ok {
		s.Records = storeRecords(cfg, p)
	}

	return s
}

// storeRecords are the records of the config's store plugin, its program found as planning finds a
// plugin's: the plugins directory's copy, else fetched, built or unpacked by its pin.
func storeRecords(cfg *config.Config, p config.Plugin) *pluginstore.Records {
	return &pluginstore.Records{
		Plugin: plugin.Plugin{Name: p.Name, Config: p.Config, Timeout: storeCallTimeout},
		Resolve: func(ctx context.Context) (string, error) {
			f, err := pluginsFor(ctx, cfg, pluginsDir(), defaultCache(), config.Platform(), func(q config.Plugin) bool { return q.Name == p.Name }, false)
			if err != nil {
				return "", err
			}

			return f.files[p.Name], nil
		},
	}
}

// storeCallTimeout bounds one call of a store plugin: a listing of every live recording included.
const storeCallTimeout = 10 * time.Minute

func codeRemote(cfg *config.Config) string {
	switch {
	case cfg.Git.Remote != "":
		return cfg.Git.Remote
	case cfg.Reports():
		return repoURL(cfg.GitHub.Repository)
	default:
		return cfg.Git.Origin
	}
}

func host(cfg *config.Config) report.GitHub { //nolint:ireturn // the config chooses the host
	if !cfg.Reports() {
		return report.Nowhere{}
	}

	return newGitHubHost(cfg)
}

// storeSeed keeps the store's scratch repository between plans on this machine, so a plan fetches only
// the records written since the last one.
func storeSeed(cache, remote string) string {
	sum := sha256.Sum256([]byte(remote))

	return filepath.Join(cache, "store", hex.EncodeToString(sum[:6]))
}

// With the github section only a remote on the trusted git host gets the token: the config a step reads
// may be a proposed change's, which could name any remote.
func remoteToken(cfg *config.Config, purpose, remote string) string {
	token := tokenFor(cfg, purpose)
	if token == "" || !cfg.Reports() || onTrustedHost(remote) {
		return token
	}

	warnf(os.Stderr, "pipelinegen: %s is not on the trusted git host %s, so it gets no token\n", secret.Redact(remote), trustedGit())

	return ""
}

func trustedGit() string {
	if base := os.Getenv(envGitURL); base != "" {
		return strings.TrimRight(base, "/")
	}

	return defaultGit
}

// onTrustedHost compares scheme and host without case, and refuses a URL with a user.
func onTrustedHost(remote string) bool {
	r, err := url.Parse(remote)
	t, terr := url.Parse(trustedGit())

	return err == nil && terr == nil && r.User == nil && strings.EqualFold(r.Scheme, t.Scheme) && strings.EqualFold(r.Host, t.Host)
}

func repoURL(repository string) string {
	base := os.Getenv(envGitURL)
	if base == "" {
		base = defaultGit
	}

	return strings.TrimRight(base, "/") + "/" + repository + ".git"
}

func branchStore(cfg *config.Config, remote, branch string) *branchstore.Store {
	return &branchstore.Store{
		Remote: remote, Token: remoteToken(cfg, plugin.PurposeStore, remote), Env: sshEnv(cfg, plugin.PurposeStore), Branch: branch,
		Retries: cfg.WriteRetries(), Backoff: cfg.WriteBackoff(), RetryFor: cfg.WriteDeadline(), Deadline: cfg.StoreDeadline(),
	}
}

func closeStore(s *gitstore.Store, err *error) {
	if n := s.Retried(); n > 0 {
		warnf(os.Stderr, "pipelinegen: writes to the repository's refs needed %d retries\n", n)
	}

	if cerr := s.Close(); cerr != nil && *err == nil {
		*err = cerr
	}
}

type step struct {
	common

	tools   string
	plugins *batchPlugins
	// setupEnd marks the batch's first step, the one that says slice states are written only at the batch's
	// end for want of a token.
	setupEnd bool

	reporter *report.Reporter
}

func (s *step) close(err *error) {
	closeStore(s.reporter.Store, err)

	if cerr := s.plugins.close(); cerr != nil && *err == nil {
		*err = cerr
	}
}

// When the root is a forge's hand-off, loadChange reads the config from a checkout of the change.
func (s *step) loadChange(ctx context.Context) (*config.Config, error) {
	checkout, err := handOffCheckout(ctx, os.Getenv(envRun), s.root, true)
	if err != nil {
		return nil, err
	}

	if checkout != "" {
		s.root = checkout
	}

	return s.load()
}

// A batch task needs no store token: its slices' states go to GitHub from pipelinegen's own step when
// the batch ends.
func (s *step) warnNoToken(cfg *config.Config, name string) {
	switch {
	// a remote of its own may need no token, and the store says so when
	// it does.
	case tokenFor(cfg, plugin.PurposeStore) != "" || !cfg.Reports():
	case name != sliceCommand:
		warnf(os.Stderr, "pipelinegen %s: no %s in the environment\n", name, tokenEnv(cfg, plugin.PurposeStore))
	case s.setupEnd:
		warnf(os.Stderr, "pipelinegen %s: no %s: the batch's slices show when the batch ends\n", name, tokenEnv(cfg, plugin.PurposeStore))
	}
}

func (s *step) open(ctx context.Context, name string, args []string, more func(*flag.FlagSet)) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	s.register(fs)

	// a batch task's steps run where the tree under test is source/.
	if name == sliceCommand {
		fs.Lookup("root").DefValue = treeDir
		s.root = treeDir
	}

	if name == sliceCommand || name == batchEndCommand || name == flushCommand {
		pathVar(fs, &s.tools, "tools", "tools", "the batch task's tools, which hold the correlators", reads)
	}

	if more != nil {
		more(fs)
	}

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	// a suite job's steps read the tested commit's config as history.
	s.history = true

	cfg, err := s.loadChange(ctx)
	if err != nil {
		return err
	}

	id, err := runIDFromEnv(ctx, cfg)
	if err != nil {
		return err
	}

	if err := trustChange(cfg, buildChange(ctx, cfg, id)); err != nil {
		return err
	}

	s.warnNoToken(cfg, name)

	store := openStore(cfg)
	// a batch's steps write its slices into the batch's own ref.
	store.Batch = batchFromEnv()

	s.reporter = &report.Reporter{
		Config:   cfg,
		ID:       id,
		Store:    store,
		GitHub:   host(cfg),
		Warn:     os.Stderr,
		URL:      os.Getenv(envJobURL),
		Checkout: s.root,
		Attach:   attachRun,
		NoToken:  tokenFor(cfg, plugin.PurposeStore) == "" && name == sliceCommand,
	}

	// only a recording run asks its correlators for anything.
	if suite := os.Getenv(envSuite); s.tools != "" && cfg.Records(id.Run, suite) {
		// The batch's correlators describe themselves once, at its setup's end; later steps read what they said.
		kept, claimed, observed := "", "", ""
		if f := fs.Lookup("result"); f != nil && f.Value.String() != "" {
			kept = filepath.Join(f.Value.String(), report.JournalDir, describedFile)
			claimed = filepath.Join(f.Value.String(), report.JournalDir, claimedFile)
			observed = filepath.Join(f.Value.String(), report.JournalDir, observedDir)
		}

		if s.plugins, err = loadCorrelators(ctx, cfg, suite, s.tools, kept); err != nil {
			return err
		}

		s.plugins.observed = observed

		s.plugins.claim(s.root, claimed)

		// a task file's own steps don't know their task's time limit.
		s.reporter.Collectors = s.plugins.collectors(cfg, suite, time.Time{})

		// planning binds through the plugins, so the recordings are theirs.
		if usesPlugins(cfg) {
			s.reporter.Observer = s.plugins
		}
	}

	return nil
}

func runReport(ctx context.Context, args []string, stdout io.Writer) error {
	err := subcommands(ctx, args, []sub{
		{"start", "set every suite's status and the run's, and make the comment", reportStart},
		{"slice", "report one slice or whole-suite check from its own step", reportSlice},
		{"whole", "report a whole-suite check's end from its result", reportWhole},
		{"batch-end", "close the slices of a batch still without a final state", func(ctx context.Context, a []string) error {
			return reportBatchEnd(ctx, a, stdout)
		}},
		{"flush", "write the ends of a piperun session's confirmed slices from its journal, once or every period: --confirmed <file> [--every <period>]", func(ctx context.Context, a []string) error {
			return reportFlush(ctx, a, stdout)
		}},
		{"suite", "mark the suite's end and set its status and the run's", reportSuite},
		{"finish", "show the run's final state", reportFinish},
		{"collect", "write what every slice of the suite recorded into a directory", reportCollect},
		{"files", "write what a kept run's failed slices left to show why into a directory", func(ctx context.Context, a []string) error {
			return reportFiles(ctx, a, stdout)
		}},
	})
	if errors.Is(err, runs.ErrSuperseded) {
		// A change whose branch moved on ran nothing: its ensure ends quietly.
		return say(stdout, "pipelinegen report: %v\n", err)
	}

	return err
}

func reportCollect(ctx context.Context, args []string) (err error) {
	var (
		s          step
		suite, out string
	)

	if err := s.open(ctx, "report collect", args, func(fs *flag.FlagSet) {
		textVar(fs, &suite, "suite", os.Getenv(envSuite), "the suite")
		pathVar(fs, &out, "out", "slices", "the directory to write into", writes)
	}); err != nil {
		return err
	}

	defer closeStore(s.reporter.Store, &err)

	if suite == "" {
		return fmt.Errorf("%w: name the suite", errUsage)
	}

	defer s.memory("report collect", suite).during(ctx, os.Stderr)()

	_, err = report.Collect(ctx, s.reporter.Store, s.reporter.ID, suite, out)

	return err
}

func reportStart(ctx context.Context, args []string) (err error) {
	var s step
	if err := s.open(ctx, "report start", args, nil); err != nil {
		return err
	}

	defer closeStore(s.reporter.Store, &err)

	return noPlanIsNothing(s.reporter.Start(ctx))
}

func noPlanIsNothing(err error) error {
	if errors.Is(err, report.ErrNoPlan) {
		warnf(os.Stderr, "pipelinegen: %v: nothing to report\n", err)

		return nil
	}

	return err
}

// reportWhole reports a whole-suite check as its own slice whole.<name>, from the result.json it left
// (none: errored).
func reportWhole(ctx context.Context, args []string) (err error) {
	var (
		s            step
		name, result string
	)

	if err := s.open(ctx, "report whole", args, func(fs *flag.FlagSet) {
		textVar(fs, &name, "name", "", "the whole-suite check, as the suite's whole_suite names it")
		pathVar(fs, &result, "result", "", "the check's result directory, holding result.json", reads)
	}); err != nil {
		return err
	}

	defer closeStore(s.reporter.Store, &err)

	if name == "" || result == "" {
		return fmt.Errorf("%w: --name and --result are needed", errUsage)
	}

	if err := needEnv(envSuite); err != nil {
		return err
	}

	defer s.memory("report whole", os.Getenv(envSuite)).during(ctx, os.Stderr)()

	ev := report.SliceEvent{
		Suite: os.Getenv(envSuite), Slice: report.WholeName(name), Label: report.WholeName(name),
		JobURL: os.Getenv(envJobURL), Source: s.root, Result: filepath.Join(result, "result.json"),
	}

	// a check that left no result stopped: its time limit may have.
	ev.Stopped = wholeStopped(s.reporter.Config, s.reporter.ID.Run, ev.Suite, name, ev.Result)

	got, err := s.reporter.Slice(ctx, ev)
	if err != nil {
		return noPlanIsNothing(err)
	}

	if got != report.Passed {
		return fmt.Errorf("%s %s: %w", ev.Slice, got, errNotPassed)
	}

	return nil
}

func reportSlice(ctx context.Context, args []string) (err error) {
	var (
		s                       step
		state, result, coverage string
	)

	if err := s.open(ctx, "report slice", args, func(fs *flag.FlagSet) {
		textVar(fs, &state, "state", "", "running, to report the slice's start")
		pathVar(fs, &result, "result", "", "the result directory, holding result.json", reads)
		pathVar(fs, &coverage, "coverage", "", "the slice's coverage directory (recording runs)", reads)
	}); err != nil {
		return err
	}

	defer closeStore(s.reporter.Store, &err)

	if state != "" && state != "running" {
		return fmt.Errorf("%w: -state takes only running", errUsage)
	}

	if err := needEnv(envSuite, envSlice, envLabel); err != nil {
		return err
	}

	ev := report.SliceEvent{
		Suite: os.Getenv(envSuite), Slice: os.Getenv(envSlice), Label: os.Getenv(envLabel),
		JobURL: os.Getenv(envJobURL), Running: state == "running", Source: s.root,
		Result: filepath.Join(result, "result.json"),
	}
	if coverage != "" {
		ev.Coverage = []string{coverage}
	}

	got, err := s.reporter.Slice(ctx, ev)
	if err != nil {
		return err
	}

	if !ev.Running && got != report.Passed {
		return fmt.Errorf("%s %s: %w", ev.Slice, got, errNotPassed)
	}

	return nil
}

type batchSlice struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Target  string   `json:"target,omitempty"`
	Kind    string   `json:"kind,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Tests   []string `json:"tests,omitempty"`
}

func readBatch(dir, batch string) ([]batchSlice, error) {
	p := filepath.Join(dir, batch, "slices.json")

	data, err := os.ReadFile(p) //nolint:gosec // the run's own batch file
	if err != nil {
		return nil, fmt.Errorf("batch: %w", err)
	}

	var out []batchSlice
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("batch %s: %w", p, err)
	}

	return out, nil
}

func batchEvents(suite string, list []batchSlice, root, result, coverage string) []report.SliceEvent {
	evs := make([]report.SliceEvent, 0, len(list))

	for _, sl := range list {
		evs = append(evs, sliceEvent(suite, sl, root, result, coverage))
	}

	return evs
}

func sliceEvent(suite string, sl batchSlice, root, result, coverage string) report.SliceEvent {
	ev := report.SliceEvent{
		Suite: suite, Slice: sl.Name, Label: sl.Label, JobURL: os.Getenv(envJobURL), Source: root,
		Result: filepath.Join(result, filepath.FromSlash(sl.Name)+".json"),
		Extras: filepath.Join(result, filepath.FromSlash(sl.Name)),
	}
	if ev.Label == "" {
		ev.Label = sl.Name
	}

	if coverage != "" {
		ev.Coverage = []string{report.PartDir(coverage, sl.Name, 1), filepath.Join(coverage, "_setup")}
		ev.CoverageRoot = coverage
	}

	return ev
}

func reportBatchEnd(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		s                                       step
		batch, batches, result, coverage, begun string
		confirmed, requeued, pools              string
	)

	if err := s.open(ctx, batchEndCommand, args, func(fs *flag.FlagSet) {
		textVar(fs, &batch, "batch", batchFromEnv(), "the batch's number")
		pathVar(fs, &batches, "batches", "batches", "the directory of the run's batches", reads)
		pathVar(fs, &result, "result", "result", "the batch's result directory", reads)
		pathVar(fs, &coverage, "coverage", "", "the batch's coverage directory (recording runs)", reads)
		pathVar(fs, &begun, "started", "", "the directory an older pipeline's lock step stamped when the batch's work began", reads)
		pathVar(fs, &confirmed, "confirmed", "", "a piperun session's end: the items whose end piperun recorded ($PIPERUN_CONFIRMED, JSON lines {\"key\", \"outcome\"}), the only slices it closes", readsFile)
		pathVar(fs, &requeued, "requeued", "", "a piperun session's end: the items it handed back to the pool ($PIPERUN_REQUEUED, JSON lines {\"key\", \"reason\"}), left open for another session", readsFile)
		pathVar(fs, &pools, "pools", "", "with --confirmed, the pools' slices plan --emit wrote, for the labels of slices the journal never saw (default pools)", reads)
	}); err != nil {
		return err
	}

	defer s.close(&err)

	if err := needEnv(envSuite); err != nil {
		return err
	}

	defer s.memory("report batch-end", os.Getenv(envSuite)).during(ctx, os.Stderr)()

	if confirmed != "" || requeued != "" {
		return sessionEnd(ctx, &s, sessionFiles{batch: batch, result: result, coverage: coverage, confirmed: confirmed, requeued: requeued, pools: pools}, stdout)
	}

	list, err := readBatch(batches, batch)
	if err != nil {
		return err
	}

	// a batch that left no coverage at all is said once, not once per
	// slice and correlator.
	if coverage != "" && holdsNoFile(coverage) {
		s.reporter.Nothing = "batch " + batch + " recorded nothing"
	}

	suite := os.Getenv(envSuite)
	evs := batchEvents(suite, list, s.root, result, coverage)

	// a correlator the batch left to this step that this task can't run
	// is said once, before anything else; its slices fail saying so.
	if s.reporter.Observer != nil {
		s.plugins.sayLeftUnrunnable(suite, evs, os.Stderr)
	}

	// what the batch's journal holds goes in first, so a batch killed
	// between flushes loses no slice that ended.
	flushed, err := s.reporter.Flush(ctx, report.JournalIn(result))
	if err != nil {
		return unwritten(batch, evs, err)
	}

	// a task that stopped before every slice reported says why, as far as
	// the stamps tell: its time limit, or not.
	stop := markStopped(s.reporter, evs, flushed, batch, batches, begun)

	// a batch that found the worker short of memory started nothing, and says so for each slice.
	if short := shortOfMemoryIn(result); short != "" {
		stop = short

		for i := range evs {
			evs[i].Short = "not run: " + short
		}
	}

	states, err := s.reporter.Close(ctx, evs)
	if err != nil {
		return unwritten(batch, evs, err)
	}

	if !slices.ContainsFunc(evs, func(ev report.SliceEvent) bool { return states[suite+"/"+ev.Slice] != report.NotRun }) {
		what := "ran nothing"
		if coverage != "" {
			what = "recorded nothing"
		}

		return fmt.Errorf("batch %s %s: %s: %w", batch, what, noneRan(len(evs), stop), errNotPassed)
	}

	nothing := ""
	if s.reporter.Nothing != "" {
		nothing = fmt.Sprintf("; %s: no slice left coverage in %s, so none of its slices is recorded", s.reporter.Nothing, coverage)
	}

	if stop != "" {
		nothing += "; " + stop
	}

	return batchNotPassed(ctx, &s, batch, suite, evs, states, nothing)
}

// The step's log is often all a person reads, so batchNotPassed says why below the error.
func batchNotPassed(ctx context.Context, s *step, batch, suite string, evs []report.SliceEvent, states map[string]report.State, more string) error {
	var bad []string

	names := make([]string, 0, len(evs))

	for _, ev := range evs {
		names = append(names, ev.Slice)

		if st := states[suite+"/"+ev.Slice]; st != report.Passed {
			bad = append(bad, ev.Slice+" "+string(st))
		}
	}

	if len(bad) == 0 {
		return nil
	}

	why := whyNotPassed(ctx, s.reporter.Store, s.reporter.ID, suite, names)

	return withWhy(fmt.Errorf("batch %s: %s%s: %w", batch, strings.Join(bad, ", "), more, errNotPassed), why)
}

// unwritten says how the slices ended by their results and that the run doesn't know it, so a rerun of
// the build runs them again.
func unwritten(batch string, evs []report.SliceEvent, err error) error {
	if !errors.Is(err, gitstore.ErrGit) && !errors.Is(err, branchstore.ErrGit) {
		return err
	}

	passed := 0

	for _, ev := range evs {
		if st, _ := report.ReadResult(ev.Result); st == report.Passed {
			passed++
		}
	}

	return fmt.Errorf("batch %s: %d of %d slices passed by their results, but %w, so the run doesn't know how they ended; rerun the build, which runs them again: %w",
		batch, passed, len(evs), report.ErrUnrecorded, err)
}

func reportSuite(ctx context.Context, args []string) (err error) {
	var (
		s       step
		batches string
	)

	err = s.open(ctx, "report suite", args, func(fs *flag.FlagSet) {
		pathVar(fs, &batches, "batches", "", "the directory of the suite's batches, where the start step stamped when the suite's work began", reads)
	})
	if errors.Is(err, config.ErrUnreadable) {
		// its tree step closed the gate: nothing ran, and with no config
		// there is nowhere to report to.
		warnf(os.Stderr, "pipelinegen report suite: not run: this commit's config can't be read by the pinned pipelinegen: %v\n", err)

		return nil
	}

	if err != nil {
		return err
	}

	defer closeStore(s.reporter.Store, &err)

	if err := needEnv(envSuite); err != nil {
		return err
	}

	defer s.memory("report suite", os.Getenv(envSuite)).during(ctx, os.Stderr)()

	if why, err := suiteStopWhy(ctx, &s, batches); err != nil {
		return noPlanIsNothing(err)
	} else if why != "" {
		s.reporter.Stopped = why
	}

	ok, err := s.reporter.Suite(ctx, os.Getenv(envSuite))
	if err != nil {
		return noPlanIsNothing(err)
	}

	if !ok {
		suite := os.Getenv(envSuite)
		why := whyNotPassed(ctx, s.reporter.Store, s.reporter.ID, suite, nil)

		return withWhy(fmt.Errorf("suite %s: %w", suite, errNotPassed), why)
	}

	return nil
}

func reportFinish(ctx context.Context, args []string) (err error) {
	var s step
	if err := s.open(ctx, "report finish", args, nil); err != nil {
		return err
	}

	defer closeStore(s.reporter.Store, &err)

	ok, err := s.reporter.Finish(ctx)
	if err != nil {
		return noPlanIsNothing(err)
	}

	if !ok {
		return fmt.Errorf("run %s: %w", s.reporter.ID.Run, errNotPassed)
	}

	return nil
}

const (
	sliceCommand    = "slice"
	reportCommand   = "report"
	batchEndCommand = reportCommand + " batch-end"
)

func runSlice(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"start", "mark a slice of the running batch as running: <name>", func(ctx context.Context, args []string) error { return sliceStep(ctx, args, true, stdout) }},
		{"end", "end a slice of the running batch: its result, recording and final state: <name>", func(ctx context.Context, args []string) error { return sliceStep(ctx, args, false, stdout) }},
		{"list", "print the running batch's slices, one per line", func(ctx context.Context, args []string) error { return runSliceList(ctx, args, stdout) }},
		{"memory", "wait, before the batch's setup, until the worker has the memory the batch should use, or leave the batch's slices not run", sliceMemory},
		{releaseVerb, "hand back, at the batch's end, the cores the worker's broker granted slice memory: trap 'tools/pipelinegen slice release' EXIT", sliceRelease},
		{"setup-end", "close what the batch's setup ran, once before its first slice", func(ctx context.Context, args []string) error { return sliceSetupEnd(ctx, args, stdout) }},
		{"flush", "write the starts and ends the batch's journal holds", func(ctx context.Context, args []string) error { return sliceFlush(ctx, args, stdout) }},
		{runVerb, "run the batch's slices through the suite's steps, or a suite's slices on this machine: [--suite <s>] [--slice <name>]...", func(ctx context.Context, args []string) error { return sliceRun(ctx, args, stdout) }},
		{"mark", "mark a slice boundary of a bundle the running batch gave a driver: start <name>, stop <name>", func(ctx context.Context, args []string) error { return sliceMark(ctx, args, stdout) }},
		{"prepare", "run the suite's prepare steps into the prepared directory", func(ctx context.Context, args []string) error { return slicePrepare(ctx, args, stdout) }},
		{"bookkeeping-files", "write what a batch's end reads of the tree under test into a directory of its own: --root source --out bookkeeping", func(ctx context.Context, args []string) error {
			return sliceBookkeepingFiles(ctx, args, stdout)
		}},
	})
}

// A slice that did not pass doesn't fail sliceStep, so the batch goes on to its next slice.
func sliceStep(ctx context.Context, args []string, start bool, stdout io.Writer) (err error) {
	var (
		s                         step
		batches, result, coverage string
	)

	name, args := takeName(args)

	if err := s.open(ctx, sliceCommand, args, func(fs *flag.FlagSet) {
		pathVar(fs, &batches, "batches", "batches", "the directory of the run's batches", reads)
		pathVar(fs, &result, "result", "result", "the batch's result directory", reads|writes)
		pathVar(fs, &coverage, "coverage", "coverage", "the batch's coverage directory", reads|writes)
	}); err != nil {
		return err
	}

	defer s.close(&err)

	// A start or end runs on the test's clock: it writes the journal and never reaches the network.
	s.reporter.NoFlush = true

	if err := needEnv(envSuite, envBatch); err != nil {
		return err
	}

	list, err := readBatch(batches, os.Getenv(envBatch))
	if err != nil {
		return err
	}

	i := slices.IndexFunc(list, func(sl batchSlice) bool { return sl.Name == name })
	if i < 0 {
		return fmt.Errorf("%w: no slice %q in batch %s", errUsage, name, os.Getenv(envBatch))
	}

	suite := os.Getenv(envSuite)

	rec := ""
	if s.reporter.Config.Records(s.reporter.ID.Run, suite) {
		rec = coverage
	}

	ev := sliceEvent(suite, list[i], s.root, result, rec)
	j := report.JournalIn(result)

	if s.reporter.ID.Run != localRun || measuring() {
		s.reporter.Meter = cgroupMeter()
	}

	if start {
		if err := checkPlanOnce(ctx, s.reporter, result); err != nil {
			return err
		}

		if err := awaitMemoryOnce(ctx, s.reporter, batches, suite, result); err != nil {
			return err
		}
	}

	if start && rec != "" {
		if err := errors.Join(needCollector(s.reporter.Config, s.plugins, suite), needObservable(s.reporter.Config, s.plugins, suite), needInterpreters(s.plugins, suite, "slice start")); err != nil {
			return err
		}
	}

	got, err := s.noted(ctx, j, ev, start, rec != "")
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "slice %s: %s\n", name, got); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

func (s *step) noted(ctx context.Context, j *report.Journal, ev report.SliceEvent, start, recording bool) (report.State, error) {
	every := flushEvery(s.reporter.Config, s.reporter.ID.Run)

	if start {
		// in a recording run the setup's coverage must be closed first, or
		// the first slice's recording would hold the whole start-up.
		err := s.reporter.Started(ctx, j, ev, recording, every)
		if errors.Is(err, report.ErrNoSetupEnd) {
			err = noSetupEnd(err)
		}

		return report.Running, err
	}

	// a slice whose start was refused has no recording, and its end is refused alike: the batch's end
	// closes it as errored, never passed
	if recording {
		if err := setupEnded(j); err != nil {
			return "", err
		}
	}

	return s.reporter.Ended(ctx, j, ev, every)
}

func noSetupEnd(err error) error {
	return fmt.Errorf("%w: %w: call `tools/pipelinegen slice setup-end` once after the setup, before the first slice", errUsage, err)
}

func setupEnded(j *report.Journal) error {
	ended, err := j.SetupEnded()
	if err != nil {
		return fmt.Errorf("slice: %w", err)
	}

	if !ended {
		return noSetupEnd(report.ErrNoSetupEnd)
	}

	return nil
}

// On a branch store every flush of every batch is a push to one branch, so the default is longer there.
func flushEvery(cfg *config.Config, run string) time.Duration {
	rep := cfg.Runs[run].Report
	if cfg.OnBranch() && rep.Flush == "" {
		return branchFlush
	}

	return rep.FlushEvery()
}

const branchFlush = 5 * time.Minute

func sliceSetupEnd(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		s                step
		result, coverage string
	)

	s.setupEnd = true

	if err := s.open(ctx, sliceCommand, args, func(fs *flag.FlagSet) {
		pathVar(fs, &result, "result", "result", "the batch's result directory, which holds its journal", writes)
		pathVar(fs, &coverage, "coverage", "coverage", "the batch's coverage directory", writes)
	}); err != nil {
		return err
	}

	defer s.close(&err)

	if err := needEnv(envSuite); err != nil {
		return err
	}

	if err := checkPlanOnce(ctx, s.reporter, result); err != nil {
		return err
	}

	if !s.reporter.Config.Records(s.reporter.ID.Run, os.Getenv(envSuite)) {
		s.reporter.Collectors = nil
	} else if err := errors.Join(needCollector(s.reporter.Config, s.plugins, os.Getenv(envSuite)), needObservable(s.reporter.Config, s.plugins, os.Getenv(envSuite))); err != nil {
		return err
	}

	if err := s.reporter.SetupEnd(ctx, report.JournalIn(result), filepath.Join(coverage, "_setup")); err != nil {
		return err
	}

	return say(stdout, "setup ended\n")
}

// The batch's ensure calls sliceFlush, so a batch killed between flushes loses no slice that ended.
func sliceFlush(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		s      step
		result string
	)

	var every time.Duration

	if err := s.open(ctx, sliceCommand, args, func(fs *flag.FlagSet) {
		pathVar(fs, &result, "result", "result", "the batch's result directory, which holds its journal", reads)
		fs.DurationVar(&every, "every", 0, "flush again every period until stopped, in the background, at the lowest priority (0: once)")
	}); err != nil {
		return err
	}

	defer s.close(&err)

	// without a token nothing can be written; the batch's end will.
	if s.reporter.NoToken {
		return say(stdout, "flushed nothing: no %s, the batch's end writes its slices\n", tokenEnv(s.reporter.Config, plugin.PurposeStore))
	}

	if every > 0 {
		return flushInBackground(ctx, s.reporter, report.JournalIn(result), every, stdout)
	}

	states, err := s.reporter.Flush(ctx, report.JournalIn(result))
	if err != nil {
		return err
	}

	return say(stdout, "flushed %s\n", plural(len(states), "slice"))
}

// Each write is tried once, since the next round writes what one gave up, and no step's time limit
// ends its writes.
func inBackground(s *gitstore.Store) {
	s.Retries, s.Until = 0, time.Time{}
	if s.Branch != nil {
		s.Branch.Retries, s.Branch.Until = 0, time.Time{}
	}
}

// flushInBackground runs at the lowest CPU and I/O priority, since it shares the machine with the tests,
// and gives a round up rather than wait out a busy store's back-offs.
func flushInBackground(ctx context.Context, r *report.Reporter, j *report.Journal, every time.Duration, stdout io.Writer) error {
	osproc.LowestPriority()

	// the batch stops it by PID when its tests end.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	inBackground(r.Store)

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return say(stdout, "flushing stopped\n")
		case <-tick.C:
		}

		if states, err := r.Flush(ctx, j); err != nil {
			warnf(os.Stderr, "pipelinegen slice flush: this round wrote nothing, the next will: %v\n", err)
		} else if len(states) > 0 {
			warnf(os.Stderr, "pipelinegen slice flush: flushed %s\n", plural(len(states), "slice"))
		}
	}
}

func runAlsoTo(ctx context.Context, args []string, _ io.Writer) (err error) {
	var (
		c           common
		run, change string
	)

	n := len(args)
	for n > 0 && isNumber(args[n-1]) {
		n--
	}

	prs, err := ints(strings.Join(args[n:], ","))
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("also-to", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &run, "run", "", "the run")
	textVar(fs, &change, "change", "", "the change, pr-<number>")

	if err := parse(ctx, fs, args[:n]); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	st := openStore(cfg)
	defer closeStore(st, &err)

	return report.WriteAlsoTo(ctx, st, run, change, prs)
}

func isNumber(s string) bool {
	_, err := strconv.Atoi(s)

	return err == nil
}

// planChecked marks that the job found its plan: one read a job.
const planChecked = ".plan-checked"

func checkPlanOnce(ctx context.Context, r *report.Reporter, result string) error {
	// A batch step without the store's credential needn't read it: the batches step refused a run without
	// its plan before any batch started.
	if r.NoToken {
		return nil
	}

	mark := filepath.Join(result, planChecked)
	if _, err := os.Stat(mark); err == nil {
		return nil
	}

	if err := r.CheckPlan(ctx); err != nil {
		if errors.Is(err, report.ErrNoPlan) {
			return fmt.Errorf("%w: run %s (%s), change %s (%s), commit %s (%s): the plan of this run is not there, so nothing a slice records would count; "+
				"plan the change under the same names, or set %s to the change the plan was made for",
				err, r.ID.Run, envRun, r.ID.Change, envChange, short12(r.ID.Commit), envCommit, envChange)
		}

		return err
	}

	if err := os.MkdirAll(result, 0o750); err != nil {
		return fmt.Errorf("plan check: %w", err)
	}

	if err := os.WriteFile(mark, nil, 0o600); err != nil {
		return fmt.Errorf("plan check: %w", err)
	}

	return nil
}

func holdsNoFile(dir string) bool {
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			return errFoundFile
		}

		return nil
	})

	return !errors.Is(err, errFoundFile)
}

var errFoundFile = errors.New("found a file")
