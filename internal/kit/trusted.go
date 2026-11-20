package kit

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/gitssh"
	"github.com/sideways-works/pipelinegen/internal/toolcalls"
	"github.com/sideways-works/pipelinegen/plugin"
)

// trustedProblems are the tool's calls that don't get the spec's trusted keys byte for byte in
// plugin.EnvTrusted; a call without a path is a command a message names for a person.
func trustedProblems(s plugin.Spec, steps []stepCall) []string {
	var out []string

	for _, sc := range steps {
		if !strings.Contains(sc.prog, "/") {
			continue
		}

		got, ok := sc.env[plugin.EnvTrusted]

		switch {
		case !ok:
			out = appendNew(out, sc.where+": no "+plugin.EnvTrusted)
		case got != s.Trusted:
			out = appendNew(out, sc.where+": "+plugin.EnvTrusted+" isn't the spec's trusted keys as they are")
		}
	}

	return out
}

// pipelineStepProblems are the tool's calls not told by plugin.EnvPipelineStep=1 that they are a
// pipeline's step: a planning step then keeps no Bazel output and starts Bazel cold every build.
func pipelineStepProblems(steps []stepCall) []string {
	var out []string

	for _, sc := range steps {
		if !strings.Contains(sc.prog, "/") {
			continue
		}

		if got := sc.env[plugin.EnvPipelineStep]; got != "1" {
			out = appendNew(out, fmt.Sprintf("%s: %s is %q, not 1", sc.where, plugin.EnvPipelineStep, got))
		}
	}

	return out
}

// repoTaskProblems are the repository's own tasks handed a var the spec declares for no task: a
// runner gives them their declared secrets and nothing else.
func repoTaskProblems(s plugin.Spec, text []byte) []string {
	declared := declaredSecrets(s)

	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return []string{"the pipeline isn't YAML: " + err.Error()}
	}

	var (
		out  []string
		walk func(v any, job string)
	)

	walk = func(v any, job string) {
		switch x := v.(type) {
		case map[string]any:
			if name, ok := x["name"].(string); ok && job == "" {
				if _, isJob := x["plan"]; isJob {
					job = name
				}
			}

			_, file := x["file"].(string)
			_, config := x["config"]

			if x["task"] != nil && file && !config {
				out = append(out, givenTo(fmt.Sprintf("job %s, task %v", job, x["task"]), x, declared)...)
			}

			for _, k := range slices.Sorted(maps.Keys(x)) {
				walk(x[k], job)
			}
		case []any:
			for _, e := range x {
				walk(e, job)
			}
		}
	}

	walk(doc, "")

	return out
}

func declaredSecrets(s plugin.Spec) map[string]bool {
	declared := map[string]bool{}

	for _, r := range s.Runs {
		for _, rs := range r.Suites {
			for _, m := range []map[string]string{rs.Secrets.Prepare, rs.Secrets.Batch, rs.Secrets.Whole} {
				for _, v := range m {
					declared[v] = true
				}
			}
		}
	}

	return declared
}

func givenTo(where string, step map[string]any, declared map[string]bool) []string {
	var out []string

	for _, field := range []string{"params", "vars"} {
		m, _ := step[field].(map[string]any) //nolint:errcheck // a task without them is handed nothing

		for _, k := range slices.Sorted(maps.Keys(m)) {
			if k == plugin.EnvTrusted {
				out = appendNew(out, where+": "+plugin.EnvTrusted+", which only the tool's own steps get")
			}

			if k == plugin.EnvPipelineStep {
				out = appendNew(out, where+": "+plugin.EnvPipelineStep+", the mark of the tool's own steps")
			}

			if strings.HasPrefix(k, "PIPELINEGEN_SSH_KEY_") {
				out = appendNew(out, where+": "+k+", an ssh key of the tool's, which no task of the repository's gets")
			}

			for _, ref := range varRef.FindAllStringSubmatch(fmt.Sprint(m[k]), -1) {
				if !declared[ref[1]] {
					out = appendNew(out, fmt.Sprintf("%s: %s.%s is ((%s)), which no secret declares", where, field, k, ref[1]))
				}
			}
		}
	}

	return out
}

const planCmd = "plan"

func writingTokens() []string {
	var out []string

	for _, p := range plugin.Purposes {
		if p != plugin.PurposeRead {
			out = append(out, toolcalls.PurposeEnv(p))
		}
	}

	return append(out, toolcalls.EnvToken, gitssh.KeyEnv(plugin.PurposeStore))
}

// proposedPlanProblems are, with tokens split by purpose, the steps that plan a change's own tree
// (plan --out, which runs its code) holding a token that writes: they may only read.
func proposedPlanProblems(steps []stepCall) []string {
	var out []string

	for _, sc := range steps {
		if !strings.Contains(sc.prog, "/") || len(sc.args) == 0 || sc.args[0] != planCmd || !hasFlag(sc.args, "out") {
			continue
		}

		for _, env := range writingTokens() {
			if _, ok := sc.env[env]; ok {
				out = appendNew(out, sc.where+": plans the change's own tree holding "+env)
			}
		}
	}

	return out
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		a, _, _ = strings.Cut(strings.TrimLeft(a, "-"), "=")
		if a == name {
			return true
		}
	}

	return false
}

