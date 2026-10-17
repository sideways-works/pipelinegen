package check

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/sideways-works/pipelinegen/internal/buildfile"
	"github.com/sideways-works/pipelinegen/internal/config"
)

// Run checks the repository at root: its work tree, or with staged what the
// next commit holds (git's index), reporting every finding about a staged
// file and, when the commit changes a BUILD or .bzl file, the setup or the
// config, every finding it brings that HEAD lacks. With staged, each tree is
// checked under its own config. An error means the check could not run.
func Run(ctx context.Context, cfg *config.Config, root string, staged bool, o Options) ([]Finding, error) {
	if !staged {
		tree, err := WorkTree(ctx, root)
		if err != nil {
			return nil, err
		}

		return FindWith(cfg, tree, nil, o), nil
	}

	index, closeIndex, err := IndexTree(ctx, root)
	if err != nil {
		return nil, err
	}

	defer func() { _ = closeIndex() }() //nolint:errcheck // reading is done; a failed close changes no finding

	paths, hasHead, err := stagedPaths(ctx, root)
	if err != nil {
		return nil, err
	}

	indexCfg, err := configIn(index, cfg, config.Parse)
	if err != nil {
		return nil, err
	}

	all := FindWith(indexCfg, index, nil, o)

	touched := map[string]bool{}
	for _, p := range paths {
		touched[p] = true
	}

	var before map[string]bool

	if hasHead && slices.ContainsFunc(paths, reshapes) {
		if before, err = commitFindings(ctx, cfg, root, "HEAD", o); err != nil {
			return nil, err
		}
	}

	return slices.DeleteFunc(all, func(f Finding) bool {
		if touched[f.Path] || slices.ContainsFunc(f.Files, func(p string) bool { return touched[p] }) {
			return false
		}

		// a commit without HEAD (the first) brings every finding
		return hasHead && (before == nil || before[f.key()])
	}), nil
}

// Commit checks a commit as it is, under its own config: what planning runs
// before it plans that commit, so leftovers in a work tree never count.
func Commit(ctx context.Context, root, rev string, o Options) ([]Finding, error) {
	tree, closeTree, err := CommitTree(ctx, root, rev)
	if err != nil {
		return nil, err
	}

	defer func() { _ = closeTree() }() //nolint:errcheck // reading is done; a failed close changes no finding

	cfg, err := configIn(tree, nil, config.Parse)
	if err != nil {
		return nil, err
	}

	return FindWith(cfg, tree, nil, o), nil
}

// ErrNoConfig is a tree without pipelinegen.yaml where one is needed.
var ErrNoConfig = errors.New("no " + config.FileName)

func configIn(tree Tree, fallback *config.Config, parse func([]byte) (*config.Config, error)) (*config.Config, error) {
	if !slices.Contains(tree.Files, config.FileName) {
		if fallback == nil {
			return nil, ErrNoConfig
		}

		return fallback, nil
	}

	data, err := tree.Read(config.FileName)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", config.FileName, err)
	}

	c, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.FileName, err)
	}

	return c, nil
}

func reshapes(p string) bool {
	switch p {
	case config.FileName, moduleFile, bazelIgnore, gitIgnore:
		return true
	}

	return buildfile.IsBuildFile(p) || path.Ext(p) == ".bzl"
}

// commitFindings are the findings a commit already had, by key. Its config
// is read as history, so the commit that moves a config with retired keys
// passes; a config that can't say what it held leaves nothing known.
func commitFindings(ctx context.Context, fallback *config.Config, root, rev string, o Options) (map[string]bool, error) {
	tree, closeTree, err := CommitTree(ctx, root, rev)
	if err != nil {
		return nil, err
	}

	defer func() { _ = closeTree() }() //nolint:errcheck // reading is done; a failed close changes no finding

	cfg, err := configIn(tree, fallback, config.ParseHistory)
	if errors.Is(err, config.ErrUnreadable) {
		return map[string]bool{}, nil
	}

	if err != nil {
		return nil, err
	}

	out := map[string]bool{}
	for _, f := range FindWith(cfg, tree, nil, o) {
		out[f.key()] = true
	}

	return out, nil
}

func stagedPaths(ctx context.Context, root string) ([]string, bool, error) {
	if _, err := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		if !errors.Is(err, ErrGit) {
			return nil, false, err
		}

		out, err := git(ctx, root, "ls-files", "-z", "--cached")
		if err != nil {
			return nil, false, err
		}

		return nulSplit(out), false, nil
	}

	out, err := git(ctx, root, "diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR", "--no-renames")
	if err != nil {
		return nil, false, fmt.Errorf("staged files: %w", err)
	}

	return nulSplit(out), true, nil
}
