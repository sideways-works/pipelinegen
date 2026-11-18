package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

var noticeRunner = strings.Replace(fakeRunner, `echo '{"files": ["pipeline.yml"]}'`,
	`printf '%s\n' '{"files": ["pipeline.yml"], "notices": ["placement: pr-go is placed by no rule", "placement: pr-go is placed by no rule", "two\nlines"]}'`, 1)

func TestARunnersNoticesAreSaidOnceAndFailNothing(t *testing.T) {
	if !strings.Contains(noticeRunner, "notices") {
		t.Fatal("the fake runner answers no notices")
	}

	r := testrepo.New(t)

	cfg := readText(t, filepath.Join(r.Dir, "pipelinegen.yaml")) +
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: " + strings.Repeat("0", 40) + "}\n"
	r.Write("pipelinegen.yaml", cfg)

	plugins := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(plugins, "ci"), []byte(noticeRunner)); err != nil {
		t.Fatal(err)
	}

	c, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	conf, err := pickRunner(c, "")
	if err != nil {
		t.Fatal(err)
	}

	text, notices, err := pipelineText(t.Context(), c, r.Dir, conf, "", plugins)
	if err != nil || string(text) != "from the runner: fixture\n" {
		t.Fatalf("pipeline: %q %v", text, err)
	}

	want := []string{"placement: pr-go is placed by no rule", "two lines"}
	if !slices.Equal(notices, want) {
		t.Fatalf("notices %q, want %q", notices, want)
	}

	var said bytes.Buffer

	sayNotices(&said, "ci", notices)

	if got := said.String(); got != "pipelinegen pipeline: runner ci: placement: pr-go is placed by no rule\npipelinegen pipeline: runner ci: two lines\n" {
		t.Errorf("said %q", got)
	}

	if stdout, _, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins); code != 0 || stdout != "from the runner: fixture\n" {
		t.Fatalf("pipeline: exit %d: %q", code, stdout)
	}

	_, _, without := run(t, "check", "-root", r.Dir, "-plugins", t.TempDir())

	stdout, _, code := run(t, "check", "-root", r.Dir, "-plugins", plugins)
	if code != without || !strings.Contains(stdout, "warning: pipelinegen.yaml: runner ci: placement: pr-go is placed by no rule\n") ||
		!strings.Contains(stdout, "warning: pipelinegen.yaml: runner ci: two lines\n") {
		t.Fatalf("check: exit %d, %d without the runner: %s", code, without, stdout)
	}
}

// echoRunner writes the request it got as its pipeline.
const echoRunner = `#!/bin/sh
in=$(cat)
case "$1" in
describe) echo '{"protocol": 1, "kind": "runner", "name": "echo", "version": "v0", "capabilities": ["pipeline", "placement"]}' ;;
pipeline)
  work=$(printf '%s' "$in" | sed -n 's/.*"work":"\([^"]*\)".*/\1/p')
  printf '%s\n' "$in" > "$work/pipeline.yml"
  echo '{"files": ["pipeline.yml"]}' ;;
*) exit 1 ;;
esac
`

func TestEachRunnerIsGivenTheRunsItOwns(t *testing.T) {
	r := testrepo.New(t)

	zero := strings.Repeat("0", 40)
	cfg := strings.NewReplacer(
		"    compare: merged_target\n", "    compare: merged_target\n    runner: ci\n",
		"    compare: none\n", "    compare: none\n    runner: ci\n",
	).Replace(readText(t, filepath.Join(r.Dir, "pipelinegen.yaml"))) +
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: " + zero + "}\n" +
		"  - {name: next, kind: runner, repository: o/next, commit: " + zero + "}\n"
	r.Write("pipelinegen.yaml", strings.Replace(cfg, "    compare: last_passed\n", "    compare: last_passed\n    runner: next\n", 1))

	plugins := t.TempDir()
	for _, name := range []string{"ci", "next"} {
		if err := testenv.WriteExecutable(t.Context(), filepath.Join(plugins, name), []byte(echoRunner)); err != nil {
			t.Fatal(err)
		}
	}

	if _, stderr, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins); code != 2 || !strings.Contains(stderr, "2 runners are configured (ci, next): name the one whose pipeline to write with --for") {
		t.Fatalf("no --for: exit %d: %s", code, stderr)
	}

	if _, stderr, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins, "-for", "go-sbom"); code != 2 || !strings.Contains(stderr, "--for go-sbom: no runner plugin of that name (runners: ci, next)") {
		t.Fatalf("--for a name no runner has: exit %d: %s", code, stderr)
	}

	for runner, want := range map[string][2][]string{
		"ci":   {{"nightly", "pr"}, {"main"}},
		"next": {{"main"}, {"nightly", "pr"}},
	} {
		stdout, stderr, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins, "-for", runner)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", runner, code, stderr)
		}

		var req plugin.PipelineRequest
		if err := json.Unmarshal([]byte(stdout), &req); err != nil {
			t.Fatalf("%s: %v: %s", runner, err, stdout)
		}

		var runs []string
		for _, sr := range req.Spec.Runs {
			runs = append(runs, sr.Name)
		}

		if !slices.Equal(runs, want[0]) || !slices.Equal(req.Spec.OtherRuns, want[1]) || req.Spec.Runner != runner {
			t.Errorf("%s: runs %v, other runs %v; want %v", runner, runs, req.Spec.OtherRuns, want)
		}

		var trusted config.Trusted
		if err := json.Unmarshal([]byte(req.Spec.Trusted), &trusted); err != nil || trusted.RunRunners["main"] != "next" || len(trusted.RunSuites) != 3 {
			t.Errorf("%s: the trusted keys hold every run and its owner: %+v %v", runner, trusted.RunRunners, err)
		}
	}
}

func TestOneRunnersSpecNamesNoOtherRuns(t *testing.T) {
	r := testrepo.New(t)

	r.Write("pipelinegen.yaml", readText(t, filepath.Join(r.Dir, "pipelinegen.yaml"))+
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: "+strings.Repeat("0", 40)+"}\n")

	plugins := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(plugins, "ci"), []byte(echoRunner)); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins)
	if code != 0 || strings.Contains(stdout, "other_runs") || strings.Contains(stdout, `"runner":`) || strings.Contains(stdout, "run_runners") || strings.Contains(stdout, "runner_configs") {
		t.Fatalf("exit %d: %s %s", code, stdout, stderr)
	}

	if forCI, _, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins, "-for", "ci"); code != 0 || !sameButWork(forCI, stdout) {
		t.Errorf("--for the only runner: exit %d:\n%s\nwithout:\n%s", code, forCI, stdout)
	}
}

// sameButWork compares two requests but for their work directories, which each call has its own of.
func sameButWork(a, b string) bool {
	work := regexp.MustCompile(`"work":"[^"]*"`)

	return work.ReplaceAllString(a, "") == work.ReplaceAllString(b, "")
}
