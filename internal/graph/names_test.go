package graph

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func goTree(units map[string]string) *deps.Tree {
	t := tree(nil)

	for id, d := range units {
		g := id[len("go:"):]
		g = g[:len("x")]
		t.Units[id] = deps.Unit{ID: id, Group: g, Digest: d}
		t.Typed = append(t.Typed, plugin.Link{From: id, To: GroupPrefix + g, Kind: KindFollow})
	}

	t.Typed = append(t.Typed, plugin.Link{From: GroupPrefix + "a", To: GroupPrefix + "b", Kind: KindFollow})

	return t
}

func TestANewNameNothingBindsSelectsTheSlicesLinkingItsPackage(t *testing.T) {
	b := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1", "go:c#func:C": "1"})
	h := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1", "go:b#func:New": "1", "go:c#func:C": "1"})

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "links-b", HasList: true, List: []string{"go:a#func:A"}},
		{Suite: "s", Name: "c-only", HasList: true, List: []string{"go:c#func:C"}},
	}

	off := Options{On: func(string) bool { return false }}
	if got := selectedNames(t, b, h, ss, off); !got["links-b"] || got["c-only"] {
		t.Fatalf("unreached-new off: selected %v, want links-b only", got)
	}

	on := Options{On: func(n string) bool { return n == narrowUnreachedNew }}
	if got := selectedNames(t, b, h, ss, on); len(got) != 0 {
		t.Fatalf("unreached-new on: selected %v, want nothing", got)
	}

	sel, err := Select(context.Background(), b, h, ss, []string{Executed}, on)
	if err != nil {
		t.Fatal(err)
	}

	left := sel.LeftOut[narrowUnreachedNew]
	if _, ok := left[Key{Suite: "s", Name: "links-b", Width: Executed}]; !ok || len(left) != 1 {
		t.Fatalf("unreached-new left out %v, want links-b", left)
	}

	h = goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "2", "go:c#func:C": "1"})
	if got := selectedNames(t, b, h, ss, off); len(got) != 0 {
		t.Fatalf("a changed unbound body: selected %v, want nothing", got)
	}
}

func specLinked(t *deps.Tree, roots []string) map[string]bool {
	saved := t.Units
	t.Units = maps.Clone(saved)

	for _, ln := range t.Typed {
		for _, end := range []string{ln.From, ln.To} {
			if strings.HasPrefix(end, GroupPrefix) {
				t.Units[end] = deps.Unit{ID: end}
			}
		}
	}

	out := map[string]bool{}

	for id := range specBound(t, roots, false, 0) {
		if g, ok := strings.CutPrefix(id, GroupPrefix); ok {
			out[g] = true
		}
	}

	t.Units = saved

	return out
}

func specNames(t *deps.Tree, ss []plugins.BindSlice, dependents bool) map[string][]string {
	wide := t.CannotTell
	t.CannotTell = nil

	defer func() { t.CannotTell = wide }()

	bound := map[string]bool{}

	for _, sl := range ss {
		maps.Copy(bound, specBound(t, sl.List, dependents, sl.Depth))
	}

	out := map[string][]string{}

	for id, u := range t.Units {
		if !bound[id] {
			out[u.Group] = append(out[u.Group], id)
		}
	}

	for g := range out {
		sort.Strings(out[g])
	}

	return out
}

func TestUnboundNamesSelectWhatTheRulesSay(t *testing.T) {
	off := Options{On: func(string) bool { return false }}
	byNames := 0

	for seed := range uint64(800) {
		r := rand.New(rand.NewPCG(seed, 17)) //nolint:gosec // a test's repeatable trees
		units := 4 + r.IntN(20)
		b, h := randomTypedTrees(r, units)

		for _, tr := range []*deps.Tree{b, h} {
			for id, u := range tr.Units {
				tr.Typed = append(tr.Typed, plugin.Link{From: id, To: GroupPrefix + u.Group, Kind: KindFollow})
			}
		}

		imp := plugin.Link{From: GroupPrefix + "g0", To: GroupPrefix + "g1", Kind: KindFollow}
		b.Typed, h.Typed = append(b.Typed, imp), append(h.Typed, imp)

		if r.IntN(2) == 0 {
			id := fmt.Sprintf("u:%03d", units+5)
			h.Units[id] = deps.Unit{ID: id, Group: "g1", Digest: "1"}
		}

		var ss []plugins.BindSlice

		for i := range 1 + r.IntN(5) {
			sl := plugins.BindSlice{Suite: "s", Name: fmt.Sprintf("t%d", i), HasList: true, Depth: r.IntN(3)}
			for range 1 + r.IntN(3) {
				sl.List = append(sl.List, fmt.Sprintf("u:%03d", r.IntN(units)))
			}

			ss = append(ss, sl)
		}

		got, err := Select(context.Background(), b, h, ss, []string{Executed, Dependents}, off)
		if err != nil {
			t.Fatal(err)
		}

		for _, width := range []string{Executed, Dependents} {
			want := specSelected(b, h, ss, width)
			nb, nh := specNames(b, ss, width == Dependents), specNames(h, ss, width == Dependents)

			for _, sl := range ss {
				k := Key{Suite: sl.Suite, Name: sl.Name, Width: width}

				for g := range mergedKeys(specLinked(b, sl.List), specLinked(h, sl.List)) {
					if !slices.Equal(nb[g], nh[g]) && !want[k] {
						want[k] = true
						byNames++
					}
				}

				if _, sel := got.Selected[k]; sel != want[k] {
					t.Fatalf("seed %d: %v selected = %v, the rules say %v\npaths %+v\nbase %v\nhead %v\nlist %v\nnames %v / %v",
						seed, k, sel, want[k], got.Selected[k], b.Typed, h.Typed, sl.List, nb, nh)
				}
			}
		}
	}

	if byNames == 0 {
		t.Fatal("no slice was selected by names alone: the test proves nothing")
	}
}

