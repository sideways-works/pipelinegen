package config

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const example = `
pipeline: shop
bazel: {bazelisk: tools/bin/bazelisk}
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    exclude: [e2e/nightly-only.spec.ts]
    owner: "//{dir}:{stem}"
    extra_deps: [{tests: [e2e/access-*.spec.ts], deps: ["//backend:access"]}]
    group: "^e2e/([a-z]+)"
    prepare: ci/tasks/e2e-prepare.yml
    run_slice: ci/tasks/e2e-slice.yml
    run_on: {max_in_flight: 2}
    lock: {name: browsers, minutes: 90}
    whole_suite:
      - name: reconcile
        task: ci/tasks/reconcile.yml
        also_on: ["//backend/ledger"]
  backend:
    tag: slice-backend
    run_slice: ci/tasks/backend-slice.yml
everything_on: [pipelinegen.yaml, ci/tasks/**, MODULE.bazel]
`

func TestParseExample(t *testing.T) {
	c, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}

	if c.Bazel.Workspace != "." || c.Bazel.Bazelisk != "tools/bin/bazelisk" {
		t.Fatalf("bazel: %+v", c.Bazel)
	}

	if got := strings.Join(c.SuiteNames(), ","); got != "backend,e2e" {
		t.Fatalf("suite order: %s", got)
	}

	e2e := c.Suites["e2e"]
	if !e2e.Implicit() || e2e.ExtraDeps[0].Deps[0] != "//backend:access" || e2e.Lock.Name != "browsers" || e2e.WholeSuite[0].AlsoOn[0] != "//backend/ledger" {
		t.Fatalf("e2e: %+v", e2e)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":    "pipeline: x\nsuites: {a: {tag: t, run_slice: r, typo: 1}}",
		"no pipeline":    "suites: {a: {tag: t, run_slice: r}}",
		"no suites":      "pipeline: x",
		"no tag":         "pipeline: x\nsuites: {a: {run_slice: r}}",
		"bad group":      "pipeline: x\nsuites: {a: {tests: [x], group: '(', run_slice: r}}",
		"shared tag":     "pipeline: x\nsuites: {a: {tag: t, run_slice: r}, b: {tag: t, run_slice: r}}",
		"bad tag":        "pipeline: x\nsuites: {a: {tag: 't\"x', run_slice: r}}",
		"bad suite name": "pipeline: x\nsuites: {A_b: {tag: t, run_slice: r}}",
		"no run_slice":   "pipeline: x\nsuites: {a: {tag: t}}",
		"bad pattern":    "pipeline: x\nsuites: {a: {tag: t, run_slice: r}}\neverything_on: ['ci/[']",
		"empty pattern":  "pipeline: x\nsuites: {a: {tag: t, run_slice: r}}\neverything_on: ['']",
		"whole suite":    "pipeline: x\nsuites: {a: {tag: t, run_slice: r, whole_suite: [{name: n}]}}",
		"text no paths":  "pipeline: x\nsuites: {a: {tests: [x], run_slice: r, text_inputs: [{tests: [x]}]}}",
		"text pattern":   "pipeline: x\nsuites: {a: {tests: [x], run_slice: r, text_inputs: [{paths: ['src/[']}]}}",
	}
	for name, text := range cases {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	_, err := Parse([]byte("pipeline: x\nsuites: {a: {run_slice: r}}"))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("not ErrInvalid: %v", err)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"ci/tasks/**", "ci/tasks/e2e.yml", true},
		{"ci/tasks/**", "ci/tasks/sub/x.sh", true},
		{"ci/tasks/**", "ci/pipeline.yml", false},
		{"ci/tasks/", "ci/tasks/a/b", true},
		{"MODULE.bazel", "MODULE.bazel", true},
		{"MODULE.bazel", "sub/MODULE.bazel", false},
		{"**/BUILD.bazel", "BUILD.bazel", true},
		{"**/BUILD.bazel", "a/b/BUILD.bazel", true},
		{"*.bzl", "defs.bzl", true},
		{"*.bzl", "tools/defs.bzl", false},
		{"**/*.bzl", "tools/defs.bzl", true},
		{"a/**/z", "a/z", true},
		{"a/**/z", "a/b/c/z", true},
		{"a/**/z", "a/b/c/y", false},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pattern, c.name); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v", c.pattern, c.name, got)
		}
	}

	cfg := &Config{EverythingOn: []string{"docs/x", "ci/**"}}
	if p, ok := cfg.MatchesEverythingOn("ci/tasks/a"); !ok || p != "ci/**" {
		t.Fatalf("MatchesEverythingOn: %q %v", p, ok)
	}

	if _, ok := cfg.MatchesEverythingOn("web/a"); ok {
		t.Fatal("matched web/a")
	}
}

