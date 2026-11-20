package runs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRuns(t *testing.T, dir string, args ...string) string {
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

func pushed(t *testing.T, bare, branch, content string) string {
	t.Helper()

	dir := t.TempDir()
	gitRuns(t, dir, "init", "-q")

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	gitRuns(t, dir, "add", "f.txt")
	gitRuns(t, dir, "commit", "-q", "-m", content)
	gitRuns(t, dir, "push", "-q", "-f", bare, "HEAD:refs/heads/"+branch)

	return gitRuns(t, dir, "rev-parse", "HEAD")
}

func TestCheckoutChange(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "r.git")
	gitRuns(t, t.TempDir(), "init", "-q", "--bare", bare)

	head := pushed(t, bare, "review/cart", "one")
	ch := Change{Run: "pr", ID: "git/branch-review%2Fcart", Commit: head, Branch: "review/cart", Forge: "git", Remote: "file://" + bare}
	auth := Auth{Scope: "file://" + bare, Token: "a-read-token-for-the-checkout"}

	dir := filepath.Join(t.TempDir(), "change")
	if err := CheckoutChange(t.Context(), dir, ch, auth); err != nil {
		t.Fatal(err)
	}

	if got := gitRuns(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("checked out %s, want %s", got, head)
	}

	cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{auth.Token, "extraheader", "extraHeader", "helper", "Authorization"} {
		if strings.Contains(string(cfg), bad) {
			t.Errorf("the checkout's config holds %q:\n%s", bad, cfg)
		}
	}

	pushed(t, bare, "review/cart", "two")

	err = CheckoutChange(t.Context(), filepath.Join(t.TempDir(), "again"), ch, auth)
	if !errors.Is(err, ErrSuperseded) || !strings.Contains(err.Error(), "review/cart is at ") {
		t.Fatalf("after a push: %v", err)
	}
}

func TestCheckoutChangeRefusesHostileBranches(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "r.git")
	gitRuns(t, t.TempDir(), "init", "-q", "--bare", bare)

	for _, branch := range []string{"-upload-pack=touch x", "--help", "../escape", "a\nb", "review/..x", "", "HEAD@{1}", "with space"} {
		ch := Change{Run: "pr", ID: "x", Commit: "0123456789abcdef0123456789abcdef01234567", Branch: branch, Forge: "git", Remote: "file://" + bare}

		if err := CheckoutChange(t.Context(), filepath.Join(t.TempDir(), "c"), ch, Auth{}); !errors.Is(err, errBadBranch) {
			t.Errorf("%q: %v", branch, err)
		}
	}
}
