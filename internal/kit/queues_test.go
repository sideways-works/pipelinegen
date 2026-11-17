package kit

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestNoJobIsSerializedUnlessTheSpecAsks(t *testing.T) {
	s := plugin.Spec{
		Suites: []plugin.SpecSuite{{Name: "e2e", MaxInFlight: 2}, {Name: "go"}},
		Runs: []plugin.SpecRun{
			{Name: "main", Trigger: plugin.SpecTrigger{Kind: "branch"}, Suites: []plugin.SpecRunSuite{{Suite: "e2e"}, {Suite: "go"}}},
			{Name: "full", Trigger: plugin.SpecTrigger{Kind: plugin.TriggerContinuous}, Suites: []plugin.SpecRunSuite{{Suite: "go"}}},
		},
	}

	planning := "- {name: plan-full, plan: [{task: plan}]}\n"

	for want, text := range map[string]string{
		"": `jobs:
- {name: main-e2e, max_in_flight: 2, plan: []}
- {name: main-go, plan: []}
- {name: plan-full, plan: [{task: plan}]}
- {name: full-go, plan: []}
- {name: pipeline, plan: []}
`,
		"job main-go: max_in_flight, which nothing asks for":                                  "jobs:\n- {name: main-go, max_in_flight: 1, plan: []}\n" + planning,
		"job pipeline: serial, which nothing asks for":                                        "jobs:\n- {name: pipeline, serial: true, plan: []}\n" + planning,
		"job full-go: serial_groups, which nothing asks for":                                  "jobs:\n- {name: full-go, serial_groups: [x], plan: []}\n" + planning,
		"job plan-main: serial, which nothing asks for":                                       "jobs:\n- {name: plan-main, serial: true, plan: []}\n" + planning,
		"job plan-full: a continuous run's planning job with serial: its builds would queue":  "jobs:\n- {name: plan-full, serial: true, plan: [{task: plan}]}\n",
		"job plan-full: a continuous run's planning job with max_in_flight: its builds would": "jobs:\n- {name: plan-full, max_in_flight: 1, plan: [{task: plan}]}\n",
		"job plan-full: PIPELINEGEN_SERIAL_PLAN, which the tool no longer reads":              "jobs:\n- {name: plan-full, plan: [{task: plan, params: {PIPELINEGEN_SERIAL_PLAN: '1'}}]}\n",
		"job main-go: PIPELINEGEN_SERIAL_PLAN, which the tool no longer reads":                "jobs:\n" + planning + "- {name: main-go, plan: [{task: x, params: {PIPELINEGEN_SERIAL_PLAN: '1'}}]}\n",
	} {
		got := strings.Join(serializedJobs(s, []byte(text)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}

func TestAContinuousRunsSuitesStartOnlyForAPlan(t *testing.T) {
	s := plugin.Spec{
		Suites: []plugin.SpecSuite{{Name: "go"}},
		Runs:   []plugin.SpecRun{{Name: "full", Trigger: plugin.SpecTrigger{Kind: plugin.TriggerContinuous}, Suites: []plugin.SpecRunSuite{{Suite: "go"}}}},
	}

	planned := "- {name: plan-full, plan: [{task: plan}, {across: [{var: p, values: x}], do: [{put: planned-full}]}]}\n"

	for want, text := range map[string]string{
		"":                                      "jobs:\n" + planned + "- {name: full-go, plan: [{in_parallel: [{get: handoff, resource: planned-full, trigger: true}, {get: trigger, resource: branch-main}]}]}\n",
		"job full-go: triggers on branch-main":  "jobs:\n" + planned + "- {name: full-go, plan: [{get: trigger, resource: branch-main, trigger: true}]}\n",
		"job full-go: triggers on planned-full": "jobs:\n- {name: plan-full, plan: [{task: plan}, {put: planned-full}]}\n- {name: full-go, plan: [{get: handoff, resource: planned-full, trigger: true}]}\n",
	} {
		got := strings.Join(unplannedStarts(s, []byte(text)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}

func TestAContinuousRunDecidesBeforeItsPrepare(t *testing.T) {
	s := plugin.Spec{
		Suites: []plugin.SpecSuite{{Name: "go"}},
		Runs: []plugin.SpecRun{
			{Name: "main", Trigger: plugin.SpecTrigger{Kind: "branch"}, Suites: []plugin.SpecRunSuite{{Suite: "go"}}},
			{Name: "full", Trigger: plugin.SpecTrigger{Kind: plugin.TriggerContinuous}, Suites: []plugin.SpecRunSuite{{Suite: "go"}}},
		},
	}

	task := func(script string) string {
		return "jobs:\n- name: plan-main\n  plan: [{task: plan, config: {run: {path: sh, args: [-ec, 'tools/pipelinegen plan --run main']}}}]\n" +
			"- name: plan-full\n  plan: [{task: plan, config: {run: {path: sh, args: [-ec, '" + script + "']}}}]\n"
	}

	for want, script := range map[string]string{
		"": "tools/pipelinegen plan --decide --run full --gate handoff\n\nprepare_in trigger\n\nexec tools/pipelinegen plan --run full --gate handoff",
		"job plan-full: its planning task doesn't ask for the decision": "prepare_in trigger\n\nexec tools/pipelinegen plan --run full --gate handoff",
		"job plan-full: its decision and its plan don't share one task": "tools/pipelinegen plan --decide --run full --gate handoff\n\nexec tools/pipelinegen plan --run full --gate other",
	} {
		got := strings.Join(undecidedPrepares(s, []byte(task(script))), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}

func TestAPullRequestRunsSuitesStartOnlyForAHandOff(t *testing.T) {
	s := plugin.Spec{
		Suites: []plugin.SpecSuite{{Name: "go"}},
		Runs:   []plugin.SpecRun{{Name: "pr", Trigger: plugin.SpecTrigger{Kind: plugin.TriggerPullRequests}, Suites: []plugin.SpecRunSuite{{Suite: "go"}}}},
	}

	write := func(gate string) string {
		return "{task: write, config: {run: {path: sh, args: [-ec, 'exec tools/pipelinegen plan --run pr --trigger trigger --from plan" + gate + "']}}}"
	}

	gatedPut := "{across: [{var: p, values: x}], do: [{put: planned-pr}]}"
	sweep := "- {name: sweep-pr, plan: [{across: [{var: h, values: x}], do: [{put: planned-pr}]}]}\n"
	suite := "- {name: pr-go, plan: [{get: trigger, resource: planned-pr, trigger: true}]}\n"

	for want, text := range map[string]string{
		"": "jobs:\n- {name: plan-pr, plan: [" + write(" --gate handoff") + ", " + gatedPut + "]}\n" + sweep + suite,
		"job pr-go: triggers on planned-pr, which job plan-pr puts outside an across": "jobs:\n- {name: plan-pr, plan: [" + write(" --gate handoff") + ", {put: planned-pr}]}\n" + sweep + suite,
		"job pr-go: triggers on planned-pr, which no job puts inside an across":       "jobs:\n- {name: plan-pr, plan: [" + write(" --gate handoff") + "]}\n" + suite,
		"job pr-go: triggers on pr-trigger, which no job puts inside an across":       "jobs:\n- {name: pr-go, plan: [{get: trigger, resource: pr-trigger, trigger: true}]}\n",
		"job plan-pr, step write: plan --from without --gate":                         "jobs:\n- {name: plan-pr, plan: [" + write("") + ", " + gatedPut + "]}\n" + sweep + suite,
	} {
		got := strings.Join(unhandedStarts(s, []byte(text)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("want %q, got %q", want, got)
		}
	}
}
