package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

// A proposed change is planned in two steps, so its code never runs where a credential that writes is.
// The first runs the change's code with the store read only and leaves plan.json or failed.txt; the
// second runs none of it, checks that the plan is of this change, and writes it.
const (
	planOutFile   = "plan.json"
	planFailedOut = "failed.txt"
)

var errNotThisChange = errors.New("the plan is not of this change")

func planOut(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, cache, bdPath, dir string, stdout io.Writer) error {
	// The first step runs the change's code and holds the read token alone: it writes no records.
	store.ReadOnly = true
	store.Token = remoteToken(cfg, plugin.PurposeRead, store.Remote)

	if store.Branch != nil {
		store.Branch.Token = store.Token
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	if _, ok, err := readPlan(ctx, store, reportID(ch)); err != nil || ok {
		if err == nil {
			err = say(stdout, "%s %s: planned already\n", ch.Run, ch.ID)
		}

		return err
	}

	if admitted, err := admittedFirst(ctx, cfg, ch, stdout); err != nil || !admitted {
		return err
	}

	// the change's code runs here: no credential that writes may.
	lines, err := guardProposed(cfg)
	if err != nil {
		return errors.Join(err, os.WriteFile(filepath.Join(dir, planFailedOut), []byte(err.Error()+"\n"), 0o600))
	}

	plan, err := planChange(ctx, cfg, ch, store, trigger, cache, bdPath)
	if err == nil {
		sayFirst(plan, firstWarnings(cfg, ch.Run, lines))
	}

	if err != nil {
		// The second step shows the failure, so no suite waits on a run that will never start.
		if werr := os.WriteFile(filepath.Join(dir, planFailedOut), []byte(err.Error()+"\n"), 0o600); werr != nil {
			return errors.Join(err, werr)
		}

		return say(stdout, "%s %s: no plan: %v\n", ch.Run, ch.ID, err)
	}

	data, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, planOutFile), data, 0o600); err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	return say(stdout, "%s %s: %s (for the next step to write)\n", ch.Run, ch.ID, summary(plan))
}

func admitted(ctx context.Context, cfg *config.Config, ch runs.Change, stdout io.Writer) (bool, error) {
	if ch.Connector != "" {
		return stillTheHead(ctx, cfg, ch, stdout)
	}

	if ch.PR == 0 {
		return true, nil
	}

	gh := &runs.GitHub{API: os.Getenv(envAPIURL), Repository: cfg.GitHub.Repository, Token: tokenFor(cfg, plugin.PurposeChanges)}

	ok, why, err := runs.Admit(ctx, gh, cfg, ch, report.RunContext(cfg, ch.Run))
	if err != nil || ok {
		return ok, err
	}

	// no plan: every suite job's gate skips.
	return false, say(stdout, "%s %s: not planned: %s\n", ch.Run, ch.ID, why)
}

// A branch that moved on is superseded: the newer head is the sweep's to plan, never this change's
// under its older id.
func stillTheHead(ctx context.Context, cfg *config.Config, ch runs.Change, stdout io.Writer) (bool, error) {
	auth := runs.Auth{Scope: httpsScope(ch.Remote), Token: tokenFor(cfg, plugin.PurposeRead), Env: sshEnv(cfg, plugin.PurposeRead)}

	tip, err := runs.BranchTip(ctx, ch.Remote, ch.Branch, auth)
	if err != nil {
		return false, err
	}

	if tip != ch.Commit {
		return false, say(stdout, "%s %s: superseded, not run: %s is at %s now\n", ch.Run, ch.ID, ch.Branch, short12(tip))
	}

	return true, nil
}

// The first step may hold the read token alone, so it plans even when GitHub won't answer it; the
// second step asks again with the changes token.
func admittedFirst(ctx context.Context, cfg *config.Config, ch runs.Change, stdout io.Writer) (bool, error) {
	if ch.PR == 0 || tokenFor(cfg, plugin.PurposeChanges) != "" {
		return admitted(ctx, cfg, ch, stdout)
	}

	gh := &runs.GitHub{API: os.Getenv(envAPIURL), Repository: cfg.GitHub.Repository, Token: tokenFor(cfg, plugin.PurposeRead)}

	ok, why, err := runs.Admit(ctx, gh, cfg, ch, report.RunContext(cfg, ch.Run))
	if err != nil {
		warnf(os.Stderr, "pipelinegen plan: can't ask whether the run takes %s with the read token, planned all the same: %v\n", ch.ID, err)

		return true, nil
	}

	if !ok {
		return false, say(stdout, "%s %s: not planned: %s\n", ch.Run, ch.ID, why)
	}

	return true, nil
}

