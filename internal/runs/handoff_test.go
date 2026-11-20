package runs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
)

const handOffCommit = "0123456789abcdef0123456789abcdef01234567"

func changesRun(forges ...config.Forge) *config.Config {
	return &config.Config{
		Forges: forges,
		Runs:   map[string]config.Run{"review": {Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}}}},
	}
}

func TestAChangeFromAForgeComesAsAHandOff(t *testing.T) {
	cfg := changesRun(config.Forge{Name: "git", Plugin: config.ForgeGit})
	want := Change{Run: "review", ID: "git/branch-review%2Fcart", Commit: handOffCommit, Branch: "review/cart", Base: "main"}

	dir := t.TempDir()
	if err := WriteRunFile(dir, want); err != nil {
		t.Fatal(err)
	}

	if got, err := FromTrigger(t.Context(), cfg, "review", dir); err != nil || got != want {
		t.Fatalf("%+v %v", got, err)
	}

	other := t.TempDir()
	if err := WriteRunFile(other, Change{Run: "nightly", ID: "main", Commit: handOffCommit}); err != nil {
		t.Fatal(err)
	}

	if _, err := FromTrigger(t.Context(), cfg, "review", other); err == nil || !strings.Contains(err.Error(), `is for run "nightly", not "review"`) {
		t.Errorf("another run's: %v", err)
	}

	if err := (Head{PR: 7, Commit: handOffCommit, Branch: "b"}).Files(dir); err != nil {
		t.Fatal(err)
	}

	if _, err := FromTrigger(t.Context(), cfg, "review", dir); !errors.Is(err, ErrMoved) {
		t.Errorf("both: %v", err)
	}

	if _, err := FromTrigger(t.Context(), cfg, "review", t.TempDir()); err == nil {
		t.Error("no run.json read as something")
	}
}

func TestARepositorysOwnRunJSONIsNoHandOff(t *testing.T) {
	dir := t.TempDir()

	if err := (Head{PR: 7, Commit: handOffCommit, Branch: "feature"}).Files(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, RunFile), []byte(`{"run": "review", "change": "x", "commit": "y"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := FromTrigger(t.Context(), changesRun(), "review", dir)
	if err != nil || got != (Change{Run: "review", ID: "pr-7", Commit: handOffCommit, PR: 7, Branch: "feature", Base: "main"}) {
		t.Fatalf("%+v %v", got, err)
	}
}
