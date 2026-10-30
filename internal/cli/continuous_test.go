package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/spec"
	"github.com/sideways-works/pipelinegen/plugin"
)

const continuousConfig = `
pipeline: shop
suites:
  e2e:
    tag: slice-e2e
    run_slice: ci/tasks/e2e-slice.yml
git: {remote: "REMOTE"}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  full:
    trigger: {branch: main, continuous: true}
    compare: none
    select: all
    record: true
    suites: {e2e: {}}
`

type continuousWorld struct {
	root, trigger string
	first         string
	cfg           *config.Config
	store         *gitstore.Store
	push          func() string
}

func newContinuousWorld(t *testing.T) *continuousWorld {
	t.Helper()

	old := roundPoll
	roundPoll = 50 * time.Millisecond

	t.Cleanup(func() { roundPoll = old })

	remote := storetest.Bare(t)

	cfgText := strings.Replace(continuousConfig, "REMOTE", remote, 1)
	if onBranchStore {
		cfgText += "store: {backend: branch}\n"
	}

	w := &continuousWorld{root: t.TempDir(), trigger: filepath.Join(t.TempDir(), "trigger")}
	writeFile(t, filepath.Join(w.root, config.FileName), cfgText)

	work := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()

		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git(work, "init", "-q", "-b", "main")

	w.push = func() string {
		t.Helper()

		git(work, "commit", "-q", "--allow-empty", "-m", "next")
		git(work, "push", "-q", "--force", remote, "HEAD:refs/heads/main")

		return git(work, "rev-parse", "HEAD")
	}

	w.first = w.push()

	if out, err := exec.CommandContext(t.Context(), "git", "clone", "-q", remote, w.trigger).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}

	cfg, err := config.Parse([]byte(cfgText))
	if err != nil {
		t.Fatal(err)
	}

	w.cfg = cfg
	w.store = openStore(cfg)

	t.Cleanup(func() {
		if err := w.store.Close(); err != nil {
			t.Error(err)
		}
	})

	for k, v := range map[string]string{envGitURL: "", envToken: "", envJobURL: "", envPR: "", envAPIURL: "", envSerialPlan: ""} {
		t.Setenv(k, v)
	}

	return w
}

func (w *continuousWorld) change(commit string) runs.Change {
	return runs.Change{Run: "full", ID: "main", Commit: commit, Branch: "main"}
}

func TestAContinuousTriggerIsABranchsOwn(t *testing.T) {
	base := strings.Replace(continuousConfig, "REMOTE", "file:///nowhere", 1)

	for why, text := range map[string]string{
		"with a timer":           strings.Replace(base, "{branch: main, continuous: true}", "{branch: main, continuous: true, timer: {start: 1:00 AM, stop: 2:00 AM}}", 1),
		"manual":                 strings.Replace(base, "{branch: main, continuous: true}", "{branch: main, continuous: true, manual: true}", 1),
		"without a branch":       strings.Replace(base, "{branch: main, continuous: true}", "{continuous: true}", 1),
		"idle_minutes elsewhere": strings.Replace(base, "{branch: main, continuous: true}", "{branch: main}\n    idle_minutes: 30", 1),
		"idle_minutes below one": strings.Replace(base, "    record: true\n", "    record: true\n    idle_minutes: -1\n", 1),
	} {
		if _, err := config.Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", why)
		}
	}

	cfg, err := config.Parse([]byte(strings.Replace(base, "    record: true\n", "    record: true\n    idle_minutes: 45\n    turn_minutes: 20\n", 1)))
	if err != nil || cfg.IdleLength("full") != 45 || !strings.Contains(strings.Join(cfg.Notices, "\n"), "turn_minutes is no longer read") || len(cfg.Warnings) != 0 {
		t.Fatalf("idle_minutes: %v %v %v", err, cfg.Notices, cfg.Warnings)
	}

	// Without it, the idle bound is the start step, the prepare and one batch.
	if plain, err := config.Parse([]byte(base)); err != nil || plain.IdleLength("full") != config.DefaultStepMinutes+2*config.DefaultSuiteMinutes+10 {
		t.Fatalf("the default idle bound: %v %d", err, plain.IdleLength("full"))
	}

	sp, err := spec.From(cfg)
	if err != nil || len(sp.Runs) != 1 || sp.Runs[0].Trigger.Kind != plugin.TriggerContinuous || sp.Runs[0].Trigger.Branch != "main" {
		t.Fatalf("the runner's spec: %+v %v", sp, err)
	}

	if err := runnerRunsContinuous(cfg, "concourse", plugin.Description{Version: "v1"}); err == nil || !strings.Contains(err.Error(), "can't build a continuous run") {
		t.Fatalf("a runner without the capability: %v", err)
	}

	if err := runnerRunsContinuous(cfg, "concourse", plugin.Description{Capabilities: []string{plugin.CapContinuous}}); err != nil {
		t.Fatalf("a runner that can't run a job's builds one at a time: %v", err)
	}

	can := plugin.Description{Capabilities: []string{plugin.CapContinuous, plugin.CapSerialJobs}}
	if err := runnerRunsContinuous(cfg, "concourse", can); err != nil {
		t.Fatal(err)
	}

	serialPlans(&sp, plugin.Description{Capabilities: []string{plugin.CapContinuous}})

	if sp.Runs[0].SerialPlan {
		t.Fatal("a serial planning job asked of a runner that can't")
	}

	serialPlans(&sp, can)

	if !sp.Runs[0].SerialPlan {
		t.Fatal("the continuous run's planning job isn't asked to be serial")
	}
}

func TestALockPerBatchReachesTheRunner(t *testing.T) {
	base := strings.Replace(continuousConfig, "REMOTE", "file:///nowhere", 1)
	locked := strings.Replace(base, "    run_slice: ci/tasks/e2e-slice.yml\n", "    run_slice: ci/tasks/e2e-slice.yml\n    lock: {name: browsers, minutes: 60, slots: 2, per: PER}\n", 1)

	if _, err := config.Parse([]byte(strings.Replace(locked, "PER", "stack", 1))); err == nil {
		t.Fatal("lock.per: stack accepted")
	}

	for per, want := range map[string]string{"batch": config.LockPerBatch, "build": ""} {
		cfg, err := config.Parse([]byte(strings.Replace(locked, "PER", per, 1)))
		if err != nil {
			t.Fatal(err)
		}

		sp, err := spec.From(cfg)
		if err != nil || len(sp.Suites) != 1 || sp.Suites[0].Lock == nil || sp.Suites[0].Lock.Per != want {
			t.Fatalf("per %s: %+v %v", per, sp.Suites, err)
		}
	}
}

func TestALocksWeight(t *testing.T) {
	two := strings.Replace(strings.Replace(continuousConfig, "REMOTE", "file:///nowhere", 1),
		"    run_slice: ci/tasks/e2e-slice.yml\n",
		"    run_slice: ci/tasks/e2e-slice.yml\n    lock: {name: worker, minutes: 60, slots: 4, weight: W}\n  checks:\n    tag: slice-checks\n    run_slice: ci/tasks/checks.yml\n    lock: {name: worker, minutes: 60, slots: S, weight: 4}\n", 1)

	for why, text := range map[string]string{
		"a weight above the slots": strings.NewReplacer("W", "5", "S", "4").Replace(two),
		"two slot counts":          strings.NewReplacer("W", "1", "S", "8").Replace(two),
	} {
		if _, err := config.Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", why)
		}
	}

	if _, err := config.Parse([]byte(strings.NewReplacer("W", "1", "S", "4").Replace(two))); err != nil {
		t.Fatal(err)
	}
}
