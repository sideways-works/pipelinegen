// Package runs knows what a build of the generated pipeline is about.
package runs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/config"

	"github.com/sideways-works/pipelinegen/internal/secret"
)

// Change is what a build tests.
type Change struct {
	Run       string `json:"run"`
	ID        string `json:"change"`
	Commit    string `json:"commit"`
	PR        int    `json:"pr"`
	Branch    string `json:"branch"`
	Base      string `json:"base,omitempty"`
	Connector string `json:"connector,omitempty"`
	// Remote is the code's remote the change is fetched from, written by
	// the trusted sweep: never read from the change's own tree.
	Remote string `json:"remote,omitempty"`
	// Doubt says why a branch's commit isn't trusted as the branch's: it
	// is no longer on it, or the checkout can't tell.
	Doubt string `json:"doubt,omitempty"`
	// Handed is when the sweep handed the change on, with a random part.
	// Nothing reads it: it exists only so that the same head handed on
	// again is a new version of the CI system's resource, which would
	// fold an identical one into the old.
	Handed string `json:"handed,omitempty"`
}

// Proposed tells whether the change is proposed, its code not yet
// trusted: a pull request, or a change a connector listed. Such a change
// never writes what a trusted plan reads.
func (c Change) Proposed() bool { return c.PR != 0 || c.Connector != "" || c.Doubt != "" }

// ErrTrigger wraps a trigger directory that can't be read.
var ErrTrigger = errors.New("trigger")

// ErrMoved is a trigger the config reads as something it isn't: a
// proposed change whose config says its run is a branch's, or the
// reverse.
var ErrMoved = errors.New("a change may not move where its run reads or writes")

const (
	formCheckout    = "a branch's checkout"
	formPullRequest = "a pull request's"
	formHandOff     = "a connector's hand-off"
)

func triggerForm(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".git", "resource", "version.json")); err == nil {
		return formPullRequest
	}

	_, runFile := os.Stat(filepath.Join(dir, RunFile))
	if _, git := os.Stat(filepath.Join(dir, ".git")); runFile == nil && git != nil {
		return formHandOff
	}

	return formCheckout
}

// FromTrigger reads what a build's trigger input stands for: a pull request
// resource's head (its .git/resource files), a connector's hand-off, or a
// branch's checkout. The trigger's form decides, never the config: the
// config may be the proposed change's own, and a change that says its run
// is a branch's is still a proposed change, refused rather than trusted.
func FromTrigger(ctx context.Context, cfg *config.Config, run, dir string) (Change, error) {
	r, ok := cfg.Runs[run]
	if !ok {
		return Change{}, fmt.Errorf("%w: no run %q in the config", ErrTrigger, run)
	}

	pr := r.Trigger.PullRequests
	form := triggerForm(dir)

	want := formCheckout

	switch {
	case pr != nil && cfg.ChangesFromConnector():
		want = formHandOff
	case pr != nil:
		want = formPullRequest
	}

	if form != want {
		return Change{}, fmt.Errorf("%w: the trigger is %s, and run %s of this config takes %s", ErrMoved, form, run, takes(want))
	}

	switch form {
	case formHandOff:
		return fromHandOff(run, dir)
	case formPullRequest:
		return fromPullRequest(run, pr.Base, dir)
	}

	commit, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return Change{}, fmt.Errorf("%w: %w", ErrTrigger, err)
	}

	commit = strings.TrimSpace(commit)

	return Change{
		Run: run, ID: r.Trigger.Branch, Commit: commit, Branch: r.Trigger.Branch,
		Doubt: onBranch(ctx, dir, r.Trigger.Branch, commit),
	}, nil
}

func takes(form string) string {
	switch form {
	case formPullRequest:
		return "pull requests"
	case formHandOff:
		return "a connector's changes"
	}

	return "a branch's commits"
}

