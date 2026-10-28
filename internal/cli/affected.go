package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldiff"
	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/kit"
	"github.com/sideways-works/pipelinegen/internal/lists"
)

func runAffected(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c                    common
		lp                   localPlan
		base, head, suite    string
		all, asJSON, noLists bool
		sameConfig, local    bool
	)

	fs := flag.NewFlagSet("affected", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &base, "base", "", "the commit to compare with (none: every slice)")
	textVar(fs, &head, "head", "HEAD", "the commit to plan")
	textVar(fs, &suite, "suite", "", "only this suite")
	lp.register(fs)
	fs.BoolVar(&all, "all", false, "select every slice")
	fs.BoolVar(&asJSON, "json", false, "print the plan as JSON")
	fs.BoolVar(&noLists, "no-lists", false, "bind recorded slices to everything instead of reading the lists")
	fs.BoolVar(&local, "local", false, "read the lists from the local store `pipelinegen record` writes, not the remote")
	fs.BoolVar(&sameConfig, "same-config", false, "plan both trees under the config --config names (default the work tree's), reading neither commit's own: to plan commits from before the config existed")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	ctx = withFill(ctx, "affected", *lp.noFill)

	from, err := fromFlags(noLists, local)
	if err != nil {
		return err
	}

	root := absRoot(c.root)

	// Snapshots and work trees are kept by commit, so revisions such as HEAD~1 resolve here.
	if base, head, err = resolveRevs(ctx, root, base, head); err != nil {
		return err
	}

	cfg, err := affectedConfig(ctx, &c, root, head, sameConfig)
	if err != nil {
		return err
	}

	if _, err := pickSuites(cfg, suite); err != nil {
		return err
	}

	plan, err := affectedPlanUnder(ctx, cfg, root, base, head, all, repoCache(ctx, lp.cache, root), lp.bazelDiff, from, pluginsIn(root, lp.plugins), sameConfig)
	if err != nil {
		return err
	}

	if suite != "" {
		plan.Suites = onlySuite(plan.Suites, suite)
	}

	planWarnings(os.Stderr, plan)

	return writeAffected(stdout, plan, asJSON)
}

func writeAffected(stdout io.Writer, plan *affected.Plan, asJSON bool) error {
	var b bytes.Buffer

	if asJSON {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")

		if err := enc.Encode(plan); err != nil {
			return fmt.Errorf("encode: %w", err)
		}

		return emit(stdout, &b)
	}

	writePlan(&b, plan)

	return emit(stdout, &b)
}

func pluginsIn(root, flag string) string {
	if flag == "" {
		return filepath.Join(root, "tools", "plugins")
	}

	return absOr(flag)
}

type listsFrom int

const (
	listsRemote listsFrom = iota
	listsNone
	listsLocal
)

func fromFlags(noLists, local bool) (listsFrom, error) {
	switch {
	case noLists && local:
		return 0, fmt.Errorf("%w: --no-lists and --local exclude each other", errUsage)
	case noLists:
		return listsNone, nil
	case local:
		return listsLocal, nil
	default:
		return listsRemote, nil
	}
}

