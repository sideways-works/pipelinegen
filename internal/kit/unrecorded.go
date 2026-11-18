package kit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	gostrings "strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// exitUnrecorded is a report step's exit when its records could not be written (cli.ExitUnrecorded):
// a pipeline may tolerate a report step's other failures, never this one.
const exitUnrecorded = 4

var recordingSteps = []string{"report batch-end", "report suite"}

// swallowedUnrecorded are tasks running a recording step where its exit 4 can't fail the job: in a try,
// or a script not ending with 4 when the tool does, run with a stand-in tool and no package installs; a
// task that runs the tool through a program of the runner's is judged by its runs.
func swallowedUnrecorded(ctx context.Context, text []byte, runs *taskRuns, installs ...string) []string {
	var doc map[string]any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	jobs, _ := doc["jobs"].([]any) //nolint:errcheck // a pipeline without jobs has nothing to check

	var out []string

	for _, j := range jobs {
		job, ok := j.(map[string]any)
		if !ok {
			continue
		}

		name := fmt.Sprint(job["name"])

		for _, k := range append([]string{"plan"}, hookKeys...) {
			out = append(out, unrecordedWalk{runs: runs, installs: installs}.in(ctx, job[k], name, false)...)
		}
	}

	return out
}

type unrecordedWalk struct {
	runs     *taskRuns
	installs []string
}

func (w unrecordedWalk) in(ctx context.Context, v any, job string, tried bool) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, w.in(ctx, e, job, tried)...)
		}

		return out
	case map[string]any:
		var out []string

		if task, ok := x["task"]; ok {
			out = append(out, w.task(ctx, x, task, job, tried)...)
		}

		for k, c := range x {
			if k == "config" {
				continue
			}

			out = append(out, w.in(ctx, c, job, tried || k == tryKey)...)
		}

		return out
	}

	return nil
}

// task is a task's problem: the exit 4 a try lets pass, or a run that ends otherwise.
func (w unrecordedWalk) task(ctx context.Context, x map[string]any, task any, job string, tried bool) []string {
	step, run := recordingStep(x)

	res := w.runs.result(job, x)
	if step == "" && (res == nil || res.recording == "") {
		return nil
	}

	if step == "" {
		step = res.recording
	}

	where := fmt.Sprintf("job %s, task %v (%s)", job, task, step)

	var problem string

	switch {
	case tried:
		problem = "inside a try, which lets its exit 4 pass"
	case run != nil:
		problem = passesUnrecorded(ctx, run, w.installs)
	default:
		problem = unrecordedProblem(res.unrecorded, res.said)
	}

	if problem == "" {
		return nil
	}

	return []string{where + ": " + problem}
}

func recordingStep(task map[string]any) (string, map[string]any) {
	cfg, _ := task["config"].(map[string]any) //nolint:errcheck // a task from a file has none here
	run, _ := cfg["run"].(map[string]any)     //nolint:errcheck // nor a run

	words := []string{fmt.Sprint(run["path"])}
	if args, ok := run["args"].([]any); ok {
		for _, a := range args {
			words = append(words, fmt.Sprint(a))
		}
	}

	line := gostrings.Join(words, " ")

	for _, step := range recordingSteps {
		if gostrings.Contains(gostrings.Join(gostrings.Fields(line), " "), "pipelinegen "+step) {
			return step, run
		}
	}

	return "", nil
}

const standIn = "#!/bin/sh\n[ \"$1\" = report ] && exit 4\nexit 0\n"

func passesUnrecorded(ctx context.Context, run map[string]any, installs []string) string {
	dir, err := os.MkdirTemp("", "kit-unrecorded-")
	if err != nil {
		return err.Error()
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover temporary directory harms nothing

	for _, d := range []string{"tools", "trigger", "source", "batches", "result"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			return err.Error()
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "tools", "pipelinegen"), []byte(standIn), 0o700); err != nil { //nolint:gosec // the kit's own stand-in, run by the kit
		return err.Error()
	}

	args := make([]string, 0, 4)

	if a, ok := run["args"].([]any); ok {
		for _, x := range a {
			arg := fmt.Sprint(x)
			for _, in := range installs {
				arg = gostrings.ReplaceAll(arg, in, ":\n")
			}

			args = append(args, arg)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, fmt.Sprint(run["path"]), args...) //nolint:gosec // the runner's own step, run as Concourse would
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir}

	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError

	switch {
	case err == nil:
		return unrecordedProblem(0, "")
	case errors.As(err, &exit):
		return unrecordedProblem(exit.ExitCode(), gostrings.TrimSpace(string(out)))
	default:
		return unrecordedProblem(-1, "can't run: "+err.Error())
	}
}

// unrecordedProblem is what a step's exit, when the tool exits 4 for a report, says of it: -1 is a
// step that couldn't run.
func unrecordedProblem(code int, said string) string {
	switch code {
	case exitUnrecorded:
		return ""
	case 0:
		return "exits 0 when the tool exits 4"
	case -1:
		return said
	}

	return fmt.Sprintf("exits %d when the tool exits 4: %s", code, said)
}
