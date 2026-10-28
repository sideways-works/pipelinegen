package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/check"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/depsrepo"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
)

// One deps repository serves every suite, so the highest level any suite asks for applies to all.
func narrowingLevel(cfg *config.Config) int {
	level := cfg.Narrowing
	for _, s := range cfg.SuiteNames() {
		level = max(level, cfg.NarrowingFor(s))
	}

	return level
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

func runLearn(ctx context.Context, args []string, stdout io.Writer) error {
	var (
		c    common
		stub bool
	)

	fs := flag.NewFlagSet("learn", flag.ContinueOnError)
	c.register(fs)
	fs.BoolVar(&stub, "stub", false, "write the stub deps repository a fresh checkout needs")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if !stub {
		return fmt.Errorf("%w: learn takes --stub; planning writes the real bindings itself", errUsage)
	}

	if err := depsrepo.Stub(c.root); err != nil {
		return fmt.Errorf("learn: %w", err)
	}

	if err := say(stdout, "%s\n", strings.TrimSpace("wrote "+filepath.Join(c.root, ".pipelinegen", "deps"))); err != nil {
		return err
	}

	// Planning walks and writes no deps repository: the stub only keeps the lines that name it loadable.
	if outward() {
		return say(stdout, "%s\n", learnStubWarning)
	}

	return nil
}

const learnStubWarning = "warning: planning no longer reads the deps repository: remove the " + check.DepsRepo +
	" local_repository lines from MODULE.bazel, the " + check.HiddenDir + " lines from .bazelignore and .gitignore," +
	" and this step from the checkout's setup; the next release refuses them"

func hasDepsRepo(workspace string) bool {
	data, err := os.ReadFile(filepath.Join(workspace, "MODULE.bazel")) //nolint:gosec // the workspace's own module file
	if err != nil {
		return false
	}

	return strings.Contains(string(data), `"pipelinegen_deps"`)
}

// planningLists reads the base's lists (else its nearest first-parent ancestor's) with every live run's
// recordings unioned in.
func planningLists(ctx context.Context, store *gitstore.Store, repo, base string, walk int) (recording.Lists, *lists.Source, error) {
	if base == "" {
		return recording.Lists{}, nil, nil
	}

	l, src, err := lists.ForWithin(ctx, store, repo, base, walk)
	if err != nil {
		return nil, nil, err
	}

	return l, &src, nil
}
