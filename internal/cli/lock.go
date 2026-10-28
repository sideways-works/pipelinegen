package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/lock"
	"github.com/sideways-works/pipelinegen/plugin"
)

// leaseMargin keeps a lease past its suite's time limit, so a slow end never loses it while the job runs.
const leaseMargin = 15 * time.Minute

func openLock(cfg *config.Config, name string) (*lock.Lock, error) {
	l, ok := configFor(cfg, name)
	if !ok {
		return nil, fmt.Errorf("%w: no suite has lock %q", errUsage, name)
	}

	out := lockAt(cfg, name, l.Repository)
	out.Slots = l.Slots
	out.TTL = time.Duration(leaseMinutes(cfg, name))*time.Minute + leaseMargin

	if s, ok := cfg.Suites[os.Getenv(envSuite)]; ok && s.Lock != nil && s.Lock.Name == name {
		out.Weight = s.Lock.Weight
	}

	return out, nil
}

func lockAt(cfg *config.Config, name, repository string) *lock.Lock {
	remote := codeRemote(cfg)
	if repository != "" {
		remote = repoURL(repository)
	}

	out := &lock.Lock{Store: openStoreAt(cfg, remote), Name: name, Log: os.Stderr}

	// On a branch store the leases have a branch of their own without history, so a take or a release never
	// waits behind the records' writes.
	if cfg.OnBranch() {
		out.Branch = branchStore(cfg, remote, cfg.LocksBranchName())
		out.Branch.Roots = true
	}

	return out
}

func closeLock(l *lock.Lock, err *error) {
	if l.Branch != nil {
		if cerr := l.Branch.Close(); cerr != nil && *err == nil {
			*err = cerr
		}
	}

	closeStore(l.Store, err)
}

func leaseMinutes(cfg *config.Config, name string) int {
	suite := os.Getenv(envSuite)
	if s, ok := cfg.Suites[suite]; !ok || s.Lock == nil || s.Lock.Name != name {
		suite = ""

		for _, n := range cfg.SuiteNames() {
			if l := cfg.Suites[n].Lock; l != nil && l.Name == name {
				suite = n

				break
			}
		}
	}

	return cfg.SuiteMinutes(os.Getenv(envRun), suite)
}

func holder(ctx context.Context, cfg *config.Config) (string, error) {
	id, err := runIDFromEnv(ctx, cfg)
	if err != nil {
		return "", err
	}

	if err := needEnv(envSuite); err != nil {
		return "", err
	}

	commit := id.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}

	repository := cfg.GitHub.Repository
	if !cfg.Reports() {
		repository = codeRemote(cfg)
	}

	parts := []string{repository, id.Run, id.Change, commit, os.Getenv(envSuite)}

	if b := os.Getenv(envBatch); b != "" {
		parts = append(parts, "batch", b)
	}

	if sl := os.Getenv(envSlice); sl != "" {
		parts = append(parts, sl)
	}

	return strings.Join(parts, " "), nil
}

func runLock(ctx context.Context, args []string, stdout io.Writer) error {
	return subcommands(ctx, args, []sub{
		{"acquire", "wait for a free or expired slot of a lock and take it: <name>", func(ctx context.Context, args []string) error { return lockAcquire(ctx, args, stdout) }},
		{"release", "free the slot this holder holds: <name>", func(ctx context.Context, args []string) error { return lockRelease(ctx, args, stdout) }},
	})
}

func lockStep(ctx context.Context, name string, args []string, more func(*flag.FlagSet)) (*config.Config, string, error) {
	var c common

	lockName, args := takeName(args)

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	c.register(fs)

	if more != nil {
		more(fs)
	}

	if err := parse(ctx, fs, args); err != nil {
		return nil, "", err
	}

	if lockName == "" {
		return nil, "", fmt.Errorf("%w: name the lock", errUsage)
	}

	c.history = true

	cfg, err := c.load()

	return cfg, lockName, err
}

