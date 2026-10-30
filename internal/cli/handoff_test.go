package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/secret"
)

func pushChange(t *testing.T, root, remote, branch, content string) string {
	t.Helper()

	dir := t.TempDir()
	gitIn := func(args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir

		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	gitIn("init", "-q")
	writeFile(t, filepath.Join(dir, "pipelinegen.yaml"), readFile(t, filepath.Join(root, "pipelinegen.yaml")))
	writeFile(t, filepath.Join(dir, "f.txt"), content)
	gitIn("add", "-A")
	gitIn("commit", "-q", "-m", content)
	gitIn("push", "-q", "-f", remote, "HEAD:refs/heads/"+branch)

	return gitIn("rev-parse", "HEAD")
}

func handOffFor(t *testing.T, ch runs.Change) string {
	t.Helper()

	dir := t.TempDir()
	if err := runs.WriteRunFile(dir, ch); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestAHandOffIsCheckedOutAside(t *testing.T) {
	root, _, s := runEnv(t)
	gitConnectorConfig(t, root, s.Remote)

	head := pushChange(t, root, s.Remote, "review/cart", "one")
	handOff := handOffFor(t, runs.Change{
		Run: "pr", ID: "git/branch-review%2Fcart", Commit: head, Branch: "review/cart", Base: "main", Connector: "git", Remote: s.Remote,
	})

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("PIPELINEGEN_READ_TOKEN", "a-read-token-of-the-step")

	dir, err := handOffCheckout(t.Context(), "pr", handOff, true)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(dir, tmp) || strings.Contains(dir, "cart") || gitOut(t, dir, "rev-parse", "HEAD") != head {
		t.Fatalf("checkout %s", dir)
	}

	if _, err := os.Stat(filepath.Join(dir, "pipelinegen.yaml")); err != nil {
		t.Fatalf("the change's config: %v", err)
	}

	if strings.Contains(readFile(t, filepath.Join(dir, ".git", "config")), "a-read-token-of-the-step") {
		t.Error("the checkout's config holds the token")
	}

	for _, kv := range secret.PluginEnv(nil) {
		if strings.Contains(kv, "a-read-token-of-the-step") {
			t.Errorf("a plugin gets %s", kv)
		}
	}

	closeScratch()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the checkout stayed: %v", err)
	}

	if dir, err := handOffCheckout(t.Context(), "pr", root, true); err != nil || dir != "" {
		t.Fatalf("a checkout: %q %v", dir, err)
	}
}

func TestASupersededChangeRunsNothing(t *testing.T) {
	root, _, s := runEnv(t)
	gitConnectorConfig(t, root, s.Remote)

	head := pushChange(t, root, s.Remote, "review/cart", "one")
	handOff := handOffFor(t, runs.Change{
		Run: "pr", ID: "git/branch-review%2Fcart", Commit: head, Branch: "review/cart", Base: "main", Connector: "git", Remote: s.Remote,
	})

	pushChange(t, root, s.Remote, "review/cart", "two")

	before, err := s.List(t.Context(), "refs/pipelinegen/")
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(envRun, "pr")

	stdout, stderr, code := run(t, "plan", "-run", "pr", "-trigger", handOff, "-out", t.TempDir(), "-cache", t.TempDir())
	if code != 0 || !strings.Contains(stdout, "superseded, not run: review/cart is at ") {
		t.Errorf("plan: %d %s %s", code, stdout, stderr)
	}

	gate := t.TempDir()

	stdout, stderr, code = run(t, "plan", "-run", "pr", "-trigger", handOff, "-from", t.TempDir(), "-gate", gate, "-cache", t.TempDir())
	if code != 0 || !strings.Contains(stdout, "superseded, not run") || strings.TrimSpace(readFile(t, filepath.Join(gate, "gate.json"))) != "[]" {
		t.Errorf("plan, second step: %d %s %s", code, stdout, stderr)
	}

	gate = t.TempDir()

	if stdout, stderr, code := run(t, "tree", "-run", "pr", "-suite", "e2e", "-trigger", handOff, "-out", t.TempDir(), "-gate", gate); code != 0 ||
		!strings.Contains(stdout, "superseded, not run") || strings.TrimSpace(readFile(t, filepath.Join(gate, "gate.json"))) != "[]" {
		t.Errorf("tree: %d %s %s", code, stdout, stderr)
	}

	if stdout, stderr, code := run(t, "report", "suite", "-root", handOff); code != 0 || !strings.Contains(stdout, "superseded, not run") {
		t.Errorf("report suite: %d %s %s", code, stdout, stderr)
	}

	after, err := s.List(t.Context(), "refs/pipelinegen/")
	if err != nil {
		t.Fatal(err)
	}

	if len(after) != len(before) {
		t.Fatalf("a superseded change wrote to the store: %d refs, then %d", len(before), len(after))
	}
}
