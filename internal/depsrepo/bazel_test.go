package depsrepo

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldiff"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func workspace(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	for p, s := range map[string]string{
		"MODULE.bazel": `module(name = "fixture")

local_repository = use_repo_rule("@bazel_tools//tools/build_defs/repo:local.bzl", "local_repository")

local_repository(name = "pipelinegen_deps", path = ".pipelinegen/deps")
`,
		".bazelversion": "9.2.0\n",
		".bazelignore":  ".pipelinegen\n",
		"BUILD.bazel":   "exports_files([\"data.txt\"])\n",
		"data.txt":      "data\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

type differ struct {
	t    *testing.T
	tool bazeldiff.Tool
	dir  string
	n    int
}

func newDiffer(t *testing.T) *differ {
	t.Helper()

	return &differ{t: t, tool: bazeldifftest.Tool(t), dir: workspace(t)}
}

func (d *differ) hash(tree *deps.Tree, ss []deps.Slice) string {
	d.t.Helper()

	if _, err := (Writer{}).Write(d.t.Context(), d.dir, tree, ss); err != nil {
		d.t.Fatal(err)
	}

	d.n++
	out := filepath.Join(d.t.TempDir(), "hashes.json")

	if err := d.tool.Hashes(d.t.Context(), testrepo.Bazel(d.t, d.dir), d.dir, out, out+".edges"); err != nil {
		d.t.Fatal(err)
	}

	return out
}

func (d *differ) selected(base, head string) []string {
	d.t.Helper()

	labels, err := d.tool.Impacted(d.t.Context(), testrepo.Bazel(d.t, d.dir), base, head)
	if err != nil {
		d.t.Fatal(err)
	}

	var out []string

	for _, l := range labels {
		// the package's own BUILD file is a target too, but no selector
		if _, sel, ok := strings.Cut(l, "//"+slicesDir+":"); ok && strings.Contains(l, "pipelinegen_deps") && sel != allTarget && sel != "BUILD.bazel" {
			out = append(out, sel)
		}
	}

	slices.Sort(out)

	return out
}

func unit(id, group, digest string) deps.Unit {
	return deps.Unit{ID: id, Group: group, Digest: digest}
}

func treeOf(units ...deps.Unit) *deps.Tree {
	t := &deps.Tree{Units: map[string]deps.Unit{}}
	for _, u := range units {
		t.Units[u.ID] = u
	}

	return t
}

// The ids are what plugins send: namespaced, with ':', '#', '/', '(', ')',
// spaces and letters that differ only in case.
const (
	create  = "go:internal/notes#func:Create"
	method  = "go:internal/notes#method:(*Store).Save"
	lower   = "go:internal/notes#func:create"
	spaced  = "file:web/fonts/Noto Sans/0-255.pbf"
	hashish = "go:internal/notes%23func%3ACreate"
	other   = "go:internal/money#func:Round"
)

// many is one of the units a slice holds enough of in one group for them
// to get a filegroup of their own.
func many(i int) string { return fmt.Sprintf("go:internal/many#func:F%d", i) }

func baseTree() *deps.Tree {
	tr := treeOf(
		unit(create, "internal/notes", "d1"),
		unit(method, "internal/notes", "d2"),
		unit(lower, "internal/notes", "d3"),
		unit(spaced, "web/fonts", "d4"),
		unit(hashish, "internal/notes", "d5"),
		unit(other, "internal/money", "d6"),
	)

	for i := range shareMin {
		tr.Units[many(i)] = unit(many(i), "internal/many", "m")
	}

	return tr
}

