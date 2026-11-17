package report

import (
	"context"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// A run whose only gap is slices whose batches found the worker short of memory says so at a suite's
// end, in a record the sweep judges by its name and time alone: newer than every suite build's start
// and end, it is the last word on the run, which the sweep then hands on again.
const shortFile = "short.json"

// ShortRef marks a run left with only slices not run for want of memory.
func (id ID) ShortRef() string { return id.Prefix() + "_short" }

// ShortOnly tells a run every slice of which ended, and passed but those whose batches never started
// for want of memory, of which there is one at least. A slice that failed keeps the run from being
// handed on again, so no failing test runs again unasked.
func ShortOnly(run Run) bool {
	short := false

	for _, s := range run.Suites {
		for _, sl := range s.Slices {
			switch {
			case sl.ShortOfMemory() || len(sl.Failures) > 0 && sl.Failures[0].Test == OOMTest:
				short = true
			case sl.State != Passed:
				return false
			}
		}
	}

	return short
}

// markShort writes the run's mark when ShortOnly holds; a write that fails only warns, as the states
// already say what didn't run.
func (r *Reporter) markShort(ctx context.Context, run Run) {
	if !ShortOnly(run) {
		return
	}

	data, err := encode(struct {
		At time.Time `json:"at"`
	}{r.now()})
	if err == nil {
		_, err = r.Store.Write(ctx, r.ID.Prefix()+" short of memory", gitstore.Update{Ref: r.ID.ShortRef(), Any: true, Files: map[string][]byte{shortFile: data}})
	}

	if err != nil {
		r.warnf("marking the run's slices not run for want of memory, for the sweep: %v", err)
	}
}
