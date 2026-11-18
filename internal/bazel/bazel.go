// Package bazel asks a repository's Bazel graph for its slices.
package bazel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/progress"
)

type Client struct {
	Bazelisk     string
	Dir          string
	StartupFlags []string
	// Env is Bazel's environment, nil for the process's; the tool gives it one without credentials
	// (secret.ChildEnv), since Bazel runs a tree's repository rules.
	Env []string
}

// Slice is one Bazel target carrying a suite's tag.
type Slice struct {
	// Name is a test file's path, or SliceName of a tagged target's label.
	Name string `json:"name"`
	// Label is what bazel-diff selects the slice by: the tagged target, or
	// the selector in the deps repository a test file's slice gets.
	Label string   `json:"label"`
	Tags  []string `json:"tags,omitempty"`
	// Kind is a target's rule kind (go_test, sh_test), which a suite's
	// steps may route by; empty for a test file.
	Kind string `json:"kind,omitempty"`
	// Sources are the slice's own files: the test file, or the target's
	// srcs that are files, as paths relative to the workspace.
	Sources []string `json:"sources"`
	Tests   []string `json:"tests,omitempty"`
}

func SelectorLabel(name string) string {
	return "@pipelinegen_deps//slices:" + name
}

// NormalLabel writes a deps repository label as SelectorLabel does, whatever form of the repository's name
// it came in (@@, or the canonical names Bazel gives repositories a module's rules make).
func NormalLabel(label string) string {
	repo, rest, ok := strings.Cut(label, "//")
	if !ok || !strings.Contains(repo, "pipelinegen_deps") {
		return label
	}

	return "@pipelinegen_deps//" + rest
}

var ErrQuery = errors.New("bazel query")

// Run runs Bazel with args in the workspace and returns its stdout; its stderr goes into the error.
func (c *Client) Run(ctx context.Context, args ...string) ([]byte, error) {
	defer progress.Stage(ctx, "Bazel %s", command(args))()

	all := append(append([]string{}, c.StartupFlags...), args...)

	cmd := exec.CommandContext(ctx, c.Bazelisk, all...) //nolint:gosec // runs the Bazel the config names
	cmd.Dir = c.Dir
	cmd.Env = c.Env

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: no Bazel at %q: tried PIPELINEGEN_BAZELISK, the config's bazel.bazelisk "+
				"and tools/bazelisk in the working directory; set one of them", ErrQuery, c.Bazelisk)
		}

		return nil, fmt.Errorf("%w: %s %s: %w: %s", ErrQuery, c.Bazelisk, strings.Join(all, " "), err, lastLines(stderr.String(), 20))
	}

	return stdout.Bytes(), nil
}

func TagExpr(tag string) string {
	return fmt.Sprintf(`attr(tags, "[\[ ]%s[,\]]", //...)`, regexp.QuoteMeta(tag))
}

// Slices lists the targets carrying tag, with their own source files, sorted
// by label.
func (c *Client) Slices(ctx context.Context, tag string) ([]Slice, error) {
	expr := fmt.Sprintf(`let s = %s in $s + kind("source file", labels(srcs, $s))`, TagExpr(tag))

	out, err := c.Run(ctx, "query", expr, "--output=streamed_jsonproto", "--keep_going=false")
	if err != nil {
		return nil, err
	}

	return parseSlices(out, tag)
}

type target struct {
	Type string `json:"type"`
	Rule *struct {
		Name      string `json:"name"`
		RuleClass string `json:"ruleClass"` //nolint:tagliatelle // Bazel's field name
		Attribute []struct {
			Name            string   `json:"name"`
			StringListValue []string `json:"stringListValue"` //nolint:tagliatelle // Bazel's field name
		} `json:"attribute"`
	} `json:"rule"`
	SourceFile *struct {
		Name string `json:"name"`
	} `json:"sourceFile"` //nolint:tagliatelle // Bazel's field name
}

type rule struct {
	label string
	kind  string
	tags  []string
	srcs  []string
	data  []string
}

