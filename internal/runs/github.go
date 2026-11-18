package runs

import (
	"context"
	"strconv"
	"strings"
)

// Status is one status context's newest state on a commit.
type Status struct {
	State, Description string
}

// FirstParents lists up to n commits before commit along its first
// parents, newest first; none for a first commit.
func FirstParents(ctx context.Context, repo, commit string, n int) ([]string, error) {
	if _, err := git(ctx, repo, "rev-parse", "--verify", "-q", commit+"^"); err != nil {
		return nil, nil //nolint:nilerr // no parent is no base, not a failure
	}

	out, err := git(ctx, repo, "rev-list", "--first-parent", "-n", strconv.Itoa(n), commit+"^")
	if err != nil {
		return nil, err
	}

	return strings.Fields(out), nil
}
