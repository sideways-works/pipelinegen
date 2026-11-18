package kit

import (
	"strings"
	"testing"
)

func TestAShellWrappedCallIsTheToolsCall(t *testing.T) {
	pipeline := []byte(`jobs:
- name: run-unit
  plan:
  - task: batch
    config:
      inputs: [{name: source}, {name: tools}]
      run:
        path: sh
        args: ["-ec", "(install)\nexec \"$0\" \"$@\"", tools/pipelinegen, slice, run, --root, source]
`)

	steps, err := stepCalls(pipeline, nil)
	if err != nil || len(steps) != 1 {
		t.Fatalf("%+v %v", steps, err)
	}

	if c := steps[0]; c.prog != "tools/pipelinegen" || strings.Join(c.args, " ") != "slice run --root source" || c.script != "(install)\nexec \"$0\" \"$@\"" {
		t.Fatalf("call %+v", c)
	}
}

func TestTheRegistrysVarsAreTheSpecs(t *testing.T) {
	s := packagesSpec()

	ok := []byte(`source: {username: ((registry_push.username)), password: ((registry_push.password))}
pull: {username: ((registry_pull.username)), password: ((registry_pull.password))}`)
	if p := strayVars(ok, s); p != "" {
		t.Fatalf("the registry's vars: %s", p)
	}

	if p := strayVars(ok, Spec()); !strings.Contains(p, "registry_push.username") {
		t.Fatalf("without images: %q", p)
	}
}

func TestTheRegistrysCredentialsStayOutOfTheRepositorysCode(t *testing.T) {
	s := packagesSpec()

	cases := map[string]string{ //nolint:gosec // pipelines that name vars, no credential
		"a build job that runs nothing of the repository's": `jobs:
- name: image-x
  plan:
  - task: look
    config: {inputs: [], params: {U: ((registry_push.username))}}
- name: run-unit
  plan:
  - task: image
    config: {inputs: [], params: {U: ((registry_pull.username))}}
  - task: batch
    config: {inputs: [{name: source}]}
`,
		"job run-unit holds the var that pushes": `jobs:
- name: run-unit
  plan:
  - put: cached
    params: {password: ((registry_push.password))}
  - task: batch
    file: source/ci/batch.yml
`,
		"job run-unit, task batch: holds the registry's credentials": `jobs:
- name: run-unit
  plan:
  - task: batch
    config: {inputs: [{name: source}], params: {P: ((registry_pull.password))}}
`,
	}

	for want, text := range cases {
		got := strings.Join(credentialProblems(s, []byte(text)), "; ")

		if strings.HasPrefix(want, "a build job") {
			if got != "" {
				t.Errorf("%s: %s", want, got)
			}

			continue
		}

		if !strings.Contains(got, want) {
			t.Errorf("%q, want %q", got, want)
		}
	}
}

func TestEveryWrittenStepInstallsItsPackages(t *testing.T) {
	s := packagesSpec()
	suite, planner := s.Suites[0].Packages.Install, s.PlannerPackages.Install

	text := []byte(`jobs:
- name: run-` + s.Suites[0].Name + `
  plan:
  - task: batch
    config:
      params: {PIPELINEGEN_SUITE: ` + s.Suites[0].Name + `}
      run: {path: sh, args: ["-ec", ` + quote(suite+`exec "$0" "$@"`) + `, tools/pipelinegen, slice, run]}
  - task: start
    config:
      run: {path: sh, args: ["-ec", ` + quote(planner+"tools/pipelinegen slice start --root source\n") + `]}
  - task: end
    config:
      run: {path: tools/pipelinegen, args: [report, suite]}
# ` + s.Images.Builds[0].Key + `
`)

	steps, err := stepCalls(text, nil)
	if err != nil {
		t.Fatal(err)
	}

	got := packageProblems(s, steps, text)
	if len(got) != 1 || !strings.Contains(got[0], "step end: pipelinegen report suite runs without installing concourse.planner.packages first") {
		t.Fatalf("problems: %q", got)
	}
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
