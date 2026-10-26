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

// Env is a driver's environment, made from the batch task's: its variables
// less every token and every other PIPELINEGEN_ variable, the tools
// directory first on PATH, and over them the suite's params and the set-ups'
// exports. A declared batch secret stays. It is hygiene, not a boundary: a
// test runs as the same user and can read its parent's environment.
type Env struct {
	// Base is the task's environment (os.Environ when nil).
	Base    []string
	Tools   string
	Secrets []string
	Params  map[string]string
	Cache   string
	// Commit is the commit the run runs, given to the driver as
	// plugin.EnvCommit. It is no var of Vars: a driver that passes those to
	// its tests by name would make it part of every test's cache key.
	Commit string
}

// Vars are the variables the tests get beyond the driver's own process
// environment, by name: the params (filled in), the declared secrets'
// values, and the exports. A driver whose tool hides the environment from
// tests passes each by name.
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

// For is the environment of a driver given the exports so far.
// PIPELINEGEN_MARKS is never in it: only a call that runs a bundle whose
// marks the tool serves gets it (withMarks).
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

// withMarks is env for one call of a driver, with PIPELINEGEN_MARKS only
// when the call runs a bundle whose marks the tool serves: a hook marks only
// when it sees the variable, so a slice run alone never waits for marks
// nobody takes. A nil env is the process's own.
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