// listsStore returns nil, having said why, when there is no store; the caller closes the one it returns.
func listsStore(ctx context.Context, cfg *config.Config, root string, from listsFrom, warn io.Writer, who string) *gitstore.Store {
	if from == listsNone {
		return nil
	}

	if from == listsRemote {
		// A config naming no remote prefers a recording made on this machine over the clone's remote.
		if !cfg.Reports() && cfg.Git.Remote == "" && hasLocalStore(ctx, root) {
			from = listsLocal
		} else {
			withOrigin(ctx, cfg, root)

			if codeRemote(cfg) != "" {
				return openStore(cfg)
			}
		}
	}

	path, err := localStorePath(ctx, root)
	if err != nil {
		warnf(warn, "pipelinegen %s: %v: recorded slices are bound to everything\n", who, err)

		return nil
	}

	url, ok, err := localStoreURL(ctx, path, false)

	switch {
	case err != nil:
		warnf(warn, "pipelinegen %s: %v: recorded slices are bound to everything\n", who, err)
	case !ok && from == listsLocal:
		warnf(warn, "pipelinegen %s: no local store at %s (`pipelinegen record` makes it): recorded slices are bound to everything\n", who, path)
	case !ok:
		warnf(warn, "pipelinegen %s: no remote to read the recordings from (git.remote, the github section or the checkout's origin), and no local store (`pipelinegen record` makes one): recorded slices are bound to everything\n", who)
	default:
		warnf(warn, "pipelinegen %s: lists from the local store (recorded on this machine): %s\n", who, path)

		return &gitstore.Store{Remote: url, Retries: config.DefaultRetries}
	}

	return nil
}

func hasLocalStore(ctx context.Context, root string) bool {
	path, err := localStorePath(ctx, root)
	if err != nil {
		return false
	}

	_, ok, err := localStoreURL(ctx, path, false)

	return err == nil && ok
}

// baseLists returns no lists when they can't be read, which binds every recorded slice to everything.
func baseLists(ctx context.Context, cfg *config.Config, root, base string, from listsFrom, warn io.Writer) (recording.Lists, lists.Marks) {
	warn = listsWarnings(cfg, warn)

	store := listsStore(ctx, cfg, root, from, warn, "affected")
	if store == nil {
		return recording.Lists{}, nil
	}

	got, src, err := planningLists(ctx, store, root, base, cfg.ListsCommits())
	if cerr := store.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		warnf(warn, "pipelinegen affected: no lists (%v): recorded slices are bound to everything\n", err)

		return recording.Lists{}, nil
	}

	warnGone(src)

	return got, marksOf(src)
}

func affectedPlan(ctx context.Context, cfg *config.Config, root, base, head string, all bool, cache, bdPath string, from listsFrom, pluginsDir string) (*affected.Plan, error) {
	return affectedPlanUnder(ctx, cfg, root, base, head, all, cache, bdPath, from, pluginsDir, false)
}

const sameConfigWarning = "planned under one config for both trees (--same-config): neither commit's own " + config.FileName + " was read"

