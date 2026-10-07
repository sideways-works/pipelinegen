package secret

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// PipelineStepVar is set, to 1, in every step of a generated pipeline that
// runs the tool, and nowhere else: it says the process is a pipeline's
// step and nothing more.
const PipelineStepVar = "PIPELINEGEN_PIPELINE_STEP"

// jobURLVar is the link to a step's job; older runners set it in their suite
// steps alone, which count as pipeline steps by it.
const jobURLVar = "PIPELINEGEN_JOB_URL"

// InPipeline tells whether this process is a pipeline's step rather than a
// person's command.
func InPipeline() bool { return os.Getenv(PipelineStepVar) != "" || os.Getenv(jobURLVar) != "" }

// ChildEnv is the environment of a program the tool starts on a tree that
// may be a proposed change's (Bazel and its repository rules, bazelisk,
// bazel-diff): the process's, less every purpose's token and every other
// PIPELINEGEN_ variable. keep names a variable that stays even so: the git
// read token Bazel's fetches of private modules need. It is hygiene, not a
// boundary.
func ChildEnv(keep ...string) []string {
	var env []string

	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")

		if slices.Contains(keep, name) || !childDrops(name) {
			env = append(env, kv)
		}
	}

	return env
}

func childDrops(name string) bool {
	upper := strings.ToUpper(name)

	return upper == "GITHUB_TOKEN" || upper == "GH_TOKEN" || strings.HasPrefix(upper, "PIPELINEGEN_")
}

// gitHardening are the settings every git call of the tool's takes on a
// tree or a fetch: no hook, no fsmonitor, no LFS smudge, whatever a
// worker's or a tree's set-up says.
var gitHardening = [][2]string{{"core.hooksPath", "/dev/null"}, {"core.fsmonitor", "false"}}

// HardenGit adds the hardening to a git environment: the settings as
// further GIT_CONFIG_ entries after those env holds, LFS's smudge off, and
// in a pipeline's step no system or global config either (on a person's
// machine their own git and ssh set-up keeps working).
func HardenGit(env []string) []string {
	out := make([]string, 0, len(env)+8)
	count := 0

	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")

		switch name {
		case "GIT_CONFIG_COUNT":
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				count = n
			}

			continue
		case "GIT_LFS_SKIP_SMUDGE":
			continue
		case "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL":
			if InPipeline() {
				continue
			}
		}

		out = append(out, kv)
	}

	for i, kv := range gitHardening {
		n := strconv.Itoa(count + i)
		out = append(out, "GIT_CONFIG_KEY_"+n+"="+kv[0], "GIT_CONFIG_VALUE_"+n+"="+kv[1])
	}

	out = append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count+len(gitHardening)), "GIT_LFS_SKIP_SMUDGE=1")

	if InPipeline() {
		out = append(out, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	}

	return out
}
