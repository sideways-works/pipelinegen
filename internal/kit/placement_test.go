package kit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
)

// bookkeepingFake is a runner whose placement moves a job's bookkeeping tasks to tagged workers,
// adding the gets they need there as concourse-runner does; the other kinds add something more.
func bookkeepingFake(kind string) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{Kind: plugin.KindRunner, Capabilities: []string{plugin.CapPlacement}}),
		plugin.CallPipeline: func(in []byte) (any, error) {
			var req plugin.PipelineRequest
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			_, placed := req.Config["placement"]

			text, err := yaml.Marshal(bookkeepingPipeline(kind, placed))
			if err != nil {
				return nil, fmt.Errorf("pipeline: %w", err)
			}

			if err := os.WriteFile(filepath.Join(req.Work, "p.yml"), text, 0o600); err != nil {
				return nil, fmt.Errorf("pipeline: %w", err)
			}

			return plugin.PipelineAnswer{Files: []string{"p.yml"}}, nil
		},
	}
}

func bookkeepingPipeline(kind string, placed bool) map[string]any {
	params := func() map[string]any { return map[string]any{"params": map[string]any{"STEP": "1"}} }
	task := func(name string) map[string]any {
		return map[string]any{"task": name, "image": "planner", "config": params(), "timeout": "20m"}
	}

	trigger := map[string]any{"get": "trigger", "resource": "branch-main", "trigger": true, "passed": []any{"plan-main"}, "version": "every"}
	gets := []any{trigger, map[string]any{"get": "planner"}}
	resources := []any{map[string]any{"name": "branch-main"}, map[string]any{"name": "planner"}}
	batch, batchEnd, end := task("batch"), task("batch-end"), task("end")
	batch["ensure"] = batchEnd

	if placed {
		copied := map[string]any{"get": "trigger-bookkeeping", "trigger": false, "tags": []any{"tiny"}}
		for _, k := range []string{"resource", "passed", "version"} {
			copied[k] = trigger[k]
		}

		image := map[string]any{"get": "planner-bookkeeping", "resource": "planner", "tags": []any{"tiny"}}
		gets = append(gets, copied, image)

		for _, t := range []map[string]any{batchEnd, end} {
			t["tags"] = []any{"tiny"}
			t["image"] = "planner-bookkeeping"
			t["input_mapping"] = map[string]any{"trigger": "trigger-bookkeeping"}
		}

		switch kind {
		case "runner-bookkeeping-trigger":
			copied["trigger"] = true
		case "runner-bookkeeping-passed":
			copied["passed"] = []any{"plan-nightly"}
		case "runner-bookkeeping-resource":
			resources = append(resources, map[string]any{"name": "planner-tiny"})
			image["resource"] = "planner-tiny"
		case "runner-bookkeeping-params":
			end["config"] = map[string]any{"params": map[string]any{"STEP": "1", "WORKER": "tiny"}}
		case "runner-bookkeeping-image":
			end["image"] = "trigger"
		}
	}

	return map[string]any{
		"resources": resources,
		"jobs": []any{map[string]any{
			"name":   "main-go",
			"plan":   []any{map[string]any{"in_parallel": gets}, batch},
			"ensure": end,
		}},
	}
}

func TestAPlacementMayFetchWhatAMovedTaskNeeds(t *testing.T) {
	for kind, want := range map[string][]string{
		"runner-bookkeeping":          nil,
		"runner-bookkeeping-trigger":  {"job main-go: get trigger-bookkeeping triggers the job"},
		"runner-bookkeeping-passed":   {"job main-go: get trigger-bookkeeping fetches branch-main as no get of the job does without the placement"},
		"runner-bookkeeping-resource": {"get planner-bookkeeping fetches planner-tiny, a resource the pipeline without the placement doesn't declare", "resources[planner-tiny] only with the placement"},
		"runner-bookkeeping-params":   {"jobs[main-go].ensure.config.params.WORKER only with the placement"},
		"runner-bookkeeping-image":    {`jobs[main-go].ensure.image is "planner" without the placement, "trigger" with it`},
	} {
		o := fakeOptions(t, kind, "", false, false)
		o.Config = map[string]any{"placement": map[string]any{"rules": []any{map[string]any{"for": "bookkeeping", "tags": []any{"tiny"}}}}}

		var got *Result

		for _, r := range Run(t.Context(), o) {
			if r.Check == placementCheck {
				got = &r
			}
		}

		if got == nil || got.Skipped {
			t.Errorf("%s: the check didn't run: %v", kind, got)

			continue
		}

		if want == nil && got.Problem != "" {
			t.Errorf("%s: %s", kind, got.Problem)
		}

		if want != nil && !strings.Contains(got.Problem, "differ in more than worker tags") {
			t.Errorf("%s: %q", kind, got.Problem)
		}

		for _, w := range want {
			if !strings.Contains(got.Problem, w) {
				t.Errorf("%s: %q, want %q", kind, got.Problem, w)
			}
		}
	}
}