// In a proposed change's run the tree's config is the change's, so the lease's terms come from the
// trusted keys, never from the change, which could raise its own share of a machine.
func lockStepTrusted(ctx context.Context, name string, args []string, more func(*flag.FlagSet)) (*config.Config, string, error) {
	cfg, lockName, err := lockStep(ctx, name, args, more)
	if err != nil {
		return nil, "", err
	}

	id, err := runIDFromEnv(ctx, cfg)
	if err != nil {
		return nil, "", err
	}

	if err := trustChange(cfg, buildChange(ctx, cfg, id)); err != nil {
		return nil, "", err
	}

	return cfg, lockName, nil
}

func lockAcquire(ctx context.Context, args []string, stdout io.Writer) (err error) {
	wait := plugin.LockWaitMinutes * time.Minute

	var begun string

	cfg, name, err := lockStepTrusted(ctx, "lock acquire", args, func(fs *flag.FlagSet) {
		fs.DurationVar(&wait, "wait", wait, "how long to wait for a slot before failing")
		pathVar(fs, &begun, "started", "", "a directory to stamp, once the slot is taken, with when the work it guards began (a lock taken per batch), for the batch's end to judge a time limit by", writes)
	})
	if err != nil {
		return err
	}

	l, err := openLock(cfg, name)
	if err != nil {
		return err
	}

	defer closeLock(l, &err)

	who, err := holder(ctx, cfg)
	if err != nil {
		return err
	}

	slot, err := l.Acquire(ctx, who, os.Getenv(envJobURL), wait)
	if err != nil {
		return err
	}

	if begun != "" {
		if err := writeStarted(begun, time.Now()); err != nil {
			return err
		}
	}

	_, err = fmt.Fprintf(stdout, "lock %s: slot %d until %s\n", name, slot.N, slot.Lease.Expires.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

func lockRelease(ctx context.Context, args []string, stdout io.Writer) (err error) {
	cfg, name, err := lockStepTrusted(ctx, "lock release", args, nil)
	if err != nil {
		return err
	}

	l, err := openLock(cfg, name)
	if err != nil {
		return err
	}

	defer closeLock(l, &err)

	who, err := holder(ctx, cfg)
	if err != nil {
		return err
	}

	freed, err := l.Release(ctx, who)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "lock %s: %d slots freed\n", name, freed)
	if err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}

// It finds the leases by their refs alone, so a lock a config no longer names is freed all the same.
func runUnlock(ctx context.Context, args []string, stdout io.Writer) (err error) {
	n := 0

	if len(args) > 1 && isNumber(args[1]) {
		if n, err = strconv.Atoi(args[1]); err != nil {
			return fmt.Errorf("%w: slot %q", errUsage, args[1])
		}

		args = append([]string{args[0]}, args[2:]...)
	}

	var repository string

	cfg, name, err := lockStep(ctx, "unlock", args, func(fs *flag.FlagSet) {
		textVar(fs, &repository, "repository", "", "the repository (owner/name) that holds the lock, for a lock no suite of the config names (default: the code repository)")
	})
	if err != nil {
		return err
	}

	if l, ok := configFor(cfg, name); ok && repository == "" {
		repository = l.Repository
	}

	l := lockAt(cfg, name, repository)

	defer closeLock(l, &err)

	removed, err := l.Unlock(ctx, n)
	if err != nil {
		return err
	}

	var b strings.Builder

	for _, s := range removed {
		fmt.Fprintf(&b, "lock %s: slot %d freed, held by %s until %s\n", name, s.N, s.Lease.Holder, s.Lease.Expires.Format(time.RFC3339))
	}

	if len(removed) == 0 {
		fmt.Fprintf(&b, "lock %s: nothing held\n", name)
	}

	if _, err := io.WriteString(stdout, b.String()); err != nil {
		return fmt.Errorf("write output: %w", err)
	}

	return nil
}
