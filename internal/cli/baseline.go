package cli

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/secret"
	"github.com/sideways-works/pipelinegen/plugin"
)

// isBaselineStatus recognises a baseline by this description's start.
const baselineDescription = "baseline set by hand with pipelinegen baseline, not a run"

const statusSuccess = "success"

const baselineRun = "baseline"

// baselineContext is a context of its own, so no status of the run erases the baseline and a branch
// rule requiring the run's status is never met by one. The space keeps it apart from suite contexts.
func baselineContext(cfg *config.Config, run string) string {
	return report.RunContext(cfg, run) + " baseline"
}

func isBaselineStatus(description string) bool {
	return strings.HasPrefix(description, "baseline set by hand")
}

func runBaseline(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c                   common
		runName, commitFlag string
		dryRun              bool
	)

	fs := flag.NewFlagSet("baseline", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &runName, "run", "", "the run whose base it sets (one that compares with the last passed commit)")
	textVar(fs, &commitFlag, "commit", "HEAD", "the commit declared good: HEAD, on the run's branch")
	fs.BoolVar(&dryRun, "dry-run", false, "print what would be posted or written, and do neither")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	if runName == "" {
		return fmt.Errorf("%w: --run is needed", errUsage)
	}

	if err := personOnly(); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	run, ok := cfg.Runs[runName]

	switch {
	case !ok:
		return fmt.Errorf("%w: no run %q in the config", errUsage, runName)
	case run.Trigger.PullRequests != nil:
		return fmt.Errorf("%w: run %s tests pull requests, which compare with their target, not a baseline", errSeedRefused, runName)
	case run.Compare != config.CompareLastPassed:
		return fmt.Errorf("%w: run %s compares with %s, not the last passed commit, so a baseline changes nothing", errSeedRefused, runName, run.Compare)
	}

	// Only default-branch runs are trusted with a baseline: another branch's would accept it and never use it.
	branch := cfg.Concourse.Branch
	if b := run.Trigger.Branch; b != "" && b != branch {
		return fmt.Errorf("%w: run %s is on %s, not the default branch %s: only the default branch's runs take a baseline", errSeedRefused, runName, b, branch)
	}

	root := absRoot(c.root)

	commit, err := headCommit(ctx, root, commitFlag)
	if err != nil {
		return err
	}

	if err := cleanTree(ctx, root, filepath.Join(root, "tools", "plugins")); err != nil {
		return fmt.Errorf("%w: %w", errSeedRefused, err)
	}

	if err := commitOnBranch(ctx, root, commit, branch); err != nil {
		return err
	}

	if cfg.Reports() {
		return postBaseline(ctx, cfg, runName, commit, dryRun, stdout)
	}

	return writeBaseline(ctx, cfg, root, runName, commit, dryRun, stdout)
}

func postBaseline(ctx context.Context, cfg *config.Config, run, commit string, dryRun bool, stdout io.Writer) error {
	st := report.CommitStatus{State: statusSuccess, Context: baselineContext(cfg, run), Description: baselineDescription}

	if dryRun {
		return say(stdout, "dry run, nothing posted: %s would get the status %s: success, %q\n", short12(commit), st.Context, st.Description)
	}

	token := tokenFor(cfg, plugin.PurposeReport)
	if token == "" {
		return fmt.Errorf("%w: the report credential isn't in the environment (%s); a baseline posts the run's status", errSeedRefused, tokenEnv(cfg, plugin.PurposeReport))
	}

	if err := host(cfg).SetStatus(ctx, commit, st); err != nil {
		return err
	}

	return say(stdout, "baseline: %s has the status %s: success, %q; runs of %s on it select nothing, and later ones compare with it until one passes\n",
		short12(commit), st.Context, st.Description, run)
}

func writeBaseline(ctx context.Context, cfg *config.Config, root, run, commit string, dryRun bool, stdout io.Writer) (err error) {
	withOrigin(ctx, cfg, root)

	if codeRemote(cfg) == "" {
		return fmt.Errorf("%w: the config names no store to write the baseline to (git.remote or the checkout's origin)", errSeedRefused)
	}

	if dryRun {
		return say(stdout, "dry run, nothing written: %s's lists would note a baseline of %s set by hand\n", short12(commit), run)
	}

	if !storeCredential(cfg) {
		return fmt.Errorf("%w: the store's writing credential isn't in the environment (%s); a baseline writes the shared lists", errSeedRefused, storeCredentialName(cfg))
	}

	store := openStore(cfg)
	defer closeStore(store, &err)

	note := seedNote(cfg, commit)
	note.Baseline = true

	got, err := lists.Attach{
		Repo: root, Run: baselineRun, Change: run, Commit: commit, To: commit,
		Recordings: recording.Lists{}, Walk: cfg.ListsCommits(), Trusted: true, Seed: note, KeepMarks: true,
	}.Do(ctx, store)
	if err != nil {
		return err
	}

	var b bytes.Buffer

	if got.Again {
		fmt.Fprintf(&b, "baseline: %s carries a baseline of %s already, nothing written\n", short12(commit), run)
	} else {
		fmt.Fprintf(&b, "baseline: %s's lists note a baseline of %s set by hand; runs of %s on it select nothing, and later ones compare with it until one passes\n",
			short12(commit), run, run)
	}

	return emit(stdout, &b)
}

// A seed or a baseline decides what later runs leave out, so only a person sets one, never a step.
func personOnly() error {
	if secret.InPipeline() || os.Getenv(envPR) != "" || strings.HasPrefix(os.Getenv(envChange), "pr-") {
		return fmt.Errorf("%w: it runs on a person's machine, never in a pipeline step (%s, %s, %s or %s is set)", errSeedRefused, envPipelineStep, envJobURL, envPR, envChange)
	}

	return nil
}

func headCommit(ctx context.Context, root, rev string) (string, error) {
	repo := gitx.Repo{Dir: root}

	commit, ok, err := repo.Resolve(ctx, rev)
	if err != nil {
		return "", err
	}

	if !ok {
		return "", fmt.Errorf("%w: no commit %s in %s", errUsage, rev, root)
	}

	head, _, err := repo.Resolve(ctx, "HEAD")
	if err != nil {
		return "", err
	}

	if commit != head {
		return "", fmt.Errorf("%w: %s isn't HEAD (%s): check it out first", errSeedRefused, short12(commit), short12(head))
	}

	return commit, nil
}

func commitOnBranch(ctx context.Context, root, commit, branch string) error {
	repo := gitx.Repo{Dir: root}

	for _, ref := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		tip, ok, err := repo.Resolve(ctx, ref)
		if err != nil {
			return err
		}

		if !ok {
			continue
		}

		if _, err := repo.Git(ctx, "merge-base", "--is-ancestor", commit, tip); err != nil {
			return fmt.Errorf("%w: %s isn't on %s (%s): only a commit of the branch can be its base", errSeedRefused, short12(commit), branch, ref)
		}

		return nil
	}

	return fmt.Errorf("%w: the checkout has no ref of the branch %s, so it can't tell %s is on it: fetch it first", errSeedRefused, branch, short12(commit))
}
