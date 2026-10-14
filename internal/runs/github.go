package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Statuses reads commit statuses from GitHub.
type Statuses struct {
	API        string
	Repository string
	Token      string
	Client     *http.Client
}

var errStatus = errors.New("commit status")

// State is the newest state of one status context on a commit, "" when the
// commit has none.
func (s *Statuses) State(ctx context.Context, commit, context string) (string, error) {
	state, _, err := s.Status(ctx, commit, context)

	return state, err
}

// Status is the newest state of one status context on a commit and its
// description, "" when the commit has none.
func (s *Statuses) Status(ctx context.Context, commit, context string) (state, description string, err error) {
	all, err := s.All(ctx, commit)
	if err != nil {
		return "", "", err
	}

	st := all[context]

	return st.State, st.Description, nil
}

// Status is one status context's newest state on a commit.
type Status struct {
	State, Description string
}

// All is the newest status of every context on a commit, by context; none
// when the commit has none.
func (s *Statuses) All(ctx context.Context, commit string) (map[string]Status, error) {
	api := strings.TrimRight(s.API, "/")
	if api == "" {
		api = "https://api.github.com"
	}

	u := fmt.Sprintf("%s/repos/%s/commits/%s/status?per_page=100", api, s.Repository, url.PathEscape(commit))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errStatus, err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")

	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errStatus, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity {
		return map[string]Status{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s: %s", errStatus, short(commit), resp.Status)
	}

	var combined struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			Description string `json:"description"`
		} `json:"statuses"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&combined); err != nil {
		return nil, fmt.Errorf("%w: %w", errStatus, err)
	}

	// the combined status lists each context once, its newest state
	out := make(map[string]Status, len(combined.Statuses))

	for _, st := range combined.Statuses {
		out[st.Context] = Status{State: st.State, Description: st.Description}
	}

	return out, nil
}

// FirstParents lists up to n commits before commit along its first
// parents, newest first; none for a first commit.
func FirstParents(ctx context.Context, repo, commit string, n int) ([]string, error) {
	// a first commit has nothing before it
	if _, err := git(ctx, repo, "rev-parse", "--verify", "-q", commit+"^"); err != nil {
		return nil, nil //nolint:nilerr // no parent is no base, not a failure
	}

	out, err := git(ctx, repo, "rev-list", "--first-parent", "-n", strconv.Itoa(n), commit+"^")
	if err != nil {
		return nil, err
	}

	return strings.Fields(out), nil
}
