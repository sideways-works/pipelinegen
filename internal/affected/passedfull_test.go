package affected

import (
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func allPlan(suites map[string][]bazel.Slice) *Plan {
	p := &Plan{All: true, Reason: "every slice runs in this run"}

	names := make([]string, 0, len(suites))
	for name := range suites {
		names = append(names, name)
	}

	slices.Sort(names)

	for _, name := range names {
		p.Suites = append(p.Suites, SuitePlan{Suite: name, All: true, Reason: "every slice runs in this run", Selected: suites[name], Untouched: []string{}})
	}

	return p
}

func TestAFullRunLeavesOutWhatPassedWithinItsWindow(t *testing.T) {
	cfg := &config.Config{Suites: map[string]config.Suite{
		"unit":     {},
		"coverage": {WholeSuite: []config.WholeSuiteCheck{{Name: "report"}}},
	}}
	p := allPlan(map[string][]bazel.Slice{
		"unit":     passSlices("a", "b", "c"),
		"coverage": passSlices("x", "y"),
	})

	// a and b passed since, unreached; c didn't; the coverage suite's slices passed too, but its check
	// needs every one of them
	rb := passedAt(false, map[string]string{"a": "c1", "b": "c2", "x": "c1", "y": "c1"})

	if !KeepPassed(p, cfg, rb) {
		t.Fatal("nothing left out")
	}

	if p.All || p.Reason != "" {
		t.Fatalf("the plan still says every slice runs: %v %q", p.All, p.Reason)
	}

	for _, sp := range p.Suites {
		switch sp.Suite {
		case "unit":
			if got := sliceNames(sp.Selected); !slices.Equal(got, []string{"c"}) || sp.All || len(sp.Untouched) != 2 {
				t.Fatalf("unit runs %v, untouched %v", got, sp.Untouched)
			}

			if len(sp.Narrowed) != 2 || sp.Narrowed[0].Narrowing != plugins.NarrowPassedSince || sp.Narrowed[0].Reached != 3 {
				t.Fatalf("unit's narrowed %+v", sp.Narrowed)
			}
		case "coverage":
			if !sp.All || len(sp.Selected) != 2 {
				t.Fatalf("a suite with a whole-suite check left slices out: %+v", sp)
			}
		}
	}
}

func TestAPlanOfWhatChangedIsNoFullRunsToKeep(t *testing.T) {
	p := allPlan(map[string][]bazel.Slice{"unit": passSlices("a")})
	p.All = false

	if KeepPassed(p, &config.Config{Suites: map[string]config.Suite{"unit": {}}}, passedAt(false, map[string]string{"a": "c1"})) {
		t.Fatal("left out of a plan that isn't of every slice")
	}
}
