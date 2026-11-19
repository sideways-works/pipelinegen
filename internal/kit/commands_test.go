package kit

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/toolcalls"
)

func TestAStepMountsWhatItsCallReads(t *testing.T) {
	text := []byte(`jobs:
- name: main-e2e
  plan:
  - task: batches
    config:
      inputs: [{name: tools}, {name: trigger}]
      outputs: [{name: batches}]
      params: {GITHUB_TOKEN: x, PIPELINEGEN_TRIGGER: trigger}
      run:
        path: tools/pipelinegen
        args: [batches, --run, main, --root, source]
  - task: stale
    config:
      inputs: [{name: tools}, {name: repo}]
      run:
        path: sh
        args:
        - -ec
        - |
          # before pipelinegen could report it
          echo "pipelinegen pipeline --out pipeline.yml" >&2
          (exec 2>err; tools/pipelinegen pipeline --root repo --out pipeline.yml) && echo done
          tools/pipelinegen report whole --name x --nope
          eval "$(tools/pipelinegen pipeline --root repo)"
`)

	cmds := Commands{
		"batches": {Flags: map[string]toolcalls.Flag{
			"run": {}, "root": {Default: ".", Use: toolcalls.Reads}, "trigger": {Default: "trigger", Use: toolcalls.Reads},
			"out": {Default: "batches", Use: toolcalls.Writes}, "suite": {},
		}, Needs: toolcalls.Needs{Token: toolcalls.TokenRequired}},
		"pipeline": {Flags: map[string]toolcalls.Flag{
			"root": {Default: ".", Use: toolcalls.Reads}, "out": {Use: toolcalls.Writes | toolcalls.File},
		}, Needs: toolcalls.Needs{ConcourseURL: true}},
		"report whole": {Flags: map[string]toolcalls.Flag{"name": {}}},
	}

	seen := map[string]bool{}

	calls, mounts := callChecks(cmds, Spec(), text, nil, seen)
	if want := []string{"pipelinegen call: pipelinegen report whole: no flag -nope"}; !slices.Equal(calls, want) {
		t.Errorf("calls %q", calls)
	}

	if want := []string{"job main-e2e, step batches: pipelinegen batches: reads source, which it doesn't mount and no earlier call wrote"}; !slices.Equal(mounts, want) {
		t.Errorf("mounts %q", mounts)
	}

	if !seen["batches"] || !seen["pipeline"] || seen["report whole"] {
		t.Errorf("seen %v", seen)
	}
}

func TestATaskMayReadWhatAnEarlierCallWrote(t *testing.T) {
	text := []byte(`jobs:
- name: main-e2e
  plan:
  - task: start
    config:
      inputs: [{name: trigger}]
      outputs: [{name: tools}, {name: source}, {name: batches}]
      params: {GITHUB_TOKEN: x, PIPELINEGEN_TRIGGER: trigger}
      run:
        path: sh
        args:
        - -ec
        - |
          curl -fsSL -o tools/pipelinegen https://example.invalid/pipelinegen && chmod +x tools/pipelinegen
          PIPELINEGEN_BAZELISK=tools/bazelisk exec tools/pipelinegen git-auth NPM_TOKEN >/dev/null || true
          tools/pipelinegen tree --run main --out source
          tools/pipelinegen batches --run main --root source --out batches
          tools/pipelinegen slice start e2e.a --root source
          tools/pipelinegen pipeline --plugins tools/plugins --root source --out pipeline.yml
  - task: early
    config:
      inputs: [{name: tools}, {name: trigger}]
      outputs: [{name: source}]
      params: {GITHUB_TOKEN: x, PIPELINEGEN_TRIGGER: trigger}
      run:
        path: sh
        args: [-ec, "tools/pipelinegen slice start e2e.a --root source && tools/pipelinegen tree --run main --out source"]
  - task: toolless
    config:
      inputs: [{name: trigger}, {name: source}]
      params: {GITHUB_TOKEN: x, PIPELINEGEN_TRIGGER: trigger}
      run:
        path: tools/pipelinegen
        args: [slice, start, e2e.a, --root, source]
`)

	cmds := Commands{
		"tree": {Flags: map[string]toolcalls.Flag{
			"run": {}, "trigger": {Default: "trigger", Use: toolcalls.Reads}, "out": {Default: "source", Use: toolcalls.Writes},
		}, Needs: toolcalls.Needs{Token: toolcalls.TokenRequired}},
		"batches": {Flags: map[string]toolcalls.Flag{
			"run": {}, "root": {Default: ".", Use: toolcalls.Reads}, "trigger": {Default: "trigger", Use: toolcalls.Reads},
			"out": {Default: "batches", Use: toolcalls.Writes},
		}, Needs: toolcalls.Needs{Token: toolcalls.TokenRequired}},
		"pipeline": {Flags: map[string]toolcalls.Flag{
			"plugins": {Default: "tools/plugins", Use: toolcalls.Reads}, "root": {Default: ".", Use: toolcalls.Reads},
			"out": {Use: toolcalls.Writes | toolcalls.File},
		}},
		"git-auth":    {Words: true},
		"slice start": {Named: true, Flags: map[string]toolcalls.Flag{"root": {Default: ".", Use: toolcalls.Reads}}},
	}

	seen := map[string]bool{}

	calls, mounts := callChecks(cmds, Spec(), text, nil, seen)
	if len(calls) > 0 {
		t.Errorf("calls %q", calls)
	}

	if !seen["git-auth"] {
		t.Errorf("seen %v", seen)
	}

	want := []string{
		"job main-e2e, step early: pipelinegen slice start: reads source, which it doesn't mount and no earlier call wrote",
		"job main-e2e, step toolless: pipelinegen slice start: runs from tools, which it doesn't mount",
	}
	if !slices.Equal(mounts, want) {
		t.Errorf("mounts\n%s", strings.Join(mounts, "\n"))
	}
}

