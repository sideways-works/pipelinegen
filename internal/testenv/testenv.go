// Package testenv keeps a test binary's git off the machine's global and system config and its
// environment clear of a build's; every package whose tests run git calls Git from an init.
package testenv

import (
	"os"
	"slices"
	"strings"
)

// kept are the variables tests read to be told something by whoever runs them: where bazel-diff
// and Bazel's output are, how big a benchmark is.
var kept = []string{"PIPELINEGEN_DEPS_SCALE", "PIPELINEGEN_WRITE_SPEC"}

// Scrubbed tells whether a variable of the tests' starting environment is dropped: every one the
// tool reads, and the token, so a test sees only what it sets itself.
func Scrubbed(name string) bool {
	if name == "GITHUB_TOKEN" || name == "GH_TOKEN" {
		return true
	}

	return strings.HasPrefix(name, "PIPELINEGEN_") && !strings.HasPrefix(name, "PIPELINEGEN_TEST_") && !slices.Contains(kept, name)
}

// scrubbedMark tells a process the test binary starts (the tool under test) that its parent
// scrubbed already: what it finds is what the test set for it.
const scrubbedMark = "PIPELINEGEN_TEST_SCRUBBED"

// scrub drops the scrubbed variables from the process's environment, once: in the test process
// itself, never in a child it starts.
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

// Git drops a build's environment and points git's global config at the empty device, the system
// config off, for this process and its children; HOME stays, for Bazel's and bazelisk's caches.
func Git() {
	scrub()

	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"} {
		if err := os.Setenv(k, v); err != nil {
			panic("testenv: " + err.Error())
		}
	}
}