func baseSlices() []deps.Slice {
	return []deps.Slice{
		{Suite: "e2e", Name: "e2e/notes.spec.ts", Units: []string{create, method}},
		{Suite: "e2e", Name: "e2e/lower.spec.ts", Units: []string{lower}},
		{Suite: "e2e", Name: "e2e/fonts.spec.ts", Units: []string{spaced}},
		{Suite: "e2e", Name: "e2e/escaped.spec.ts", Units: []string{hashish}},
		{Suite: "backend", Name: "internal/money/money_test.go", Groups: []string{"internal/money"}},
		{Suite: "backend", Name: "internal/money/money_test.go", Width: "dependents", Groups: []string{"internal/money"}, Units: []string{create}},
		{Suite: "e2e", Name: "e2e/new.spec.ts", Everything: true},
		{Suite: "e2e", Name: "e2e/data.spec.ts", Data: []string{"//:data.txt"}},
		{Suite: "e2e", Name: "e2e/gone.spec.ts", Units: []string{"go:internal/notes#func:Gone"}},
		{Suite: "e2e", Name: "e2e/many.spec.ts", Units: manyIDs()},
	}
}

func manyIDs() []string {
	out := make([]string, 0, shareMin)
	for i := range shareMin {
		out = append(out, many(i))
	}

	return out
}

func withDigest(tree *deps.Tree, id, digest string) *deps.Tree {
	out := treeOf()
	maps.Copy(out.Units, tree.Units)

	u := out.Units[id]
	u.Digest = digest
	out.Units[id] = u

	return out
}

func TestBazelDiffSelectsBySelector(t *testing.T) {
	d := newDiffer(t)
	ss := baseSlices()
	base := d.hash(baseTree(), ss)

	cases := []struct {
		name  string
		tree  *deps.Tree
		touch func(dir string)
		want  []string
	}{
		{"nothing changed", baseTree(), nil, nil},
		{"a digest changed", withDigest(baseTree(), create, "d1'"), nil,
			[]string{"e2e/new.spec.ts", "e2e/notes.spec.ts", "internal/money/money_test.go.dependents"}},
		{"only the lower-case twin changed", withDigest(baseTree(), lower, "d3'"), nil,
			[]string{"e2e/lower.spec.ts", "e2e/new.spec.ts"}},
		{"only the id that looks escaped changed", withDigest(baseTree(), hashish, "d5'"), nil,
			[]string{"e2e/escaped.spec.ts", "e2e/new.spec.ts"}},
		{"a unit of a slice's own filegroup changed", withDigest(baseTree(), many(3), "m'"), nil,
			[]string{"e2e/many.spec.ts", "e2e/new.spec.ts"}},
		{"a file with spaces changed", withDigest(baseTree(), spaced, "d4'"), nil,
			[]string{"e2e/fonts.spec.ts", "e2e/new.spec.ts"}},
		{"a unit added to a group held whole", func() *deps.Tree {
			tr := baseTree()
			tr.Units["go:internal/money#func:Floor"] = unit("go:internal/money#func:Floor", "internal/money", "d7")

			return tr
		}(), nil, []string{"e2e/new.spec.ts", "internal/money/money_test.go", "internal/money/money_test.go.dependents"}},
		{"a listed unit appears", func() *deps.Tree {
			tr := baseTree()
			tr.Units["go:internal/notes#func:Gone"] = unit("go:internal/notes#func:Gone", "internal/notes", "d8")

			return tr
		}(), nil, []string{"e2e/gone.spec.ts", "e2e/new.spec.ts"}},
		{"a listed unit goes", func() *deps.Tree {
			tr := baseTree()
			delete(tr.Units, method)

			return tr
		}(), nil, []string{"e2e/new.spec.ts", "e2e/notes.spec.ts"}},
		{"a data label's file changed", baseTree(), func(dir string) {
			if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, []string{"e2e/data.spec.ts"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.touch != nil {
				c.touch(d.dir)

				t.Cleanup(func() {
					if err := os.WriteFile(filepath.Join(d.dir, "data.txt"), []byte("data\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				})
			}

			head := d.hash(c.tree, ss)
			if got := d.selected(base, head); !slices.Equal(got, c.want) {
				t.Fatalf("selected %q, want %q", got, c.want)
			}
		})
	}
}
