package runs

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchesListsTheRemotesHeads(t *testing.T) {
	git := func(dir string, args ...string) string {
		t.Helper()

		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	work, bare := t.TempDir(), filepath.Join(t.TempDir(), "r.git")
	git(work, "init", "-q", "-b", "main")
	git(work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "one")
	git(work, "init", "-q", "--bare", bare)
	git(work, "checkout", "-q", "-b", "review/a")
	git(work, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two")
	git(work, "push", "-q", bare, "--all")
	git(work, "push", "-q", bare, "HEAD:refs/tags/v1")

	got, err := Branches(t.Context(), "file://"+bare, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got["main"] != git(work, "rev-parse", "main") || got["review/a"] != git(work, "rev-parse", "review/a") {
		t.Fatalf("%v", got)
	}

	if _, err := Branches(t.Context(), "file://"+filepath.Join(t.TempDir(), "none.git"), nil); err == nil {
		t.Fatal("an unreachable remote listed")
	}
}
