package bazel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/progress"
)

// Groups are the filegroups (and aliases) among some labels and the
// filegroups below them, opened down to what they hold.
type Groups struct {
	// Members are, per group opened, what it holds: files, targets kept
	// whole and the groups below it, as canonical labels.
	Members map[string][]string `json:"members"`
	// As are the generated files among the members, each with the rule
	// that makes it: such a file stands as that rule's label.
	As map[string]string `json:"as,omitempty"`
}

// maxGroupDepth bounds the descent; Bazel allows no cycles, so it only
// guards against a query that answers something unexpected.
const maxGroupDepth = 64

// OpenGroups opens the filegroups among labels, level by level, with one
// query per level. A filegroup with an output_group or a select(), any
// other rule, a generated file, a label of another repository and a label
// the tree lacks all stay whole.
func (c *Client) OpenGroups(ctx context.Context, labels []string) (*Groups, error) {
	g := &Groups{Members: map[string][]string{}, As: map[string]string{}}
	seen := map[string]bool{}

	todo := make([]string, 0, len(labels))
	for _, l := range labels {
		if l, ok := MainCanonical(l); ok && !seen[l] {
			seen[l] = true
			todo = append(todo, l)
		}
	}

	for depth := 0; len(todo) > 0 && depth < maxGroupDepth; depth++ {
		slices.Sort(todo)

		ts, err := c.groupTargets(ctx, todo)
		if err != nil {
			return nil, err
		}

		var next []string

		for _, t := range ts {
			switch {
			case t.generated != "":
				g.As[t.label] = t.generated
			case t.members != nil:
				g.Members[t.label] = t.members

				for _, m := range t.members {
					if m, ok := MainCanonical(m); ok && !seen[m] {
						seen[m] = true
						next = append(next, m)
					}
				}
			}
		}

		todo = next
	}

	return g, nil
}

// Leaves are what a label stands for once its groups are opened, each with
// the groups it was found through, innermost first. A label that is no
// group is its own leaf.
func (g *Groups) Leaves(label string) map[string][]string {
	out := map[string][]string{}
	g.leaves(label, nil, out, map[string]bool{})

	return out
}

func (g *Groups) leaves(label string, through []string, out map[string][]string, open map[string]bool) {
	if l, ok := MainCanonical(label); ok {
		label = l
	}

	members, isGroup := g.members(label)
	if !isGroup {
		leaf := g.leaf(label)
		if _, ok := out[leaf]; !ok {
			out[leaf] = slices.Clone(through)
		}

		return
	}

	if open[label] {
		return
	}

	open[label] = true
	defer delete(open, label)

	inner := append([]string{label}, through...)
	for _, m := range members {
		g.leaves(m, inner, out, open)
	}
}

// IsGroup tells whether the label was opened as a group.
func (g *Groups) IsGroup(label string) bool {
	_, ok := g.members(label)

	return ok
}

// Members of a group, nil for a label that is none.
func (g *Groups) MembersOf(label string) []string {
	m, _ := g.members(label)

	return m
}

func (g *Groups) members(label string) ([]string, bool) {
	if g == nil {
		return nil, false
	}

	if l, ok := MainCanonical(label); ok {
		label = l
	}

	m, ok := g.Members[label]

	return m, ok
}

// Leaf is the label a member stands as: a generated file as its rule.
func (g *Groups) Leaf(label string) string { return g.leaf(label) }

func (g *Groups) leaf(label string) string {
	if l, ok := MainCanonical(label); ok {
		label = l
	}

	if g != nil {
		if r, ok := g.As[label]; ok {
			if c, ok := MainCanonical(r); ok {
				return c
			}

			return r
		}
	}

	return label
}

// MainCanonical writes a label of the main repository as //pkg:name; ok is
// false for another repository's label or something that is no label.
func MainCanonical(l string) (string, bool) {
	for _, p := range []string{"@@//", "@//"} {
		if rest, ok := strings.CutPrefix(l, p); ok {
			l = "//" + rest

			break
		}
	}

	rest, ok := strings.CutPrefix(l, "//")
	if !ok {
		return "", false
	}

	pkg, name, found := strings.Cut(rest, ":")
	if !found {
		if pkg == "" {
			return "", false
		}

		name = path.Base(pkg)
	}

	return "//" + pkg + ":" + name, true
}

type groupTarget struct {
	label     string
	members   []string
	generated string
}