func parseSlices(out []byte, tag string) ([]Slice, error) {
	rules, files, err := readTargets(out)
	if err != nil {
		return nil, err
	}

	result := make([]Slice, 0, len(rules))

	for _, r := range rules {
		if !slices.Contains(r.tags, tag) {
			return nil, fmt.Errorf("%w: %s came back without tag %s", ErrQuery, r.label, tag)
		}

		s := Slice{Name: SliceName(r.label), Label: r.label, Kind: r.kind, Tags: r.tags, Sources: []string{}}

		for _, src := range r.srcs {
			if !files[src] {
				continue
			}

			if p, ok := LabelPath(src); ok {
				s.Sources = append(s.Sources, p)
			}
		}

		sort.Strings(s.Sources)
		result = append(result, s)
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Label < result[j].Label })

	return result, nil
}

func readTargets(out []byte) ([]rule, map[string]bool, error) {
	var (
		rules []rule
		files = map[string]bool{}
	)

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var t target
		if err := json.Unmarshal(line, &t); err != nil {
			return nil, nil, fmt.Errorf("%w: unreadable line: %w", ErrQuery, err)
		}

		switch {
		case t.SourceFile != nil:
			files[t.SourceFile.Name] = true
		case t.Rule != nil:
			r := rule{label: t.Rule.Name, kind: t.Rule.RuleClass}

			for _, a := range t.Rule.Attribute {
				switch a.Name {
				case "tags":
					r.tags = a.StringListValue
				case "srcs":
					r.srcs = a.StringListValue
				case "data":
					r.data = a.StringListValue
				}
			}

			rules = append(rules, r)
		}
	}

	if err := sc.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrQuery, err)
	}

	return rules, files, nil
}

// Existing returns which of the labels name targets that exist. A query
// that names missing targets still answers for the others.
func (c *Client) Existing(ctx context.Context, labels []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(labels) == 0 {
		return out, nil
	}

	defer progress.Stage(ctx, "Bazel query")()

	all := append(append([]string{}, c.StartupFlags...), "query", "set("+strings.Join(labels, " ")+")", "--output=label", "--keep_going")

	cmd := exec.CommandContext(ctx, c.Bazelisk, all...) //nolint:gosec // runs the Bazel the config names
	cmd.Dir = c.Dir
	cmd.Env = c.Env

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// exit 3: some targets are missing, the rest answered
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 3) {
		return nil, fmt.Errorf("%w: %s query: %w: %s", ErrQuery, c.Bazelisk, err, lastLines(stderr.String(), 20))
	}

	for line := range strings.SplitSeq(stdout.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[NormalLabel(line)] = true
		}
	}

	return out, nil
}

// Rule is a rule a query returned, with the attributes planning reads.
type Rule struct {
	Label string
	Srcs  []string
	Data  []string
}

func (c *Client) Rules(ctx context.Context, expr string) ([]Rule, error) {
	out, err := c.Run(ctx, "query", expr, "--output=streamed_jsonproto", "--keep_going=false")
	if err != nil {
		return nil, err
	}

	return parseRules(out)
}

func parseRules(out []byte) ([]Rule, error) {
	rules, _, err := readTargets(out)
	if err != nil {
		return nil, err
	}

	result := make([]Rule, 0, len(rules))
	for _, r := range rules {
		result = append(result, Rule{Label: NormalLabel(r.label), Srcs: r.srcs, Data: r.data})
	}

	return result, nil
}

// SliceName is a slice's name: its label without the leading slashes, with
// every slash and colon turned into a dot.
func SliceName(label string) string {
	return strings.NewReplacer("/", ".", ":", ".").Replace(strings.TrimLeft(label, "/@"))
}

// LabelPath turns a main repository file's label into its workspace path, //pkg/sub:file.go into
// pkg/sub/file.go; other repositories' labels have none here.
func LabelPath(label string) (string, bool) {
	rest, ok := strings.CutPrefix(label, "//")
	if !ok {
		rest, ok = strings.CutPrefix(label, "@//")
	}

	if !ok {
		rest, ok = strings.CutPrefix(label, "@@//")
	}

	if !ok {
		return "", false
	}

	pkg, name, found := strings.Cut(rest, ":")
	if !found {
		return "", false
	}

	if pkg == "" {
		return name, true
	}

	return pkg + "/" + name, true
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}

	return strings.Join(lines, "\n")
}

func command(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}

	return ""
}
