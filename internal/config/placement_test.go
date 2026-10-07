package config

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestRetiredPlacementIsRefused(t *testing.T) {
	text := `pipeline: shop
concourse:
  planner: {tags: [planner]}
  resource_tags: [cloud-1]
  bookkeeping: {tags: []}
suites:
  e2e: {tests: [e2e/*.spec.ts], run_slice: ci/e2e.yml, run_on: {tags: [browsers, big], max_in_flight: 2}}
  go: {tests: ["**/*_test.go"], run_slice: ci/go.yml, run_on: {serial_group: go}}
plugins:
  - {name: ci, kind: runner, repository: s/runner, release: {version: v1, sha256: "1111111111111111111111111111111111111111111111111111111111111111"}}
`

	_, err := Parse([]byte(text))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}

	want := []string{
		"concourse.planner.tags: placement is the runner's: plugins.ci.config.placement.rules: {for: pipeline, tags: [planner]} and {for: planning, tags: [planner]}",
		"concourse.resource_tags: placement is the runner's: plugins.ci.config.placement.rules: {for: checks, tags: [cloud-1]}",
		"concourse.bookkeeping.tags: placement is the runner's: plugins.ci.config.placement.rules: {for: bookkeeping, tags: []}",
		"suites.e2e.run_on.tags: placement is the runner's: plugins.ci.config.placement.rules: {for: suite, suite: e2e, tags: [browsers, big]}",
	}

	var got []string
	for line := range strings.SplitSeq(err.Error(), "\n") {
		got = append(got, strings.TrimPrefix(line, ErrInvalid.Error()+": "))
	}

	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	moved := strings.NewReplacer(
		"  planner: {tags: [planner]}\n", "", "  resource_tags: [cloud-1]\n", "", "  bookkeeping: {tags: []}\n", "",
		"tags: [browsers, big], ", "",
	).Replace(text)

	c, err := Parse([]byte(moved))
	if err != nil {
		t.Fatal(err)
	}

	if c.Suites["e2e"].RunOn.MaxInFlight != 2 || c.Suites["go"].RunOn.SerialGroup != "go" {
		t.Errorf("run_on: %+v %+v", c.Suites["e2e"].RunOn, c.Suites["go"].RunOn)
	}
}

func TestRetiredBookkeepingWithoutTags(t *testing.T) {
	_, err := Parse([]byte("pipeline: x\nconcourse: {bookkeeping: {}}\nsuites: {a: {tag: t, run_slice: r}}\n"))
	if err == nil || !strings.Contains(err.Error(), "concourse.bookkeeping: placement is the runner's: remove it") {
		t.Errorf("got %v", err)
	}
}

func TestHistoryReadsRetiredPlacement(t *testing.T) {
	c, err := ParseHistory([]byte("pipeline: x\nconcourse: {resource_tags: [a]}\nsuites: {a: {tag: t, run_slice: r, run_on: {tags: [b]}}}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if len(c.Warnings) == 0 {
		t.Error("no warning")
	}
}

const runnerBase = trustedBase + `  - name: ci
    kind: runner
    repository: s/runner
    release: {version: v1, sha256: "3333333333333333333333333333333333333333333333333333333333333333"}
    config:
      placement:
        default: [small]
        rules: [{for: suite, suite: e2e, tags: [browsers]}]
`

func TestRunnerConfigIsTrusted(t *testing.T) {
	trusted := parseTrusted(t, runnerBase).Trusted()

	moved := parseTrusted(t, strings.Replace(runnerBase, "tags: [browsers]", "tags: [big]", 1))
	if edited := moved.WithTrusted(trusted); !slices.Equal(edited, []string{"plugins.ci.config"}) {
		t.Fatalf("edited %v", edited)
	}

	r, _ := moved.Runner()
	if got, err := json.Marshal(r.Config); err != nil || !strings.Contains(string(got), `"tags":["browsers"]`) {
		t.Errorf("the change's own block stayed: %s %v", got, err)
	}

	reordered := parseTrusted(t, strings.Replace(runnerBase,
		"        default: [small]\n        rules: [{for: suite, suite: e2e, tags: [browsers]}]\n",
		"        rules: [{tags: [browsers], suite: e2e, for: suite}]\n        default: [small]\n", 1))
	if edited := reordered.WithTrusted(trusted); len(edited) > 0 {
		t.Errorf("reordered: edited %v", edited)
	}
}

func TestRunnerConfigFromAnOlderPipeline(t *testing.T) {
	trusted := parseTrusted(t, runnerBase).Trusted()
	trusted.RunnerConfig = nil

	c := parseTrusted(t, strings.Replace(runnerBase, "tags: [browsers]", "tags: [big]", 1))
	if edited := c.WithTrusted(trusted); len(edited) > 0 {
		t.Errorf("edited %v", edited)
	}

	if r, _ := c.Runner(); r.Config == nil {
		t.Error("the change's own block is gone")
	}
}
