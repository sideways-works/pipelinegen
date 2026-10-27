package kit

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func pipelineOf(env, params string) []byte {
	return []byte(`jobs:
- name: pr-e2e
  plan:
  - task: plan
    config:
      inputs: [{name: tools}, {name: trigger}]
      params: {` + env + `}
      run:
        path: sh
        args: [-ec, "tools/pipelinegen plan --run pr --trigger trigger --out plans/head"]
  - task: prepare
    file: source/ci/e2e-prepare.yml
    params: {` + params + `}
`)
}

func TestEveryStepOfTheToolsGetsTheTrustedKeys(t *testing.T) {
	s := Spec()
	if s.Trusted == "" {
		t.Fatal("the fixture spec has no trusted keys")
	}

	quoted := "'" + strings.ReplaceAll(s.Trusted, "'", "''") + "'"

	for _, tc := range []struct {
		env  string
		want string
	}{
		{plugin.EnvTrusted + ": " + quoted, ""},
		{"GITHUB_TOKEN: x", "no " + plugin.EnvTrusted},
		{plugin.EnvTrusted + `: '{}'`, "isn't the spec's trusted keys"},
	} {
		steps, err := stepCalls(pipelineOf(tc.env, ""))
		if err != nil {
			t.Fatal(err)
		}

		got := strings.Join(trustedProblems(s, steps), "; ")
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q", tc.env, got)
		}
	}

	steps, err := stepCalls([]byte("jobs:\n- name: p\n  plan:\n  - task: say\n    config:\n      run: {path: sh, args: [-ec, \"echo 'run pipelinegen pipeline --out p.yml'\"]}\n"))
	if err != nil || len(trustedProblems(s, steps)) != 0 {
		t.Errorf("a command for a person: %v %v", err, trustedProblems(s, steps))
	}
}

func TestEveryStepOfTheToolsIsAPipelinesStep(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{plugin.EnvPipelineStep + `: "1"`, ""},
		{"PIPELINEGEN_JOB_URL: https://ci/jobs/plan", plugin.EnvPipelineStep + ` is "", not 1`},
		{plugin.EnvPipelineStep + `: "yes"`, `is "yes", not 1`},
	} {
		steps, err := stepCalls(pipelineOf(tc.env, ""))
		if err != nil {
			t.Fatal(err)
		}

		got := strings.Join(pipelineStepProblems(steps), "; ")
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q", tc.env, got)
		}
	}

	steps, err := stepCalls([]byte("jobs:\n- name: p\n  plan:\n  - task: say\n    config:\n      run: {path: sh, args: [-ec, \"echo 'run pipelinegen pipeline --out p.yml'\"]}\n"))
	if err != nil || len(pipelineStepProblems(steps)) != 0 {
		t.Errorf("a command for a person: %v %v", err, pipelineStepProblems(steps))
	}
}

