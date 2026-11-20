package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/report"
)

func startedRun(t *testing.T) (string, *githubAPI) {
	t.Helper()

	root, api, s := runEnv(t)
	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}], "untouched": [], "whole_suite": []}]}`
	if err := report.WritePlan(t.Context(), s, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	return root, api
}

// A config with a github section and no forge entry reports through the pinned GitHub forge,
// downloaded into pipelinegen's cache and checked by its checksum, as a step without the bootstrap's does.
func TestAGitHubConfigReportsThroughTheFetchedForge(t *testing.T) {
	root, api := startedRun(t)

	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Chdir(t.TempDir())

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/pr/e2e"); !strings.HasPrefix(got, "pending ") {
		t.Fatalf("status: %q", got)
	}

	// kept by its file's checksum, which the pin gives or its release's SHA256SUMS lists
	if kept, _ := filepath.Glob(filepath.Join(cache, "pipelinegen", "forges", "*", "github-forge")); len(kept) != 1 { //nolint:errcheck // a valid pattern
		t.Fatalf("the forge isn't in the cache: %v", kept)
	}
}

// A forge whose binary isn't the one its pin names never runs: the
// step fails, and nothing reaches GitHub.
func TestAForgeThatIsNotItsPinNeverRuns(t *testing.T) {
	root, api := startedRun(t)

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	pin := config.PinnedForges[config.ForgeGitHub]

	t.Cleanup(func() { config.PinnedForges[config.ForgeGitHub] = pin })

	wrong := pin
	wrong.SHA256 = config.Checksums{config.Platform(): strings.Repeat("0", 64)}

	if wrong.Sums != "" {
		wrong.Sums = strings.Repeat("0", 64)
	}

	config.PinnedForges[config.ForgeGitHub] = wrong

	if _, stderr, code := run(t, "report", "start", "-root", root); code == 0 || !strings.Contains(stderr, "checksum") {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/pr/e2e"); got != "" {
		t.Fatalf("a status reached GitHub: %q", got)
	}
}
