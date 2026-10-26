// Package toolcalls describes pipelinegen's commands as a CI step calls
// them: each flag and whether the path it names is read or written, and what
// a command reads and needs beyond its flags. The tool fills it from its own
// flag sets; the plugin kit and the contracts' table of steps are checked
// against it, so the three never disagree.
package toolcalls

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/plugin"
)

// Use is what a command does with the path a flag names.
type Use uint8

// The uses of a path; a flag with none names no path.
const (
	Reads Use = 1 << iota
	Writes
	// File marks a path that names a file: written in the step's own
	// directory, it needs no output.
	File
)

// Flag is one flag of a command.
type Flag struct {
	Default string
	// Bool flags take no value after a space.
	Bool bool
	Use  Use
}

// Token says whether a command needs the pipeline's token.
type Token string

// The token needs.
const (
	NoToken Token = ""
	// TokenOptional: without it the command writes nothing remote and
	// says so (a batch task's slice steps).
	TokenOptional Token = "optional"
	// TokenRequired: it writes refs or statuses.
	TokenRequired Token = "required"
)

// Needs are what a command reads and needs beyond its flags.
type Needs struct {
	// Reads are directories it reads by their default place, relative to
	// the build's directory.
	Reads []string `json:"reads,omitempty"`
	// Recording are directories it reads only in a run that records its
	// suite.
	Recording []string `json:"recording,omitempty"`
	// ConfigFrom names the flag whose directory the config is read from,
	// whatever --root says.
	ConfigFrom string `json:"config_from,omitempty"`
	// RunFromEnv: it learns its run from PIPELINEGEN_CHANGE and
	// PIPELINEGEN_COMMIT, else the file PIPELINEGEN_RUN_FILE names, else
	// the checkout PIPELINEGEN_TRIGGER names.
	RunFromEnv bool `json:"run_from_env,omitempty"`
	// Bazel: it runs Bazel, through PIPELINEGEN_BAZELISK when that is set.
	Bazel bool  `json:"bazel,omitempty"`
	Token Token `json:"token,omitempty"`
	// ReadOnlyWith is a flag with which the command writes no refs or
	// statuses, so the read token is enough when the tokens are split by
	// purpose: plan --out, which plans a change's own tree and leaves the
	// writing to the step after it.
	ReadOnlyWith string `json:"read_only_with,omitempty"`
	// ConcourseURL: it writes the pipeline, the one thing that needs
	// Concourse's address, which it may take from
	// PIPELINEGEN_CONCOURSE_URL.
	ConcourseURL bool `json:"concourse_url,omitempty"`
}

// Command is one command or subcommand ("report batch-end").
type Command struct {
	Flags map[string]Flag
	// Named commands take a name before their flags (lock acquire <name>).
	Named bool
	// Words: it takes words of its own among its flags (a slot's number,
	// pull requests' numbers).
	Words bool
	Needs Needs
}

// Commands are the tool's commands by name.
type Commands map[string]Command

// The environment the tool reads in a step.
const (
	EnvToken        = "GITHUB_TOKEN" //nolint:gosec // a variable's name, not a credential
	EnvChange       = "PIPELINEGEN_CHANGE"
	EnvCommit       = "PIPELINEGEN_COMMIT"
	EnvRunFile      = "PIPELINEGEN_RUN_FILE"
	EnvTrigger      = "PIPELINEGEN_TRIGGER"
	EnvBazelisk     = "PIPELINEGEN_BAZELISK"
	EnvConcourseURL = "PIPELINEGEN_CONCOURSE_URL"
)

// PurposeEnv is the variable a purpose's own token is in, when the config
// splits the token by purpose: PIPELINEGEN_<PURPOSE>_TOKEN.
func PurposeEnv(purpose string) string {
	return "PIPELINEGEN_" + strings.ToUpper(purpose) + "_TOKEN"
}

