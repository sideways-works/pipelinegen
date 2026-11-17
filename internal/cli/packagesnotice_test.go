package cli

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestARunnerThatInstallsNothingIsNamed(t *testing.T) {
	cfg := &config.Config{Packages: config.Packages{{Name: "jq"}}}
	old := plugin.Description{Version: "v0.2.0", Capabilities: []string{plugin.CapSteps}}

	got := runnerInstalls(cfg, "concourse", old)
	if len(got) != 1 || !strings.Contains(got[0], "concourse v0.2.0 installs no packages") {
		t.Fatalf("notices: %q", got)
	}

	if got := runnerInstalls(cfg, "concourse", plugin.Description{Capabilities: []string{plugin.CapPackages}}); len(got) > 0 {
		t.Fatalf("a runner that installs: %q", got)
	}

	if got := runnerInstalls(&config.Config{}, "concourse", old); len(got) > 0 {
		t.Fatalf("no packages: %q", got)
	}
}
