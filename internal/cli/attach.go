package cli

import (
	"context"
	"errors"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/report"
)

var errNoCheckout = errors.New("no checkout of the commit's history to find its ancestors' lists in")

func attachRun(ctx context.Context, r *report.Reporter, end report.Ending) error {
	if r.Checkout == "" {
		return errNoCheckout
	}

	recs, marks, err := runRecordings(ctx, r.Store, r.ID)
	if err != nil {
		return err
	}

	// Judge each recording by its own slice's state: a slice the plan doesn't name (a plan rewritten by a
	// later batch) may have failed unseen.
	passed, err := passedSlices(ctx, r.Store, r.ID)
	if err != nil {
		return err
	}

	shrink, whole := judged(recs, passed, end.All)
	told := passedMarks(marks, passed)

	got, err := lists.Attach{
		Repo: absRoot(r.Checkout), Run: r.ID.Run, Change: r.ID.Change, Commit: r.ID.Commit, To: r.ID.Commit,
		Recordings: recs, CannotTell: told, Shrinkable: shrink, Whole: whole,
		SuitesPassed: end.Suites, Walk: r.Config.ListsCommits(), Trusted: attachTrusted(r.Config, r.ID),
	}.Do(ctx, r.Store)
	if err != nil {
		return err
	}

	if !got.Again && r.Warn != nil {
		warnf(r.Warn, "%s\n", attachedText(got, r.ID.Run+" "+r.ID.Change+" "+short12(r.ID.Commit)))
	}

	return nil
}

// A failed slice's recording can stop half way, so it never shrinks a list nor makes one.
func judged(recs recording.Lists, passed map[string]bool, all bool) (shrink, whole map[string]bool) { //nolint:nonamedreturns // two sets, said apart
	shrink, whole = map[string]bool{}, map[string]bool{}

	for k := range recs {
		if !passed[k] {
			continue
		}

		whole[k] = true

		if all {
			shrink[k] = true
		}
	}

	return shrink, whole
}

// A failed slice keeps its older list or mark.
func passedMarks(marks lists.Marks, passed map[string]bool) lists.Marks {
	out := lists.Marks{}

	for k, ms := range marks {
		if passed[k] {
			out[k] = ms
		}
	}

	return out
}

func attachTrusted(cfg *config.Config, id report.ID) bool {
	return !id.Proposed && trustedRun(cfg, id.Run, id.Change)
}
