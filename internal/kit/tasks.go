package kit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	gostrings "strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/plugin"
)

// taskRunner runs the tasks of a runner's pipeline that run the tool through something other than a
// script naming it, the runner's own program say: each task the pipeline marks as the tool's step
// (PIPELINEGEN_PIPELINE_STEP), in the order its job runs it, against a stand-in for the tool that
// notes every call and writes what the call names as its output. A task gets what Concourse would
// hand it: its inputs as earlier tasks of its job left them or a get fetched them, empty outputs and
// caches, its params, and, as a task that fetched them would hold, every tool the spec pins in
// tools/ (the runner's own program in tools/plugins/<name>, the others stand-ins), each at its pin.
// No task reaches the network: every proxy names a port nothing listens on.
type taskRunner struct {
	spec     plugin.Spec
	runner   string
	work     string
	installs []string
	// tampered places each tool pinned by sums changed, so that it no longer matches its line in
	// the list beside it.
	tampered bool
}

// taskRuns are what the tasks of one pipeline did when they ran, by task.
type taskRuns struct {
	byTask map[string]*taskRun
}

type taskRun struct {
	calls [][]string
	// recording is the first report step among the calls, unrecorded the task's exit when the
	// stand-in exits 4 for a report, and said what it printed then.
	recording  string
	unrecorded int
	said       string
	// task names the task; exit and output are its first run's, listed where the tools pinned by
	// sums in its directory don't check against their lists after it.
	task   string
	exit   int
	output string
	listed []string
	// handed says the task takes tools as an input, which an earlier task of its job fetched.
	handed bool
}

// standInTool is the kit's pipelinegen, and every other pinned tool but the runner. It notes each
// call in .kit-calls beside tools/, one argument after another, each ended by \x1f; opens every gate
// but a decision's; and writes each --out as the command does: the pipeline file, the sweep's
// heads (unplanned), or a directory.
const standInTool = `#!/bin/sh
dir=$(dirname "$0")
for a in "$@"; do printf '%s\037' "$a"; done >> "$dir/../.kit-calls"
echo >> "$dir/../.kit-calls"
if [ -e "$dir/.kit-unrecorded" ] && [ "$1" = report ]; then exit 4; fi
[ "$1" = git-auth ] && exit 0
decide=
for a in "$@"; do case "$a" in -decide|--decide) decide=1 ;; esac; done
head() {
  mkdir -p "$1/.git/resource"
  echo '{"commit":"0123456789abcdef0123456789abcdef01234567","pr":"7"}' > "$1/.git/resource/version.json"
  echo '[]' > "$1/.git/resource/metadata.json"
  echo '{"run":"pr","change":"7","commit":"0123456789abcdef0123456789abcdef01234567"}' > "$1/run.json"
}
out() {
  case "$1" in
  pipeline) mkdir -p "$(dirname "$2")" && echo 'jobs: []' > "$2" ;;
  unplanned) head "$2/plan/7" && head "$2/handoff/9" ;;
  *) mkdir -p "$2" ;;
  esac
}
gate() { [ -n "$decide" ] || { mkdir -p "$1" && echo '["1"]' > "$1/gate.json"; }; }
prev=
for a in "$@"; do
  case "$prev" in -gate|--gate) gate "$a" ;; -out|--out) out "$1" "$a" ;; esac
  case "$a" in -gate=*|--gate=*) gate "${a#*=}" ;; -out=*|--out=*) out "$1" "${a#*=}" ;; esac
  prev=$a
done
exit 0
`

// deadProxy is where the tasks' proxies point: nothing listens there.
const deadProxy = "http://127.0.0.1:9"

const (
	tryKey         = "try"
	callsFile      = ".kit-calls"
	unrecordedFlag = ".kit-unrecorded"
	taskTimeout    = time.Minute
)

// toolsStep says the pipeline marks a task as a step of the tool's.
func toolsStep(step, cfg map[string]any) bool {
	for _, ps := range []any{cfg["params"], step["params"]} {
		if m, ok := ps.(map[string]any); ok && fmt.Sprint(m[plugin.EnvPipelineStep]) == "1" {
			return true
		}
	}

	return false
}

