package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/depsrepo"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func usesPlugins(cfg *config.Config) bool {
	for _, p := range cfg.Plugins {
		if p.Kind == config.KindSBOM || p.Kind == config.KindCorrelator {
			return true
		}
	}

	return false
}

// What a suite's setup ran binds every slice of the suite, or its setup target with the setup split in
// force. Walked suites' slices are not bound; with every suite walked no deps repository is written and
// the key is empty.
func pluginsPrepare(cfg *config.Config, l recording.Lists, marks lists.Marks, bz func(dir string) *bazel.Client, set *plugins.Set, w deps.Writer, book *planBook, walk *walkTrees) func(context.Context, string, string, map[string][]bazel.Slice) (string, error) {
	level := narrowingLevel(cfg)

	return func(ctx context.Context, tree, commit string, tests map[string][]bazel.Slice) (string, error) {
		t, err := set.Tree(ctx, tree, commit, level)
		if err != nil {
			return "", err
		}

		var bs []plugins.BindSlice

		split := set.On(plugins.NarrowSetupSplit, level)
		bound, setups := lists.Bound(l, split)

		groups := newGroupOpener(cfg, bz(tree), filepath.Dir(tree), commit)

		for _, suite := range sortedKeys(tests) {
			more, err := suiteBindSlices(ctx, cfg, bz(tree), groups, tree, commit, suite, tests[suite], bound, setups, marks, set, book)
			if err != nil {
				return "", err
			}

			bs = append(bs, more...)
		}

		var walked []plugins.BindSlice

		if walk != nil {
			walked, bs = splitWalked(cfg, bs)

			// the walked slices' own test files without units are units of
			// their own, as Bind makes them for the slices it binds.
			plugins.OwnFiles(tree, t, walked)

			if err := walk.prepared(ctx, commit, t, walked); err != nil {
				return "", err
			}

			// With every suite walked nothing is bound, and an earlier plan's deps repository would only be hashed.
			if len(bs) == 0 {
				book.keepInputs(cfg, commit, t, walked)

				if err := depsrepo.Remove(tree); err != nil {
					return "", err
				}

				return "", nil
			}
		}

		slices, err := set.Bind(ctx, tree, t, bs)
		if err != nil {
			return "", err
		}

		slices = toEntrySample(bs, slices, set.On(plugins.NarrowPluginEntry, level))

		book.keep(commit, t, slices)

		// after keep, which starts the tree's record afresh.
		if walk != nil {
			book.keepInputs(cfg, commit, t, walked)
		}

		slices = toSample(t, slices, set.On(plugins.NarrowSample, level), set.On(plugins.NarrowSampleBound, level))

		key, err := w.Write(ctx, tree, t, slices)
		if err != nil {
			return "", fmt.Errorf("deps repository: %w", err)
		}

		return key, nil
	}
}

func suiteBindSlices(ctx context.Context, cfg *config.Config, bzc *bazel.Client, groups *groupOpener, tree, commit, suite string, sls []bazel.Slice,
	bound recording.Lists, setups map[string][]recording.ID, marks lists.Marks, set *plugins.Set, book *planBook,
) ([]plugins.BindSlice, error) {
	var bs []plugins.BindSlice

	_, depth := cfg.FollowFor("", suite)

	files := make([]string, 0, len(sls))
	for _, sl := range sls {
		files = append(files, sl.Name)
	}

	data, err := sliceData(ctx, bzc, tree, suite, cfg.Suites[suite], files)
	if err != nil {
		return nil, fmt.Errorf("data of %s: %w", suite, err)
	}

	units := sliceUnits(tree, cfg.Suites[suite], files)

	// a suite bound by the graph binds through its owners; a list
	// it has anyway (a suite switched from recorded) still counts.
	graph := cfg.Suites[suite].Bind != config.BindRecorded

	declared := make(map[string][]string, len(sls))
	for _, sl := range sls {
		declared[sl.Name] = labelsOnly(commentData(filepath.Join(tree, filepath.FromSlash(sl.Name))))
	}

	// A recorded slice's filegroups open into what they hold, so a file in one is judged as a file.
	leaves, opened := data, (*bazel.Groups)(nil)

	if !graph {
		// unopened, each filegroup is one label: wider, never a miss
		if l, g, err := openedData(ctx, groups, data, declared); err != nil {
			set.Warnf("suite %s: its slices' filegroups can't be opened, so each stays one label: %v", suite, err)
		} else {
			leaves, opened = l, g
		}
	}

	if setup := unitIDs(setups[suite]); len(setup) > 0 {
		bs = append(bs, plugins.BindSlice{Suite: suite, Name: recording.SetupTarget(suite), List: setup, HasList: true, Depth: depth, Setup: true})
	}

	for _, sl := range sls {
		list, has := bound[suite+"/"+sl.Name]

		// a recorded slice with no list yet is bound to every unit;
		// the plan names it, so why can say so.
		if !has && !graph {
			book.noList(commit, suite, sl.Name)
		}

		// A slice a correlator couldn't tell binds everything until a recording tells again: its list misses what it ran.
		ms := marks[suite+"/"+sl.Name]
		if len(ms) > 0 {
			list, has = nil, false

			for _, m := range ms {
				set.Warnf("%s/%s: %s couldn't tell: %s; bound to everything", suite, sl.Name, m.Plugin, m.Reason)
			}
		}

		byGraph := graph && !has && len(ms) == 0

		// Bound by the graph through nothing but its own files, what they import reaches it through nothing,
		// so the plan runs it on every change and says why.
		if byGraph && len(data[sl.Name]) == 0 && len(units[sl.Name]) == 0 {
			book.aloneSlice(commit, suite, sl.Name)
		}

		bs = append(bs, plugins.BindSlice{
			Suite: suite, Name: sl.Name, Sources: sl.Sources, Tests: sl.Tests,
			List: unitIDs(list), HasList: has, Depth: depth, Data: leaves[sl.Name], Units: units[sl.Name],
			Declared: declared[sl.Name], Tops: data[sl.Name], Groups: opened,
			Graph: byGraph,
		})
	}

	return bs, nil
}

