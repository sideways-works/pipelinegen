package kit

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	gostrings "strings"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/toolcalls"
	"github.com/sideways-works/pipelinegen/plugin"
)

// Commands are the tool's commands as a step calls them; the tool hands
// the kit its own.
type Commands = toolcalls.Commands

var inScript = regexp.MustCompile(`(?:^|[\s;&|("])((?:[^\s"'$();&|]*/)?pipelinegen)((?:[ \t]+[^\s;&|)]+)+)`)

type stepCall struct {
	where string
	// prog is the program as the task names it; a bare name runs from PATH, where no bootstrapped
	// tool is, so it is a message for a person, whose flags alone are checked.
	prog    string
	args    []string
	inputs  []string
	outputs []string
	caches  []string
	env     map[string]string
	script  string
}

func stepCalls(text []byte) ([]stepCall, error) {
	var doc any
	if err := yaml.Unmarshal(text, &doc); err != nil {
		return nil, fmt.Errorf("the pipeline isn't YAML: %w", err)
	}

	var (
		out  []stepCall
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

			if cfg, ok := x["config"].(map[string]any); ok && x["task"] != nil {
				out = append(out, taskCalls(fmt.Sprintf("job %s, step %v", job, x["task"]), x, cfg)...)
			}

			for _, k := range slices.Sorted(func(yield func(string) bool) {
				for k := range x {
					if !yield(k) {
						return
					}
				}
			}) {
				walk(x[k], job)
			}
		case []any:
			for _, e := range x {
				walk(e, job)
			}
		}
	}

	walk(doc, "")

	return out, nil
}

func taskCalls(where string, step, cfg map[string]any) []stepCall {
	run, _ := cfg["run"].(map[string]any) //nolint:errcheck // a task without a run calls nothing
	p, _ := run["path"].(string)          //nolint:errcheck // a path that isn't a string is no call
	args := stringList(run["args"])

	base := stepCall{
		where: where, inputs: names(cfg["inputs"], "name"), outputs: names(cfg["outputs"], "name"),
		caches: names(cfg["caches"], "path"), env: map[string]string{},
	}

	if b := path.Base(p); (b == "sh" || b == "bash") && len(args) > 1 {
		base.script = args[1]
	}

	// the step's own params come after its config's, as Concourse merges them
	for _, ps := range []any{cfg["params"], step["params"]} {
		if m, ok := ps.(map[string]any); ok {
			for k, v := range m {
				base.env[k] = fmt.Sprint(v)
			}
		}
	}

	if path.Base(p) == toolName {
		c := base
		c.prog, c.args = p, args

		return []stepCall{c}
	}

	var out []stepCall

	// sh -c 'install the packages; exec "$0" "$@"' tools/pipelinegen args...: the shell runs the
	// tool, its arguments as they are, after the script
	if wrapped := shellWrapped(p, args); len(wrapped) > 0 {
		c := base
		c.prog, c.args = wrapped[0], wrapped[1:]
		out = append(out, c)
		args = args[:2]
	}

	for _, a := range args {
		for line := range gostrings.SplitSeq(a, "\n") {
			// a comment may name the tool in prose
			if c := gostrings.Index(line, "#"); c >= 0 {
				line = line[:c]
			}

			for _, m := range inScript.FindAllStringSubmatchIndex(line, -1) {
				prog := line[m[2]:m[3]]

				// a path is the tool only where a command starts: elsewhere
				// it is a file named (a download's target, say)
				if gostrings.Contains(prog, "/") && !commandStarts(line[:m[2]]) {
					continue
				}

				c := base
				c.prog, c.args = prog, unquote(line[m[4]:m[5]])
				out = append(out, c)
			}
		}
	}

	return out
}

const (
	toolName     = "pipelinegen"
	sliceCommand = "slice"
)