func taskKey(job string, step map[string]any) string {
	data, err := yaml.Marshal(step)
	if err != nil {
		return job
	}

	return job + "\n" + string(data)
}

// result is what a task of the pipeline did when it ran; nil for a task that didn't run.
func (tr *taskRuns) result(job string, step map[string]any) *taskRun {
	if tr == nil {
		return nil
	}

	return tr.byTask[taskKey(job, step)]
}

// runTasks runs the pipeline's tasks that the kit can't read the calls of.
func runTasks(ctx context.Context, r taskRunner, text []byte) *taskRuns {
	out := &taskRuns{byTask: map[string]*taskRun{}}

	var doc struct {
		Jobs []map[string]any `yaml:"jobs"`
	}

	if err := yaml.Unmarshal(text, &doc); err != nil {
		return out
	}

	for _, job := range doc.Jobs {
		r.runJob(ctx, job, out.byTask)
	}

	return out
}

// runJob runs a job's tasks in its plan's order, then its hooks, in a directory of the job's that
// holds the artifacts its gets fetched and its tasks put out.
func (r taskRunner) runJob(ctx context.Context, job map[string]any, out map[string]*taskRun) {
	name := fmt.Sprint(job["name"])

	dir, err := os.MkdirTemp(r.work, "job-")
	if err != nil {
		return
	}

	defer os.RemoveAll(dir) //nolint:errcheck // a leftover directory under the kit's work harms nothing

	arts := filepath.Join(dir, "artifacts")

	var walk func(v any)

	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			if get, ok := x["get"].(string); ok {
				fetched(filepath.Join(arts, get))
			}

			if cfg, ok := x["config"].(map[string]any); ok && x["task"] != nil {
				key := taskKey(name, x)
				if _, ran := out[key]; !ran && toolsStep(x, cfg) && len(taskCalls("", x, cfg)) == 0 {
					out[key] = r.runTask(ctx, dir, arts, x, cfg)
				}
			}

			for _, k := range []string{"in_parallel", "steps", "do", tryKey, "aggregate"} {
				walk(x[k])
			}

			for _, h := range hookKeys {
				walk(x[h])
			}
		}
	}

	walk(job["plan"])

	for _, h := range hookKeys {
		walk(job[h])
	}
}

// fetched is a get's artifact: a checkout of a commit, or a pull request's or a hand-off's head.
func fetched(dir string) {
	if _, err := os.Stat(dir); err == nil {
		return
	}

	for p, body := range map[string]string{
		".git/resource/version.json":  `{"commit":"0123456789abcdef0123456789abcdef01234567","pr":"7"}`,
		".git/resource/metadata.json": "[]",
		"version":                     `{"run":"pr","change":"7","commit":"0123456789abcdef0123456789abcdef01234567"}`,
		"run.json":                    `{"run":"pr","change":"7","commit":"0123456789abcdef0123456789abcdef01234567"}`,
	} {
		f := filepath.Join(dir, p)
		if os.MkdirAll(filepath.Dir(f), 0o750) != nil || os.WriteFile(f, []byte(body+"\n"), 0o600) != nil {
			return
		}
	}
}

func (r taskRunner) runTask(ctx context.Context, dir, arts string, step, cfg map[string]any) *taskRun {
	res := &taskRun{unrecorded: -1, task: fmt.Sprint(step["task"]), handed: slices.Contains(names(cfg["inputs"], "name"), "tools")}

	td, err := r.prepare(dir, arts, step, cfg)
	if err != nil {
		res.said, res.exit, res.output = err.Error(), -1, "can't run: "+err.Error()

		return res
	}

	res.exit, res.output = r.exec(ctx, td, step, cfg)
	res.listed = r.listedProblems(td)
	res.calls = recordedCalls(td)

	for _, c := range res.calls {
		if s := reportStep(c); s != "" {
			res.recording = s

			break
		}
	}

	if res.recording != "" {
		res.unrecorded, res.said = r.unrecordedExit(ctx, dir, arts, step, cfg)
	}

	for _, o := range names(cfg["outputs"], "name") {
		to := filepath.Join(arts, mapped(step["output_mapping"], o))
		if os.RemoveAll(to) == nil {
			_ = os.CopyFS(to, os.DirFS(filepath.Join(td, o))) //nolint:errcheck // an output that can't be kept is an empty input later
		}
	}

	return res
}