// onBranch says why a branch's checkout of commit can't be trusted as the
// branch's, "" when it can: the commit is no longer on the branch as
// fetched, or the checkout holds no branch to tell by. Such a run is planned
// as a proposed change.
func onBranch(ctx context.Context, dir, branch, commit string) string {
	if checkBranch(ctx, branch) != nil {
		return fmt.Sprintf("can't tell whether this commit is on %q", branch)
	}

	for _, ref := range []string{"refs/heads/" + branch, "refs/remotes/origin/" + branch} {
		if _, err := git(ctx, dir, "rev-parse", "--verify", "-q", "--end-of-options", ref+"^{commit}"); err != nil {
			continue
		}

		if _, err := git(ctx, dir, "merge-base", "--is-ancestor", commit, ref); err != nil {
			if shallow, _ := git(ctx, dir, "rev-parse", "--is-shallow-repository"); strings.TrimSpace(shallow) == "true" { //nolint:errcheck // unknown is not shallow
				return "can't tell whether this commit is on " + branch + ": the trigger's checkout is too shallow to relate them"
			}

			return "this commit is no longer on " + branch
		}

		return ""
	}

	return "can't tell whether this commit is on " + branch + ": the trigger's checkout holds no " + branch
}

func fromHandOff(run, dir string) (Change, error) {
	p := filepath.Join(dir, RunFile)

	ch, err := ReadRunFile(p)
	if err != nil {
		return Change{}, fmt.Errorf("%w: %w", ErrTrigger, err)
	}

	if ch.Run != run {
		return Change{}, fmt.Errorf("%w: %s is for run %q, not %q", ErrTrigger, p, ch.Run, run)
	}

	return ch, nil
}

// fromPullRequest reads the files the pull request resource leaves in
// .git/resource: version.json ({"pr", "commit"}) and head_name.
func fromPullRequest(run, base, dir string) (Change, error) {
	res := filepath.Join(dir, ".git", "resource")

	data, err := os.ReadFile(filepath.Join(res, "version.json")) //nolint:gosec // the trigger's own files
	if err != nil {
		return Change{}, fmt.Errorf("%w: %w", ErrTrigger, err)
	}

	var v struct {
		PR     string `json:"pr"`
		Commit string `json:"commit"`
	}

	if err := json.Unmarshal(data, &v); err != nil {
		return Change{}, fmt.Errorf("%w: version.json: %w", ErrTrigger, err)
	}

	n, err := strconv.Atoi(v.PR)
	if err != nil || n <= 0 || v.Commit == "" {
		return Change{}, fmt.Errorf("%w: version.json holds no pull request and commit", ErrTrigger)
	}

	head, err := os.ReadFile(filepath.Join(res, "head_name")) //nolint:gosec // the trigger's own files
	if err != nil {
		return Change{}, fmt.Errorf("%w: %w", ErrTrigger, err)
	}

	return Change{
		Run: run, ID: "pr-" + v.PR, Commit: v.Commit, PR: n,
		Branch: strings.TrimSpace(string(head)), Base: base,
	}, nil
}

// RunFile is the name of the file that tells the steps after the tree step
// which run and change the build is about.
const RunFile = "run.json"

// WriteRunFile writes the change into dir.
func WriteRunFile(dir string, ch Change) error {
	data, err := json.MarshalIndent(ch, "", "  ")
	if err != nil {
		return fmt.Errorf("run file: %w", err)
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("run file: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, RunFile), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("run file: %w", err)
	}

	return nil
}

// ReadRunFile reads the change a step's run file holds.
func ReadRunFile(path string) (Change, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a file the pipeline's own steps wrote
	if err != nil {
		return Change{}, fmt.Errorf("run file: %w", err)
	}

	var ch Change
	if err := json.Unmarshal(data, &ch); err != nil {
		return Change{}, fmt.Errorf("run file %s: %w", path, err)
	}

	if ch.Run == "" || ch.ID == "" || ch.Commit == "" {
		return Change{}, fmt.Errorf("%w: %s: no run, change or commit", errRunFile, path)
	}

	return ch, nil
}

var (
	errGit     = errors.New("git")
	errRunFile = errors.New("run file")
)

func gitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // git with pipelinegen's own arguments
	cmd.Dir = dir

	cmd.Env = secret.GitEnv(env...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w %s: %w: %s", errGit, strings.Join(args, " "), err, secret.Redact(strings.TrimSpace(stderr.String())))
	}

	return strings.TrimSpace(stdout.String()), nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitEnv(ctx, dir, nil, args...)
}
