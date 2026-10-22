package report

import (
	"context"
	"errors"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Nowhere is the host of a repository that reports to none: a run's state
// lives in the store alone and the CI system's own job results are the
// report.
type Nowhere struct{}

// SetStatus keeps nothing.
func (Nowhere) SetStatus(context.Context, string, CommitStatus) error { return nil }

// Statuses has none.
func (Nowhere) Statuses(context.Context, string) ([]CommitStatus, error) { return nil, nil }

// Comments has none.
func (Nowhere) Comments(context.Context, int) ([]IssueComment, error) { return nil, nil }

// CreateComment keeps nothing.
func (Nowhere) CreateComment(context.Context, int, string) error { return nil }

// UpdateComment keeps nothing.
func (Nowhere) UpdateComment(context.Context, int64, string) error { return nil }

// DeleteComment keeps nothing.
func (Nowhere) DeleteComment(context.Context, int64) error { return nil }

// CommitSubject is unknown: the commit's id names it.
func (Nowhere) CommitSubject(context.Context, string) (string, error) { return "", nil }

// RunState is the state a run's status would show for one commit, read from
// its records in the store: success, failure, error or pending, or "" when
// the commit was never planned.
func RunState(ctx context.Context, store *gitstore.Store, cfg *config.Config, id ID) (string, error) {
	state, _, err := RunStatusOf(ctx, store, cfg, id)

	return state, err
}

// RunStatusOf is RunState with the line the run's status would carry.
func RunStatusOf(ctx context.Context, store *gitstore.Store, cfg *config.Config, id ID) (state, description string, err error) { //nolint:nonamedreturns // two strings, said apart
	if err := id.Check(); err != nil {
		return "", "", err
	}

	snap, err := readSnapshot(ctx, store, cfg, id)
	if errors.Is(err, ErrNoPlan) {
		return "", "", nil
	}

	if err != nil {
		return "", "", err
	}

	state, description = buildRun(id, snap, nil).RunStatus()

	return state, description, nil
}