func TestAPipelineReferencesOnlyWhatTheSpecGives(t *testing.T) {
	ok := []byte(`params: {T: ((github_token)), N: ((npm_token)), B: ((.:batch))}`)
	if p := strayVars(ok, Spec()); p != "" {
		t.Errorf("the fixture's own vars: %s", p)
	}

	stray := []byte(`params: {T: ((github_token)), D: ((deploy_key))}`)
	if p := strayVars(stray, Spec()); p != "it references deploy_key" {
		t.Errorf("a var nothing declares: %q", p)
	}

	split := Spec()
	split.GitHub.Tokens = map[string]string{"read": "read_token", "changes": "github_token", "report": "github_token", "store": "github_token"}

	if p := strayVars([]byte(`params: {R: ((read_token)), T: ((github_token))}`), split); p != "" {
		t.Errorf("a purpose's var: %s", p)
	}
}

func TestATaskFilesRecordingEndMountsTheTree(t *testing.T) {
	end := func(suite, inputs string) string {
		return `
- name: main-` + suite + `
  plan:
  - task: end
    config:
      inputs: [` + inputs + `]
      params: {GITHUB_TOKEN: x, PIPELINEGEN_TRIGGER: trigger, PIPELINEGEN_RUN: main, PIPELINEGEN_SUITE: ` + suite + `}
      run:
        path: tools/pipelinegen
        args: [report, batch-end, --batch, "1", --result, result, --root, bookkeeping]`
	}

	text := []byte("jobs:" +
		end("e2e", "{name: tools}, {name: trigger}, {name: bookkeeping}, {name: batches}, {name: result}, {name: coverage}") +
		end("backend", "{name: tools}, {name: trigger}, {name: bookkeeping}, {name: batches}, {name: result}") + "\n")

	cmds := Commands{
		"report batch-end": {Flags: map[string]toolcalls.Flag{
			"batch": {}, "root": {Default: ".", Use: toolcalls.Reads}, "result": {Default: "result", Use: toolcalls.Reads},
			"batches": {Default: "batches", Use: toolcalls.Reads},
		}, Needs: toolcalls.Needs{Recording: []string{"coverage"}, TaskFileRecording: []string{"source"}, RunFromEnv: true, Token: toolcalls.TokenRequired}},
	}

	_, mounts := callChecks(cmds, Spec(), text, nil, map[string]bool{})

	// e2e records and runs its batches by a task file, so its end observes them in the tree; backend records nothing
	if want := []string{"job main-e2e, step end: pipelinegen report batch-end: reads source, which it doesn't mount and no earlier call wrote"}; !slices.Equal(mounts, want) {
		t.Errorf("mounts %q", mounts)
	}
}