func mergedKeys(a, b map[string]bool) map[string]bool {
	out := maps.Clone(a)
	maps.Copy(out, b)

	return out
}

func TestASetsInputUnitIsNoNameAndNotBoundWhole(t *testing.T) {
	ss := []plugins.BindSlice{{Suite: "s", Name: "links-b", HasList: true, List: []string{"go:a#func:A"}}}
	off := Options{On: func(string) bool { return false }}

	b := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1"})
	h := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1", "go:b#api:b": "1"})

	if got := selectedNames(t, b, h, ss, off); !got["links-b"] {
		t.Fatal("a new unit nothing binds didn't select the slice linking its package: the case proves nothing")
	}

	b.InputUnits, h.InputUnits = []string{"go:b#api:b"}, []string{"go:b#api:b"}
	if got := selectedNames(t, b, h, ss, off); len(got) != 0 {
		t.Fatalf("a new input unit selected %v, want nothing", got)
	}

	whole := func(api string) *deps.Tree {
		tr := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1", "go:b#api:b": api})
		tr.Typed = append(tr.Typed, plugin.Link{From: "go:a#func:A", To: "go:b#func:Old", Kind: KindFollow})
		tr.CannotTell = map[string]string{"go:b#func:Old": "generated"}

		return tr
	}

	on := Options{On: func(n string) bool { return n == narrowUnreachedNew }}

	if got := selectedNames(t, whole("1"), whole("2"), ss, on); !got["links-b"] {
		t.Fatal("a changed unit of a group bound whole didn't select: the case proves nothing")
	}

	b, h = whole("1"), whole("2")
	b.InputUnits, h.InputUnits = []string{"go:b#api:b"}, []string{"go:b#api:b"}

	if got := selectedNames(t, b, h, ss, on); len(got) != 0 {
		t.Fatalf("a changed input unit of a group bound whole selected %v, want nothing", got)
	}
}

func TestADeclaredInputUnitIsANameAndBoundWhole(t *testing.T) {
	ss := []plugins.BindSlice{{Suite: "s", Name: "links-b", HasList: true, List: []string{"go:a#func:A"}}}
	off := Options{On: func(string) bool { return false }}

	declared := func(tr *deps.Tree, id string) *deps.Tree {
		u := tr.Units[id]
		u.File = "b/b.go"
		tr.Units[id] = u
		tr.InputUnits = []string{id}

		return tr
	}

	b := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1"})
	h := declared(goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1", "go:b#func:New": "1"}), "go:b#func:New")

	if got := selectedNames(t, b, h, ss, off); !got["links-b"] {
		t.Fatalf("a new declared input unit selected %v, want the slice linking its package", got)
	}

	whole := func(d string) *deps.Tree {
		tr := declared(goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1", "go:b#func:New": d}), "go:b#func:New")
		tr.Typed = append(tr.Typed, plugin.Link{From: "go:a#func:A", To: "go:b#func:Old", Kind: KindFollow})
		tr.CannotTell = map[string]string{"go:b#func:Old": "generated"}

		return tr
	}

	on := Options{On: func(n string) bool { return n == narrowUnreachedNew }}

	if got := selectedNames(t, whole("1"), whole("2"), ss, on); !got["links-b"] {
		t.Fatalf("a changed declared input unit of a group bound whole selected %v, want the slice binding the group", got)
	}
}
