package driver

import (
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// shared are the tool's own variables a driver and its tests keep: what a
// batch task's script saw of its run, none of them a secret.
var shared = []string{
	"PIPELINEGEN_RUN", "PIPELINEGEN_SUITE", "PIPELINEGEN_BATCH",
	"PIPELINEGEN_RECORD", "PIPELINEGEN_RUN_FILE",
}

// Env is the batch task's environment less tokens (a declared batch secret stays) and PIPELINEGEN_ variables,
// tools first on PATH, then params and exports: hygiene, not a boundary, as a test can read its parent's.
type Env struct {
	// Base is the task's environment (os.Environ when nil).
	Base    []string
	Tools   string
	Secrets []string
	Params  map[string]string
	Cache   string
	// Commit is the run's commit, given as plugin.EnvCommit and no var of Vars: a driver passing those to
	// its tests by name would make it part of every test's cache key.
	Commit string
}

// Vars are the variables the tests get beyond the driver's own environment: the params, the declared
// secrets' values and the exports, which a driver whose tool hides the environment passes by name.
func (e Env) Vars(exports map[string]string) map[string]string {
	out := map[string]string{}

	for k, v := range e.Params {
		out[k] = strings.ReplaceAll(v, "{cache}", e.Cache)
	}

	base := e.base()

	for _, kv := range base {
		name, value, _ := strings.Cut(kv, "=")
		if slices.Contains(e.Secrets, name) {
			out[name] = value
		}
	}

	maps.Copy(out, exports)

	return out
}

func (e Env) base() []string {
	if e.Base != nil {
		return e.Base
	}

	return os.Environ()
}

// For is a driver's environment given the exports so far; PIPELINEGEN_MARKS is never in it, only in a
// call running a bundle whose marks the tool serves (withMarks).
func (e Env) For(exports map[string]string) []string {
	over := e.Vars(exports)

	var out []string

	for _, kv := range e.base() {
		name, value, _ := strings.Cut(kv, "=")

		switch {
		case over[name] != "" || hasKey(over, name):
			continue
		case slices.Contains(e.Secrets, name):
		case drops(name):
			continue
		case name == "PATH" && e.Tools != "":
			kv = "PATH=" + e.Tools + string(os.PathListSeparator) + value
		}

		out = append(out, kv)
	}

	if e.Tools != "" && !slices.ContainsFunc(out, func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }) {
		out = append(out, "PATH="+e.Tools)
	}

	for _, k := range slices.Sorted(maps.Keys(over)) {
		out = append(out, k+"="+over[k])
	}

	if e.Commit != "" {
		out = append(out, plugin.EnvCommit+"="+e.Commit)
	}

	return out
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]

	return ok
}

func drops(name string) bool {
	upper := strings.ToUpper(name)

	if upper == "GITHUB_TOKEN" || upper == "GH_TOKEN" {
		return true
	}

	return strings.HasPrefix(upper, "PIPELINEGEN_") && !slices.Contains(shared, name)
}

// withMarks is env with PIPELINEGEN_MARKS only for a call running a bundle whose marks the tool serves:
// a hook marks only when it sees it, so a slice run alone never waits for marks nobody takes.
func withMarks(env []string, req plugin.DriverRequest) []string {
	if env == nil {
		env = os.Environ()
	}

	out := slices.DeleteFunc(slices.Clone(env), func(kv string) bool { return strings.HasPrefix(kv, plugin.EnvMarks+"=") })

	if req.Marks != nil && req.Marks.Dir != "" {
		out = append(out, plugin.EnvMarks+"="+req.Marks.Dir)
	}

	return out
}
