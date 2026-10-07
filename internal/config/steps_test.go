package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const withSteps = `
pipeline: shop
plugins:
  - {name: go, kind: driver, repository: o/go-driver, release: {version: v1, sha256: ` + sha + `}, config: {timeout: 60m}}
  - {name: bazel, kind: driver, repository: o/bazel-driver, release: {version: v1, sha256: ` + sha + `}}
suites:
  go:
    tests: ["**/*_test.go"]
    image: golang
    params: {GOFLAGS: -trimpath}
    steps:
      - run: go
        batch: true
        with: {tags: [cover]}
  smoke:
    tests: ["**/*_smoke.sh"]
    steps:
      - {prepare: command, image: golang, with: {run: [go, build, .]}}
      - {setup: command, with: {start: [server]}}
      - {run: command, take: ["web/**"], with: {each: [sh, "{source}"]}}
      - {run: bazel, take: ["kind:sh_test", "//api/..."]}
`

const sha = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseSteps(t *testing.T) {
	c, err := Parse([]byte(withSteps))
	if err != nil {
		t.Fatal(err)
	}

	g := c.Suites["go"]
	if !g.HasSteps() || len(g.StepsOf(StepRun)) != 1 || !g.Steps[0].Bundles() {
		t.Fatalf("go: %+v", g.Steps)
	}

	if conf := c.DriverConfig(g.Steps[0]); conf["timeout"] != "60m" || conf["tags"] == nil {
		t.Fatalf("config: %v", conf)
	}

	s := c.Suites["smoke"]
	if got := []string{s.Steps[0].Kind(), s.Steps[1].Kind(), s.Steps[2].Kind()}; !slices.Equal(got, []string{StepPrepare, StepSetup, StepRun}) {
		t.Fatalf("kinds: %v", got)
	}

	if s.Steps[2].Bundles() {
		t.Fatal("batch is off unless a step says so")
	}
}

func TestStepsRoute(t *testing.T) {
	c, err := Parse([]byte(withSteps))
	if err != nil {
		t.Fatal(err)
	}

	s := c.Suites["smoke"]

	cases := []struct {
		sl   RoutedSlice
		step int
		ok   bool
	}{
		{RoutedSlice{Name: "web/a_smoke.sh"}, 2, true},
		{RoutedSlice{Name: "x", Label: "//cli:x", Kind: "sh_test"}, 3, true},
		{RoutedSlice{Name: "y", Label: "//api/v1:y", Kind: "go_test"}, 3, true},
		{RoutedSlice{Name: "z", Label: "//apis:z", Kind: "go_test"}, -1, false},
		{RoutedSlice{Name: "other/b_smoke.sh"}, -1, false},
	}

	for _, tc := range cases {
		if i, ok := s.RunStepFor(tc.sl); i != tc.step || ok != tc.ok {
			t.Errorf("%+v: step %d %v, want %d %v", tc.sl, i, ok, tc.step, tc.ok)
		}
	}

	if i, ok := c.Suites["go"].RunStepFor(RoutedSlice{Name: "anything"}); i != 0 || !ok {
		t.Fatalf("only step: %d %v", i, ok)
	}
}

func TestTwoStepsTakingOneSliceRouteItNowhere(t *testing.T) {
	st := Suite{Steps: []Step{{Run: "a", Take: []string{"**"}}, {Run: "b", Take: []string{"x/**"}}}}

	if _, ok := st.RunStepFor(RoutedSlice{Name: "x/a"}); ok {
		t.Fatal("a slice two run steps take was routed")
	}
}

func TestParseRejectsSteps(t *testing.T) {
	head := "pipeline: x\nplugins: [{name: d, kind: driver, repository: o/d, release: {version: v1, sha256: " + sha + "}}]\nsuites: {a: {tests: [x], "
	cases := map[string]string{
		"both":             head + "run_slice: r, steps: [{run: d}]}}",
		"two drivers":      head + "steps: [{run: d, setup: d}]}}",
		"no driver":        head + "steps: [{batch: true}]}}",
		"unknown driver":   head + "steps: [{run: nope}]}}",
		"order":            head + "steps: [{run: d}, {setup: d}]}}",
		"batch on setup":   head + "steps: [{setup: d, batch: true}, {run: d}]}}",
		"take on prepare":  head + "steps: [{prepare: d, take: [x]}, {run: d}]}}",
		"image on run":     head + "steps: [{run: d, image: golang}]}}",
		"no run step":      head + "steps: [{setup: d}]}}",
		"two without take": head + "steps: [{run: d}, {run: command}]}}",
		"bad take":         head + "steps: [{run: d, take: ['a/[']}]}}",
		"empty kind":       head + "steps: [{run: d, take: ['kind:']}]}}",
		"named command":    "pipeline: x\nplugins: [{name: command, kind: driver, repository: o/d, release: {version: v1, sha256: " + sha + "}}]\nsuites: {a: {tests: [x], steps: [{run: command}]}}",
	}

	for name, text := range cases {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := Parse([]byte(head + "steps: [{run: command}]}}")); err != nil {
		t.Fatalf("the built-in driver needs no entry: %v", err)
	}
}

func TestAChangesStepsAreTheTrustedOnes(t *testing.T) {
	trusted, err := Parse([]byte(withSteps))
	if err != nil {
		t.Fatal(err)
	}

	blob, err := json.Marshal(trusted.Trusted())
	if err != nil {
		t.Fatal(err)
	}

	edited := strings.Replace(withSteps, "with: {tags: [cover]}", "with: {tags: [cover], flags: [-exec, evil]}", 1)
	edited = strings.Replace(edited, "config: {timeout: 60m}", "config: {timeout: 1s}", 1)

	own, err := Parse([]byte(edited))
	if err != nil {
		t.Fatal(err)
	}

	var tr Trusted
	if err := json.Unmarshal(blob, &tr); err != nil {
		t.Fatal(err)
	}

	keys := own.WithTrusted(tr)

	if !slices.Contains(keys, "suites.*.steps") || !slices.Contains(keys, "plugins") {
		t.Fatalf("edited keys: %v", keys)
	}

	conf := own.DriverConfig(own.Suites["go"].Steps[0])
	if conf["flags"] != nil || conf["timeout"] != "60m" {
		t.Fatalf("the change's own steps or driver config stayed: %v", conf)
	}
}
