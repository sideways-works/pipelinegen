package affected

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/buildfile"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gotests"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// TestFileSlices lists the tracked files a suite's patterns match, outside Bazel-ignored directories,
// that hold a test: a Go file a top-level Test function, any other unless it matches the helpers.
func TestFileSlices(ctx context.Context, tree string, s config.Suite) ([]bazel.Slice, error) {
	files, err := trackedFiles(ctx, tree)
	if err != nil {
		return nil, err
	}

	ignored := buildfile.Ignored(files, func(p string) ([]byte, error) {
		return os.ReadFile(filepath.Join(tree, filepath.FromSlash(p))) //nolint:gosec // the work tree's own settings
	})

	out := []bazel.Slice{}

	for _, f := range files {
		if !matchAny(s.Tests, f) || matchAny(s.Exclude, f) {
			continue
		}

		if _, in := ignored(f); in {
			continue
		}

		sl := bazel.Slice{Name: f, Label: bazel.SelectorLabel(f), Sources: []string{f}}

		if strings.HasSuffix(f, ".go") {
			sl.Tests = goTestNames(filepath.Join(tree, filepath.FromSlash(f)))
			if len(sl.Tests) == 0 {
				continue
			}
		} else if matchAny(s.Helpers, f) {
			continue
		}

		out = append(out, sl)
	}

	return out, nil
}

func trackedFiles(ctx context.Context, tree string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", tree, "ls-files", "-z") //nolint:gosec // git in pipelinegen's own work tree
	cmd.Env = secret.HardenGit(os.Environ())

	var stderr strings.Builder

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list the test files: git ls-files in %s: %w: %s "+
			"(the work tree is pipelinegen's checkout of the commit under test, under --cache; "+
			"check that the directory exists and that git trusts it)", tree, err, strings.TrimSpace(stderr.String()))
	}

	var files []string

	for f := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}

	sort.Strings(files)

	return files, nil
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if config.MatchGlob(p, name) {
			return true
		}
	}

	return false
}

func goTestNames(file string) []string {
	src, err := os.ReadFile(file) //nolint:gosec // a tracked file of the work tree
	if err != nil {
		return nil
	}

	names, err := gotests.Names(file, src)
	if err != nil {
		return nil
	}

	return names
}