func reportStep(args []string) string {
	if len(args) > 1 && args[0] == "report" && slices.Contains(recordingSteps, "report "+args[1]) {
		return "report " + args[1]
	}

	return ""
}

// unrecordedExit runs the task again with a stand-in whose report exits 4.
func (r taskRunner) unrecordedExit(ctx context.Context, dir, arts string, step, cfg map[string]any) (int, string) {
	td, err := r.prepare(dir, arts, step, cfg)
	if err != nil {
		return -1, "can't run: " + err.Error()
	}

	if err := os.WriteFile(filepath.Join(td, "tools", unrecordedFlag), nil, 0o600); err != nil {
		return -1, "can't run: " + err.Error()
	}

	code, said := r.exec(ctx, td, step, cfg)

	return code, said
}

func mapped(m any, name string) string {
	if mm, ok := m.(map[string]any); ok {
		if to, ok := mm[name].(string); ok {
			return to
		}
	}

	return name
}

// prepare is a task's directory as Concourse would hand it over, with the pinned tools in tools/.
func (r taskRunner) prepare(dir, arts string, step, cfg map[string]any) (string, error) {
	td, err := os.MkdirTemp(dir, "task-")
	if err != nil {
		return "", fmt.Errorf("a task's directory: %w", err)
	}

	inputs, _ := cfg["inputs"].([]any) //nolint:errcheck // a task without inputs has none

	for _, in := range inputs {
		m, ok := in.(map[string]any)
		if !ok {
			continue
		}

		name := path.Clean(fmt.Sprint(m["name"]))
		from := filepath.Join(arts, mapped(step["input_mapping"], name))

		if _, err := os.Stat(from); err != nil {
			if m["optional"] == true {
				continue
			}

			from = ""
		}

		if from == "" {
			err = os.MkdirAll(filepath.Join(td, name), 0o750)
		} else {
			err = os.CopyFS(filepath.Join(td, name), os.DirFS(from))
		}

		if err != nil {
			return "", fmt.Errorf("input %s: %w", name, err)
		}
	}

	for _, d := range append(names(cfg["outputs"], "name"), names(cfg["caches"], "path")...) {
		if err := os.MkdirAll(filepath.Join(td, d), 0o750); err != nil {
			return "", fmt.Errorf("%s: %w", d, err)
		}
	}

	return td, r.placeTools(td)
}

func (r taskRunner) placeTools(td string) error {
	if err := os.MkdirAll(filepath.Join(td, "tools", "plugins"), 0o750); err != nil {
		return fmt.Errorf("tools: %w", err)
	}

	for _, t := range r.spec.Tools {
		dest := filepath.Join(td, "tools", "plugins", t.Name)

		switch t.Kind {
		case plugin.ToolPipelinegen:
			dest = filepath.Join(td, "tools", toolName)
		case plugin.ToolBazelisk:
			dest = filepath.Join(td, "tools", "bazelisk")
		}

		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}

		if t.Kind == plugin.KindRunner {
			if err := linkOrCopy(r.runner, dest); err != nil {
				return fmt.Errorf("the runner: %w", err)
			}

			continue
		}

		if err := os.WriteFile(dest, []byte(standInTool), 0o700); err != nil { //nolint:gosec // the kit's own stand-in
			return fmt.Errorf("%s: %w", t.Name, err)
		}
	}

	return r.placeLists(td)
}

// placeLists puts beside each tool pinned by sums the list its pin names, as a task that fetched
// the tool holds it.
func (r taskRunner) placeLists(td string) error {
	for _, t := range r.spec.Tools {
		if t.Sums == "" {
			continue
		}

		list := filepath.Join(td, "tools", "plugins", t.Name+sumsSuffix)
		if err := os.WriteFile(list, sumsList(t, placedSum(t, r.runner)), 0o600); err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}

		if r.tampered {
			if err := tamper(filepath.Join(td, "tools", "plugins", t.Name)); err != nil {
				return fmt.Errorf("%s: %w", t.Name, err)
			}
		}
	}

	return nil
}