type holdings struct {
	trusted, step, repo, planning, forge, ssh, packages []string
	packagesBuilt                                       bool
}

func (h *holdings) add(v specVariant, text []byte, runs *taskRuns) {
	if steps, err := stepCalls(text, runs); err == nil {
		for _, p := range trustedProblems(v.spec, steps) {
			h.trusted = appendNew(h.trusted, v.label()+p)
		}

		for _, p := range pipelineStepProblems(steps) {
			h.step = appendNew(h.step, v.label()+p)
		}

		if v.name == splitTokens || v.name == forgeRun {
			for _, p := range proposedPlanProblems(steps) {
				h.planning = appendNew(h.planning, v.label()+p)
			}
		}
	}

	for _, p := range repoTaskProblems(v.spec, text) {
		h.repo = appendNew(h.repo, v.label()+p)
	}

	if v.name == packagesRun {
		h.packagesBuilt = true

		if steps, err := stepCalls(text, runs); err == nil {
			for _, p := range packageProblems(v.spec, steps, text) {
				h.packages = appendNew(h.packages, v.label()+p)
			}
		}
	}

	if v.name == forgeRun {
		h.forge = append(h.forge, forgeProblems(text, runs)...)

		if steps, err := stepCalls(text, runs); err == nil {
			h.ssh = append(h.ssh, sshKeyProblems(v.spec, steps)...)
		}
	}
}

// sshKeyProblems are, with ssh keys in the spec, the tool's steps holding a purpose's token without
// its key, or a key that isn't the spec's var.
func sshKeyProblems(s plugin.Spec, steps []stepCall) []string {
	var out []string

	for _, sc := range steps {
		if !strings.Contains(sc.prog, "/") {
			continue
		}

		for _, p := range gitssh.Purposes {
			_, token := sc.env[toolcalls.PurposeEnv(p)]
			key, has := sc.env[gitssh.KeyEnv(p)]

			switch {
			case token && !has:
				out = appendNew(out, sc.where+": holds the "+p+" token without "+gitssh.KeyEnv(p))
			case has && key != "(("+s.SSHKeys[p]+"))":
				out = appendNew(out, sc.where+": "+gitssh.KeyEnv(p)+" isn't the spec's var")
			case has && !token:
				out = appendNew(out, sc.where+": holds "+gitssh.KeyEnv(p)+" without the "+p+" token: a key for what the step doesn't do")
			}
		}
	}

	return out
}

func forgeProblems(text []byte, runs *taskRuns) []string {
	steps, err := stepCalls(text, runs)
	if err != nil {
		return []string{err.Error()}
	}

	var unplanned, out, from bool

	for _, sc := range steps {
		if !strings.Contains(sc.prog, "/") || len(sc.args) == 0 {
			continue
		}

		switch {
		case sc.args[0] == "unplanned":
			unplanned = true
		case sc.args[0] == planCmd && hasFlag(sc.args, "out"):
			out = true
		case sc.args[0] == planCmd && hasFlag(sc.args, "from"):
			from = true
		}
	}

	var problems []string

	if !unplanned || !out || !from {
		problems = append(problems, fmt.Sprintf("the chain lacks a step: unplanned %v, plan --out %v, plan --from %v", unplanned, out, from))
	}

	if strings.Contains(string(text), "type: github-pr") {
		problems = append(problems, "a pull request resource starts a run whose changes come from a forge")
	}

	return problems
}

func (h *holdings) report(k *kit) {
	k.Check("every step of the tool's gets the spec's trusted keys", strings.Join(firstFew(h.trusted), "; "))
	k.Check("every step of the tool's is told it runs as a pipeline's step", strings.Join(firstFew(h.step), "; "))
	k.Check("the repository's tasks get their declared secrets and nothing else", strings.Join(firstFew(h.repo), "; "))
	k.Check("with tokens by purpose, planning a change's own tree holds no token that writes", strings.Join(firstFew(h.planning), "; "))

	if k.can(plugin.CapSSHKeys) {
		k.Check("a step holds the ssh key of each purpose it holds the token of, and no other", strings.Join(firstFew(h.ssh), "; "))
	} else {
		k.Skip("a step holds the ssh key of each purpose it holds the token of, and no other", "the runner hands steps no ssh key")
	}

	if h.packagesBuilt {
		k.Check("every step the runner writes installs its packages first, every cached image is built, and the registry's credentials reach "+
			"no step that runs the repository's code", strings.Join(firstFew(h.packages), "; "))
	} else {
		k.Skip("every step the runner writes installs its packages first, every cached image is built, and the registry's credentials reach "+
			"no step that runs the repository's code", "the runner installs no packages")
	}

	if k.can(plugin.CapForgeChanges) {
		k.Check("a forge's changes are swept, planned in two steps and handed on", strings.Join(firstFew(h.forge), "; "))
	} else {
		k.Skip("a forge's changes are swept, planned in two steps and handed on", "the runner doesn't build a forge's chain")
	}
}
