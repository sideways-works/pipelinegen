package graphtest

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/depsrepo"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

var (
	filegroupRE = regexp.MustCompile(`(?s)filegroup\(\n    name = "([^"]*)",\n    srcs = \[\n(.*?)\n?    \],\n\)`)
	quotedRE    = regexp.MustCompile(`"([^"]*)"`)
)

func parseTargets(files map[string][]byte) map[string][]string {
	targets := map[string][]string{}

	for path, data := range files {
		if !strings.HasSuffix(path, "BUILD.bazel") {
			continue
		}

		pkg := strings.TrimSuffix(strings.TrimSuffix(path, "BUILD.bazel"), "/")

		for _, m := range filegroupRE.FindAllStringSubmatch(string(data), -1) {
			var srcs []string
			for _, q := range quotedRE.FindAllStringSubmatch(m[2], -1) {
				srcs = append(srcs, q[1])
			}

			targets["//"+pkg+":"+m[1]] = srcs
		}
	}

	return targets
}

func reach(files map[string][]byte, targets map[string][]string, selector string) []string {
	var out []string

	seen := map[string]bool{}

	var visit func(label string)

	visit = func(label string) {
		if seen[label] {
			return
		}

		seen[label] = true

		if strings.HasPrefix(label, "@@") {
			out = append(out, "label "+label)

			return
		}

		if srcs, ok := targets[label]; ok {
			pkg, _, _ := strings.Cut(strings.TrimPrefix(label, "//"), ":")

			for _, s := range srcs {
				if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "@@") {
					visit(s)
				} else {
					visit("//" + pkg + ":" + strings.TrimPrefix(s, ":"))
				}
			}

			return
		}

		pkg, name, _ := strings.Cut(strings.TrimPrefix(label, "//"), ":")
		out = append(out, "file "+pkg+"/"+name+" "+string(files[pkg+"/"+name]))
	}

	visit("//slices:" + selector)
	slices.Sort(out)

	return out
}

func TestContentDecidesAsTheDepsRepository(t *testing.T) {
	partial := 0

	for seed := range uint64(40) {
		pair := Generate(Params{
			Seed: seed, Units: 300, GroupSize: 6, Links: 1, Slices: 12, Listed: 2, Unlisted: 2,
			Changed: 2, Added: 1, Removed: 1, CannotTell: 3, Depth: 2, Core: seed%4 == 0,
		})

		set := &plugins.Set{}

		sel, bound, err := Forward(t.Context(), set, pair.Base, pair.Head, pair.Slices)
		if err != nil {
			t.Fatal(err)
		}

		for i := range bound {
			for j := range bound[i] {
				if j%3 == 0 {
					bound[i][j].Data = []string{"//pkg:data"}
				}
			}
		}

		baseFiles := depsrepo.Files(pair.Base, bound[0])
		headFiles := depsrepo.Files(pair.Head, bound[1])
		baseTargets, headTargets := parseTargets(baseFiles), parseTargets(headFiles)

		byKey := map[Key]deps.Slice{}
		for _, d := range bound[0] {
			byKey[KeyOf(d)] = d
		}

		for _, h := range bound[1] {
			k := KeyOf(h)
			b := byKey[k]

			inBase, inHead := reach(baseFiles, baseTargets, depsrepo.Selector(b)), reach(headFiles, headTargets, depsrepo.Selector(h))
			repo := !slices.Equal(inBase, inHead)
			content := Differs(Content(pair.Base, b), Content(pair.Head, h)) != ""

			if repo != content {
				t.Fatalf("seed %d, %v: the deps repository says changed=%v, the content %v\nbase %v\nhead %v",
					seed, k, repo, content, inBase, inHead)
			}

			if _, ok := sel[k]; ok != content {
				t.Fatalf("seed %d, %v: Selected says %v, the content %v", seed, k, ok, content)
			}
		}

		if len(sel) > 0 && len(sel) < len(bound[1]) {
			partial++
		}
	}

	// the trees are sparse enough that most pairs select some slices and
	// leave out others: a check that only ever sees all or nothing proves
	// little
	if partial < 20 {
		t.Fatalf("only %d of 40 pairs select some slices and not others", partial)
	}
}

func TestAnUnchangedPairSelectsNothing(t *testing.T) {
	pair := Generate(Params{Seed: 7, Units: 200, Links: 3, Slices: 30, Listed: 5, Unlisted: 5, CannotTell: 4, Depth: 2})
	again := Generate(Params{Seed: 7, Units: 200, Links: 3, Slices: 30, Listed: 5, Unlisted: 5, CannotTell: 4, Depth: 2})

	sel, _, err := Forward(t.Context(), &plugins.Set{}, pair.Base, again.Base, pair.Slices)
	if err != nil {
		t.Fatal(err)
	}

	if len(sel) > 0 {
		t.Fatalf("an unchanged tree selects:\n%s", Describe(sel))
	}
}

func ExampleDiffers() {
	fmt.Println(Differs([]string{"a", "b"}, []string{"a", "c"}))
	// Output: - b
}
