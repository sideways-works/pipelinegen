package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestAPlanningStepLeavesNoBazelServerBehind(t *testing.T) {
	testrepo.NeedBazel(t)

	bd := bazeldifftest.Tool(t).Path
	r := testrepo.New(t)

	r.Write(config.FileName, readFile(t, filepath.Join(r.Dir, config.FileName))+"untested:\n  - {paths: [\"**\"], reason: the fixture's other files}\n")
	dependOnWhatTheyTest(r)
	first := r.Commit("first")

	r.Write("api/money/money.go", readFile(t, filepath.Join(r.Dir, "api", "money", "money.go"))+"\n// changed\n")
	second := r.Commit("second")

	cfg := loadConfig(t, r.Dir)

	t.Setenv(envPipelineStep, "1")
	t.Setenv(envJobURL, "")
	t.Setenv("BAZELISK_HOME", "")

	cache := filepath.Join(t.TempDir(), "plan-cache")
	t.Cleanup(func() { stopBazelIn(t, cfg, r.Dir, cache) })

	start := time.Now()

	if _, err := planCommits(t.Context(), cfg, nil, r.Dir, "main", first, second, false, cache, bd, ""); err != nil {
		t.Fatal(err)
	}

	pidFile := filepath.Join(cache, bazelDir, "base", "server", "server.pid.txt")

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("no server after the plan: %v", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || !running(pid) {
		t.Fatalf("the plan's server %q isn't running: %v", data, err)
	}

	trimAfter(t.Context(), cfg, r.Dir, cache, start)

	for deadline := time.Now().Add(time.Minute); running(pid) && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
	}

	if running(pid) {
		t.Errorf("Bazel's server %d still runs after the step ended", pid)
	}

	// a step whose server is gone, or that never started one, starts none to stop it
	trimAfter(t.Context(), cfg, r.Dir, cache, start)

	if fileExists(pidFile) {
		t.Errorf("a server was started again: %s", pidFile)
	}
}

func running(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
