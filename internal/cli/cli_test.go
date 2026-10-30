package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := Run(t.Context(), args, &stdout, &stderr)

	return stdout.String(), stderr.String(), code
}

func TestUsage(t *testing.T) {
	if _, stderr, code := run(t); code != 2 || !strings.Contains(stderr, "slices") {
		t.Fatalf("no args: %d %q", code, stderr)
	}

	if _, _, code := run(t, "nope"); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}

	if _, _, code := run(t, "slices", "-nope"); code != 2 {
		t.Fatalf("unknown flag: %d", code)
	}

	if _, _, code := run(t, "slices", "-root", t.TempDir()); code != 1 {
		t.Fatalf("no config: %d", code)
	}
}

func TestSlicesCommand(t *testing.T) {
	testrepo.Parallel(t)

	r := testrepo.New(t)
	testrepo.Bazel(t, r.Dir)

	stdout, stderr, code := run(t, "slices", "-root", r.Dir, "-suite", "e2e")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	want := "e2e //e2e:checkout\n  e2e/checkout.spec.ts\ne2e //e2e:orders\n  e2e/orders.spec.ts\n"
	if stdout != want {
		t.Fatalf("got\n%s\nwant\n%s", stdout, want)
	}

	if _, _, code := run(t, "slices", "-root", r.Dir, "-suite", "nope"); code != 2 {
		t.Fatalf("unknown suite: %d", code)
	}

	stdout, _, code = run(t, "slices", "-root", r.Dir, "-json")
	if code != 0 || !strings.Contains(stdout, `"suite": "backend"`) || !strings.Contains(stdout, `"//backendtests:money_test"`) {
		t.Fatalf("json: %d %s", code, stdout)
	}
}

func TestAffectedCommand(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path

	r := testrepo.New(t)
	base := r.Git("rev-parse", "HEAD")

	r.Write("api/money/money.go", "package money // changed\n")
	r.Commit("money")

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}

	want := "backend: 1 selected, 0 untouched\n  run //backendtests:money_test\ne2e: 1 selected, 1 untouched\n  run //e2e:checkout\n  run whole-suite check reconcile\nspecs: 1 selected, 1 untouched\n  run e2e/checkout.spec.ts\n"
	if stdout != want {
		t.Fatalf("got\n%s\nwant\n%s", stdout, want)
	}

	stdout, _, code = run(t, "affected", "-root", r.Dir, "-cache", cache, "-bazel-diff", bd, "-suite", "e2e")
	if code != 0 || !strings.HasPrefix(stdout, "every slice: nothing to compare with\ne2e: 2 selected") {
		t.Fatalf("no base: %d %q", code, stdout)
	}
}

func TestAffectedFetchesThePinnedRelease(t *testing.T) {
	testrepo.NeedBazel(t)

	tool, err := differFor(t.Context(), &config.Config{}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(tool.Path); err != nil {
		t.Fatal(err)
	}
}

func TestCommandsDescribeThemselves(t *testing.T) {
	docs := Commands()
	if len(docs) != len(commands()) {
		t.Fatalf("%d of %d commands described", len(docs), len(commands()))
	}

	flags := map[string]bool{}

	for _, d := range docs {
		for _, f := range d.Flags {
			flags[d.Name+" -"+f.Name] = true
		}
	}

	for _, want := range []string{"slices -suite", "slices -root", "affected -base", "affected -bazel-diff"} {
		if !flags[want] {
			t.Errorf("no %s in %v", want, flags)
		}
	}
}

func TestPipelineNeedsARunner(t *testing.T) {
	r := testrepo.New(t)

	if _, stderr, code := run(t, "pipeline", "-root", r.Dir); code != 1 || !strings.Contains(stderr, "no runner plugin") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

const fakeRunner = `#!/bin/sh
in=$(cat)
case "$1" in
describe) echo '{"protocol": 1, "kind": "runner", "name": "fake", "version": "v0", "capabilities": ["pipeline", "placement"]}' ;;
pipeline)
  work=$(printf '%s' "$in" | sed -n 's/.*"work":"\([^"]*\)".*/\1/p')
  name=$(printf '%s' "$in" | sed -n 's/.*"spec":{"pipeline":"\([^"]*\)".*/\1/p')
  printf 'from the runner: %s\n' "$name" > "$work/pipeline.yml"
  echo '{"files": ["pipeline.yml"]}' ;;
*) exit 1 ;;
esac
`

func TestPipelineThroughTheRunner(t *testing.T) {
	r := testrepo.New(t)

	cfg := readText(t, filepath.Join(r.Dir, "pipelinegen.yaml")) +
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: " + strings.Repeat("0", 40) + "}\n"
	r.Write("pipelinegen.yaml", cfg)

	runner := filepath.Join(t.TempDir(), "ci")
	if err := testenv.WriteExecutable(t.Context(), runner, []byte(fakeRunner)); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, "pipeline", "-root", r.Dir, "-runner", runner)
	if code != 0 || stdout != "from the runner: fixture\n" {
		t.Fatalf("exit %d: %q %s", code, stdout, stderr)
	}

	if _, _, code := run(t, "pipeline", "-root", r.Dir, "-plugins", t.TempDir()); code != 1 {
		t.Fatalf("a missing runner: exit %d", code)
	}
}