func affectedPlanUnder(ctx context.Context, cfg *config.Config, root, base, head string, all bool, cache, bdPath string, from listsFrom, pluginsDir string, same bool) (*affected.Plan, error) {
	differ, err := differFor(ctx, cfg, cache, bdPath)
	if err != nil {
		return nil, err
	}

	l, marks := recording.Lists{}, lists.Marks(nil)
	if base != "" && loadsPlugins(cfg, hasDepsRepo(filepath.Join(root, cfg.Bazel.Workspace))) {
		l, marks = baseLists(ctx, cfg, root, base, from, os.Stderr)
	}

	planOne := func(c *config.Config, everything string, meaning *config.Meaning) (*affected.Plan, error) {
		var p planning

		_, warnings, err := checkCommit(ctx, root, head)
		if same {
			warnings, err = []string{sameConfigWarning}, nil
		}

		// A head without a config of its own (an older commit) still plans.
		switch {
		case errors.Is(err, check.ErrNoConfig):
			warnings = nil
		case err != nil:
			warnings = []string{"the check couldn't read the head: " + err.Error()}
		}

		// A plan that runs everything for a reason known up front binds nothing, so it asks no plugin.
		known, err := affected.Everything(ctx, affected.Options{Config: c, Repo: gitx.Repo{Dir: root}, Base: base, Head: head, All: all || everything != "", AllReason: everything})
		if err != nil {
			return nil, err
		}

		claimed := claimsIfUnknown(ctx, c, cache, pluginsDir, root, head, known)

		deps := hasDepsRepo(filepath.Join(root, c.Bazel.Workspace))

		// The plugins' answers stay in the local cache: only a pipeline step writes them to the repository's refs.
		if loadsPlugins(c, deps) && known == "" {
			var done func()
			if p, done, err = withPlugins(ctx, c, nil, cache, l, marks, root, pluginsDir, base, head); err != nil {
				return nil, err
			}

			defer done()
		}

		plan, err := affected.Compute(ctx, affected.Options{
			Config: c, Repo: gitx.Repo{Dir: root}, Cache: cache,
			Base: base, Head: head, All: all || everything != "", AllReason: everything, Differ: differ, ToolKey: toolKey(differ),
			Bazel: bazelFor(c, root, cache), Prepare: p.prepare, Stub: stubFor(deps), Follow: followFor(c, ""),
			Narrowed: narrowedHook(p.book, p.walk, p.set), EntryPool: entryPoolHook(p.book, p.walk), Meaning: meaning, Unlisted: p.book.unlistedHook(), Why: p.book.whyHook(), Reasons: p.book.reasonsHook(), ExplainLimit: c.ExplainLimitOrDefault(), Sample: sampleRule(c, p.set),
			Written: writtenHook(p.book), Expect: p.book.expectHook(), Select: p.walk.selection(), Inputs: inputsHook(p.book, p.walk), Unobservable: unobservableHook(p.walk), Declared: p.book.declaredHook(),
			Alone:   p.book.aloneHook(),
			Claimed: claimed,
		})
		if err != nil {
			return nil, err
		}

		if p.set != nil {
			r := p.set.Report()
			plan.Plugins = &r
		}

		plan.Warnings = append(plan.Warnings, warnings...)
		plan.Warnings = append(plan.Warnings, correlatorWarnings(p, c)...)
		p.book.markUnlisted(plan)
		plan.Narrowing = narrowingLevel(c)

		return plan, nil
	}

	if same {
		return planOne(cfg, "", nil)
	}

	// A local plan is not a trusted run's: a base that can't plan runs everything.
	return withBaseConfig(ctx, cfg, root, "", base, head, false, planOne)
}

func planWarnings(w io.Writer, p *affected.Plan) {
	ws := slices.Clone(p.Warnings)
	if p.Plugins != nil {
		ws = append(ws, p.Plugins.Warnings...)
	}

	for _, msg := range ws {
		warnf(w, "pipelinegen affected: %s\n", msg)
	}
}

// differFor runs bazel-diff without credentials: it runs on trees a proposed change may have made.
func differFor(ctx context.Context, cfg *config.Config, cache, path string) (bazeldiff.Tool, error) {
	if path != "" {
		return bazeldiff.Tool{Path: path, Env: cachedBazelEnv(cfg, cache)}, nil
	}

	rel := bazeldiff.Pinned
	if cfg.BazelDiff.Version != "" {
		rel = bazeldiff.Release{Version: cfg.BazelDiff.Version, SHA256: cfg.BazelDiff.SHA256, URL: cfg.BazelDiff.URL}
	}

	tool, err := bazeldiff.Fetch(ctx, &http.Client{Timeout: 5 * time.Minute}, cache, rel)
	tool.Env = cachedBazelEnv(cfg, cache)

	return tool, err
}

// repoCache is one directory per repository (by git directory, so worktrees share it), unless --cache
// names another. The cache's lock holds one plan at a time.
func repoCache(ctx context.Context, cache, root string) string {
	dir := cacheOf(ctx, cache, root)
	pinPlanningToolchains(root, dir)

	return dir
}

func cacheOf(ctx context.Context, cache, root string) string {
	if cache != defaultCache() {
		return cache
	}

	dir, err := gitx.Repo{Dir: root}.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return cache
	}

	sum := sha256.Sum256([]byte(strings.TrimSpace(dir)))
	name := filepath.Base(filepath.Dir(strings.TrimSpace(dir)))

	return filepath.Join(cache, "repos", name+"-"+hex.EncodeToString(sum[:6]))
}

func defaultCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "pipelinegen")
	}

	return filepath.Join(dir, "pipelinegen")
}

func onlySuite(suites []affected.SuitePlan, name string) []affected.SuitePlan {
	for _, sp := range suites {
		if sp.Suite == name {
			return []affected.SuitePlan{sp}
		}
	}

	return nil
}

func writePlan(b *bytes.Buffer, p *affected.Plan) {
	if p.All {
		fmt.Fprintf(b, "every slice: %s\n", p.Reason)
	}

	for _, sp := range p.Suites {
		fmt.Fprintf(b, "%s: %d selected, %d untouched\n", sp.Suite, len(sp.Selected), len(sp.Untouched))

		for _, sl := range sp.Selected {
			fmt.Fprintf(b, "  run %s\n", shownName(sl))
		}

		for _, w := range sp.WholeSuite {
			fmt.Fprintf(b, "  run whole-suite check %s\n", w)
		}
	}
}

func shownName(sl bazel.Slice) string {
	if strings.HasPrefix(sl.Label, "@pipelinegen_deps//") {
		return sl.Name
	}

	return sl.Label
}

func warnGone(src *lists.Source) {
	if src != nil && src.Gone {
		warnf(os.Stderr, "pipelinegen affected: %s\n", lists.GoneText(*src))
	}
}

func hasRecorded(cfg *config.Config) bool {
	return slices.ContainsFunc(cfg.SuiteNames(), func(s string) bool { return cfg.Suites[s].Bind == config.BindRecorded })
}

// pinPlanningToolchains puts the repository's version files above the plugins' work tree, so a version
// manager picks the repository's toolchain there and not the machine's default.
func pinPlanningToolchains(root, cache string) {
	if err := kit.PinToolchains(root, cache); err != nil {
		warnf(os.Stderr, "pipelinegen: the repository's version files didn't reach the planning tree in %s (%v): plugins there get the machine's default toolchain\n", cache, err)
	}
}

// affectedConfig reads head's config from git, never from the work tree, so a plan of another commit is
// that commit's; --config and --same-config override it.
func affectedConfig(ctx context.Context, c *common, root, head string, same bool) (*config.Config, error) {
	if _, env, err := configFromEnv(); err != nil || c.config != "" || same || env {
		return c.load()
	}

	data, ok, err := gitx.Repo{Dir: root}.Show(ctx, head, "./"+config.FileName)
	if err != nil {
		return nil, err
	}

	if !ok {
		return nil, fmt.Errorf("%w: %s has no %s; to plan it under another config, give that with --config and --same-config", config.ErrUnreadable, short12(head), config.FileName)
	}

	cfg, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("config %s:%s: %w", short12(head), config.FileName, err)
	}

	for _, w := range cfg.Warnings {
		warnf(os.Stderr, "pipelinegen: config %s:%s: %s\n", short12(head), config.FileName, w)
	}

	if work, err := os.ReadFile(filepath.Join(root, config.FileName)); err == nil && !bytes.Equal(work, data) { //nolint:gosec // the repository's own config
		warnf(os.Stderr, "pipelinegen affected: planned under %s's %s; the work tree's differs and doesn't count (plan under it with --config %s --same-config)\n",
			short12(head), config.FileName, config.FileName)
	}

	return cfg, nil
}

func resolveRevs(ctx context.Context, root, base, head string) (string, string, error) {
	b, err := resolveRev(ctx, root, base)
	if err != nil {
		return "", "", err
	}

	h, err := resolveRev(ctx, root, head)

	return b, h, err
}

func resolveRev(ctx context.Context, root, rev string) (string, error) {
	if rev == "" {
		return "", nil
	}

	commit, ok, err := gitx.Repo{Dir: root}.Resolve(ctx, rev)
	if err != nil {
		return "", err
	}

	if !ok {
		return "", fmt.Errorf("%w: no commit %s in %s", errUsage, rev, root)
	}

	return commit, nil
}
