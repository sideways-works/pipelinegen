package affected

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

const stepsConfig = `
pipeline: shop
sample_on: [pipelinegen.yaml]
plugins:
  - {name: units, kind: sbom, repository: o/units, release: {version: v1, sha256: ` + stepsSHA + `}}
  - {name: runs, kind: correlator, repository: o/runs, release: {version: v1, sha256: ` + stepsSHA + `}}
  - {name: gotest, kind: driver, repository: o/gotest, release: {version: v1, sha256: ` + stepsSHA + `}}
suites:
  checks:
    tag: check
    sample: 1
    steps:
      - {run: command, take: ["//lint/..."], with: {each: [lint]}}
      - {run: command, take: ["//vet/..."], with: {each: [vet]}}
  unit:
    tests: ["**/*_test.go"]
    bind: recorded
    sample: 1
    steps:
      - {run: gotest}
`

const stepsSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func stepsMeaning(t *testing.T, from, to string) (*config.Config, *config.Meaning) {
	t.Helper()

	base, err := config.Parse([]byte(stepsConfig))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stepsConfig, from) {
		t.Fatalf("no %q", from)
	}

	head, err := config.Parse([]byte(strings.Replace(stepsConfig, from, to, 1)))
	if err != nil {
		t.Fatal(err)
	}

	return head, config.MeaningOf(base, head, "")
}

func planSuite(t *testing.T, o Options, suite string, all []bazel.Slice, rule *SampleRule) SuitePlan {
	t.Helper()

	changes := []gitx.Change{{Path: config.FileName}}
	sp := selectSuite(suite, o.Config.Suites[suite], all, map[string]bool{}, false)
	on := sampleOnHit(o.Config, suite, o.beyondConfig(changes))

	cfgd := o.configSelect(&sp, suite, all, "head", rule)
	sampleSuite(&sp, o.Config.Suites[suite], suite, all, map[string]bool{}, rule, "0123456789abcdef", on, changes, entrySide{
		reached: func(sl bazel.Slice) bool { return cfgd.kept[sl.Name] }, pool: cfgd.pool, drivers: len(cfgd.pool) > 0,
	})
	driverNarrowed(&sp, cfgd, rule)

	return sp
}

func sliceNames(ss []bazel.Slice) []string {
	out := []string{}
	for _, sl := range ss {
		out = append(out, sl.Name)
	}

	return out
}

func TestAStepsChangeRunsWhatTheStepTakes(t *testing.T) {
	cfg, m := stepsMeaning(t, "with: {each: [lint]}", "with: {each: [lint, --strict]}")
	o := Options{Config: cfg, Meaning: m}

	all := []bazel.Slice{
		{Name: "lint.a", Label: "//lint/a:check"}, {Name: "lint.b", Label: "//lint/b:check"},
		{Name: "vet.a", Label: "//vet/a:check"}, {Name: "vet.b", Label: "//vet/b:check"},
	}

	rule := &SampleRule{N: 1, Group: func(string) string { return "g" }}
	sp := planSuite(t, o, "checks", all, rule)

	if got := sliceNames(sp.Selected); !slices.Equal(got, []string{"lint.a", "lint.b"}) || len(sp.Narrowed) != 0 {
		t.Fatalf("selected %v, narrowed %+v", got, sp.Narrowed)
	}

	rs := sp.Reasons["//lint/a:check"]
	if len(rs) != 1 || rs[0].Kind != KindConfig || rs[0].What != "suites.checks.steps[0] (run command), which takes it, changed" {
		t.Errorf("reasons %+v", rs)
	}
}

func TestAnUnderstoodConfigIsNoSampleOnFile(t *testing.T) {
	all := []bazel.Slice{{Name: "lint.a", Label: "//lint/a:check"}, {Name: "vet.a", Label: "//vet/a:check"}}
	rule := &SampleRule{N: 1, Group: func(string) string { return "g" }}

	cfg, m := stepsMeaning(t, "    sample: 1\n    steps:\n      - {run: command, take: [\"//lint", "    sample: 2\n    steps:\n      - {run: command, take: [\"//lint")
	if sp := planSuite(t, Options{Config: cfg, Meaning: m}, "checks", all, rule); len(sp.Selected) != 0 {
		t.Errorf("an understood change selected %v", sliceNames(sp.Selected))
	}

	cfg, m = stepsMeaning(t, "    tag: check\n", "    tag: check\n    minutes: 30\n")
	if sp := planSuite(t, Options{Config: cfg, Meaning: m}, "checks", all, rule); len(sp.Selected) != 1 || len(sp.Narrowed) != 1 || sp.Narrowed[0].Narrowing != SampledByConfig {
		t.Errorf("a change it doesn't understand: %v, %+v", sliceNames(sp.Selected), sp.Narrowed)
	}
}