type groupAttr struct {
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	StringValue     string   `json:"stringValue"`     //nolint:tagliatelle // Bazel's field name
	StringListValue []string `json:"stringListValue"` //nolint:tagliatelle // Bazel's field name
}

type groupLine struct {
	Rule *struct {
		Name      string      `json:"name"`
		RuleClass string      `json:"ruleClass"` //nolint:tagliatelle // Bazel's field name
		Attribute []groupAttr `json:"attribute"`
	} `json:"rule"`
	GeneratedFile *struct {
		Name           string `json:"name"`
		GeneratingRule string `json:"generatingRule"` //nolint:tagliatelle // Bazel's field name
	} `json:"generatedFile"` //nolint:tagliatelle // Bazel's field name
}

// groupTargets asks for the labels with selects left unflattened, so a
// select() shows as such; the expression goes in a file, since a level
// may name thousands of files. Labels the tree lacks are left out.
func (c *Client) groupTargets(ctx context.Context, labels []string) ([]groupTarget, error) {
	defer progress.Stage(ctx, "Bazel query of filegroups")()

	dir, err := os.MkdirTemp("", "pipelinegen-groups-")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQuery, err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // scratch

	file := filepath.Join(dir, "query.txt")
	if err := os.WriteFile(file, []byte("set("+strings.Join(labels, " ")+")\n"), 0o600); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQuery, err)
	}

	all := append(append([]string{}, c.StartupFlags...), "query", "--query_file="+file, "--output=streamed_jsonproto", "--keep_going",
		"--proto:flatten_selects=false", "--proto:output_rule_attrs=srcs,data,actual,output_group")

	cmd := exec.CommandContext(ctx, c.Bazelisk, all...) //nolint:gosec // runs the Bazel the config names
	cmd.Dir = c.Dir
	cmd.Env = c.Env

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// exit 3: some labels are missing, the rest answered
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 3) {
		return nil, fmt.Errorf("%w: %s query: %w: %s", ErrQuery, c.Bazelisk, err, lastLines(stderr.String(), 20))
	}

	return parseGroupTargets(stdout.Bytes())
}

func parseGroupTargets(out []byte) ([]groupTarget, error) {
	var ts []groupTarget

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}

		var t groupLine
		if err := json.Unmarshal(line, &t); err != nil {
			return nil, fmt.Errorf("%w: unreadable line: %w", ErrQuery, err)
		}

		switch {
		case t.GeneratedFile != nil:
			ts = append(ts, groupTarget{label: t.GeneratedFile.Name, generated: t.GeneratedFile.GeneratingRule})
		case t.Rule != nil:
			if members, ok := opened(t.Rule.RuleClass, t.Rule.Attribute); ok {
				ts = append(ts, groupTarget{label: t.Rule.Name, members: members})
			}
		}
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrQuery, err)
	}

	return ts, nil
}

// opened are a rule's members when it is a group to open: a filegroup
// without an output_group or a select(), or an alias without a select().
func opened(class string, attrs []groupAttr) ([]string, bool) {
	if class != "filegroup" && class != "alias" {
		return nil, false
	}

	members := []string{}

	for _, a := range attrs {
		if a.Type == "SELECTOR_LIST" {
			return nil, false
		}

		switch {
		case a.Name == "output_group" && a.StringValue != "":
			return nil, false
		case class == "filegroup" && (a.Name == "srcs" || a.Name == "data"):
			members = append(members, a.StringListValue...)
		case class == "alias" && a.Name == "actual" && a.StringValue != "":
			members = append(members, a.StringValue)
		}
	}

	if class == "alias" && len(members) == 0 {
		return nil, false
	}

	return members, true
}

// Through are the groups below top, top included, that label is found
// through, innermost first; ok is false where top doesn't hold it.
func (g *Groups) Through(top, label string) ([]string, bool) {
	top = g.canonical(top)
	if !g.IsGroup(top) {
		return nil, false
	}

	var find func(at string, seen map[string]bool) []string

	find = func(at string, seen map[string]bool) []string {
		if seen[at] {
			return nil
		}

		seen[at] = true

		for _, m := range g.MembersOf(at) {
			m = g.canonical(m)
			if m == label || g.leaf(m) == label {
				return []string{at}
			}

			if g.IsGroup(m) {
				if chain := find(m, seen); chain != nil {
					return append(chain, at)
				}
			}
		}

		return nil
	}

	chain := find(top, map[string]bool{})

	return chain, chain != nil
}

func (g *Groups) canonical(l string) string {
	if c, ok := MainCanonical(l); ok {
		return c
	}

	return l
}
