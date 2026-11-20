package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestTwoHandOffsOfOneHeadAreTwoVersions(t *testing.T) {
	root, _, s := runEnv(t)
	gitForgeConfig(t, root, s.Remote)
	cfg := loadConfig(t, root)

	commit := pushBranch(t, s.Remote, "review/cart")
	h := runs.Head{Commit: commit, Branch: "review/cart", Change: "git/branch-review%2Fcart"}
	id := headID("pr", h)

	var (
		texts   []string
		changes []runs.Change
	)

	for range 2 {
		dir := t.TempDir()
		if err := handOff(cfg, id, h, dir); err != nil {
			t.Fatal(err)
		}

		texts = append(texts, readFile(t, filepath.Join(dir, runs.RunFile)))

		ch, err := runs.ReadRunFile(filepath.Join(dir, runs.RunFile))
		if err != nil {
			t.Fatal(err)
		}

		changes = append(changes, ch)
	}

	if texts[0] == texts[1] || changes[0].Handed == "" {
		t.Fatalf("two hand-offs of one head are one version:\n%s", texts[0])
	}

	if !reflect.DeepEqual(reportID(changes[0]), reportID(changes[1])) {
		t.Fatalf("two hand-offs name two changes: %+v %+v", reportID(changes[0]), reportID(changes[1]))
	}

	if err := report.WritePlan(t.Context(), s, reportID(changes[0]), []byte(`{"suites": []}`)); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := readPlan(t.Context(), s, reportID(changes[1])); err != nil || !ok {
		t.Fatalf("the second hand-off finds no plan: %v %v", ok, err)
	}
}

func TestAPipelineOfAForgesRunsNeedsARunnerThatBuildsThem(t *testing.T) {
	root, _, s := runEnv(t)
	gitForgeConfig(t, root, s.Remote)
	cfg := loadConfig(t, root)

	able := plugin.Description{Version: "v1", Capabilities: []string{plugin.CallPipeline, plugin.CapForgeChanges}, ResourceTypes: []string{mockType}}

	if err := runnerBuildsForgeRuns(cfg, "ci", plugin.Description{Version: "v0", Capabilities: []string{plugin.CallPipeline}}); err == nil ||
		!strings.Contains(err.Error(), "takes changes from the git forge, which runner ci v0 can't build a pipeline for") {
		t.Errorf("a runner without the chain: %v", err)
	}

	if err := runnerBuildsForgeRuns(cfg, "ci", able); err != nil {
		t.Errorf("a runner with it: %v", err)
	}

	cfg.Concourse.Mock = "missing"
	if err := runnerBuildsForgeRuns(cfg, "ci", able); err == nil || !strings.Contains(err.Error(), "fly -t <target> workers -d") {
		t.Errorf("no mock: %v", err)
	}

	if err := runnerBuildsForgeRuns(cfg, "ci", plugin.Description{Capabilities: []string{plugin.CapForgeChanges}}); err != nil {
		t.Errorf("a runner without mock: %v", err)
	}

	if err := runnerBuildsForgeRuns(loadConfig(t, writeConfig(t, reportConfig)), "ci", plugin.Description{}); err != nil {
		t.Errorf("a GitHub run: %v", err)
	}
}

func writeConfig(t *testing.T, text string) string {
	t.Helper()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "pipelinegen.yaml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestAPipelineReachesAnSSHRemoteOnlyThroughARunnerThatHandsTheKey(t *testing.T) {
	root, _, s := runEnv(t)
	remote := "ssh://git@git.example.test" + strings.TrimPrefix(s.Remote, "file://")
	gitForgeConfig(t, root, remote)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		`git: {remote: "`+remote+`"}`,
		`git: {remote: "`+remote+`", ssh_key: {read: read_key, store: store_key}, known_hosts: ["git.example.test ssh-ed25519 AAAAkey"]}`, 1))

	cfg := loadConfig(t, root)

	if err := runnerHandsSSHKeys(cfg, "ci", plugin.Description{Version: "v0"}); err == nil || !strings.Contains(err.Error(), "runner ci v0 hands its steps no ssh key") {
		t.Errorf("a runner without keys: %v", err)
	}

	if err := runnerHandsSSHKeys(cfg, "ci", plugin.Description{Capabilities: []string{plugin.CapSSHKeys}}); err != nil {
		t.Errorf("a runner with them: %v", err)
	}

	if err := runnerHandsSSHKeys(loadConfig(t, writeConfig(t, reportConfig)), "ci", plugin.Description{}); err != nil {
		t.Errorf("no ssh remote: %v", err)
	}
}
