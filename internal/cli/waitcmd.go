package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// waitKinds are the tasks of the repository's own that wait with wait: a suite's prepare and its
// whole-suite checks.
var waitKinds = []string{config.StepPrepare, config.StepWhole}

// runWait is a repository's task's own call before its work: under the suite's memory.wait or cpu.wait
// it waits while the worker is under memory or CPU pressure, asking memory.broker when it names one, and
// starts anyway once its wait is over. It never fails the task for room.
func runWait(ctx context.Context, args []string, _ io.Writer) error {
	var c common

	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	c.register(fs)
	fs.Lookup("root").DefValue = treeDir
	c.root = treeDir

	// the task's kind comes first, as git-auth's variable does
	kind := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		kind, args = args[0], args[1:]
	}

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if fs.NArg() != 0 || !slices.Contains(waitKinds, kind) {
		return fmt.Errorf("%w: wait <kind>, one of %v", errUsage, waitKinds)
	}

	if err := needEnv(envSuite); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	run := os.Getenv(envRun)
	if run == localRun {
		return nil
	}

	suiteStepMemory(cfg, kind, run, os.Getenv(envSuite), os.Getenv(envCommit)).admit(ctx, os.Stderr)

	return nil
}