// shellWrapped is the tool's call a shell runs after its script, with the
// script's $0 and $@: sh -c script tools/pipelinegen args.
func shellWrapped(p string, args []string) []string {
	if b := path.Base(p); b != "sh" && b != "bash" || len(args) < 3 {
		return nil
	}

	if !gostrings.HasPrefix(args[0], "-") || !gostrings.Contains(args[0], "c") || path.Base(args[2]) != toolName {
		return nil
	}

	if !gostrings.Contains(args[1], `"$0" "$@"`) {
		return nil
	}

	return args[2:]
}

var leading = []string{"exec", "if", "elif", "while", "until", "then", "do", "else", "time", "!", "{"}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)

func commandStarts(before string) bool {
	for {
		before = gostrings.TrimRight(before, " \t")
		if before == "" {
			return true
		}

		for _, sep := range []string{";", "&&", "||", "|", "(", "$(", "`", `"$(`} {
			if gostrings.HasSuffix(before, sep) {
				return true
			}
		}

		i := gostrings.LastIndexAny(before, " \t")
		word := before[i+1:]

		if !slices.Contains(leading, word) && !assignment.MatchString(word) {
			return false
		}

		before = before[:i+1]
	}
}

func names(v any, field string) []string {
	list, _ := v.([]any) //nolint:errcheck // anything else names nothing

	out := make([]string, 0, len(list))

	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			if s, ok := m[field].(string); ok {
				out = append(out, gostrings.SplitN(path.Clean(s), "/", 2)[0])
			}
		}
	}

	return out
}

func stringList(v any) []string {
	list, _ := v.([]any) //nolint:errcheck // anything else holds no args

	out := make([]string, 0, len(list))

	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}

	return out
}

func unquote(s string) []string {
	out := make([]string, 0, gostrings.Count(s, " ")+1)

	for w := range gostrings.FieldsSeq(s) {
		out = append(out, gostrings.Trim(w, `"'`))
	}

	return out
}

// callChecks are the calls the tool can't run and the steps that don't mount what their command reads
// or keep what it writes; an output no earlier call of the task wrote is an empty directory.
func callChecks(cmds Commands, s plugin.Spec, text []byte, seen map[string]bool) (calls, mounts []string) { //nolint:nonamedreturns // two kinds of problem
	steps, err := stepCalls(text)
	if err != nil {
		return []string{err.Error()}, nil
	}

	written := map[string][]string{}

	for _, sc := range steps {
		c, err := cmds.Parse(sc.args)
		if err != nil {
			calls = appendNew(calls, err.Error())

			continue
		}

		seen[c.Command] = true

		if !gostrings.Contains(sc.prog, "/") {
			continue
		}

		reads, writes, problems := cmds.Mounts(c, sc.env, records(s, sc.env))

		has := slices.Concat(sc.inputs, sc.caches, written[sc.where])
		keeps := append(slices.Clone(sc.outputs), sc.caches...)

		prog, _, _ := gostrings.Cut(path.Clean(sc.prog), "/")
		if !slices.Contains(has, prog) && !slices.Contains(sc.outputs, prog) {
			problems = append(problems, "runs from "+prog+", which it doesn't mount")
		}

		for _, d := range reads {
			// the plugins the bootstrap fetched beside the tool are under
			// the tool's own rule
			if d == prog {
				continue
			}

			if !slices.Contains(has, d) {
				problems = append(problems, "reads "+d+", which it doesn't mount and no earlier call wrote")
			}
		}

		written[sc.where] = append(written[sc.where], writes...)

		for _, d := range writes {
			if !slices.Contains(keeps, d) {
				problems = append(problems, "writes "+d+", which is no output or cache")
			}
		}

		for _, p := range problems {
			mounts = appendNew(mounts, fmt.Sprintf("%s: pipelinegen %s: %s", sc.where, c.Command, p))
		}
	}

	return calls, mounts
}

func records(s plugin.Spec, env map[string]string) bool {
	for _, r := range s.Runs {
		if r.Name != env["PIPELINEGEN_RUN"] {
			continue
		}

		for _, rs := range r.Suites {
			if rs.Suite == env["PIPELINEGEN_SUITE"] {
				return rs.Records
			}
		}
	}

	return false
}

func appendNew(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}

	return append(s, v)
}
