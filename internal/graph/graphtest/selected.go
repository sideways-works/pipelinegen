// Package graphtest holds what planning's tests compare the outward walk against: the forward computation
// it replaces, and synthetic trees to compare them on.
package graphtest

import (
	"slices"
	"strings"

	"github.com/sideways-works/pipelinegen/internal/deps"
)

type Key struct {
	Suite, Name, Width string
}

func (k Key) String() string { return k.Suite + "/" + k.Name + " (" + k.Width + ")" }

func KeyOf(d deps.Slice) Key { return Key{Suite: d.Suite, Name: d.Name, Width: d.Width} }

// Content is what a slice's selector in the deps repository depends on in one tree, sorted lines; bazel-diff
// hashes exactly this, so two trees select the slice exactly when it differs.
func Content(t *deps.Tree, d deps.Slice) []string {
	var out []string

	if d.Everything {
		out = append(out, "everything")
		for id, u := range t.Units {
			out = append(out, unitLine("all", id, u))
		}
	}

	for _, id := range d.Units {
		u, ok := t.Units[id]
		if !ok {
			out = append(out, "absent "+id)

			continue
		}

		out = append(out, unitLine("unit", id, u))
	}

	if len(d.Groups) > 0 {
		members := map[string][]string{}

		for id, u := range t.Units {
			if !summaryInput(t, id) {
				members[u.Group] = append(members[u.Group], id)
			}
		}

		for _, g := range d.Groups {
			out = append(out, "group "+g)
			for _, id := range members[g] {
				out = append(out, unitLine("in "+g, id, t.Units[id]))
			}
		}
	}

	for _, l := range d.Data {
		out = append(out, "data "+l)
	}

	slices.Sort(out)

	return slices.Compact(out)
}

func unitLine(what, id string, u deps.Unit) string {
	return what + " " + id + " group=" + u.Group + " digest=" + u.Digest
}

// Differs is the first line that tells two contents apart, from the
// sorted merge of both, marked "-" for the base's and "+" for the head's.
func Differs(base, head []string) string {
	i, j := 0, 0

	for i < len(base) || j < len(head) {
		switch {
		case j == len(head) || (i < len(base) && base[i] < head[j]):
			return "- " + base[i]
		case i == len(base) || head[j] < base[i]:
			return "+ " + head[j]
		default:
			i++
			j++
		}
	}

	return ""
}

// Selected is, per slice and width, the reason it is selected: the first
// line of its content that differs.
func Selected(base, head *deps.Tree, inBase, inHead []deps.Slice) map[Key]string {
	byKey := make(map[Key]deps.Slice, len(inBase))
	for _, d := range inBase {
		byKey[KeyOf(d)] = d
	}

	out := map[Key]string{}

	for _, h := range inHead {
		k := KeyOf(h)

		b, ok := byKey[k]
		if !ok {
			out[k] = "only in the head"

			continue
		}

		delete(byKey, k)

		if why := Differs(Content(base, b), Content(head, h)); why != "" {
			out[k] = why
		}
	}

	for k := range byKey {
		out[k] = "only in the base"
	}

	return out
}

// Describe lists a selection one slice a line, sorted, for a failure
// message.
func Describe(sel map[Key]string) string {
	lines := make([]string, 0, len(sel))
	for k, why := range sel {
		lines = append(lines, k.String()+": "+why)
	}

	slices.Sort(lines)

	return strings.Join(lines, "\n")
}
