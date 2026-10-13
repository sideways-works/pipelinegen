package plugins

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

// depthTree: the listed unit reaches "near" in one step back (near uses
// it) and in two steps forward (it uses mid, mid uses near); "far" uses
// near. With a depth of one, far is one step back from near, and near is
// no steps back from the listed unit, so far is bound.
func depthTree() *deps.Tree {
	units := map[string]deps.Unit{}
	for _, id := range []string{"g:listed", "g:mid", "g:near", "g:far"} {
		units[id] = deps.Unit{ID: id, Group: "g", Digest: "d-" + id}
	}

	return &deps.Tree{
		Units: units,
		Links: []plugin.Link{
			{From: "g:listed", To: "g:mid"},
			{From: "g:mid", To: "g:near"},
			{From: "g:near", To: "g:listed"},
			{From: "g:far", To: "g:near"},
		},
		CannotTell: map[string]string{},
	}
}

func TestDependentsDepthCountsTheFewestStepsBack(t *testing.T) {
	out, err := (&Set{}).Bind(t.Context(), t.TempDir(), depthTree(), []BindSlice{
		{Suite: "s", Name: "x", HasList: true, List: []string{"g:listed"}, Depth: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	dep := byWidth(out, "x", Dependents)
	if !slices.Contains(dep.Units, "g:far") {
		t.Errorf("depth 1 leaves out far, one step back from near, which is no steps back: %v", dep.Units)
	}
}

func TestDependentsDepthFromABindCorrelatorsUnit(t *testing.T) {
	s := newSetup(t, "depbinder")
	set := s.set(t)

	tree := depthTree()
	tree.Links = []plugin.Link{
		{From: "g:near", To: "g:listed"},
		{From: "g:far", To: "g:near"},
	}

	out, err := set.Bind(t.Context(), s.r.Dir, tree, []BindSlice{
		{Suite: "s", Name: "x", HasList: true, List: []string{"g:listed"}, Depth: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	dep := byWidth(out, "x", Dependents)
	if !slices.Contains(dep.Units, "g:far") {
		t.Errorf("near is bound by the correlator, far is one step back from it: %v", dep.Units)
	}
}

func depBinder() map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindCorrelator, Name: "uses", Capabilities: []string{plugin.CallBind},
		}),
		plugin.CallBind: func(in []byte) (any, error) {
			var req plugin.Request
			if err := decode(in, &req); err != nil {
				return nil, err
			}

			files := map[string]string{
				"units.jsonl": "\"g:near\"\n",
				"bind.jsonl":  `{"suite":"s","slice":"x","width":"dependents","units":[[0,"calls",-1]]}` + "\n",
			}

			var a plugin.Answer

			for _, name := range sortedKeys(files) {
				if err := os.WriteFile(filepath.Join(req.Work, name), []byte(files[name]), 0o600); err != nil {
					return nil, fmt.Errorf("bind: %w", err)
				}

				a.Files = append(a.Files, name)
			}

			return a, nil
		},
	}
}

func TestNarrowingDroppedKeepsTheDepth(t *testing.T) {
	tree := depthTree()
	tree.Links = []plugin.Link{
		{From: "g:near", To: "g:listed"},
		{From: "g:far", To: "g:near"},
	}
	tree.RuledOut = map[string][]plugin.Link{"wide": {{From: "g:listed", To: "g:mid"}}}

	out, err := (&Set{}).Bind(t.Context(), t.TempDir(), tree, []BindSlice{
		{Suite: "s", Name: "x", HasList: true, List: []string{"g:listed"}, Depth: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	dep := byWidth(out, "x", Dependents)
	if slices.Contains(dep.Units, "g:far") {
		t.Fatalf("far is two steps back: %v", dep.Units)
	}

	if !slices.Equal(dep.Dropped["wide"], []string{"g:mid"}) {
		t.Errorf("the wide link adds mid only, far is two steps back with it too: %v", dep.Dropped["wide"])
	}
}

func reachWithin(links []plugin.Link, roots []string, depth int) map[string]bool {
	forward, reverse := map[string][]string{}, map[string][]string{}
	for _, ln := range links {
		forward[ln.From] = append(forward[ln.From], ln.To)
		reverse[ln.To] = append(reverse[ln.To], ln.From)
	}

	type state struct {
		id   string
		back int
	}

	seen := map[state]bool{}
	queue := []state{}

	for _, r := range roots {
		seen[state{r, 0}] = true
		queue = append(queue, state{r, 0})
	}

	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]

		next := []state{}
		for _, to := range forward[s.id] {
			next = append(next, state{to, s.back})
		}

		if s.back < depth {
			for _, from := range reverse[s.id] {
				next = append(next, state{from, s.back + 1})
			}
		}

		for _, n := range next {
			if !seen[n] {
				seen[n] = true
				queue = append(queue, n)
			}
		}
	}

	out := map[string]bool{}
	for s := range seen {
		out[s.id] = true
	}

	return out
}

func TestDependentsUnderADepthMatchAWalkOverEveryPath(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4)) //nolint:gosec // test data

	for round := range 300 {
		n := 2 + r.IntN(40)
		tree := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}

		id := func(i int) string { return fmt.Sprintf("g%d:u%02d", i%4, i) }

		for i := range n {
			if r.IntN(10) > 0 {
				tree.Units[id(i)] = deps.Unit{ID: id(i), Group: fmt.Sprintf("g%d", i%4)}
			}

			if r.IntN(8) == 0 {
				tree.CannotTell[id(i)] = "test"
			}
		}

		for range r.IntN(3 * n) {
			tree.Links = append(tree.Links, plugin.Link{From: id(r.IntN(n)), To: id(r.IntN(n))})
		}

		ruled := []plugin.Link{}
		for range r.IntN(4) {
			ruled = append(ruled, plugin.Link{From: id(r.IntN(n)), To: id(r.IntN(n))})
		}

		tree.RuledOut = map[string][]plugin.Link{"wide": ruled}

		var list []string
		for range 1 + r.IntN(3) {
			list = append(list, id(r.IntN(n)))
		}

		depth := 1 + r.IntN(3)

		out, err := (&Set{}).Bind(t.Context(), t.TempDir(), tree, []BindSlice{
			{Suite: "s", Name: "x", HasList: true, List: list, Depth: depth},
		})
		if err != nil {
			t.Fatal(err)
		}

		dep := byWidth(out, "x", Dependents)

		reached := reachWithin(tree.Links, list, depth)
		wide := reachWithin(append(slices.Clone(tree.Links), ruled...), list, depth)

		var units, groups, dropped []string

		for u := range reached {
			if _, ok := tree.Units[u]; ok {
				units = append(units, u)

				if _, ok := tree.CannotTell[u]; ok {
					groups = append(groups, tree.Units[u].Group)
				}
			}
		}

		for u := range wide {
			if _, ok := tree.Units[u]; ok && !reached[u] {
				dropped = append(dropped, u)
			}
		}

		slices.Sort(units)
		slices.Sort(groups)
		slices.Sort(dropped)

		if !slices.Equal(dep.Units, units) {
			t.Fatalf("round %d, depth %d, list %v, links %v: units %v, want %v", round, depth, list, tree.Links, dep.Units, units)
		}

		if !slices.Equal(dep.Groups, slices.Compact(groups)) {
			t.Fatalf("round %d: groups %v, want %v", round, dep.Groups, groups)
		}

		if !slices.Equal(dep.Dropped["wide"], dropped) {
			t.Fatalf("round %d, depth %d, list %v, links %v, ruled out %v: dropped %v, want %v",
				round, depth, list, tree.Links, ruled, dep.Dropped["wide"], dropped)
		}

		checkReasons(t, tree.Links, list, dep.Why)
	}
}

func checkReasons(t *testing.T, links []plugin.Link, roots []string, why map[string]deps.Why) {
	t.Helper()

	has := map[plugin.Link]bool{}
	for _, ln := range links {
		has[plugin.Link{From: ln.From, To: ln.To}] = true
	}

	for id := range why {
		seen := map[string]bool{}

		for at := id; ; {
			w := why[at]
			if w.Via == "" {
				if !slices.Contains(roots, at) {
					t.Fatalf("%s leads to %s, which is no root", id, at)
				}

				break
			}

			if seen[at] {
				t.Fatalf("the reasons of %s go round", id)
			}

			seen[at] = true

			step := plugin.Link{From: w.Via, To: at}
			if w.Kind == "dependent" {
				step = plugin.Link{From: at, To: w.Via}
			}

			if !has[step] {
				t.Fatalf("%s: %s via %s is no link", id, at, w.Via)
			}

			at = w.Via
		}
	}
}
