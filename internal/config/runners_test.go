package config

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
)

const (
	concourseRunner = "  - {name: concourse, kind: runner, repository: s/plugins, path: runner/concourse, release: {version: v1, sha256: \"4444444444444444444444444444444444444444444444444444444444444444\"}, config: {placement: {default: [big]}}}\n"
	piperunRunner   = "  - {name: piperun, kind: runner, repository: s/plugins, path: runner/piperun, release: {version: v1, sha256: \"5555555555555555555555555555555555555555555555555555555555555555\"}, config: {target: \"1.2.0\"}}\n"
	twoRuns         = `runs:
  pr: {trigger: {pull_requests: {base: main}}, compare: merged_target, select: affected, suites: {e2e: {}}PR}
  main: {trigger: {branch: main}, compare: last_passed, select: affected, suites: {e2e: {}}MAIN}
`
)

func runnersConfig(t *testing.T, runners, pr, main string) *Config {
	t.Helper()

	return parseTrusted(t, trustedBase+runners+strings.NewReplacer("PR", pr, "MAIN", main).Replace(twoRuns))
}

func TestARunIsOwnedByTheOnlyRunnerWithoutNamingIt(t *testing.T) {
	c := runnersConfig(t, concourseRunner, "", "")

	if got := c.OwnedRuns("concourse"); !slices.Equal(got, []string{"main", "pr"}) {
		t.Errorf("owned: %v", got)
	}

	if p, ok := c.Runner(); !ok || p.Name != "concourse" {
		t.Errorf("runner: %v %v", p, ok)
	}

	named := runnersConfig(t, concourseRunner, ", runner: concourse", "")
	if named.RunRunner("pr") != "concourse" || named.RunRunner("main") != "concourse" {
		t.Errorf("owners: %v", named.owners())
	}
}

func TestEachRunNamesARunnerOfTheConfig(t *testing.T) {
	two := concourseRunner + piperunRunner

	if c := runnersConfig(t, two, ", runner: concourse", ", runner: piperun"); !slices.Equal(c.OwnedRuns("piperun"), []string{"main"}) || !slices.Equal(c.OwnedRuns("concourse"), []string{"pr"}) {
		t.Errorf("owned: %v and %v", c.OwnedRuns("concourse"), c.OwnedRuns("piperun"))
	}

	for _, tc := range []struct {
		runners, pr, main, want string
	}{
		{two, ", runner: concourse", "", "run main: 2 runners are configured (concourse, piperun): name the one that owns it in runner"},
		{concourseRunner, ", runner: piperun", "", "run pr: runner piperun is no runner plugin of this config (runners: concourse)"},
		{"", ", runner: go-sbom", "", "run pr: runner go-sbom is no runner plugin of this config (runners: none)"},
	} {
		_, err := Parse([]byte(trustedBase + tc.runners + strings.NewReplacer("PR", tc.pr, "MAIN", tc.main).Replace(twoRuns)))
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q %q: %v, want %q", tc.pr, tc.main, err, tc.want)
		}
	}
}

func TestOneRunnersTrustedKeysAreAsTheyWere(t *testing.T) {
	data, err := json.Marshal(runnersConfig(t, concourseRunner, ", runner: concourse", "").Trusted())
	if err != nil {
		t.Fatal(err)
	}

	unnamed, err := json.Marshal(runnersConfig(t, concourseRunner, "", "").Trusted())
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != string(unnamed) || strings.Contains(string(data), "run_runners") || strings.Contains(string(data), "runner_configs") ||
		!strings.Contains(string(data), `"runner_config":{"placement":{"default":["big"]}}`) {
		t.Fatalf("trusted: %s\nwithout runner: %s", data, unnamed)
	}
}

func TestAChangeCantMoveARunToAnotherRunner(t *testing.T) {
	two := concourseRunner + piperunRunner
	trusted := runnersConfig(t, two, ", runner: concourse", ", runner: concourse").Trusted()

	if !maps.Equal(trusted.RunRunners, map[string]string{"pr": "concourse", "main": "concourse"}) || len(trusted.RunnerConfigs) != 2 || trusted.RunnerConfig != nil {
		t.Fatalf("trusted: %+v", trusted)
	}

	moved := runnersConfig(t, two, ", runner: concourse", ", runner: piperun")
	if edited := moved.WithTrusted(trusted); !slices.Equal(edited, []string{"runs.main.runner"}) || moved.RunRunner("main") != "concourse" {
		t.Errorf("moved: edited %v, main on %s", edited, moved.RunRunner("main"))
	}

	same := runnersConfig(t, two, ", runner: concourse", ", runner: concourse")
	if edited := same.WithTrusted(trusted); len(edited) != 0 {
		t.Errorf("unmoved: edited %v", edited)
	}

	// a change that adds the second runner and moves a run to it: the runner it adds isn't pinned
	single := runnersConfig(t, concourseRunner, "", "").Trusted()

	adds := runnersConfig(t, two, ", runner: concourse", ", runner: piperun")
	if edited := adds.WithTrusted(single); !slices.Contains(edited, "runs.main.runner") || slices.Contains(edited, "runs.pr.runner") ||
		adds.RunRunner("main") != "concourse" || len(adds.Runners()) != 1 {
		t.Errorf("added: edited %v, main on %q, runners %v", edited, adds.RunRunner("main"), adds.runnerNames())
	}

	written := runnersConfig(t, concourseRunner, ", runner: concourse", "")
	if edited := written.WithTrusted(single); len(edited) != 0 {
		t.Errorf("the default written out: edited %v", edited)
	}
}

func TestEachRunnersConfigIsTrustedByName(t *testing.T) {
	two := concourseRunner + piperunRunner
	trusted := runnersConfig(t, two, ", runner: concourse", ", runner: piperun").Trusted()

	changed := runnersConfig(t, strings.Replace(two, `target: "1.2.0"`, `target: "9.9.9"`, 1), ", runner: concourse", ", runner: piperun")

	edited := changed.WithTrusted(trusted)
	if !slices.Equal(edited, []string{"plugins.piperun.config"}) {
		t.Errorf("edited %v", edited)
	}

	concourse, _ := changed.RunnerNamed("concourse")
	piperun, _ := changed.RunnerNamed("piperun")

	if piperun.Config["target"] != "1.2.0" || concourse.Config["placement"] == nil || concourse.Config["target"] != nil {
		t.Errorf("configs: concourse %v, piperun %v", concourse.Config, piperun.Config)
	}
}
