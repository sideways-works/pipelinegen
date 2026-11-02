package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

func TestMainlineIsTheDefaultBranchsFirstParents(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := checkout(t, root, 3)
	cfg := &config.Config{Concourse: config.Concourse{Branch: "main"}}

	want := slices.Clone(c)
	slices.Reverse(want)

	if got := mainline(t.Context(), cfg, runs.Change{}, root); !slices.Equal(got, want) {
		t.Errorf("from the branch's ref: %v, want %v", got, want)
	}

	other := &config.Config{Concourse: config.Concourse{Branch: "trunk"}}
	if got := mainline(t.Context(), other, runs.Change{PR: 4, Commit: c[3]}, root); got != nil {
		t.Errorf("without the branch: %v", got)
	}

	if got := mainline(t.Context(), other, runs.Change{Branch: "trunk", Commit: c[2]}, root); !slices.Equal(got, want[1:]) {
		t.Errorf("the run's own commit: %v", got)
	}

	shallow := filepath.Join(t.TempDir(), "shallow")
	if out, err := exec.CommandContext(t.Context(), "git", "clone", "--quiet", "--depth", "2", "file://"+root, shallow).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if got := mainline(t.Context(), cfg, runs.Change{}, shallow); got != nil {
		t.Errorf("a shallow checkout: %v", got)
	}
}
