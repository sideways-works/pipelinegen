package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
)

// Head is the head of an open pull request.
type Head struct {
	PR int
	// Change is the head's change id when a connector listed it
	// (git/branch-review%2Fcart); empty, the pull request's pr-<PR>.
	Change  string
	Commit  string
	Branch  string
	Updated time.Time
}

// OpenHeads lists the heads of the open pull requests into a run's base
// that pass its filters and come from the repository itself: the pull
// request resource leaves forks out.
func OpenHeads(ctx context.Context, host Changes, pr *config.PullRequests) ([]Head, error) {
	changes, err := host.Changes(ctx, changesArgs(pr, false))
	if err != nil {
		return nil, err
	}

	var heads []Head

	for _, c := range changes {
		if filtered(c, pr, c.Head) != "" {
			continue
		}

		heads = append(heads, Head{PR: c.Number, Commit: c.Head, Branch: c.HeadBranch, Updated: c.Updated})
	}

	return heads, nil
}

// ID is the head's change id: the connector's, or pr-<number> for a pull
// request listed the way runs always listed them.
func (h Head) ID() string {
	if h.Change != "" {
		return h.Change
	}

	return "pr-" + strconv.Itoa(h.PR)
}

// Label is how messages name the head: #<number> for a pull request, its
// change id otherwise.
func (h Head) Label() string {
	if h.Change != "" {
		return h.Change
	}

	return "#" + strconv.Itoa(h.PR)
}

// Files writes into dir/.git/resource what the pull request resource
// leaves there for a head (version.json, metadata.json and head_name), so
// FromTrigger reads it as the resource's own checkout.
func (h Head) Files(dir string) error {
	res := filepath.Join(dir, ".git", "resource")
	if err := os.MkdirAll(res, 0o750); err != nil {
		return fmt.Errorf("head files: %w", err)
	}

	version, err := json.Marshal(map[string]string{"pr": strconv.Itoa(h.PR), "commit": h.Commit})
	if err != nil {
		return fmt.Errorf("head files: %w", err)
	}

	type field struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}

	metadata, err := json.Marshal([]field{{"pr", strconv.Itoa(h.PR)}, {"head_name", h.Branch}, {"head_sha", h.Commit}})
	if err != nil {
		return fmt.Errorf("head files: %w", err)
	}

	for name, data := range map[string][]byte{"version.json": version, "metadata.json": metadata, "head_name": []byte(h.Branch)} {
		if err := os.WriteFile(filepath.Join(res, name), data, 0o600); err != nil {
			return fmt.Errorf("head files: %w", err)
		}
	}

	return nil
}

// CheckoutHead makes dir a checkout of a pull request's head, fetched from
// remote with the token, with origin pointing there (planning fetches the
// base's tip from it) and the resource's files, as the pull request
// resource would leave it.
func CheckoutHead(ctx context.Context, dir, remote string, h Head, auth Auth) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("checkout of #%d: %w", h.PR, err)
	}

	steps := [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", remote},
	}

	for _, args := range steps {
		if _, err := git(ctx, dir, args...); err != nil {
			return err
		}
	}

	if _, err := gitEnv(ctx, dir, tokenEnv(auth), "fetch", "-q", "origin", fmt.Sprintf("refs/pull/%d/head", h.PR)); err != nil {
		return err
	}

	// the head may have moved on since the list: its commit, not the ref's
	if err := FetchCommit(ctx, dir, h.Commit, auth); err != nil {
		return err
	}

	if _, err := git(ctx, dir, "checkout", "-q", "--detach", h.Commit); err != nil {
		return err
	}

	return h.Files(dir)
}
