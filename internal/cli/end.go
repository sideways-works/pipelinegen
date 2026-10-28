package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

// endedFile records that a run was ended by hand: its suites' later steps run nothing, and a
// continuous run's next build no longer waits for it.
const endedFile = "ended.json"

func endedRef(id report.ID) string { return id.Prefix() + "_ended" }

func endedByHand(ctx context.Context, store *gitstore.Store, id report.ID) (bool, error) {
	_, ok, err := store.First(ctx, []string{endedRef(id)})

	return ok, err
}

const endedWhy = "the run was ended by hand (pipelinegen end)"

// runEnd ends a run by hand: a round whose worker was lost, or a run nobody wants finished. It posts no
// success for what didn't run, and takes and frees nothing.
func runEnd(ctx context.Context, args []string, stdout io.Writer) (err error) {
	var (
		c                   common
		run, change, commit string
	)

	fs := flag.NewFlagSet("end", flag.ContinueOnError)
	c.register(fs)
	textVar(fs, &run, "run", "", "the run, by its name in the config")
	textVar(fs, &change, "change", "", "the change (default: the run's branch, for a run of a branch)")
	textVar(fs, &commit, "commit", "", "the run's commit, in full or its first 12 characters")

	if err := parse(ctx, fs, args); err != nil {
		return err
	}

	cfg, err := c.load()
	if err != nil {
		return err
	}

	r, ok := cfg.Runs[run]
	if !ok || commit == "" {
		return fmt.Errorf("%w: name a run of the config with --run, and its commit with --commit", errUsage)
	}

	if change == "" {
		if r.Trigger.PullRequests != nil {
			return fmt.Errorf("%w: run %s tests changes: name the change with --change", errUsage, run)
		}

		change = r.Trigger.Branch
	}

	store := openStore(cfg)
	defer closeStore(store, &err)

	id := report.ID{Run: run, Change: change, Commit: commit}

	plan, ok, err := readPlan(ctx, store, id)
	if err != nil {
		return err
	}

	if !ok {
		return endUnplanned(ctx, store, id, stdout)
	}

	// statuses go to the full commit, which the plan names.
	if strings.HasPrefix(plan.Head, commit) {
		id.Commit = plan.Head
	}

	ends, err := report.Ends(ctx, store, cfg, id)
	if err != nil {
		return err
	}

	if err := writeEnded(ctx, store, id); err != nil {
		return err
	}

	// Slices that passed before the end have whole recordings, so they go into the lists.
	rep := &report.Reporter{
		Config: cfg, ID: id, Store: store, GitHub: host(cfg), Warn: os.Stderr, Stopped: endedWhy,
		Checkout: c.root, Attach: attachRun,
	}

	for _, sp := range plan.Suites {
		if !cfg.RunCovers(run, sp.Suite) {
			continue
		}

		if err := endSuite(ctx, rep, ends, sp.Suite, stdout); err != nil {
			return err
		}
	}

	return nil
}

func endSuite(ctx context.Context, rep *report.Reporter, ends map[string]time.Time, suite string, stdout io.Writer) error {
	head := fmt.Sprintf("%s %s %s: suite %s", rep.ID.Run, rep.ID.Change, short12(rep.ID.Commit), suite)

	if at, ok := ends[suite]; ok {
		when := "before"
		if !at.IsZero() {
			when = "at " + at.UTC().Format(time.RFC3339)
		}

		return say(stdout, "%s had ended %s: left as it is\n", head, when)
	}

	// An ended suite whose slices all passed passed; the rest are errored, never passed.
	if _, err := rep.Suite(ctx, suite); err != nil {
		return err
	}

	return say(stdout, "%s ended by hand\n", head)
}

// endUnplanned ends a round a build recorded and never planned, so the next build no longer waits for it.
func endUnplanned(ctx context.Context, store *gitstore.Store, id report.ID, stdout io.Writer) error {
	rounds, err := report.Rounds(ctx, store, id.Run, id.Change)
	if err != nil {
		return err
	}

	if _, ok := rounds[short12(id.Commit)]; !ok {
		return fmt.Errorf("%w: run %s has no plan of %s %s", errUsage, id.Run, id.Change, short12(id.Commit))
	}

	if err := writeEnded(ctx, store, id); err != nil {
		return err
	}

	return say(stdout, "%s %s %s: the round ended by hand before its plan\n", id.Run, id.Change, short12(id.Commit))
}

func writeEnded(ctx context.Context, store *gitstore.Store, id report.ID) error {
	data := fmt.Appendf(nil, "{\"at\": %q}\n", time.Now().UTC().Format(time.RFC3339))
	_, err := store.Write(ctx, "end "+id.Run+" "+id.Change+" "+short12(id.Commit)+" by hand",
		gitstore.Update{Ref: endedRef(id), Any: true, Files: map[string][]byte{endedFile: data}})

	return err
}

func endedEarly(ctx context.Context, store *gitstore.Store, ch runs.Change, suite, gate string, stdout io.Writer) (bool, error) {
	if err := runs.WriteRunFile(gate, ch); err != nil {
		return false, err
	}

	ended, err := endedByHand(ctx, store, reportID(ch))
	if err != nil || !ended {
		return false, err
	}

	if err := writeGate(gate+"/gate.json", false); err != nil {
		return true, err
	}

	return true, say(stdout, "%s %s: ended by hand: %s runs nothing\n", ch.Run, ch.ID, suite)
}