func stubFor(deps bool) func(tree string) error {
	if !deps {
		return nil
	}

	return depsrepo.Stub
}

func depsWriter() depsrepo.Writer { return depsrepo.Writer{} }

func unitIDs(list []recording.ID) []string {
	out := make([]string, 0, len(list))
	for _, id := range list {
		out = append(out, string(id))
	}

	return out
}

// The plugins' answers come from the content cache, written back to the repository's refs from a
// pipeline step only. The caller runs done once the plan is made or has failed.
func withPlugins(ctx context.Context, cfg *config.Config, store *gitstore.Store, cache string, l recording.Lists, marks lists.Marks, trigger, dir, base, head string) (p planning, done func(), err error) { //nolint:nonamedreturns // what plans and how to end it
	work, done, err := callsDir(cache)
	if err != nil {
		return planning{}, nil, err
	}

	p.set, err = plugins.Load(ctx, plugins.Options{Config: cfg, Dir: dir, Cache: cache, Answers: answersCache(cache, store), Work: work})
	if err != nil {
		done()

		return planning{}, nil, err
	}

	notPinned.say(p.set)

	p.book = newPlanBook(p.set)
	p.walk = newWalkTrees(gitx.Repo{Dir: trigger}, base, head, p.set, narrowingLevel(cfg))
	p.prepare = pluginsPrepare(cfg, l, marks, bazelFor(cfg, trigger, cache), p.set, depsWriter(), p.book, p.walk)

	return p, done, nil
}

type planning struct {
	prepare func(context.Context, string, string, map[string][]bazel.Slice) (string, error)
	set     *plugins.Set
	book    *planBook
	walk    *walkTrees
}

const callsPrefix = "plugin-calls-"

const heldFile = "held"

// A plan makes its held file within moments, so only a directory a killed plan left lacks it that long.
const unheldAge = 10 * time.Minute

func callsDir(cache string) (string, func(), error) {
	if err := os.MkdirAll(cache, 0o750); err != nil {
		return "", nil, fmt.Errorf("plugins: %w", err)
	}

	sweepCalls(cache)

	// the held file is locked before it appears in the directory, so a
	// sweep never sees it unheld.
	held, err := os.CreateTemp(cache, callsPrefix+"held-")
	if err != nil {
		return "", nil, fmt.Errorf("plugins: %w", err)
	}

	release := func() {
		held.Close()           //nolint:errcheck // closing frees the lock
		os.Remove(held.Name()) //nolint:errcheck // gone already once moved
	}

	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		release()

		return "", nil, fmt.Errorf("plugins: %w", err)
	}

	work, err := os.MkdirTemp(cache, callsPrefix)
	if err != nil {
		release()

		return "", nil, fmt.Errorf("plugins: %w", err)
	}

	if err := os.Rename(held.Name(), filepath.Join(work, heldFile)); err != nil {
		release()
		os.RemoveAll(work) //nolint:errcheck // failing anyway

		return "", nil, fmt.Errorf("plugins: %w", err)
	}

	return work, func() {
		os.RemoveAll(work) //nolint:errcheck // a scratch directory; the next plan sweeps it
		release()
	}, nil
}

func sweepCalls(cache string) {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return
	}

	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), callsPrefix) {
			continue
		}

		path := filepath.Join(cache, e.Name())

		if !e.IsDir() {
			// a held file not yet moved: its plan locks it first.
			if unheld(path) {
				os.Remove(path) //nolint:errcheck // the next sweep tries again
			}

			continue
		}

		held := filepath.Join(path, heldFile)
		if _, err := os.Stat(held); os.IsNotExist(err) {
			if info, err := e.Info(); err != nil || time.Since(info.ModTime()) < unheldAge {
				continue
			}
		} else if !unheld(held) {
			continue
		}

		os.RemoveAll(path) //nolint:errcheck // the next sweep tries again
	}
}

func unheld(path string) bool {
	f, err := os.Open(path) //nolint:gosec // a file in pipelinegen's own cache
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // closing frees the lock taken here

	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

func pluginsDir() string { return absOr(filepath.Join("tools", "plugins")) }

func marksOf(src *lists.Source) lists.Marks {
	if src == nil {
		return nil
	}

	return src.CannotTell
}
