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

// exitUnrecorded is the exit code of a report step whose records could not
// be written (cli.ExitUnrecorded): a pipeline may tolerate a report step's
// other failures, never this one.
const exitUnrecorded = 4

var recordingSteps = []string{"report batch-end", "report suite"}

// swallowedUnrecorded are the tasks that run a recording step where its exit
// 4 can't fail the job: inside a try, or in a script that doesn't end with 4
// when the tool does. Each script is run with a stand-in for the tool that
// exits 4 for a report, and with the installs of the spec's packages, which
// would run on this machine, taken out.
func swallowedUnrecorded(ctx context.Context, text []byte, installs ...string) []string {
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
			out = append(out, swallowedIn(ctx, job[k], name, false, installs)...)
		}
	}

	return out
}

func swallowedIn(ctx context.Context, v any, job string, tried bool, installs []string) []string {
	switch x := v.(type) {
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, swallowedIn(ctx, e, job, tried, installs)...)
		}

		return out
	case map[string]any:
		var out []string

		if task, ok := x["task"]; ok {
			if step, run := recordingStep(x); step != "" {
				where := fmt.Sprintf("job %s, task %v (%s)", job, task, step)

				switch {
				case tried:
					out = append(out, where+": inside a try, which lets its exit 4 pass")
				default:
					if problem := passesUnrecorded(ctx, run, installs); problem != "" {
						out = append(out, where+": "+problem)
					}
				}
			}
		}

		for k, c := range x {
			if k == "config" {
				continue
			}

			out = append(out, swallowedIn(ctx, c, job, tried || k == "try", installs)...)
		}

		return out
	}

	return nil
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
	case errors.As(err, &exit) && exit.ExitCode() == exitUnrecorded:
		return ""
	case err == nil:
		return "exits 0 when the tool exits 4"
	case errors.As(err, &exit):
		return fmt.Sprintf("exits %d when the tool exits 4: %s", exit.ExitCode(), gostrings.TrimSpace(string(out)))
	default:
		return "can't run: " + err.Error()
	}
}