func TestPipelineRefusesARunnerThatCantPlace(t *testing.T) {
	r := testrepo.New(t)

	cfg := readText(t, filepath.Join(r.Dir, "pipelinegen.yaml")) +
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: " + strings.Repeat("0", 40) + "}\n"
	r.Write("pipelinegen.yaml", cfg)

	plugins := t.TempDir()
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(plugins, "ci"), []byte(strings.Replace(fakeRunner, `["pipeline", "placement"]`, `["pipeline"]`, 1))); err != nil {
		t.Fatal(err)
	}

	want := "runner ci v0 places steps by worker tags this pipelinegen no longer passes: update the runner, which reads them from plugins.ci.config.placement"

	if _, stderr, code := run(t, "pipeline", "-root", r.Dir, "-plugins", plugins); code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("pipeline: exit %d: %s", code, stderr)
	}

	if stdout, _, code := run(t, "check", "-root", r.Dir, "-plugins", plugins); code != 1 || !strings.Contains(stdout, "runner ci refuses the pipeline: "+"runner: "+want) {
		t.Fatalf("check: exit %d: %s", code, stdout)
	}
}

func TestPipelineVarsNeedNoConcourse(t *testing.T) {
	r := testrepo.New(t)

	cfg := strings.Replace(readText(t, filepath.Join(r.Dir, "pipelinegen.yaml")), "  url: https://ci.example\n", "", 1) +
		"plugins:\n  - {name: ci, kind: runner, repository: o/ci, commit: " + strings.Repeat("0", 40) + "}\n"
	r.Write("pipelinegen.yaml", cfg)

	runner := filepath.Join(t.TempDir(), "ci")
	if err := testenv.WriteExecutable(t.Context(), runner, []byte(strings.Replace(fakeRunner, "from the runner: %s", "((github_token)) %s", 1))); err != nil {
		t.Fatal(err)
	}

	if stdout, stderr, code := run(t, "pipeline", "-root", r.Dir, "-runner", runner, "-vars"); code != 0 || stdout != "github_token\n" {
		t.Fatalf("vars: exit %d: %q %s", code, stdout, stderr)
	}

	if _, stderr, code := run(t, "pipeline", "-root", r.Dir, "-runner", runner); code != 1 || !strings.Contains(stderr, "concourse needs its url") {
		t.Fatalf("the pipeline itself: exit %d: %s", code, stderr)
	}
}

func TestSubcommandsDescribeThemselves(t *testing.T) {
	subs := map[string]map[string]bool{}

	for _, d := range Commands() {
		for _, s := range d.Subcommands {
			flags := map[string]bool{}
			for _, f := range s.Flags {
				flags[f.Name] = true
			}

			if s.Summary == "" {
				t.Errorf("%s %s has no summary", d.Name, s.Name)
			}

			subs[d.Name+" "+s.Name] = flags
		}
	}

	for name, want := range map[string][]string{
		"report start": {"root"}, "report slice": {"state", "result", "coverage"},
		"report batch-end": {"batch", "batches"}, "report suite": {"root"},
		"report finish": {"root"}, "report collect": {"suite", "out"},
		"slice start": {"batches"}, "slice end": {"coverage"}, "slice list": {"batches"},
		"lock acquire": {"wait"}, "lock release": {"root"},
		"lists attach": {"run", "change", "commit", "to"}, "lists show": {"commit", "suite", "slice"},
	} {
		flags, ok := subs[name]
		if !ok {
			t.Errorf("no subcommand %s", name)

			continue
		}

		for _, f := range want {
			if !flags[f] {
				t.Errorf("%s doesn't describe -%s", name, f)
			}
		}
	}

	if flags := subs["lists show"]; flags["to"] {
		t.Error("lists show describes attach's -to")
	}
}