// planFrom tells whether the build hands the head on: when it wrote the plan, or found it planned and
// stalled as the sweep judges a head, never for a head whose suites began.
func planFrom(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, dir string, stdout io.Writer) (bool, error) {
	id := reportID(ch)

	if _, ok, err := readPlan(ctx, store, id); err != nil || ok {
		if err != nil {
			return false, err
		}

		return handAgain(ctx, cfg, ch, store, stdout)
	}

	if admitted, err := admitted(ctx, cfg, ch, stdout); err != nil || !admitted {
		return false, err
	}

	r := &report.Reporter{
		Config: cfg, ID: id, Store: store, Warn: os.Stderr, URL: os.Getenv(envJobURL),
		GitHub: host(cfg),
	}

	if failed, err := os.ReadFile(filepath.Join(dir, planFailedOut)); err == nil { //nolint:gosec // the first step's own directory
		why := errors.New(strings.TrimSpace(string(failed))) //nolint:err113 // the first step's words, shown as they are

		return false, errors.Join(why, r.PlanFailed(ctx, why))
	}

	data, err := os.ReadFile(filepath.Join(dir, planOutFile)) //nolint:gosec // the first step's own directory
	if errors.Is(err, os.ErrNotExist) {
		// planned already or left out when the first step looked.
		return false, say(stdout, "%s %s: no plan to write\n", ch.Run, ch.ID)
	}

	if err != nil {
		return false, fmt.Errorf("plan: %w", err)
	}

	plan, err := ownPlan(ctx, cfg, ch, store, trigger, data)
	if err != nil {
		return false, errors.Join(err, r.PlanFailed(ctx, err))
	}

	if err := startRun(ctx, store, r, plan, nil); err != nil {
		// another build wrote the plan first, and hands the head on.
		return false, plannedAlready(err, ch, stdout)
	}

	sweepStore(ctx, cfg, ch, store, trigger, plan)

	return true, say(stdout, "%s %s: %s\n", ch.Run, ch.ID, summary(plan))
}

// A head the sweep gave up on goes too: a planning build started by hand is how a person starts its
// suites. It marks the head from the mark it read, so of two builds at once only one hands it on.
func handAgain(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, stdout io.Writer) (bool, error) {
	id := reportID(ch)

	pr := cfg.Runs[ch.Run].Trigger.PullRequests
	if pr == nil {
		return false, say(stdout, "%s %s: planned already\n", ch.Run, ch.ID)
	}

	refs, err := store.List(ctx, report.PlansOfChange(ch.Run, id.Change), id.Prefix())
	if err != nil {
		return false, fmt.Errorf("plan: %w", err)
	}

	mark, err := headMark(ctx, store, id)
	if err != nil {
		return false, err
	}

	sw := sweep{cfg: cfg, refs: refs, now: time.Now(), wait: pr.Missed()}

	again, why := sw.stalled(id)

	if lately, waits := sw.lately(mark); again && lately {
		again, why = false, waits
	}

	if !again {
		return false, say(stdout, "%s %s: %s: nothing handed on\n", ch.Run, ch.ID, why)
	}

	_, err = store.Write(ctx, "hand on "+ch.ID, sw.marked(report.SweptRef(id), mark))
	if errors.Is(err, gitstore.ErrStale) {
		return false, say(stdout, "%s %s: planned, another build took it up first: nothing handed on\n", ch.Run, ch.ID)
	}

	if err != nil {
		return false, fmt.Errorf("plan: %w", err)
	}

	return true, say(stdout, "%s %s: %s\n", ch.Run, ch.ID, why)
}

// The commit under test and the base are worked out again from the trigger, which the first step
// could not touch.
func ownPlan(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger string, data []byte) (*affected.Plan, error) {
	var plan affected.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("plan from the first step: %w", err)
	}

	abs, err := filepath.Abs(trigger)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	head, base, _, err := compareWith(ctx, cfg, ch, store, abs)
	if err != nil {
		return nil, err
	}

	if plan.Head != head || plan.Base != base {
		return nil, fmt.Errorf("%w: it tests %s against %s, the change is %s against %s",
			errNotThisChange, short12(plan.Head), short12(plan.Base), short12(head), short12(base))
	}

	return &plan, nil
}

// beginPlanning records that the build plans its head, so the sweep leaves the head to it until the
// plan's limit has passed. The record is a convenience: a store error only warns.
func beginPlanning(ctx context.Context, ch runs.Change, store *gitstore.Store, stdout io.Writer) error {
	id := reportID(ch)

	if _, ok, err := readPlan(ctx, store, id); err == nil && ok {
		return say(stdout, "%s %s: planned already\n", ch.Run, ch.ID)
	}

	over := ""

	if ref, ok, err := store.First(ctx, []string{id.RoundRef(report.RoundStarted)}); err == nil && ok {
		over = ref.Commit
	}

	if _, err := report.RecordRound(ctx, store, id, report.RoundStarted, os.Getenv(envJobURL), over); err != nil {
		warnf(os.Stderr, "pipelinegen plan: %s %s: the record that its planning began isn't written, so the sweep may plan it too: %v\n", ch.Run, ch.ID, err)

		return nil
	}

	return say(stdout, "%s %s: planning %s begun\n", ch.Run, ch.ID, short12(ch.Commit))
}