// Steps are the commands a runner's generated pipeline calls, the rows of
// the contracts' table of steps.
var Steps = []string{
	"plan", "tree", "batches", "report batch-end", "report whole", "report collect",
	"report suite", "lock acquire", "lock release", "pipeline", "git-auth",
}

var errUnset = errors.New("which the step doesn't set")

// ErrCall is a call the tool would refuse or misread.
var ErrCall = errors.New("pipelinegen call")

// Call is one parsed call of the tool.
type Call struct {
	Command string
	Name    string
	Flags   map[string]string
}

// Parse reads a call's arguments the way the tool's flag package does: one
// or two dashes, a value after a space or an equals sign, none for a bool
// flag. A shell redirection ends the call.
func (cs Commands) Parse(args []string) (Call, error) {
	if len(args) == 0 {
		return Call{}, fmt.Errorf("%w: a call without a command", ErrCall)
	}

	c := Call{Command: args[0], Flags: map[string]string{}}
	rest := args[1:]

	if _, ok := cs[c.Command]; !ok && len(rest) > 0 {
		if _, ok := cs[c.Command+" "+rest[0]]; ok {
			c.Command, rest = c.Command+" "+rest[0], rest[1:]
		}
	}

	cmd, ok := cs[c.Command]
	if !ok {
		return c, fmt.Errorf("%w: pipelinegen %s: no such command", ErrCall, strings.Join(args[:min(2, len(args))], " "))
	}

	if cmd.Named {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return c, fmt.Errorf("%w: pipelinegen %s: no name", ErrCall, c.Command)
		}

		c.Name, rest = rest[0], rest[1:]
	}

	for i := 0; i < len(rest); i++ {
		a := rest[i]
		if redirection(a) {
			break
		}

		if (!strings.HasPrefix(a, "-") || a == "-") && cmd.Words {
			continue
		}

		if !strings.HasPrefix(a, "-") || a == "-" {
			return c, fmt.Errorf("%w: pipelinegen %s: %q after its flags", ErrCall, c.Command, a)
		}

		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")

		f, ok := cmd.Flags[name]
		if !ok {
			return c, fmt.Errorf("%w: pipelinegen %s: no flag -%s", ErrCall, c.Command, name)
		}

		switch {
		case hasValue:
		case f.Bool:
			value = "true"
		case i+1 >= len(rest) || redirection(rest[i+1]):
			return c, fmt.Errorf("%w: pipelinegen %s: -%s without a value", ErrCall, c.Command, name)
		default:
			i++
			value = rest[i]
		}

		c.Flags[name] = value
	}

	return c, nil
}

// redirection tells a shell redirection (>out, 2>err, <in) from a word;
// a placeholder of the docs (<run>) is a word.
func redirection(w string) bool {
	r := strings.TrimLeft(w, "0123456789")

	return r != "" && (r[0] == '>' || r[0] == '<' && !strings.HasSuffix(r, ">"))
}

// Mounts are what a call reads and writes, as directories of the build (each
// path's first element), given the step's environment and whether its run
// records the suite; Problems are what the step can't satisfy whatever it
// mounts.
func (cs Commands) Mounts(c Call, env map[string]string, recording bool) (reads, writes, problems []string) { //nolint:nonamedreturns // three lists
	m := &mounts{env: env}
	cmd := cs[c.Command]

	m.flags(cmd, c)
	m.needs(cmd.Needs, c, recording)

	return dedupe(m.reads), dedupe(m.writes), m.problems
}

type mounts struct {
	env                     map[string]string
	reads, writes, problems []string
}

func (m *mounts) read(p, why string) {
	switch d, err := top(p, m.env); {
	case err != nil:
		m.problems = append(m.problems, fmt.Sprintf("%s: %v", why, err))
	case d == ".":
		m.problems = append(m.problems, why+" reads the build's own directory as a checkout")
	case d != "":
		m.reads = append(m.reads, d)
	}
}

func (m *mounts) write(p, why string) {
	switch d, err := top(p, m.env); {
	case err != nil:
		m.problems = append(m.problems, fmt.Sprintf("%s: %v", why, err))
	case d != "" && d != ".":
		m.writes = append(m.writes, d)
	}
}

