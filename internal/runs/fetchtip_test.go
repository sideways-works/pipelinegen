package runs

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestFetchTipReadsTheRemoteNotFetchHead(t *testing.T) {
	remote := testrepo.New(t)
	first := remote.Git("rev-parse", "HEAD")
	remote.Git("branch", "other")
	remote.Write("more.txt", "more\n")
	tip := remote.Commit("more")
	// a branch whose name ends like main's: ls-remote's pattern matches it
	// too, and it must not count
	remote.Git("branch", "x/refs/heads/main", first)

	checkout := filepath.Join(t.TempDir(), "checkout")
	runCmd(t, "", "git", "clone", "-q", "--branch", "other", remote.Dir, checkout)

	// another step's fetch in the same checkout left a FETCH_HEAD naming
	// another commit, and keeps it there
	runCmd(t, checkout, "git", "fetch", "-q", "origin", "other")

	fetchHead := filepath.Join(checkout, ".git", "FETCH_HEAD")
	if err := os.Chmod(fetchHead, 0o400); err != nil {
		t.Fatal(err)
	}

	got, err := FetchTip(t.Context(), checkout, "main", Auth{})
	if err != nil {
		t.Fatal(err)
	}

	if got != tip {
		t.Fatalf("tip %s, want %s", got, tip)
	}

	if out := runCmd(t, checkout, "git", "cat-file", "-t", tip); out != "commit" {
		t.Fatalf("the tip isn't in the checkout: %s", out)
	}

	if data, err := os.ReadFile(fetchHead); err != nil || !strings.HasPrefix(string(data), first) {
		t.Fatalf("FETCH_HEAD changed: %q, %v", data, err)
	}

	if _, err := FetchTip(t.Context(), checkout, "gone", Auth{}); !errors.Is(err, ErrNoBranch) {
		t.Fatalf("a missing branch: %v, want ErrNoBranch", err)
	}
}

func runCmd(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), name, args...)
	cmd.Dir = dir

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}

	return strings.TrimSpace(string(out))
}
