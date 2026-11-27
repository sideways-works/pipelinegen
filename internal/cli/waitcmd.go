package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// waitKinds are the tasks of the repository's own that wait with wait: a suite's prepare and its
// whole-suite checks, and a task of a job outside every suite, named after the kind.
var waitKinds = []string{config.StepPrepare, config.StepWhole, config.StepJob}

// jobName is a job's task's name in wait job: it names the step among the broker's grants.
var jobName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// runWait is a repository's task's own call before its work: under the suite's memory.wait or cpu.wait
// it waits while the worker is under memory or CPU pressure, asking memory.broker when it names one, and
// starts anyway once its wait is over. It never fails the task for room. With --release, in the task's
// exit trap, it hands back the cores the broker granted the wait: the task's container stays on the
// worker a while after its end, so the broker can't see the task end, and its cores would count for
// their whole lease.
func runWait(ctx context.Context, args []string, _ io.Writer) error {
	var (
		c       common
		release bool
		mb      int64
		cores   float64
	)

	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	c.register(fs)
	fs.BoolVar(&release, "release", false, "hand back the cores the worker's broker granted this task's wait, in the task's exit trap")
	fs.Int64Var(&mb, "mb", 0, "wait job: the MB the task waits for, memory.steps.bookkeeping's when 0")
	fs.Float64Var(&cores, "cores", 0, "wait job: the cores the task waits for, cpu.steps.bookkeeping's when 0")
	fs.Lookup("root").DefValue = treeDir
	c.root = treeDir

	// the task's kind comes first, as git-auth's variable does
	kind := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		kind, args = args[0], args[1:]
	}

	// a job's task names itself next
	name := ""
	if kind == config.StepJob && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if fs.NArg() != 0 || !slices.Contains(waitKinds, kind) {
		return fmt.Errorf("%w: wait <kind>, one of %v", errUsage, waitKinds)
	}

	if kind == config.StepJob {
		if !jobName.MatchString(name) || mb < 0 || cores < 0 {
			return fmt.Errorf("%w: wait job <name> [--mb <n>] [--cores <n>]: a name of letters, digits and . _ -, and no negative ask", errUsage)
		}
	} else if mb != 0 || cores != 0 {
		return fmt.Errorf("%w: --mb and --cores are wait job's", errUsage)
	} else if err := needEnv(envSuite); err != nil {
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

	s := suiteStepMemory(cfg, kind, run, os.Getenv(envSuite), os.Getenv(envCommit))
	if kind == config.StepJob {
		s = jobStepMemory(cfg, name, mb, cores)
	}

	if !release {
		s.admit(ctx, os.Stderr)

		return nil
	}

	if s.waitsForCPU() && cfg.MemoryBroker(s.suite) != "" {
		s.release(ctx, os.Stderr)
	}

	return nil
}
