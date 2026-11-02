package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestEachRepositoryPlansInACacheOfItsOwn(t *testing.T) {
	a, b := testrepo.New(t), testrepo.New(t)

	ca, cb := cacheOf(t.Context(), defaultCache(), a.Dir), cacheOf(t.Context(), defaultCache(), b.Dir)
	if ca == cb || !strings.HasPrefix(ca, filepath.Join(defaultCache(), "repos")+string(filepath.Separator)) {
		t.Fatalf("two repositories: %s and %s", ca, cb)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	a.Git("worktree", "add", "-q", "--detach", wt)

	if got := cacheOf(t.Context(), defaultCache(), wt); got != ca {
		t.Fatalf("a worktree: %s, want %s", got, ca)
	}

	if got := cacheOf(t.Context(), "/given", a.Dir); got != "/given" {
		t.Fatalf("a named cache: %s", got)
	}
}
