package affected

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

// Snapshot is what planning learnt of one commit: its slices, the files Bazel
// owns, and bazel-diff's hashes and edges.
type Snapshot struct {
	Commit string                   `json:"commit"`
	Slices map[string][]bazel.Slice `json:"slices"`
	Owned  map[string]bool          `json:"owned"`
	// DepsTargets counts the deps repository's targets bazel-diff hashed: a snapshot with fewer than
	// the tree should hold was made from a half-written repository and is never trusted.
	DepsTargets int         `json:"deps_targets"`
	Macros      *MacroCheck `json:"macros,omitempty"`
	Hashes      string      `json:"-"`
	Edges       string      `json:"-"`
}

func workspaceDir(o Options) string {
	return filepath.Join(o.Cache, "tree", o.Config.Bazel.Workspace)
}

func prepareTree(ctx context.Context, o Options, commit string) (string, map[string][]bazel.Slice, error) {
	key, _, tests, err := prepareTreeKeys(ctx, o, commit)

	return key, tests, err
}

func prepareTreeKeys(ctx context.Context, o Options, commit string) (string, string, map[string][]bazel.Slice, error) {
	tests, err := checkoutTests(ctx, o, commit)
	if err != nil {
		return "", "", nil, err
	}

	// the slices depend on the config too, so a plan under the base's config never takes one made
	// under the change's; and on the tools: an older build or another bazel-diff hashes differently
	cfg, err := configKey(struct {
		Config any
		Tools  string
	}{o.Config, o.ToolKey})
	if err != nil {
		return "", "", nil, err
	}

	if o.Prepare == nil {
		return commit + "-" + cfg, "", tests, nil
	}

	key, err := o.Prepare(ctx, filepath.Join(o.Cache, "tree"), commit, tests)
	if err != nil {
		return "", "", nil, fmt.Errorf("prepare %s: %w", short(commit), err)
	}

	return commit + "-" + cfg + "-" + key, key, tests, nil
}

func checkoutTests(ctx context.Context, o Options, commit string) (map[string][]bazel.Slice, error) {
	tree := filepath.Join(o.Cache, "tree")
	if err := o.Repo.Checkout(ctx, tree, commit); err != nil {
		return nil, err
	}

	tests := map[string][]bazel.Slice{}

	for _, name := range o.Config.SuiteNames() {
		if s := o.Config.Suites[name]; s.Implicit() {
			sl, err := TestFileSlices(ctx, workspaceDir(o), s)
			if err != nil {
				return nil, err
			}

			tests[name] = sl
		}
	}

	return tests, nil
}

func configKey(cfg any) (string, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("config key: %w", err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])[:12], nil
}

func slicesOnly(ctx context.Context, o Options, commit string) (*Snapshot, error) {
	var (
		tests map[string][]bazel.Slice
		err   error
	)

	if o.Stub != nil {
		if tests, err = checkoutTests(ctx, o, commit); err == nil {
			err = o.Stub(filepath.Join(o.Cache, "tree"))
		}
	} else {
		_, tests, err = prepareTree(ctx, o, commit)
	}

	if err != nil {
		return nil, err
	}

	all, err := listSlices(ctx, o, o.Bazel(workspaceDir(o)), tests)
	if err != nil {
		return nil, err
	}

	return &Snapshot{Commit: commit, Slices: all}, nil
}

// snapshot returns a commit's snapshot, from the cache when made with the same key and holding the deps
// repository it should; one is made only from a tree that stayed as Prepare left it while hashed.
func snapshot(ctx context.Context, o Options, commit string) (*Snapshot, error) {
	key, prepared, tests, err := prepareTreeKeys(ctx, o, commit)
	if err != nil {
		return nil, err
	}

	expect := o.expectedDeps(commit, tests)

	dir := filepath.Join(o.Cache, "snapshots")
	meta := filepath.Join(dir, key+".json")
	s := &Snapshot{Hashes: filepath.Join(dir, key+".hashes"), Edges: filepath.Join(dir, key+".edges")}

	if data, err := os.ReadFile(meta); err == nil && json.Unmarshal(data, s) == nil && fileExists(s.Hashes) && fileExists(s.Edges) { //nolint:gosec // pipelinegen's own cache
		if s.DepsTargets >= expect {
			// a snapshot read again is a snapshot in use: the cache's trim
			// keeps the ones used last
			now := time.Now()
			_ = os.Chtimes(meta, now, now) //nolint:errcheck // only the trim's order depends on it

			return s, nil
		}

		o.warnf("the cached snapshot of %s holds %d targets of the deps repository, fewer than the %d it should: hashed again",
			short(commit), s.DepsTargets, expect)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("snapshot cache: %w", err)
	}

	bz := o.Bazel(workspaceDir(o))

	if s.Slices, err = listSlices(ctx, o, bz, tests); err != nil {
		return nil, err
	}

	if s.Owned, err = ownedPaths(ctx, bz, o.Config.Bazel.Workspace); err != nil {
		return nil, err
	}

	if s.Macros, err = checkMacros(ctx, o, bz, commit); err != nil {
		return nil, err
	}

	s.Commit = commit

	// temporary names first: a cut-off run never leaves a half-written
	// file the next run would trust
	if err := o.Differ.Hashes(ctx, bz, bz.Dir, s.Hashes+".tmp", s.Edges+".tmp"); err != nil {
		return nil, err
	}

	if err := o.unchanged(ctx, commit, prepared, s.Hashes+".tmp", expect, s); err != nil {
		os.Remove(s.Hashes + ".tmp") //nolint:errcheck // failing anyway
		os.Remove(s.Edges + ".tmp")  //nolint:errcheck // failing anyway

		return nil, err
	}

	for _, p := range []string{s.Hashes, s.Edges} {
		if err := os.Rename(p+".tmp", p); err != nil {
			return nil, fmt.Errorf("snapshot cache: %w", err)
		}
	}

	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("snapshot cache: %w", err)
	}

	if err := os.WriteFile(meta, data, 0o600); err != nil {
		return nil, fmt.Errorf("snapshot cache: %w", err)
	}

	return s, nil
}