// tamper changes a placed tool so that it still runs: a program, or a script, with a line more.
// A link to the runner under test is replaced by a copy first.
func tamper(p string) error {
	data, err := os.ReadFile(p) //nolint:gosec // a tool the kit placed
	if err != nil {
		return fmt.Errorf("tamper: %w", err)
	}

	if err := os.Remove(p); err != nil {
		return fmt.Errorf("tamper: %w", err)
	}

	if err := os.WriteFile(p, append(data, "\n# changed\n"...), 0o700); err != nil { //nolint:gosec // the kit's own stand-in or the runner under test
		return fmt.Errorf("tamper: %w", err)
	}

	return nil
}

func linkOrCopy(from, to string) error {
	if os.Link(from, to) == nil {
		return nil
	}

	data, err := os.ReadFile(from) //nolint:gosec // the runner under test
	if err != nil {
		return fmt.Errorf("read the runner: %w", err)
	}

	if err := os.WriteFile(to, data, 0o700); err != nil { //nolint:gosec // the runner under test, run as its task would
		return fmt.Errorf("place the runner: %w", err)
	}

	return nil
}

// exec runs the task's command as Concourse would, with its params and no installs: the kit
// installs no package on the machine it runs on.
func (r taskRunner) exec(ctx context.Context, td string, step, cfg map[string]any) (int, string) {
	run, _ := cfg["run"].(map[string]any) //nolint:errcheck // a task without a run runs nothing

	args := stringList(run["args"])
	for i, a := range args {
		for _, in := range r.installs {
			a = gostrings.ReplaceAll(a, in, ":\n")
		}

		args[i] = a
	}

	home := filepath.Join(td, ".kit-home")
	if err := os.MkdirAll(home, 0o750); err != nil {
		return -1, "can't run: " + err.Error()
	}

	env := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
		"http_proxy=" + deadProxy, "https_proxy=" + deadProxy, "HTTP_PROXY=" + deadProxy, "HTTPS_PROXY=" + deadProxy,
		"ALL_PROXY=" + deadProxy, "all_proxy=" + deadProxy, "NO_PROXY=", "no_proxy=",
	}

	params := map[string]string{}

	for _, ps := range []any{cfg["params"], step["params"]} {
		if m, ok := ps.(map[string]any); ok {
			for k, v := range m {
				if v == nil {
					v = ""
				}

				params[k] = fmt.Sprint(v)
			}
		}
	}

	for _, k := range slices.Sorted(maps.Keys(params)) {
		env = append(env, k+"="+params[k])
	}

	ctx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, fmt.Sprint(run["path"]), args...) //nolint:gosec // the runner's own step, run as Concourse would
	cmd.Dir, cmd.Env = td, env

	said, err := cmd.CombinedOutput()

	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), gostrings.TrimSpace(string(said))
	} else if err != nil {
		return -1, "can't run: " + err.Error()
	}

	return 0, gostrings.TrimSpace(string(said))
}

func recordedCalls(td string) [][]string {
	data, err := os.ReadFile(filepath.Join(td, callsFile)) //nolint:gosec // the task's own directory
	if err != nil {
		return nil
	}

	var out [][]string

	for line := range gostrings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}

		out = append(out, gostrings.Split(gostrings.TrimSuffix(line, "\x1f"), "\x1f"))
	}

	return out
}

// calls are the calls of the tool a task made when it ran, as the stand-in noted them.
func (tr *taskRuns) calls(job string, base stepCall, step map[string]any) []stepCall {
	res := tr.result(job, step)
	if res == nil {
		return nil
	}

	out := make([]stepCall, 0, len(res.calls))

	for _, c := range res.calls {
		sc := base
		sc.prog, sc.args = "tools/"+toolName, c
		out = append(out, sc)
	}

	return out
}
