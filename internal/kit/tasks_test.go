package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

// fakeRunner is a runner's program whose steps call the tool as each test needs; it notes how it
// ran in $OUT.
const fakeRunner = `#!/bin/sh
env > "$OUT/$2.env"
case "$2" in
plan) exec tools/pipelinegen plan --run r --trigger trigger --root trigger --cache plan-cache ;;
decided)
  tools/pipelinegen plan --decide --run r --trigger trigger --root trigger --cache plan-cache --gate handoff
  [ -e handoff/gate.json ] && exit 0
  exec tools/pipelinegen plan --run r --trigger trigger --root trigger --cache plan-cache --gate handoff ;;
undecided) exec tools/pipelinegen plan --run r --trigger trigger --root trigger --cache plan-cache --gate handoff ;;
end)
  rc=0
  tools/pipelinegen report suite --root trigger --batches batches || rc=$?
  [ "$rc" = 4 ] && exit 4
  exit 0 ;;
swallow) tools/pipelinegen report suite --root trigger --batches batches || true ;;
ungated) exec tools/pipelinegen plan --run r --trigger trigger --root trigger --cache write-cache --from plan ;;
lock) tools/pipelinegen lock acquire db ;;
list) exec tools/pipelinegen unplanned --run r --root config --out sweep --cache write-cache ;;
heads) for d in sweep/plan/*; do tools/pipelinegen plan --run r --trigger "$d" --root "$d" --cache plan-cache --out plans/head; done ;;
esac
`

func runnerFor(t *testing.T, installs ...string) (taskRunner, string) {
	t.Helper()

	out := t.TempDir()
	runner := filepath.Join(t.TempDir(), "runner")

	if err := os.WriteFile(runner, []byte(fakeRunner), 0o700); err != nil {
		t.Fatal(err)
	}

	s := plugin.Spec{Tools: []plugin.SpecTool{
		{Name: "pipelinegen", Kind: plugin.ToolPipelinegen, Commit: "c"},
		{Name: "runner", Kind: plugin.KindRunner, Repository: "o/pipelinegen-runner-x", Version: "v1"},
	}}

	return taskRunner{spec: s, runner: runner, work: t.TempDir(), installs: installs}, out
}

// stepTask is a task that runs the runner's step, as the pipeline's tasks do.
func stepTask(indent, name, step, out string, more ...string) string {
	lines := []string{
		"task: " + name,
		"config:",
		"  platform: linux",
		"  inputs: [{name: trigger}, {name: sweep, optional: true}, {name: tools, optional: true}]",
		"  outputs: [{name: tools}, {name: handoff}, {name: sweep}, {name: plans}]",
		"  params: {PIPELINEGEN_PIPELINE_STEP: \"1\", OUT: " + out + "}",
		"  run: {path: sh, args: [-ec, 'exec tools/plugins/runner step \"$@\"', bootstrap, " + step + "]}",
	}

	// the lines after the first line up under it
	rest := strings.Repeat(" ", len(indent))

	return indent + strings.Join(append(lines, more...), "\n"+rest) + "\n"
}

func jobOf(name string, tasks ...string) string {
	return "jobs:\n- name: " + name + "\n  plan:\n  - get: trigger\n" + strings.Join(tasks, "")
}

