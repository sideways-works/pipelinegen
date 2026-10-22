package report

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

var errAttach = errors.New("lists not attached")

// Attacher hangs a run's recordings on its commit's lists once the run is
// over, each slice judged by its own state. The lists package does it;
// report only decides when, so it doesn't depend on learn.
type Attacher func(ctx context.Context, r *Reporter, end Ending) error

// Ending is what an Attacher is told of the run it attaches.
type Ending struct {
	// All says the run selected every slice: a slice that passed in it may
	// replace its list.
	All          bool
	Passed, Some bool
	// Suites are the suites every selected slice of which passed: only their
	// setup lists may shrink.
	Suites map[string]bool
}

// attaches tells whether this run hangs its recordings on its commit when it
// passes. A pull request's run is attached by the merge, to the commit the
// merge makes.
func (r *Reporter) attaches() bool {
	rc := r.runConfig()

	return rc.Record && rc.Trigger.PullRequests == nil
}

// attach hangs the run's recordings on its commit; attaching again is
// harmless, so every suite job that sees the run end may try.
func (r *Reporter) attach(ctx context.Context, end Ending) error {
	if r.Attach == nil {
		return fmt.Errorf("%w: this pipelinegen can't attach lists", errAttach)
	}

	if err := r.Attach(ctx, r, end); err != nil {
		return fmt.Errorf("%w: %w", errAttach, err)
	}

	return nil
}

func statusText(s string) string {
	if len(s) <= maxDescription {
		return s
	}

	cut := s[:maxDescription-3]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}

	return cut + "..."
}

func endingOf(run Run, all bool) Ending {
	end := Ending{All: all, Passed: passed(run, nil), Suites: map[string]bool{}}

	for _, s := range run.Suites {
		if passed(run, []string{s.Name}) {
			end.Suites[s.Name] = true
		}

		if slices.ContainsFunc(s.Slices, func(sl Slice) bool { return sl.State == Passed }) {
			end.Some = true
		}
	}

	return end
}

// RunEnding is what an Attacher is told of the run of id as its records
// stand.
func RunEnding(ctx context.Context, store *gitstore.Store, cfg *config.Config, id ID) (Ending, error) {
	if err := id.Check(); err != nil {
		return Ending{}, err
	}

	snap, err := readSnapshot(ctx, store, cfg, id)
	if err != nil {
		return Ending{}, err
	}

	return endingOf(buildRun(id, snap, nil), snap.plan.All), nil
}