// ErrMoved wraps a work tree that changed while bazel-diff hashed it.
var ErrMoved = errors.New("the work tree changed underneath the plan")

func (o Options) unchanged(ctx context.Context, commit, prepared, hashes string, expect int, s *Snapshot) error {
	tree := filepath.Join(o.Cache, "tree")

	at, ok, err := gitx.Repo{Dir: tree}.Resolve(ctx, "HEAD")
	if err != nil {
		return err
	}

	if !ok || at != commit {
		return fmt.Errorf("%w: %s was checked out to hash, %s was there after", ErrMoved, short(commit), short(at))
	}

	if o.Written != nil && prepared != "" {
		now, err := o.Written(tree)
		if err != nil {
			return fmt.Errorf("%w: the deps repository of %s: %w", ErrMoved, short(commit), err)
		}

		if now != prepared {
			return fmt.Errorf("%w: the deps repository of %s changed while bazel-diff hashed it (%s, then %s)", ErrMoved, short(commit), prepared, now)
		}
	}

	if o.Prepare == nil {
		return nil
	}

	if s.DepsTargets, err = countDeps(hashes); err != nil {
		return err
	}

	if s.DepsTargets < expect {
		return fmt.Errorf("%w: bazel-diff hashed %d targets of the deps repository of %s, fewer than the %d it holds", ErrMoved, s.DepsTargets, short(commit), expect)
	}

	return nil
}

func (o Options) expectedDeps(commit string, tests map[string][]bazel.Slice) int {
	if o.Prepare == nil {
		return 0
	}

	n, selectors := 0, false

	for suite, ts := range tests {
		if !o.walks(suite) {
			n += 2 * len(ts)
			selectors = true
		}
	}

	// with every suite of test files walked no deps repository is written,
	// so its hashes hold none of it
	if !selectors && len(tests) > 0 {
		return 0
	}

	if o.Expect != nil {
		n += o.Expect(commit)
	}

	return n
}

func countDeps(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // pipelinegen's own cache
	if err != nil {
		return 0, fmt.Errorf("snapshot: %w", err)
	}
	defer f.Close() //nolint:errcheck // read only

	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<20))

	if _, err := dec.Token(); err != nil { // {
		return 0, fmt.Errorf("snapshot %s: %w", path, err)
	}

	n := 0

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return 0, fmt.Errorf("snapshot %s: %w", path, err)
		}

		if tok != "hashes" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return 0, fmt.Errorf("snapshot %s: %w", path, err)
			}

			continue
		}

		if n, err = countKeys(dec); err != nil {
			return 0, fmt.Errorf("snapshot %s: %w", path, err)
		}
	}

	return n, nil
}

func countKeys(dec *json.Decoder) (int, error) {
	if _, err := dec.Token(); err != nil { // {
		return 0, err //nolint:wrapcheck // wrapped by the caller
	}

	n := 0

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return 0, err //nolint:wrapcheck // wrapped by the caller
		}

		if label, ok := tok.(string); ok && strings.HasPrefix(bazel.NormalLabel(label), "@pipelinegen_deps//") {
			n++
		}

		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return 0, err //nolint:wrapcheck // wrapped by the caller
		}
	}

	_, err := dec.Token() // }

	return n, err //nolint:wrapcheck // wrapped by the caller
}

func listSlices(ctx context.Context, o Options, bz *bazel.Client, tests map[string][]bazel.Slice) (map[string][]bazel.Slice, error) {
	all := map[string][]bazel.Slice{}

	for _, name := range o.Config.SuiteNames() {
		if o.Config.Suites[name].Implicit() {
			all[name] = tests[name]

			continue
		}

		s, err := bz.Slices(ctx, o.Config.Suites[name].Tag)
		if err != nil {
			return nil, fmt.Errorf("suite %s: %w", name, err)
		}

		all[name] = slices.DeleteFunc(s, func(sl bazel.Slice) bool { return o.Config.Suites[name].ExcludesTarget(sl.Label) })
	}

	return all, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)

	return err == nil
}