func TestARepositoryTaskGetsItsDeclaredSecretsAlone(t *testing.T) {
	s := splitSpec()

	for params, want := range map[string]string{ //nolint:gosec // var names, not credentials
		"NPM_TOKEN: ((npm_token)), PROJECTS: phone":        "",
		"GITHUB_TOKEN: ((github_store_token))":             "params.GITHUB_TOKEN is ((github_store_token)), which no secret declares",
		"T: ((github_read_token))":                         "((github_read_token)), which no secret declares",
		plugin.EnvTrusted + ": x":                          "only the tool's own steps get",
		plugin.EnvPipelineStep + `: "1"`:                   "the mark of the tool's own steps",
		"PIPELINEGEN_BATCH: ((.:batch)), X: ((.:handoff))": "",
	} {
		got := strings.Join(repoTaskProblems(s, pipelineOf("", params)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q", params, got)
		}
	}
}

func TestPlanningAChangesTreeHoldsNoTokenThatWrites(t *testing.T) {
	for env, want := range map[string]string{ //nolint:gosec // var names, not credentials
		"PIPELINEGEN_READ_TOKEN: ((r))":                                 "",
		"PIPELINEGEN_READ_TOKEN: ((r)), PIPELINEGEN_STORE_TOKEN: ((s))": "holding PIPELINEGEN_STORE_TOKEN",
		"GITHUB_TOKEN: ((t))":                                           "holding GITHUB_TOKEN",
	} {
		steps, err := stepCalls(pipelineOf(env, ""))
		if err != nil {
			t.Fatal(err)
		}

		got := strings.Join(proposedPlanProblems(steps), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q", env, got)
		}
	}
}

func TestAConnectorsChainHasItsSteps(t *testing.T) {
	chain := func(write, resource string) []byte {
		return []byte(`resources:
- {name: planned-pr, type: ` + resource + `}
jobs:
- name: sweep-pr
  plan:
  - task: sweep
    config:
      run: {path: sh, args: [-ec, "tools/pipelinegen unplanned --run pr --out sweep"]}
  - task: plan
    config:
      run: {path: sh, args: [-ec, "if prepare && (x && tools/pipelinegen plan --run pr --out plans/head); then :; fi"]}
  - task: write
    config:
      run: {path: sh, args: [-ec, "` + write + `"]}
`)
	}

	for text, want := range map[string]string{
		string(chain("if tools/pipelinegen plan --run pr --from plans/head; then :; fi", "mock")):      "",
		string(chain("echo nothing", "mock")):                                                          "plan --from false",
		string(chain("if tools/pipelinegen plan --run pr --from plans/head; then :; fi", "github-pr")): "a pull request resource starts",
	} {
		got := strings.Join(connectorProblems([]byte(text)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}

func TestACallAfterAShellKeywordIsACall(t *testing.T) {
	for _, line := range []string{
		"if tools/pipelinegen plan --from plans/head; then :; fi",
		"if false; then :; elif tools/pipelinegen plan --from plans/head; then :; fi",
		"while tools/pipelinegen plan --from plans/head; do break; done",
		"until tools/pipelinegen plan --from plans/head; do :; done",
		"x && tools/pipelinegen plan --from plans/head",
		"exec tools/pipelinegen plan --from plans/head",
	} {
		steps, err := stepCalls([]byte("jobs:\n- name: j\n  plan:\n  - task: t\n    config:\n      run: {path: sh, args: [-ec, \"" + line + "\"]}\n"))
		if err != nil {
			t.Fatal(err)
		}

		if len(steps) != 1 || !hasFlag(steps[0].args, "from") {
			t.Errorf("%s: %+v", line, steps)
		}
	}

	steps, err := stepCalls([]byte("jobs:\n- name: j\n  plan:\n  - task: t\n    config:\n      run: {path: sh, args: [-ec, \"cp tools/pipelinegen elsewhere\"]}\n"))
	if err != nil || len(steps) != 0 {
		t.Errorf("a path as a word: %+v %v", steps, err)
	}
}

func TestSSHKeysGoWhereTheirTokensGo(t *testing.T) {
	s := connectorSpec()

	for env, want := range map[string]string{ //nolint:gosec // var names, not credentials
		"PIPELINEGEN_READ_TOKEN: ((r)), PIPELINEGEN_SSH_KEY_READ: ((ssh_read_key))":                                               "",
		"PIPELINEGEN_READ_TOKEN: ((r))":                                                                                           "without PIPELINEGEN_SSH_KEY_READ",
		"PIPELINEGEN_READ_TOKEN: ((r)), PIPELINEGEN_SSH_KEY_READ: ((other))":                                                      "isn't the spec's var",
		"PIPELINEGEN_READ_TOKEN: ((r)), PIPELINEGEN_SSH_KEY_READ: ((ssh_read_key)), PIPELINEGEN_SSH_KEY_STORE: ((ssh_store_key))": "a key for what the step doesn't do",
	} {
		steps, err := stepCalls(pipelineOf(env, ""))
		if err != nil {
			t.Fatal(err)
		}

		got := strings.Join(sshKeyProblems(s, steps), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q", env, got)
		}
	}

	steps, err := stepCalls(pipelineOf("PIPELINEGEN_READ_TOKEN: ((r)), PIPELINEGEN_SSH_KEY_STORE: ((ssh_store_key))", ""))
	if err != nil || !strings.Contains(strings.Join(proposedPlanProblems(steps), "; "), "holding PIPELINEGEN_SSH_KEY_STORE") {
		t.Errorf("planning a change's tree with the store key: %v %v", err, proposedPlanProblems(steps))
	}

	if got := strings.Join(repoTaskProblems(s, pipelineOf("", "PIPELINEGEN_SSH_KEY_READ: ((ssh_read_key))")), "; "); !strings.Contains(got, "no task of the repository's gets") {
		t.Errorf("a repository task with a key: %q", got)
	}
}

func TestALockStepWithoutTheTrustedKeysIsNamed(t *testing.T) {
	s := Spec()
	quoted := "'" + strings.ReplaceAll(s.Trusted, "'", "''") + "'"

	if !strings.Contains(s.Trusted, `"locks":`) {
		t.Fatalf("the fixture spec's trusted keys hold no locks: %s", s.Trusted)
	}

	lockStep := func(env string) []byte {
		return []byte(`jobs:
- name: pr-e2e
  plan:
  - task: lock
    config:
      inputs: [{name: tools}, {name: trigger}, {name: source}]
      params: {PIPELINEGEN_TRIGGER: trigger, ` + env + `}
      run: {path: tools/pipelinegen, args: [lock, acquire, browsers, --root, source]}
`)
	}

	for env, want := range map[string]string{
		plugin.EnvTrusted + ": " + quoted: "",
		"GITHUB_TOKEN: x":                 "job pr-e2e, step lock: no " + plugin.EnvTrusted,
	} {
		steps, err := stepCalls(lockStep(env))
		if err != nil {
			t.Fatal(err)
		}

		got := strings.Join(trustedProblems(s, steps), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", env, got, want)
		}
	}
}
