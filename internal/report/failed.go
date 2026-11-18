package report

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

// PlanFailed gives every suite the run lists, and the run, an error status saying why, so none is left
// pending; it is masked, since an error may quote a plugin's or a tool's output.
func (r *Reporter) PlanFailed(ctx context.Context, cause error) error {
	if err := r.ID.Check(); err != nil {
		return err
	}

	why := oneLine(secret.Redact(cause.Error()))
	desc := statusText("planning failed: " + why)

	var statuses []CommitStatus

	if r.runConfig().Report.Statuses() {
		suites := make([]string, 0, len(r.runConfig().Suites))
		for name := range r.runConfig().Suites {
			suites = append(suites, name)
		}

		slices.Sort(suites)

		for _, s := range suites {
			statuses = append(statuses, CommitStatus{State: statusError, Context: Context(r.Config, r.ID.Run, s), Description: desc, TargetURL: r.URL})
		}

		statuses = append(statuses, CommitStatus{State: statusError, Context: RunContext(r.Config, r.ID.Run), Description: desc, TargetURL: r.URL})
	}

	body := ""

	if cm := r.runConfig().Report.Comment; cm != nil && r.ID.PR > 0 {
		var b strings.Builder

		fmt.Fprintf(&b, "%s\n**%s** run of `%s`: planning failed, so no slice ran.\n\n", Marker(r.ID.Run, r.ID.PR), r.ID.Run, short(r.ID.Commit))
		fmt.Fprintf(&b, "```\n%s\n```\n", strings.ReplaceAll(secret.Redact(cause.Error()), "```", "'''"))
		body = b.String()
	}

	alsoTo, err := readAlsoTo(ctx, r.Store, r.ID)
	if err != nil {
		r.warnf("more pull requests for the comment: %v", err)
	}

	return r.show(ctx, scope{all: true, run: true}, statuses, body, alsoTo)
}

func readAlsoTo(ctx context.Context, store *gitstore.Store, id ID) ([]int, error) {
	ref, ok, err := store.First(ctx, []string{AlsoToRef(id.Run, id.Change)})
	if err != nil || !ok {
		return nil, err
	}

	data, _, err := store.File(ctx, ref.Commit, alsoToFile)
	if err != nil {
		return nil, err
	}

	var a alsoTo
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", AlsoToRef(id.Run, id.Change), err)
	}

	return a.AlsoTo, nil
}

// CheckPlan tells whether the plan the run's steps report under exists, lest a step lose its
// recording; a store that can't be read is an error, never "no plan".
func (r *Reporter) CheckPlan(ctx context.Context) error {
	refs, err := r.Store.List(ctx, PlansPrefix(r.ID.Run, r.ID.Change))
	if err != nil {
		return fmt.Errorf("the run's plans can't be read: %w", err)
	}

	if _, ok := refs[r.ID.PlanRef()]; !ok {
		return fmt.Errorf("%w: %s is missing", ErrNoPlan, r.ID.PlanRef())
	}

	return nil
}
