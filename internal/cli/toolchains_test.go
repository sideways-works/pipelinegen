package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestThePlanningTreeTakesTheRepositorysToolchain(t *testing.T) {
	top := t.TempDir()
	repo := filepath.Join(top, "work", "repo")
	cache := filepath.Join(t.TempDir(), "cache")

	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(top, "work", ".tool-versions"), []byte("golang 1.27.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pinPlanningToolchains(repo, cache)

	if got, err := os.ReadFile(filepath.Join(cache, ".tool-versions")); err != nil || string(got) != "golang 1.27.1\n" {
		t.Fatalf("pinned %q (%v)", got, err)
	}

	if err := os.WriteFile(filepath.Join(repo, ".tool-versions"), []byte("golang 1.28.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	pinPlanningToolchains(repo, cache)

	if got, err := os.ReadFile(filepath.Join(cache, ".tool-versions")); err != nil || string(got) != "golang 1.28.0\n" {
		t.Fatalf("after a change %q (%v)", got, err)
	}
}
