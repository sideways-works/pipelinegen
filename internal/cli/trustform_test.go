package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

var asBranchRun = strings.NewReplacer(
	"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {branch: main}",
	"compare: merged_target", "compare: last_passed",
	"\n    report: {comment: {format: list}}", "",
)

func TestAPullRequestCannotMakeItsRunTrusted(t *testing.T) {
	root, s, trigger, ch, _ := proposedChange(t)

	writeFile(t, filepath.Join(trigger, "pipelinegen.yaml"), asBranchRun.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml"))))

	before, err := s.List(t.Context(), "refs/pipelinegen/")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(envTrigger, trigger)
	t.Setenv(envChange, "")
	t.Setenv(envCommit, "")

	out := t.TempDir()

	for name, args := range map[string][]string{
		"plan, first step":  {"plan", "-run", "pr", "-trigger", trigger, "-out", out, "-cache", t.TempDir()},
		"plan, second step": {"plan", "-run", "pr", "-trigger", trigger, "-from", out, "-cache", t.TempDir()},
		"plan, one step":    {"plan", "-run", "pr", "-trigger", trigger, "-cache", t.TempDir()},
		"tree":              {"tree", "-run", "pr", "-suite", "e2e", "-trigger", trigger, "-out", t.TempDir(), "-gate", t.TempDir()},
		"report suite":      {"report", "suite", "-root", trigger},
	} {
		_, stderr, code := run(t, args...)
		if code == 0 || !strings.Contains(stderr, runs.ErrMoved.Error()) {
			t.Errorf("%s: %d %s", name, code, stderr)
		}
	}

	after, err := s.List(t.Context(), "refs/pipelinegen/")
	if err != nil {
		t.Fatal(err)
	}

	if len(after) != len(before) {
		t.Fatalf("the steps wrote to the store: %d refs before, %d after", len(before), len(after))
	}

	cfg := loadConfig(t, root)
	if got, err := runs.FromTrigger(t.Context(), cfg, "pr", trigger); err != nil || got.PR != ch.PR || trustedRun(cfg, got.Run, got.ID) {
		t.Fatalf("read as the repository's own config says: %+v %v", got, err)
	}
}

func TestNoSpellingMakesAProposedRunTrusted(t *testing.T) {
	root, s, trigger, pr, _ := proposedChange(t)
	base := readFile(t, filepath.Join(root, "pipelinegen.yaml"))

	t.Cleanup(func() { _ = s.Close() }) //nolint:errcheck // the test's scratch

	headAsBranch, err := config.Parse([]byte(base + "\n"))
	if err != nil {
		t.Fatal(err)
	}

	headAsBranch.Concourse.Branch = "topic-7"

	ch, err := runs.FromTrigger(t.Context(), headAsBranch, "pr", trigger)
	if err != nil || ch.PR != pr.PR || trustedRun(headAsBranch, ch.Run, ch.ID) || trustedRun(headAsBranch, ch.Run, ch.Branch) {
		t.Fatalf("head as the default branch: %+v %v", ch, err)
	}

	renamed := strings.Replace(base, "\n  pr:\n", "\n  nightly:\n", 1)
	if renamed == base {
		t.Fatal("nothing renamed")
	}

	cfg, err := config.Parse([]byte(renamed))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := runs.FromTrigger(t.Context(), cfg, "pr", trigger); err == nil {
		t.Fatal("a config without the step's run was read")
	}

	branch := t.TempDir()
	gitT(t, branch, "init", "-q", "-b", "main")
	gitT(t, branch, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "one")

	if _, err := runs.FromTrigger(t.Context(), loadConfig(t, root), "pr", branch); err == nil || !strings.Contains(err.Error(), runs.ErrMoved.Error()) {
		t.Fatalf("a branch's checkout as a pull request: %v", err)
	}
}