const withRuns = example + `
github: {repository: o/r, token: gh}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  pr:
    trigger: {pull_requests: {base: main, draft: false}}
    compare: merged_target
    select: affected
    suites: {e2e: {params: {PROJECTS: two}}, backend: {}}
    report: {comment: {format: list}}
  nightly:
    trigger: {timer: {start: "3:00", stop: "4:00"}, branch: main}
    compare: none
    select: all
    suites: {e2e: {params: {PROJECTS: all}}}
    record: true
`

func TestParseRuns(t *testing.T) {
	c, err := Parse([]byte(withRuns))
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(c.RunNames(), ","); got != "nightly,pr" {
		t.Fatalf("runs: %s", got)
	}

	if !c.Runs["pr"].Report.Statuses() || c.Concourse.Team != "main" {
		t.Fatal("defaults")
	}

	if p := c.Params("nightly", "e2e"); p["PROJECTS"] != "all" {
		t.Fatalf("params: %v", p)
	}
}

func TestParseRejectsRuns(t *testing.T) {
	cases := map[string][2]string{
		"two triggers":         {"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {pull_requests: {base: main}, manual: true, branch: main}"},
		"no trigger":           {"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {}"},
		"no pr base":           {"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {pull_requests: {}}"},
		"merged without prs":   {"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {branch: main}"},
		"bad compare":          {"compare: merged_target", "compare: sometimes"},
		"bad select":           {"select: affected", "select: some"},
		"unknown suite":        {"backend: {}}\n    report", "nope: {}}\n    report"},
		"bad format":           {"format: list", "format: pie"},
		"bad timer":            {`start: "3:00"`, `start: "25:00"`},
		"release and commit":   {"bootstrap: {release:", "bootstrap: {commit: 0000000000000000000000000000000000000000, release:"},
		"no bazelisk":          {"bazelisk: {version: v1,", "bazelisk: {"},
		"a secret in params":   {"backend: {}}\n    report", "backend: {params: {T: ((gh))}}}\n    report"},
		"an own param":         {"backend: {}}\n    report", "backend: {params: {PIPELINEGEN_RUN: x}}}\n    report"},
		"a param and a secret": {"backend: {}}\n    report", "backend: {params: {T: x}, secrets: {batch: {T: gh}}}}\n    report"},
		"an own secret":        {"backend: {}}\n    report", "backend: {secrets: {prepare: {PIPELINEGEN_X: gh}}}}\n    report"},
		"a secret's var":       {"backend: {}}\n    report", "backend: {secrets: {whole: {T: ((gh))}}}}\n    report"},
		"planner git token":    {"planner: {", "planner: {git_token: T, "},
		"timer without branch": {"stop: \"4:00\"}, branch: main}", "stop: \"4:00\"}}"},
		"bad missed_after":     {"draft: false}}", "draft: false, missed_after: soon}}"},
		"a no missed_after":    {"draft: false}}", "draft: false, missed_after: 0s}}"},
	}
	for name, c := range cases {
		text := strings.Replace(withRuns, c[0], c[1], 1)
		if text == withRuns {
			t.Fatalf("%s: nothing replaced", name)
		}

		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSerialGroups(t *testing.T) {
	text := strings.Replace(withRuns, "run_on: {max_in_flight: 2}", "run_on: {max_in_flight: 2, serial_group: proof}", 1)
	ok := strings.Replace(text, `concourse: {url: "https://ci",`, `concourse: {serial_groups: {proof: {per_run: true}}, url: "https://ci",`, 1)

	c, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}

	if !c.Concourse.SerialGroups["proof"].PerRun {
		t.Fatalf("serial groups %v", c.Concourse.SerialGroups)
	}

	typo := strings.Replace(text, `concourse: {url: "https://ci",`, `concourse: {serial_groups: {prof: {per_run: true}}, url: "https://ci",`, 1)
	if _, err := Parse([]byte(typo)); err == nil || !strings.Contains(err.Error(), "serial_groups.prof") {
		t.Fatalf("a group no suite names: %v", err)
	}
}

func TestMissedAfter(t *testing.T) {
	c, err := Parse([]byte(withRuns))
	if err != nil {
		t.Fatal(err)
	}

	if got := c.Runs["pr"].Trigger.PullRequests.Missed(); got != 10*time.Minute {
		t.Fatalf("default %v", got)
	}

	if c, err = Parse([]byte(strings.Replace(withRuns, "draft: false}}", "draft: false, missed_after: 3m}}", 1))); err != nil {
		t.Fatal(err)
	}

	if got := c.Runs["pr"].Trigger.PullRequests.Missed(); got != 3*time.Minute {
		t.Fatalf("3m: %v", got)
	}
}

func TestDraftsAreLeftOutByDefault(t *testing.T) {
	cases := map[string]string{"": "false", "draft: false, ": "false", "draft: true, ": "true", "draft: any, ": "<nil>"}

	for set, want := range cases {
		text := strings.Replace(withRuns, "pull_requests: {base: main, draft: false}", "pull_requests: {"+set+"base: main}", 1)

		c, err := Parse([]byte(text))
		if err != nil {
			t.Fatalf("%q: %v", set, err)
		}

		got := "<nil>"
		if w := c.Runs["pr"].Trigger.PullRequests.Draft.Wanted(); w != nil {
			got = strconv.FormatBool(*w)
		}

		if got != want {
			t.Errorf("%q: wanted draft state %s, want %s", set, got, want)
		}
	}

	if _, err := Parse([]byte(strings.Replace(withRuns, "draft: false}", "draft: sometimes}", 1))); err == nil {
		t.Error("draft: sometimes was taken")
	}
}

func TestUncovered(t *testing.T) {
	c, err := Parse([]byte(withRuns + "untested:\n  - {paths: [docs/**], reason: prose}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if c.UncoveredFor("pr") != UncoveredEverything {
		t.Fatal("default")
	}

	c, err = Parse([]byte(strings.Replace(withRuns, "    record: true\n", "    record: true\n    uncovered: everything\n", 1) + "uncovered: fail\n"))
	if err != nil {
		t.Fatal(err)
	}

	if c.UncoveredFor("pr") != UncoveredFail || c.UncoveredFor("nightly") != UncoveredEverything {
		t.Fatal("override")
	}

	for _, bad := range []string{"uncovered: maybe\n", "untested:\n  - {paths: [docs/**]}\n", "untested:\n  - {paths: ['['], reason: x}\n"} {
		if _, err := Parse([]byte(withRuns + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestRebuild(t *testing.T) {
	const plugins = `plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v0, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}
`

	c, err := Parse([]byte(withRuns + plugins))
	if err != nil {
		t.Fatal(err)
	}

	if !c.Rebuilds("nightly") || c.Rebuilds("pr") || c.RebuildCheck != RebuildError {
		t.Fatalf("nightly %t, pr %t, check %q", c.Rebuilds("nightly"), c.Rebuilds("pr"), c.RebuildCheck)
	}

	if c, err := Parse([]byte(withRuns)); err != nil || c.Rebuilds("nightly") {
		t.Fatalf("without plugins: %v", err)
	}

	if c, err := Parse([]byte(strings.Replace(withRuns, "    record: true\n", "", 1) + plugins)); err != nil || c.Rebuilds("nightly") {
		t.Fatalf("a run of everything that doesn't record: %v", err)
	}

	if c, err := Parse([]byte(withRuns + plugins + "rebuild_check: warning\n")); err != nil || c.RebuildCheck != RebuildWarning {
		t.Fatalf("warning: %v", err)
	}

	if _, err := Parse([]byte(withRuns + "rebuild_check: maybe\n")); err == nil {
		t.Error("accepted rebuild_check: maybe")
	}
}

func TestFollow(t *testing.T) {
	text := strings.Replace(withRuns, "    tag: slice-backend\n", "    tag: slice-backend\n    follow: dependents\n    follow_depth: 3\n", 1)
	text = strings.Replace(text, "    record: true\n", "    record: true\n    follow: executed\n", 1)

	c, err := Parse([]byte(text + "follow_depth: 5\n"))
	if err != nil {
		t.Fatal(err)
	}

	if f, d := c.FollowFor("pr", "e2e"); f != FollowExecuted || d != 5 {
		t.Fatalf("e2e: %s %d", f, d)
	}

	if f, d := c.FollowFor("pr", "backend"); f != FollowDependents || d != 3 {
		t.Fatalf("backend: %s %d", f, d)
	}

	if f, _ := c.FollowFor("nightly", "backend"); f != FollowExecuted {
		t.Fatalf("run override: %s", f)
	}

	for _, bad := range []string{"follow: wide\n", "follow_depth: -1\n"} {
		if _, err := Parse([]byte(withRuns + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestKeep(t *testing.T) {
	c, err := Parse([]byte(withRuns))
	if err != nil {
		t.Fatal(err)
	}

	if c.Keep.Runs.For != "7d" || c.Keep.Lists.For != "90d" {
		t.Fatalf("defaults %+v", c.Keep)
	}

	c, err = Parse([]byte(strings.Replace(withRuns, "    record: true\n", "    record: true\n    keep: {last: 3}\n", 1) + "keep: {runs: {for: 5d, last: 20}, lists: {last: 50}}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if d, ok := c.KeepRuns("pr").For.Duration(); !ok || d != 5*24*time.Hour || c.KeepRuns("nightly").Last != 3 {
		t.Fatalf("keep %+v %+v", c.KeepRuns("pr"), c.KeepRuns("nightly"))
	}

	for _, p := range []Period{"12h", "2w"} {
		if _, ok := p.Duration(); !ok {
			t.Errorf("%s", p)
		}
	}

	for _, bad := range []string{"keep: {runs: {for: 5x}}\n", "keep: {runs: {last: -1}}\n", "keep: {lists: {for: 0d}}\n"} {
		if _, err := Parse([]byte(withRuns + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestKeys(t *testing.T) {
	keys := map[string]KeyDoc{}
	for _, k := range Keys() {
		keys[k.Path] = k
	}

	for _, want := range []string{
		"pipeline", "suites.<name>.tag", "suites.<name>.lock.slots", "suites.<name>.whole_suite[].also_on",
		"runs.<name>.trigger.pull_requests.once_per_ready", "concourse.planner.bootstrap.release.sha256",
		"keep.runs.for", "learn.modules", "untested[].reason",
	} {
		if _, ok := keys[want]; !ok {
			t.Errorf("no key %s", want)
		}
	}

	for path := range defaults {
		if _, ok := keys[path]; !ok {
			t.Errorf("default for unknown key %s", path)
		}
	}

	if keys["suites.<name>.parallel"].Default != "4" || keys["keep.runs.for"].Type != "config.Period" {
		t.Fatalf("%+v %+v", keys["suites.<name>.parallel"], keys["keep.runs.for"])
	}
}

func TestOwnerOf(t *testing.T) {
	s := Suite{Owner: "//{dir}:{stem}.spec"}
	if got, ok := s.OwnerOf("e2e/console/checkout.spec.ts"); !ok || got != "//e2e/console:checkout.spec" {
		t.Fatalf("%q %v", got, ok)
	}

	s = Suite{Owner: "//{dir}:{base} {file} {stem}"}
	if got, _ := s.OwnerOf("a/orders_test.go"); got != "//a:orders_test.go a/orders_test.go orders_test" {
		t.Fatalf("%q", got)
	}

	if got, _ := s.OwnerOf("root_test.go"); got != "//:root_test.go root_test.go root_test" {
		t.Fatalf("root: %q", got)
	}

	if _, ok := (Suite{}).OwnerOf("x"); ok {
		t.Fatal("no template, an owner")
	}
}

func TestPlugins(t *testing.T) {
	sum := strings.Repeat("a", 64)
	ok := withRuns + "plugins:\n  - {name: go, kind: correlator, repository: o/go, release: {version: v1, sha256: " + sum + "}, config: {modules: [x]}}\n" +
		"  - {name: ci, kind: runner, repository: o/ci, commit: " + strings.Repeat("0", 40) + "}\n"

	c, err := Parse([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}

	if r, found := c.Runner(); !found || r.Name != "ci" {
		t.Fatalf("runner %+v", r)
	}

	for _, bad := range []string{
		"plugins:\n  - {name: x, kind: linter, repository: o/x, commit: " + strings.Repeat("0", 40) + "}\n",
		"plugins:\n  - {name: x, kind: sbom, repository: o/x}\n",
		"plugins:\n  - {name: x, kind: runner, repository: o/x, commit: abc}\n",
		"plugins:\n  - {name: a, kind: runner, repository: o/a, commit: " + strings.Repeat("0", 40) + "}\n  - {name: b, kind: runner, repository: o/b, commit: " + strings.Repeat("1", 40) + "}\n",
		"plugins:\n  - {name: x, kind: sbom, repository: o/x, release: {version: v1, sha256: " + sum + ", sums: " + sum + "}}\n",
		"plugins:\n  - {name: x, kind: sbom, repository: o/x, release: {version: v1, sums: abc}}\n",
	} {
		if _, err := Parse([]byte(withRuns + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}

	c, err = Parse([]byte(withRuns + "plugins:\n  - {name: x, kind: sbom, repository: o/x, release: {version: v1, sums: " + strings.Repeat("b", 64) + "}}\n"))
	if err != nil || c.Plugins[0].Release.Pin() != strings.Repeat("b", 64) {
		t.Fatalf("pinned by sums: %v", err)
	}
}

func TestSampleOnFor(t *testing.T) {
	c := &Config{SampleOn: []string{"ci/**"}, Suites: map[string]Suite{"e2e": {SampleOn: []string{"e2e/tasks/*.yml"}}, "go": {}}}

	cases := []struct {
		suite, file, want string
		ok                bool
	}{
		{"go", "ci/go.yml", "ci/**", true},
		{"e2e", "e2e/tasks/run.yml", "e2e/tasks/*.yml", true},
		{"go", "e2e/tasks/run.yml", "", false},
		{"go", "go.mod", "", false},
	}

	for _, tc := range cases {
		if got, ok := c.SampleOnFor(tc.suite, tc.file); got != tc.want || ok != tc.ok {
			t.Errorf("%s %s: %q %v", tc.suite, tc.file, got, ok)
		}
	}

	if !c.MatchesSampleOn("e2e/tasks/run.yml") || c.MatchesSampleOn("go.mod") {
		t.Error("MatchesSampleOn")
	}
}

func TestTheConcourseURLMayComeFromTheEnvironment(t *testing.T) {
	without := strings.Replace(withRuns, `url: "https://ci", `, "", 1)
	if without == withRuns {
		t.Fatal("nothing replaced")
	}

	t.Setenv(EnvConcourseURL, "")

	if c, err := Parse([]byte(without)); err != nil || c.Concourse.URL != "" {
		t.Fatalf("no url anywhere: %v", err)
	}

	t.Setenv(EnvConcourseURL, "https://ci.example")

	c, err := Parse([]byte(without))
	if err != nil || c.Concourse.URL != "https://ci.example" {
		t.Fatalf("url from the environment: %v %v", c, err)
	}

	if c, err := Parse([]byte(withRuns)); err != nil || c.Concourse.URL != "https://ci" {
		t.Fatalf("url from the file: %v", err)
	}
}

func TestAKeyGoingAwayIsAWarning(t *testing.T) {
	c, err := Parse([]byte(strings.Replace(withRuns, `repository: {uri: x},`, `repository: {uri: x}, vars: [gh],`, 1)))
	if err != nil {
		t.Fatal(err)
	}

	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "concourse.vars") {
		t.Fatalf("warnings %v", c.Warnings)
	}
}

func TestASuitesCorrelatorsAreEntries(t *testing.T) {
	plugins := `plugins:
  - {name: go, kind: correlator, repository: o/go, release: {version: v1, sha256: ` + strings.Repeat("a", 64) + `}}
  - {name: ts, kind: correlator, repository: o/ts, release: {version: v1, sha256: ` + strings.Repeat("a", 64) + `}}
`
	listed := strings.Replace(example, "    tag: slice-backend\n", "    tag: slice-backend\n    bind: recorded\n    correlators: [go]\n", 1) + plugins

	c, err := Parse([]byte(listed))
	if err != nil {
		t.Fatal(err)
	}

	if got := c.CorrelatorsFor("backend"); !slices.Equal(got, []string{"go"}) {
		t.Errorf("listed: %v", got)
	}

	if got := c.CorrelatorsFor("e2e"); !slices.Equal(got, []string{"go", "ts"}) {
		t.Errorf("by default: %v", got)
	}

	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "correlator ts observes no recorded suite") }) {
		t.Errorf("warnings %v", c.Warnings)
	}

	if _, err := Parse([]byte(strings.Replace(listed, "correlators: [go]", "correlators: [nope]", 1))); err == nil {
		t.Error("a correlator nothing names: accepted")
	}
}

func TestCollectSeconds(t *testing.T) {
	sum := strings.Repeat("a", 64)
	plugins := `plugins:
  - {name: go, kind: correlator, repository: o/go, release: {version: v1, sha256: ` + sum + `}}
  - {name: sql, kind: correlator, repository: o/sql, release: {version: v1, sha256: ` + sum + `}, collect_seconds: 60}
  - {name: pg, kind: sbom, repository: o/pg, release: {version: v1, sha256: ` + sum + `}}
`
	recorded := strings.Replace(example, "    tag: slice-backend\n", "    tag: slice-backend\n    bind: recorded\n", 1)

	c, err := Parse([]byte(recorded + plugins))
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range c.Plugins {
		want := map[string]time.Duration{"go": 10 * time.Second, "sql": time.Minute, "pg": 10 * time.Second}[p.Name]
		if p.CollectLimit() != want {
			t.Errorf("%s: %s, want %s", p.Name, p.CollectLimit(), want)
		}
	}

	for name, c := range map[string]struct{ from, to, want string }{
		"negative":       {"collect_seconds: 60", "collect_seconds: -1", "plugin sql: collect_seconds must be a positive number of seconds"},
		"not correlator": {"repository: o/pg,", "repository: o/pg, collect_seconds: 30,", "plugin pg: collect_seconds bounds a correlator's collect calls, and this entry's kind is sbom"},
		"too long together": {"collect_seconds: 60", "collect_seconds: 85",
			"suite backend: the collect calls of its correlators may take 95s together (go 10s, sql 85s), more than 90s"},
	} {
		_, err := Parse([]byte(recorded + strings.Replace(plugins, c.from, c.to, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}

	if _, err := Parse([]byte(example + strings.Replace(plugins, "collect_seconds: 60", "collect_seconds: 85", 1))); err != nil {
		t.Errorf("not recorded: %v", err)
	}
}

func TestWrites(t *testing.T) {
	c, err := Parse([]byte(withRuns))
	if err != nil {
		t.Fatal(err)
	}

	if c.WriteRetries() != 10 || c.WriteBackoff() != time.Second || c.WriteDeadline() != 10*time.Minute {
		t.Fatalf("defaults %d %s %s", c.WriteRetries(), c.WriteBackoff(), c.WriteDeadline())
	}

	c, err = Parse([]byte(withRuns + "writes: {deadline: 15m}\nlimits: {steps: 30}\n"))
	if err != nil || c.WriteDeadline() != 15*time.Minute {
		t.Fatalf("deadline: %v %v", c, err)
	}

	c, err = Parse([]byte(withRuns + "limits: {steps: 8}\n"))
	if err != nil || c.WriteDeadline() != 4*time.Minute {
		t.Fatalf("the default within short steps: %v %v", c, err)
	}

	for text, want := range map[string]int{"writes: {retries: 0}\n": 0, "writes: {retries: 50, backoff: 2s}\n": 50} {
		c, err := Parse([]byte(withRuns + text))
		if err != nil || c.WriteRetries() != want {
			t.Fatalf("%q: %v %v", text, c, err)
		}
	}

	c, err = Parse([]byte(withRuns + "writes: {backoff: 2s}\n"))
	if err != nil || c.WriteBackoff() != 2*time.Second || c.WriteRetries() != 10 {
		t.Fatalf("backoff: %v %v", c, err)
	}

	for _, bad := range []string{
		"writes: {retries: 51}\n", "writes: {retries: -1}\n", "writes: {backoff: soon}\n", "writes: {deadline: soon}\n",
		"writes: {deadline: 20m}\n", "writes: {deadline: 5m}\nlimits: {steps: 5}\n",
	} {
		if _, err := Parse([]byte(withRuns + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestStore(t *testing.T) {
	c, err := Parse([]byte(withRuns))
	if err != nil {
		t.Fatal(err)
	}

	if c.OnBranch() || c.StoreBranchName() != "pipelinegen" || c.LocksBranchName() != "pipelinegen-locks" || c.StoreDeadline() != 10*time.Minute {
		t.Fatalf("defaults %v %s %s %s", c.OnBranch(), c.StoreBranchName(), c.LocksBranchName(), c.StoreDeadline())
	}

	c, err = Parse([]byte(withRuns + "store: {backend: branch, branch: ci/state, deadline: 2m}\n"))
	if err != nil || !c.OnBranch() || c.StoreBranchName() != "ci/state" || c.LocksBranchName() != "ci/state-locks" || c.StoreDeadline() != 2*time.Minute {
		t.Fatalf("set: %v %v", c, err)
	}

	for _, bad := range []string{
		"store: {backend: database}\n", "store: {branch: \"a b\"}\n", "store: {branch: ../x}\n", "store: {deadline: soon}\n",
		"store: {backend: branch, branch: main}\n",
	} {
		if _, err := Parse([]byte(withRuns + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}

	onDefault := strings.Replace(withRuns, "branch: main}", "branch: pipelinegen}", 1)
	for _, fine := range []string{withRuns + "store: {branch: main}\n", onDefault} {
		if _, err := Parse([]byte(fine)); err != nil {
			t.Errorf("refused a run of the store's branch under refs: %v", err)
		}
	}

	if _, err := Parse([]byte(onDefault + "store: {backend: branch}\n")); err == nil {
		t.Error("accepted a branch store on a run's branch")
	}

	if _, err := Parse([]byte(withRuns + "store: {branch: mai}\n")); err != nil {
		t.Errorf("refused a branch no run is triggered by: %v", err)
	}
}

func TestMacrosDeclareAnotherRepositorysMacro(t *testing.T) {
	const base = "pipeline: x\nsuites: {a: {tests: [\"**/*_test.go\"], run_slice: r}}\n"

	c, err := Parse([]byte(base + "macros:\n  \"@tools//web:defs.bzl%web_project\": {reads: [\"src/**\", index.html], reason: builds its sources}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if m := c.Macros["@tools//web:defs.bzl%web_project"]; len(m.Reads) != 2 || m.Reason != "builds its sources" {
		t.Fatalf("got %+v", c.Macros)
	}

	for _, bad := range []string{
		`"//web:defs.bzl%web_project": {reads: [x], reason: own}`,
		`"@//web:defs.bzl%web_project": {reads: [x], reason: own}`,
		`"@tools//web:defs.bzl": {reads: [x], reason: no name}`,
		`"@tools//web:defs%web_project": {reads: [x], reason: no bzl}`,
		`"@tools//web:defs.bzl%web_project": {reads: [x]}`,
		`"@tools//web:defs.bzl%web_project": {reason: nothing read}`,
		`"@tools//web:defs.bzl%web_project": {reads: ["../up"], reason: outside}`,
		`"@tools//web:defs.bzl%web_project": {reads: ["["], reason: bad pattern}`,
	} {
		if _, err := Parse([]byte(base + "macros:\n  " + bad + "\n")); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

var withoutHost = strings.NewReplacer(
	"github: {repository: o/r, token: gh}", "git: {remote: \"git://example.test/r.git\"}",
	"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {branch: main}",
	"compare: merged_target", "compare: last_passed",
	"\n    report: {comment: {format: list}}", "",
).Replace(withRuns)

func TestRunsNeedNoHost(t *testing.T) {
	c, err := Parse([]byte(withoutHost))
	if err != nil {
		t.Fatal(err)
	}

	if c.Reports() || c.Git.Remote != "git://example.test/r.git" {
		t.Fatalf("reports %v to %q", c.Reports(), c.Git.Remote)
	}

	for _, remote := range []string{"https://git.example.test/group/sub/r.git", "http://127.0.0.1:7176/r.git", "file:///srv/git/r.git"} {
		if _, err := Parse([]byte(strings.Replace(withoutHost, "git://example.test/r.git", remote, 1))); err != nil {
			t.Errorf("%s: %v", remote, err)
		}
	}

	gh, err := Parse([]byte(withRuns))
	if err != nil || !gh.Reports() {
		t.Fatalf("a github section reports: %v", err)
	}
}

func TestWithoutAHostWhatNeedsOneIsRefused(t *testing.T) {
	cases := map[string][3]string{
		"pull requests":       {"trigger: {branch: main}", "trigger: {pull_requests: {base: main}}", "needs a host or a connector that lists them"},
		"statuses asked for":  {"compare: last_passed\n", "compare: last_passed\n    report: {status: true}\n", "report.status needs a host"},
		"a lock's repository": {"lock: {name: browsers, minutes: 90}", "lock: {name: browsers, minutes: 90, repository: o/locks}", "lock.repository needs the github section"},
		"a password":          {"git://example.test/r.git", "https://u:p@example.test/r.git", "without a user, password"},
		"a user":              {"git://example.test/r.git", "https://u@example.test/r.git", "without a user, password"},
		"ssh":                 {"git://example.test/r.git", "ssh://git@example.test/r.git", "an ssh remote needs git.ssh_key"},
		"scp form":            {"git://example.test/r.git", "git@example.test:r.git", "an ssh remote needs git.known_hosts"},
		"a query":             {"git://example.test/r.git", "https://example.test/r.git?token=x", "query"},
		"no path":             {"git://example.test/r.git", "https://example.test/", "https, http, git or file URL"},
	}

	for name, c := range cases {
		text := strings.Replace(withoutHost, c[0], c[1], 1)
		if text == withoutHost {
			t.Fatalf("%s: nothing replaced", name)
		}

		cfg, err := Parse([]byte(text))
		if err == nil {
			err = cfg.ValidatePipeline()
		}

		if err == nil || !strings.Contains(err.Error(), c[2]) {
			t.Errorf("%s: %v", name, err)
		}
	}

	half := strings.Replace(withRuns, "github: {repository: o/r, token: gh}", "github: {repository: o/r}", 1)
	if _, err := Parse([]byte(half)); err == nil || !strings.Contains(err.Error(), "github needs a repository and the name of its token var") {
		t.Errorf("half a github section: %v", err)
	}
}

func TestRunsCheckBeforeThePipelineCan(t *testing.T) {
	early := strings.NewReplacer(
		"git: {remote: \"git://example.test/r.git\"}\n", "",
	).Replace(withoutHost)
	before, _, _ := strings.Cut(early, "concourse:")
	_, after, _ := strings.Cut(early, "\nruns:")
	early = before + "runs:" + after

	c, err := Parse([]byte(early))
	if err != nil {
		t.Fatal(err)
	}

	err = c.ValidatePipeline()
	for _, want := range []string{"git.remote, or the github section", "concourse needs a repository source and a planner image", "the image's digest"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("pipeline: want %q in %v", want, err)
		}
	}

	if !errors.Is(err, ErrInvalid) {
		t.Errorf("not ErrInvalid: %v", err)
	}

	for name, text := range map[string]string{"with a host": withRuns, "with a remote": withoutHost} {
		c, err := Parse([]byte(text))
		if err != nil {
			t.Fatal(err)
		}

		if err := c.ValidatePipeline(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	half := strings.Replace(withoutHost, "digest: \"sha256:x\", ", "", 1)
	if _, err := Parse([]byte(half)); err == nil || !strings.Contains(err.Error(), "the image's digest") {
		t.Errorf("half a concourse section: %v", err)
	}
}

func TestTokensByPurpose(t *testing.T) {
	one, err := Parse([]byte(withRuns))
	if err != nil {
		t.Fatal(err)
	}

	if one.GitHub.SplitTokens() != nil || one.GitHub.TokenFor("read") != "gh" || one.GitHub.TokenFor("store") != "gh" {
		t.Fatalf("one token: %v", one.GitHub.SplitTokens())
	}

	split, err := Parse([]byte(strings.Replace(withRuns, "token: gh}", "token: gh, tokens: {read: gh_read}}", 1)))
	if err != nil {
		t.Fatal(err)
	}

	if got := split.GitHub.SplitTokens(); got["read"] != "gh_read" || got["changes"] != "gh" || got["report"] != "gh" || got["store"] != "gh" || len(got) != 4 {
		t.Fatalf("split: %v", got)
	}

	if _, err := Parse([]byte(strings.Replace(withRuns, "token: gh}", "tokens: {read: r, changes: c, report: p, store: s}}", 1))); err != nil {
		t.Fatalf("every purpose its own, no one token: %v", err)
	}

	for name, c := range map[string][2]string{
		"a purpose with no var": {"github: {repository: o/r, tokens: {read: r, report: p, store: s}}", "github.tokens.changes is missing"},
		"a value, not a name":   {"github: {repository: o/r, token: gh, tokens: {read: \"((r))\"}}", "github.tokens.read must be the name of a var"},
		"no repository":         {"github: {tokens: {read: r, changes: c, report: p, store: s}}", "github needs a repository"},
	} {
		text := strings.Replace(withRuns, "github: {repository: o/r, token: gh}", c[0], 1)
		if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), c[1]) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBatchesAreDealtApartFromParallel(t *testing.T) {
	const base = "pipeline: x\nsuites:\n  a: {tests: [\"**/*_test.go\"], run_slice: r"

	for text, want := range map[string]int{
		base + "}\n":                           DefaultParallel,
		base + ", parallel: 3}\n":              3,
		base + ", parallel: 3, batches: 12}\n": 12,
	} {
		c, err := Parse([]byte(text))
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}

		if got := c.Suites["a"].BatchCount(); got != want {
			t.Errorf("%s: %d batches, want %d", text, got, want)
		}
	}

	for _, bad := range []string{base + ", parallel: 3, batches: 2}\n", base + ", batches: 2}\n", base + ", batches: -1}\n"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestTheBootstrapFetchesTheDefaultBazelisk(t *testing.T) {
	var c Config

	c.Concourse.Planner.Bootstrap.Release = &Release{Version: "v0.2.2", SHA256: strings.Repeat("a", 64)}
	c.defaults()

	if got := c.Concourse.Planner.Bootstrap.Bazelisk; got != DefaultBazelisk {
		t.Errorf("bazelisk = %+v, want the default %+v", got, DefaultBazelisk)
	}
}

func TestANarrowingsLevelNeedsANoteAndALevel(t *testing.T) {
	for entry, want := range map[string]string{
		`{off_at: 3, note: "every reader declared"}`: "",
		`{off_at: 1, note: "every reader declared"}`: "",
		`{off_at: 3}`:            "narrowings.document-units: needs a note",
		`{off_at: 3, note: " "}`: "narrowings.document-units: needs a note",
		`{off_at: 0, note: "x"}`: "narrowings.document-units: off_at must be 1 to 5",
		`{off_at: 6, note: "x"}`: "narrowings.document-units: off_at must be 1 to 5",
	} {
		c, err := Parse([]byte(strings.Replace(withRuns, "pipeline: shop\n", "pipeline: shop\nnarrowings: {document-units: "+entry+"}\n", 1)))

		switch {
		case want == "" && (err != nil || c.Narrowings["document-units"].OffAt == 0):
			t.Errorf("%s: %v", entry, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: %v, want %q", entry, err, want)
		}
	}
}
