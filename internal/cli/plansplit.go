package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

// A proposed change is planned in two steps, so its code never runs where a credential that writes is:
// the first, read only, leaves plan.json or failed.txt; the second checks the plan is this change's.
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

	if admitted, err := admittedFirst(ctx, cfg, ch, store, stdout); err != nil || !admitted {
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

func admitted(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, stdout io.Writer) (bool, error) {
	if ch.Connector != "" {
		return stillTheHead(ctx, cfg, ch, stdout)
	}

	if ch.PR == 0 {
		return true, nil
	}

	ok, why, err := runs.Admit(ctx, newGitHubHost(cfg), cfg, ch, plannedSinceIn(ctx, store, reportID(ch)))
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

// The first step may hold the read token alone, which no connector call carries: it plans without
// asking, and the second step asks with the changes token.
func admittedFirst(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, stdout io.Writer) (bool, error) {
	if ch.PR == 0 || tokenFor(cfg, plugin.PurposeChanges) != "" {
		return admitted(ctx, cfg, ch, store, stdout)
	}

	return true, nil
}

// planFromEmit writes the plan as planFrom does and emits it; a plan already written is emitted
// again, since piperun reruns a step that ended unseen, and an emission is taken only with its end.
func planFromEmit(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, from string, em *emitter, stdout io.Writer) error {
	handOn, err := planFromWith(ctx, cfg, ch, store, trigger, from, em, stdout)
	if !handOn {
		endPlanning(ctx, ch, store, stdout)
	}

	return err
}

// planFrom tells whether the build hands the head on: when it wrote the plan, or found it planned and
// stalled as the sweep judges a head, never for a head whose suites began.
func planFrom(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, dir string, stdout io.Writer) (bool, error) {
	return planFromWith(ctx, cfg, ch, store, trigger, dir, nil, stdout)
}

// planFromWith is planFrom, and with an emitter emits the plan it wrote, or the one it found.
func planFromWith(ctx context.Context, cfg *config.Config, ch runs.Change, store *gitstore.Store, trigger, dir string, em *emitter, stdout io.Writer) (bool, error) {
	id := reportID(ch)

	if _, ok, err := readPlan(ctx, store, id); err != nil || ok {
		switch {
		case err != nil:
			return false, err
		case em != nil:
			return true, em.stored(ctx, stdout)
		}

		return handAgain(ctx, cfg, ch, store, stdout)
	}

	if admitted, err := admitted(ctx, cfg, ch, store, stdout); err != nil || !admitted {
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

	// an emission piperun would refuse fails the plan before it is written, saying why
	var emitted emission
	if em != nil {
		if emitted, err = em.build(ctx, plan, false); err != nil {
			return false, errors.Join(err, r.PlanFailed(ctx, err))
		}
	}

	if err := startRun(ctx, store, r, plan, nil); err != nil {
		if em != nil && errors.Is(err, report.ErrPlanned) {
			return true, em.stored(ctx, stdout)
		}

		// another build wrote the plan first, and hands the head on.
		return false, plannedAlready(err, ch, stdout)
	}

	sweepStore(ctx, cfg, ch, store, trigger, plan)

	if err := say(stdout, "%s %s: %s\n", ch.Run, ch.ID, summary(plan)); err != nil || em == nil {
		return true, err
	}

	return true, em.write(emitted, stdout)
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

	if plan.Head == head && plan.Base == base {
		return &plan, nil
	}

	if olderTip(ctx, cfg, ch, abs, &plan, base) {
		return &plan, nil
	}

	return nil, fmt.Errorf("%w: it tests %s against %s, the change is %s against %s",
		errNotThisChange, short12(plan.Head), short12(plan.Base), short12(head), short12(base))
}

var fullCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// olderTip accepts a plan merged into an older tip of a target that moved on: that tip must be in the
// target's history, and merging the change into it here must give the plan's head.
func olderTip(ctx context.Context, cfg *config.Config, ch runs.Change, repo string, plan *affected.Plan, tip string) bool {
	if cfg.Runs[ch.Run].Compare != config.CompareMergedTarget || plan.Base == "" || !fullCommit.MatchString(plan.Base) {
		return false
	}

	if err := runs.FetchCommit(ctx, repo, plan.Base, readAuth(ctx, cfg, repo)); err != nil {
		return false
	}

	if in, err := (gitx.Repo{Dir: repo}).IsAncestor(ctx, plan.Base, tip); err != nil || !in {
		return false
	}

	if merged, err := runs.Merge(ctx, repo, ch.Commit, plan.Base); err != nil || merged != plan.Head {
		return false
	}

	warnf(os.Stderr, "pipelinegen plan: %s %s: planned against %s; the target has moved on to %s since\n", ch.Run, ch.ID, short12(plan.Base), short12(tip))

	return true
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

// endPlanning drops the planning record of a head that still has no plan, so the sweep takes it up
// after missed_after. Like the record, removing it is a convenience: a store error only warns.
func endPlanning(ctx context.Context, ch runs.Change, store *gitstore.Store, stdout io.Writer) {
	if !ch.Proposed() {
		return
	}

	id := reportID(ch)

	if _, planned, err := readPlan(ctx, store, id); err != nil || planned {
		return
	}

	ref, ok, err := store.First(ctx, []string{id.RoundRef(report.RoundStarted)})
	if err == nil && ok {
		_, err = store.Write(ctx, "planning ended "+ch.ID, gitstore.Update{Ref: ref.Name, Old: ref.Commit, Delete: true})
	}

	switch {
	case errors.Is(err, gitstore.ErrStale):
		// another build began planning the head since: its record stays.
	case err != nil:
		warnf(os.Stderr, "pipelinegen plan: %s %s: the record that its planning began isn't removed, so the sweep leaves the head until the plan's limit has passed: %v\n", ch.Run, ch.ID, err)
	case ok:
		_ = say(stdout, "%s %s: planning ended without a plan; the sweep may take the head up\n", ch.Run, ch.ID) //nolint:errcheck // the log is for people
	}
}