func (m *mounts) flags(cmd Command, c Call) {
	value := func(name string) string {
		if v, ok := c.Flags[name]; ok {
			return v
		}

		return cmd.Flags[name].Default
	}

	for _, name := range slices.Sorted(keysOf(cmd.Flags)) {
		f := cmd.Flags[name]

		v := value(name)
		if name == "root" && cmd.Needs.ConfigFrom != "" {
			v = value(cmd.Needs.ConfigFrom)
		}

		if f.Use == 0 || v == "" {
			continue
		}

		if f.Use&Reads != 0 {
			m.read(v, "-"+name)
		}

		// a file in the build's own directory lives only in the step
		if f.Use&Writes != 0 && (f.Use&File == 0 || strings.Contains(path.Clean(v), "/")) {
			m.write(v, "-"+name)
		}
	}
}

func (m *mounts) needs(n Needs, c Call, recording bool) {
	for _, d := range n.Reads {
		m.read(d, d)
	}

	if recording {
		for _, d := range n.Recording {
			m.read(d, d)
		}
	}

	if b := m.env[EnvBazelisk]; b != "" && n.Bazel {
		m.read(b, EnvBazelisk)
	}

	if n.RunFromEnv && (m.env[EnvChange] == "" || m.env[EnvCommit] == "") {
		switch {
		case m.env[EnvRunFile] != "":
			m.read(m.env[EnvRunFile], EnvRunFile)
		case m.env[EnvTrigger] != "":
			m.read(m.env[EnvTrigger], EnvTrigger)
		default:
			m.problems = append(m.problems, "no way to learn its run: none of "+EnvChange+" and "+EnvCommit+", "+EnvRunFile+" or "+EnvTrigger)
		}
	}

	// one token for every purpose, or the purposes' own
	_, one := m.env[EnvToken]
	_, store := m.env[PurposeEnv(plugin.PurposeStore)]

	_, readOnly := c.Flags[n.ReadOnlyWith]
	_, read := m.env[PurposeEnv(plugin.PurposeRead)]

	if n.Token == TokenRequired && !one && !store && (n.ReadOnlyWith == "" || !readOnly || !read) {
		m.problems = append(m.problems, "writes refs or statuses without "+EnvToken+" or "+PurposeEnv(plugin.PurposeStore))
	}

	if _, ok := m.env[EnvConcourseURL]; ok && !n.ConcourseURL {
		m.problems = append(m.problems, "gets "+EnvConcourseURL+", which only writing the pipeline needs")
	}
}

// top is the directory of the build a path lies in, its first element,
// with $VAR and ${VAR} taken from the step's environment; "" for an
// absolute path, which is no mount.
func top(p string, env map[string]string) (string, error) {
	var missing []string

	p = expand(p, func(k string) string {
		v, ok := env[k]
		if !ok {
			missing = append(missing, k)
		}

		return v
	})

	if len(missing) > 0 {
		return "", fmt.Errorf("names $%s, %w", missing[0], errUnset)
	}

	if path.IsAbs(p) {
		return "", nil
	}

	p = path.Clean(p)
	if i := strings.Index(p, "/"); i >= 0 {
		return p[:i], nil
	}

	return p, nil
}

func expand(s string, get func(string) string) string {
	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])

			continue
		}

		name, n := "", 0

		if s[i+1] == '{' {
			if end := strings.IndexByte(s[i:], '}'); end > 0 {
				name, n = s[i+2:i+end], end
			}
		} else {
			j := i + 1
			for j < len(s) && (s[j] == '_' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}

			name, n = s[i+1:j], j-i-1
		}

		if name == "" {
			b.WriteByte(s[i])

			continue
		}

		b.WriteString(get(name))

		i += n
	}

	return b.String()
}

func keysOf(m map[string]Flag) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func dedupe(s []string) []string {
	slices.Sort(s)

	return slices.Compact(s)
}
