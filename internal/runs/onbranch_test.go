package runs

import (
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

func TestABranchsCommitMustBeOnTheBranch(t *testing.T) {
	cfg := &config.Config{Runs: map[string]config.Run{"nightly": {Trigger: config.Trigger{Branch: "main"}}}}

	repo := t.TempDir()
	gitRuns(t, repo, "init", "-q", "-b", "main")
	gitRuns(t, repo, "commit", "-q", "--allow-empty", "-m", "one")
	old := gitRuns(t, repo, "rev-parse", "HEAD")
	gitRuns(t, repo, "commit", "-q", "--allow-empty", "-m", "two")

	ch, err := FromTrigger(t.Context(), cfg, "nightly", repo)
	if err != nil || ch.Doubt != "" || ch.Proposed() {
		t.Fatalf("the branch's tip: %+v %v", ch, err)
	}

	gitRuns(t, repo, "checkout", "-q", "--detach", old)

	if ch, err := FromTrigger(t.Context(), cfg, "nightly", repo); err != nil || ch.Proposed() {
		t.Fatalf("an older commit: %+v %v", ch, err)
	}

	gitRuns(t, repo, "checkout", "-q", "--orphan", "elsewhere")
	gitRuns(t, repo, "commit", "-q", "--allow-empty", "-m", "rewritten")

	ch, err = FromTrigger(t.Context(), cfg, "nightly", repo)
	if err != nil || ch.Doubt != "this commit is no longer on main" || !ch.Proposed() {
		t.Fatalf("off the branch: %+v %v", ch, err)
	}

	other := t.TempDir()
	gitRuns(t, other, "init", "-q", "-b", "trunk")
	gitRuns(t, other, "commit", "-q", "--allow-empty", "-m", "one")

	ch, err = FromTrigger(t.Context(), cfg, "nightly", other)
	if err != nil || !ch.Proposed() || ch.Doubt == "" {
		t.Fatalf("no main to tell by: %+v %v", ch, err)
	}
}

func TestAShallowCheckoutOfTheTipIsTrusted(t *testing.T) {
	cfg := &config.Config{Runs: map[string]config.Run{"nightly": {Trigger: config.Trigger{Branch: "main"}}}}

	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "r.git")
	gitRuns(t, work, "init", "-q", "-b", "main")
	gitRuns(t, work, "commit", "-q", "--allow-empty", "-m", "one")
	old := gitRuns(t, work, "rev-parse", "HEAD")
	gitRuns(t, work, "commit", "-q", "--allow-empty", "-m", "two")
	gitRuns(t, work, "commit", "-q", "--allow-empty", "-m", "three")
	gitRuns(t, t.TempDir(), "clone", "-q", "--bare", work, bare)
	gitRuns(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")

	tip := filepath.Join(t.TempDir(), "tip")
	gitRuns(t, t.TempDir(), "clone", "-q", "--depth", "1", "--single-branch", "--branch", "main", "file://"+bare, tip)

	if ch, err := FromTrigger(t.Context(), cfg, "nightly", tip); err != nil || ch.Proposed() {
		t.Fatalf("the tip at depth 1: %+v %v", ch, err)
	}

	pinned := filepath.Join(t.TempDir(), "pinned")
	gitRuns(t, t.TempDir(), "clone", "-q", "--depth", "1", "--single-branch", "--branch", "main", "file://"+bare, pinned)
	gitRuns(t, pinned, "fetch", "-q", "--depth", "1", "origin", old)
	gitRuns(t, pinned, "checkout", "-q", "--detach", old)

	ch, err := FromTrigger(t.Context(), cfg, "nightly", pinned)
	if err != nil || !ch.Proposed() || ch.Doubt != "can't tell whether this commit is on main: the trigger's checkout is too shallow to relate them" {
		t.Fatalf("an old commit at depth 1: %+v %v", ch, err)
	}
}
