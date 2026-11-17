package report

import (
	"context"
	"time"
)

// GitHub is the host a run reports through: its code connector's
// statuses, comment and subject calls.
type GitHub interface {
	// SetStatus sets one commit status.
	SetStatus(ctx context.Context, sha string, st CommitStatus) error
	// Statuses lists a commit's newest status per context.
	Statuses(ctx context.Context, sha string) ([]CommitStatus, error)
	// Comment keeps one comment on a pull request: the one that starts
	// with marker, made when there is none, its body replaced when it
	// differs, and any other that starts with marker removed.
	Comment(ctx context.Context, pr int, marker, body string) error
	// CommitSubject is the first line of a commit's message.
	CommitSubject(ctx context.Context, sha string) (string, error)
}

// CommitStatus is one status on a commit.
type CommitStatus struct {
	State       string     `json:"state"`
	Context     string     `json:"context"`
	Description string     `json:"description"`
	TargetURL   string     `json:"target_url,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}
