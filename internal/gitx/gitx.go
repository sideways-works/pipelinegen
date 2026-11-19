// Package gitx runs the few git commands pipelinegen needs.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/progress"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

type Repo struct {
	Dir string
}

var ErrGit = errors.New("git")

func (r Repo) Git(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, args...)
}

// run runs git and returns its stdout; an exit status 1 is returned as exitOne, for the commands
// that answer yes or no with it.
func (r Repo) run(ctx context.Context, args ...string) (string, error) {
	defer progress.Git(ctx, args)()

	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with pipelinegen's own arguments
	cmd.Dir = r.Dir
	cmd.Env = secret.HardenGit(os.Environ())

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
			return "", errExitOne
		}

		return "", fmt.Errorf("%w %s: %w: %s", ErrGit, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}

var errExitOne = errors.New("exit status 1")

func (r Repo) owns(ctx context.Context, dir string) bool {
	theirs, err := Repo{Dir: dir}.run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false
	}

	ours, err := r.run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")

	return err == nil && strings.TrimSpace(theirs) == strings.TrimSpace(ours)
}

// Show returns a file's content at a commit, and false when the commit has no such file.
func (r Repo) Show(ctx context.Context, commit, path string) ([]byte, bool, error) {
	if _, err := r.run(ctx, "cat-file", "-e", commit+":"+path); err != nil {
		if errors.Is(err, errExitOne) || strings.Contains(err.Error(), "exit status 128") {
			return nil, false, nil
		}

		return nil, false, err
	}

	out, err := r.run(ctx, "cat-file", "blob", commit+":"+path)
	if err != nil {
		return nil, false, err
	}

	return []byte(out), true, nil
}

// Resolve returns the full commit id of rev, and false when there is no such commit.
func (r Repo) Resolve(ctx context.Context, rev string) (string, bool, error) {
	out, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	if errors.Is(err, errExitOne) {
		return "", false, nil
	}

	if err != nil {
		return "", false, err
	}

	return strings.TrimSpace(out), true, nil
}

// IsAncestor tells whether a is an ancestor of b (or b itself).
func (r Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := r.run(ctx, "merge-base", "--is-ancestor", a, b)

	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, errExitOne):
		return false, nil
	default:
		return false, err
	}
}

// Change is one changed path between two commits; a rename shows as the old path deleted and the
// new one added.
type Change struct {
	Path string
	// Status is git's letter: A added, M modified, D deleted, T type changed.
	Status string
}

func (r Repo) Diff(ctx context.Context, base, head string) ([]Change, error) {
	out, err := r.run(ctx, "diff", "--name-status", "--no-renames", "-z", base, head, "--")
	if err != nil {
		return nil, err
	}

	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields) == 1 && fields[0] == "" {
		return nil, nil
	}

	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("%w: diff output has an odd number of fields", ErrGit)
	}

	changes := make([]Change, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		changes = append(changes, Change{Status: fields[i], Path: fields[i+1]})
	}

	return changes, nil
}

// Checkout puts commit into dir, a worktree of r reused across calls so per-directory state
// (Bazel's output base) persists; untracked files are removed except what .gitignore names.
func (r Repo) Checkout(ctx context.Context, dir, commit string) error {
	// a work tree kept in a cache may belong to a checkout that is gone (a CI system mounts each
	// build's input at a new path): start it afresh
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil && !r.owns(ctx, dir) {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("checkout: %w", err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
			return fmt.Errorf("checkout: %w", err)
		}

		// a worktree whose directory is gone is pruned first, or git refuses
		if _, err := r.run(ctx, "worktree", "prune"); err != nil {
			return err
		}

		_, err := r.run(ctx, "worktree", "add", "--force", "--detach", dir, commit)

		return err
	}

	tree := Repo{Dir: dir}
	if _, err := tree.run(ctx, "checkout", "--quiet", "--force", "--detach", commit); err != nil {
		return err
	}

	_, err := tree.run(ctx, "clean", "--quiet", "-d", "--force")

	return err
}
