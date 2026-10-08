// Package testenv keeps a test binary to what its tests set up: git sees no
// global or system config of the machine, so no credential helper of the
// machine's answers a git call, and the process carries none of the
// environment of a build that runs the tests. Every package whose tests run
// git or the tool's commands calls Git from an init in a test file.
package testenv

import (
	"os"
	"slices"
	"strings"
)

// kept are the variables tests read to be told something by whoever runs
// them: where bazel-diff and Bazel's output are, how big a benchmark is.
var kept = []string{"PIPELINEGEN_DEPS_SCALE", "PIPELINEGEN_WRITE_SPEC"}

// Scrubbed tells whether a variable of the environment the tests start in is
// dropped: every one the tool reads, and the token, so a test sees only what
// it sets itself.
func Scrubbed(name string) bool {
	if name == "GITHUB_TOKEN" || name == "GH_TOKEN" {
		return true
	}

	return strings.HasPrefix(name, "PIPELINEGEN_") && !strings.HasPrefix(name, "PIPELINEGEN_TEST_") && !slices.Contains(kept, name)
}

// scrubbedMark tells a process the test binary starts (the tool under
// test, run as a child) that its parent scrubbed already: what it finds is
// what the test set for it.
const scrubbedMark = "PIPELINEGEN_TEST_SCRUBBED"

// scrub drops the scrubbed variables from the process's environment, once:
// in the test process itself, never in a child it starts.
func scrub() {
	if os.Getenv(scrubbedMark) != "" {
		return
	}

	defer func() {
		if err := os.Setenv(scrubbedMark, "1"); err != nil {
			panic("testenv: " + err.Error())
		}
	}()

	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); Scrubbed(name) {
			if err := os.Unsetenv(name); err != nil {
				panic("testenv: " + err.Error())
			}
		}
	}
}

// Git drops the environment of a build running the tests, then points git's
// global config at the empty device and turns the system config off, for
// this process and every process it starts; no file is made. HOME stays:
// Bazel and bazelisk keep their caches under it.
func Git() {
	scrub()

	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"} {
		if err := os.Setenv(k, v); err != nil {
			panic("testenv: " + err.Error())
		}
	}
}
