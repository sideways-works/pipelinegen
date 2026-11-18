package cli

import (
	"os"
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

// A config with a github section and no connector entry reports through the pinned GitHub connector,
// downloaded into pipelinegen's cache and checked by its checksum, as a step without the bootstrap's does.
func TestAGitHubConfigReportsThroughTheFetchedConnector(t *testing.T) {
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

	pin := config.PinnedConnectors[config.ConnectorGitHub]
	sum, _ := pin.SHA256.For(config.Platform())

	if _, err := os.Stat(filepath.Join(cache, "pipelinegen", "connectors", sum, "github-connector")); err != nil {
		t.Fatalf("the connector isn't in the cache: %v", err)
	}
}

// A connector whose binary isn't the one its pin names never runs: the
// step fails, and nothing reaches GitHub.
func TestAConnectorThatIsNotItsPinNeverRuns(t *testing.T) {
	root, api := startedRun(t)

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	pin := config.PinnedConnectors[config.ConnectorGitHub]

	t.Cleanup(func() { config.PinnedConnectors[config.ConnectorGitHub] = pin })

	wrong := pin
	wrong.SHA256 = config.Checksums{config.Platform(): strings.Repeat("0", 64)}
	config.PinnedConnectors[config.ConnectorGitHub] = wrong

	if _, stderr, code := run(t, "report", "start", "-root", root); code == 0 || !strings.Contains(stderr, "checksum") {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/pr/e2e"); got != "" {
		t.Fatalf("a status reached GitHub: %q", got)
	}
}
