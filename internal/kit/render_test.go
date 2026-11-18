package kit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// fakeRenders: "render" renders piperun as it should and fails when a secret's value reaches it,
// "render-loose" answers marks no answer has, "render-badrange" declares a range that
// doesn't parse, "render-unready" refuses a target version its range admits.
func fakeRenders(kind string) []plugin.Render {
	switch kind {
	case "render", "render-loose", "render-unready":
		return []plugin.Render{{Target: "piperun", Versions: ">=1.2 <2", Plugins: map[string]string{"go-test": ">=1.0 <2"}}}
	case "render-badrange":
		return []plugin.Render{{Target: "piperun", Versions: "about 1.2"}}
	default:
		return nil
	}
}

func fakeRender(kind string) func(context.Context, plugin.RenderRequest) (plugin.RenderAnswer, error) {
	return func(_ context.Context, req plugin.RenderRequest) (plugin.RenderAnswer, error) {
		if kind == "render-unready" && req.TargetVersion != "1.2.0" {
			return plugin.RenderAnswer{}, fmt.Errorf("can't render piperun %s", req.TargetVersion) //nolint:err113 // a fake's refusal
		}

		if os.Getenv(kitSecret) != "" || !slices.Contains(req.Secrets, "STRIPE_KEY") {
			return plugin.RenderAnswer{}, fmt.Errorf("secrets %q, and the kit's in the environment: %q", req.Secrets, os.Getenv(kitSecret)) //nolint:err113 // a fake's refusal
		}

		step := map[string]any{"uses": "go-test", "with": req.Step.With, "secrets": req.Secrets, "items": req.Shape.Kind}

		data, err := json.Marshal(step)
		if err != nil {
			return plugin.RenderAnswer{}, fmt.Errorf("render: %w", err)
		}

		a := plugin.RenderAnswer{Steps: []json.RawMessage{data}, Marks: plugin.MarksProtocol, Results: "results", Caches: map[string]string{"gomod": "/go/pkg/mod"}}
		if kind == "render-loose" {
			a.Marks = "bell"
		}

		return a, nil
	}
}

const renderFixture = `{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}], "render": {
	"targets": {"piperun": {"versions": ["1.2.0", "1.9.3"]}},
	"step": {"run": "fake-driver", "with": {"packages": "./..."}},
	"secrets": ["STRIPE_KEY"], "params": {"PROJECTS": "a,b"}}}}`

func checkNamed(results []Result, name string) (Result, bool) {
	for _, r := range results {
		if r.Check == name {
			return r, true
		}
	}

	return Result{}, false
}

func TestARenderingDriverAnswersEachDeclaredTarget(t *testing.T) {
	results := Run(t.Context(), driverOptionsWith(t, "render", renderFixture))
	if failed := failedChecks(results); len(failed) > 0 {
		t.Fatalf("failed:\n%s", strings.Join(failed, "\n"))
	}

	if _, ran := checkNamed(results, renderCheck); !ran {
		t.Fatalf("no check %q", renderCheck)
	}
}

func TestTheKitRefusesABrokenRenderer(t *testing.T) {
	for kind, want := range map[string]string{
		"render-loose":    `marks "bell"`,
		"render-badrange": `renders: target piperun: versions "about 1.2"`,
		"render-unready":  "piperun 1.9.3, pool: ",
	} {
		failed := strings.Join(failedChecks(Run(t.Context(), driverOptionsWith(t, kind, renderFixture))), "\n")
		if !strings.Contains(failed, want) {
			t.Errorf("%s: failed:\n%s\nwant %q", kind, failed, want)
		}
	}
}

func TestARenderingFixtureNamesVersionsInsideTheRange(t *testing.T) {
	for fixture, want := range map[string]string{
		`{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}]}}`:                                                                                  "has no render",
		`{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}], "render": {"targets": {}}}}`:                                                       "piperun: the fixture names no version of it",
		`{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}], "render": {"targets": {"piperun": {"versions": ["2.0.0"]}}}}}`:                     `version "2.0.0" is outside >=1.2 <2`,
		`{"driver": {"slices": [{"name": "a_test"}, {"name": "b_test"}], "render": {"targets": {"piperun": {"versions": ["1.2.0"], "shapes": ["heap"]}}}}}`: `shape "heap"`,
	} {
		r, ran := checkNamed(Run(t.Context(), driverOptionsWith(t, "render", fixture)), renderCheck)
		if !ran || !strings.Contains(r.Problem, want) {
			t.Errorf("%s: %+v, want %q", fixture, r, want)
		}
	}
}

func TestADriverWithoutRendersIsNotAskedToRender(t *testing.T) {
	results := Run(t.Context(), driverOptionsWith(t, "honest", renderFixture))
	if _, ran := checkNamed(results, renderCheck); ran {
		t.Errorf("a driver without renders got %q", renderCheck)
	}

	if failed := failedChecks(results); len(failed) > 0 {
		t.Fatalf("failed:\n%s", strings.Join(failed, "\n"))
	}
}