func TestADriversEntryReachesTheSlicesItRuns(t *testing.T) {
	cfg, m := stepsMeaning(t, "repository: o/gotest, release: {version: v1,", "repository: o/gotest, release: {version: v2,")

	all := []bazel.Slice{
		{Name: "a_test.go", Label: bazel.SelectorLabel("a_test.go")}, {Name: "b_test.go", Label: bazel.SelectorLabel("b_test.go")},
		{Name: "c_test.go", Label: bazel.SelectorLabel("c_test.go")}, {Name: "new_test.go", Label: bazel.SelectorLabel("new_test.go")},
	}

	o := Options{Config: cfg, Meaning: m, Unlisted: func(string, string) []string { return []string{"new_test.go"} }}

	on := &SampleRule{N: 1, EntryOn: true, OffAt: map[string]int{SampledByPluginEntry: 4}, Group: func(string) string { return "g" }}
	sp := planSuite(t, o, "unit", all, on)

	if got := sliceNames(sp.Selected); len(got) != 2 || !slices.Contains(got, "new_test.go") {
		t.Errorf("plugin-entry on: selected %v", got)
	}

	if len(sp.Narrowed) != 1 || sp.Narrowed[0].Narrowing != SampledByPluginEntry || len(sp.Narrowed[0].Dropped) != 2 || sp.Narrowed[0].OffAt != 4 ||
		!strings.HasPrefix(sp.Narrowed[0].Reason, "the entry of plugin gotest changed") {
		t.Errorf("plugin-entry on: narrowed %+v", sp.Narrowed)
	}

	sp = planSuite(t, o, "unit", all, &SampleRule{N: 1, Group: func(string) string { return "g" }})
	if got := sliceNames(sp.Selected); len(got) != 4 || len(sp.Narrowed) != 0 {
		t.Errorf("plugin-entry off: selected %v, narrowed %+v", got, sp.Narrowed)
	}

	if rs := sp.Reasons[bazel.SelectorLabel("a_test.go")]; len(rs) != 1 || rs[0].Kind != KindPlugin || rs[0].What != "gotest" {
		t.Errorf("plugin-entry off: reasons %+v", rs)
	}
}

func TestASliceTheConfigAddsRuns(t *testing.T) {
	one := bazel.Slice{Name: "one", Label: "//s:one"}
	two := bazel.Slice{Name: "two", Label: "//s:two"}

	own := &Plan{Suites: []SuitePlan{
		{Suite: "s", Selected: []bazel.Slice{}, Untouched: []string{"//s:one", "//s:two"}, slices: []bazel.Slice{one, two}},
		{Suite: "fresh", Selected: []bazel.Slice{}, Untouched: []string{"//s:one"}, slices: []bazel.Slice{one}},
	}}
	byBase := &Plan{Suites: []SuitePlan{{Suite: "s", Selected: []bazel.Slice{}, Untouched: []string{"//s:one"}}}}

	got := Union(own, byBase)

	for _, c := range []struct{ suite, want, why string }{
		{"s", "two", AddedSliceReason},
		{"fresh", "one", AddedSuiteReason},
	} {
		i := slices.IndexFunc(got.Suites, func(s SuitePlan) bool { return s.Suite == c.suite })
		sp := got.Suites[i]

		if names := sliceNames(sp.Selected); !slices.Equal(names, []string{c.want}) {
			t.Errorf("%s selected %v", c.suite, names)

			continue
		}

		if rs := sp.Reasons[sp.Selected[0].Label]; len(rs) != 1 || rs[0].Kind != KindNewSlice || rs[0].What != c.why {
			t.Errorf("%s: reasons %+v", c.suite, rs)
		}
	}
}
