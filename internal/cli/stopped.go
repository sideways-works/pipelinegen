package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// startedFile stamps when a stretch the CI system bounds by a time limit began: the suite's work in
// batches/, or, in an older pipeline, a batch's work in its lock step's output.
const startedFile = "started.json"

type started struct {
	At time.Time `json:"at"`
}

func writeStarted(dir string, now time.Time) error {
	data, err := json.Marshal(started{At: now.UTC().Truncate(time.Second)})
	if err != nil {
		return fmt.Errorf("started: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // the step's own output directory
		return fmt.Errorf("started: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, startedFile), append(data, '\n'), 0o600); err != nil { //nolint:gosec // the step's own output directory
		return fmt.Errorf("started: %w", err)
	}

	return nil
}

func readStarted(dir string) time.Time {
	if dir == "" {
		return time.Time{}
	}

	data, err := os.ReadFile(filepath.Join(dir, startedFile)) //nolint:gosec // the run's own stamp
	if err != nil {
		return time.Time{}
	}

	var s started
	if json.Unmarshal(data, &s) != nil {
		return time.Time{}
	}

	return s.At
}

func limitOf(cfg *config.Config, run, suite string) string {
	return fmt.Sprintf("%d minutes (%s)", cfg.SuiteMinutes(run, suite), cfg.SuiteMinutesKey(run, suite))
}

type stopTimes struct {
	now, suite, batch time.Time
}

// From the suite's stamp alone a batch can only have reached its limit once that long has passed since
// the suite began; a batch's own stamp says it exactly.
func batchStopped(cfg *config.Config, run, suite, batch, where string, at stopTimes) string {
	limit := time.Duration(cfg.SuiteMinutes(run, suite)) * time.Minute
	key := cfg.SuiteMinutesKey(run, suite)
	what := "batch " + batch + " stopped " + where

	exact := func(from time.Time, whose string) string {
		took := at.now.Sub(from)
		if took >= limit {
			return fmt.Sprintf("%s after %s: %s time limit of %s stopped it; raise %s if it needs longer", what, minutes(took), whose, limitOf(cfg, run, suite), key)
		}

		return fmt.Sprintf("%s after %s, within %s time limit of %s: its task died; the build log says why", what, minutes(took), whose, limitOf(cfg, run, suite))
	}

	switch {
	case !at.batch.IsZero():
		return exact(at.batch, "its")
	case !at.suite.IsZero() && at.now.Sub(at.suite) < limit:
		return fmt.Sprintf("%s, %s after the suite began: not its time limit of %s; its task died: the build log says why", what, minutes(at.now.Sub(at.suite)), limitOf(cfg, run, suite))
	}

	return fmt.Sprintf("%s; its time limit is %s: the build log says `timeout exceeded` when that was the cause; raise %s if the batch needs longer", what, limitOf(cfg, run, suite), key)
}

func suiteStopped(cfg *config.Config, run, suite string, began bool, at stopTimes) string {
	limit := time.Duration(cfg.SuiteMinutes(run, suite)) * time.Minute
	key := cfg.SuiteMinutesKey(run, suite)

	if !began {
		return fmt.Sprintf("suite %s's work never began: its start step (the tree, the batches) failed or reached its time limit of %d minutes (limits.steps); the build log says which", suite, cfg.Limits.StepMinutes())
	}

	took := at.now.Sub(at.suite)

	switch {
	case at.suite.IsZero():
	case took < limit:
		return fmt.Sprintf("suite %s stopped %s after its work began, before these slices reported: not its time limit of %s; its prepare or a batch failed, or the build was aborted: the build log says which", suite, minutes(took), limitOf(cfg, run, suite))
	}

	return fmt.Sprintf("suite %s stopped before these slices reported: its prepare or a batch failed, or reached its time limit of %s; the build log says which (`timeout exceeded`); raise %s if a task needs longer", suite, limitOf(cfg, run, suite), key)
}

func minutes(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m == 1 {
		return "1 minute"
	}

	return fmt.Sprintf("%d minutes", m)
}

func openSlices(evs []report.SliceEvent, flushed map[string]report.State) ([]string, string) {
	var open []string

	running, wrote := "", false

	for _, ev := range evs {
		if st := flushed[ev.Suite+"/"+ev.Slice]; st == report.Running {
			running = ev.Slice
		}

		if _, err := os.Stat(ev.Result); errors.Is(err, os.ErrNotExist) {
			open = append(open, ev.Slice)
		} else {
			wrote = true
		}
	}

	switch {
	case running != "":
		return open, "while slice " + running + " ran"
	case !wrote && len(flushed) == 0:
		return open, "before its first slice started"
	}

	return open, "before every slice reported"
}

func suiteStopWhy(ctx context.Context, s *step, batches string) (string, error) {
	if batches == "" {
		return "", nil
	}

	suite := os.Getenv(envSuite)

	states, err := report.States(ctx, s.reporter.Store, s.reporter.ID, suite)
	if err != nil {
		return "", err
	}

	for _, st := range states {
		if st == report.Pending || st == report.Running {
			at := stopTimes{now: time.Now()}
			at.suite = readStarted(batches)

			// the start step deals the batches last: without them it never
			// finished.
			_, err := os.Stat(filepath.Join(batches, "batches.json"))

			return suiteStopped(s.reporter.Config, s.reporter.ID.Run, suite, err == nil, at), nil
		}
	}

	return "", nil
}

func markStopped(r *report.Reporter, evs []report.SliceEvent, flushed map[string]report.State, batch, batches, begun string) string {
	open, where := openSlices(evs, flushed)
	if len(open) == 0 {
		return ""
	}

	// Without a stamp nothing tells a time limit from a setup that failed.
	at := stopTimes{now: time.Now(), suite: readStarted(batches), batch: readStarted(begun)}
	if at.suite.IsZero() && at.batch.IsZero() {
		return ""
	}

	stop := batchStopped(r.Config, r.ID.Run, os.Getenv(envSuite), batch, where, at)

	for i := range evs {
		evs[i].Stopped = stop
	}

	return stop
}

func noneRan(n int, stop string) string {
	if stop != "" {
		return "none of its " + plural(n, "slice") + " ran: " + stop
	}

	return "its setup failed before any slice ran, so none of its " + plural(n, "slice") + " ran"
}

// Only a check's build log tells a dead task from a time limit.
func wholeStopped(cfg *config.Config, run, suite, name, result string) string {
	if _, err := os.Stat(result); !errors.Is(err, os.ErrNotExist) {
		return ""
	}

	return fmt.Sprintf("the whole-suite check %s stopped before it wrote its result; its time limit is %s: the build log says `timeout exceeded` when that was the cause; raise %s if the check needs longer",
		name, limitOf(cfg, run, suite), cfg.SuiteMinutesKey(run, suite))
}