func TestARunnersStepIsRunForItsCalls(t *testing.T) {
	r, out := runnerFor(t)

	unmarked := strings.Replace(stepTask("  - ", "unmarked", "lock", out), `PIPELINEGEN_PIPELINE_STEP: "1", `, "", 1)
	text := []byte(jobOf("plan-main", stepTask("  - ", "plan", "plan", out), unmarked,
		"  - task: named\n    config: {platform: linux, run: {path: sh, args: [-ec, 'tools/pipelinegen tree --out source']}, params: {PIPELINEGEN_PIPELINE_STEP: \"1\"}}\n"))

	steps, err := stepCalls(text, runTasks(t.Context(), r, text))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, sc := range steps {
		got = append(got, sc.where+": "+sc.prog+" "+strings.Join(sc.args, " "))
	}

	want := "job plan-main, step plan: tools/pipelinegen plan --run r --trigger trigger --root trigger --cache plan-cache; " +
		"job plan-main, step named: tools/pipelinegen tree --out source"
	if strings.Join(got, "; ") != want {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}

	if _, err := os.Stat(filepath.Join(out, "plan.env")); err != nil {
		t.Fatal("the step didn't run")
	}

	env := string(readFile(t, filepath.Join(out, "plan.env")))
	for _, want := range []string{"\nhttps_proxy=" + deadProxy + "\n", "\nHTTPS_PROXY=" + deadProxy + "\n", "\nOUT=" + out + "\n"} {
		if !strings.Contains("\n"+env, want) {
			t.Errorf("the step's environment lacks %q", strings.TrimSpace(want))
		}
	}

	// only a task the pipeline marks as the tool's step runs
	if _, err := os.Stat(filepath.Join(out, "lock.env")); err == nil {
		t.Error("a task not marked as the tool's step ran")
	}
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

// A job's tasks see what earlier ones put out, as Concourse hands an output on as an input.
func TestARunnersStepsShareTheirJobsArtifacts(t *testing.T) {
	r, out := runnerFor(t)
	text := []byte(jobOf("sweep-pr", stepTask("  - ", "sweep", "list", out), stepTask("  - ", "plan", "heads", out)))

	runs := runTasks(t.Context(), r, text)

	steps, err := stepCalls(text, runs)
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, sc := range steps {
		got = append(got, strings.Join(sc.args, " "))
	}

	want := "unplanned --run r --root config --out sweep --cache write-cache; " +
		"plan --run r --trigger sweep/plan/7 --root sweep/plan/7 --cache plan-cache --out plans/head"
	if strings.Join(got, "; ") != want {
		t.Errorf("calls:\n%s", strings.Join(got, "\n"))
	}

	if got := strings.Join(forgeProblems(text, runs), "; "); !strings.Contains(got, "plan --from false") {
		t.Errorf("a chain without plan --from: %q", got)
	}
}

func TestARunnersStepsAreJudgedByTheirCalls(t *testing.T) {
	s := Spec()
	s.Runs = []plugin.SpecRun{{Name: "full", Trigger: plugin.SpecTrigger{Kind: plugin.TriggerContinuous, Branch: "main"}}}

	for step, want := range map[string]string{
		"decided":   "",
		"undecided": "doesn't ask for the decision",
	} {
		r, out := runnerFor(t)

		text := []byte(jobOf("plan-full", stepTask("  - ", "plan", step, out)))

		got := strings.Join(undecidedPrepares(s, text, runTasks(t.Context(), r, text)), "; ")
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", step, got, want)
		}
	}

	r, out := runnerFor(t)
	text := []byte(jobOf("plan-pr", stepTask("  - ", "write", "ungated", out)))

	if got := strings.Join(unhandedStarts(Spec(), text, runTasks(t.Context(), r, text)), "; "); !strings.Contains(got, "plan --from without --gate") {
		t.Errorf("an ungated write: %q", got)
	}

	text = []byte(jobOf("pr-e2e", stepTask("  - ", "start", "lock", out)))

	if got := strings.Join(lockSteps(text, runTasks(t.Context(), r, text)), "; "); !strings.Contains(got, "job pr-e2e, step start: runs pipelinegen lock") {
		t.Errorf("a lock: %q", got)
	}
}

func TestARunnersReportStepFailsItsJobWhenItCannotRecord(t *testing.T) {
	for name, c := range map[string]struct {
		step, want string
		try        bool
	}{
		"passed on": {step: "end"},
		"swallowed": {step: "swallow", want: "job main-e2e, task end (report suite): exits 0 when the tool exits 4"},
		"in a try":  {step: "end", try: true, want: "inside a try"},
	} {
		t.Run(name, func(t *testing.T) {
			r, out := runnerFor(t)

			task := stepTask("    ", "end", c.step, out)
			if c.try {
				task = "    try:\n" + stepTask("      ", "end", c.step, out)
			}

			text := []byte("jobs:\n- name: main-e2e\n  plan: [{get: trigger}]\n  ensure:\n" + task)

			got := strings.Join(swallowedUnrecorded(t.Context(), text, runTasks(t.Context(), r, text)), "; ")
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
}

// The kit installs no package where it runs: a step runs without its installs.
func TestARunnersStepRunsWithoutItsInstalls(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "installed")
	install := "touch " + marker + "; "

	r, out := runnerFor(t, install)
	task := strings.Replace(stepTask("  - ", "plan", "plan", out), "args: [-ec, '", "args: [-ec, '"+install, 1)

	text := []byte(jobOf("plan-main", task))

	steps, err := stepCalls(text, runTasks(t.Context(), r, text))
	if err != nil || len(steps) != 1 {
		t.Fatalf("%d calls, %v", len(steps), err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Error("the kit ran the step's install")
	}

	if got := steps[0].script; !strings.Contains(got, install) {
		t.Errorf("the step's script lost its install: %q", got)
	}
}
