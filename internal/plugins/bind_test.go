package plugins

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

// tree: handler → service → store; store ← other; config can't be told;
// the contract file has units, of which the slice touched one.
func bindTree() *deps.Tree {
	units := map[string]deps.Unit{}

	for _, id := range []string{"g:handler", "g:service", "g:store", "g:other", "g:config", "api:create", "api:delete"} {
		group := strings.TrimPrefix(id, "g:")
		if strings.HasPrefix(id, "api:") {
			group = "api.yaml"
		}

		units[id] = deps.Unit{ID: id, Group: group, Digest: "d-" + id}
	}

	return &deps.Tree{
		Units: units,
		Links: []plugin.Link{
			{From: "g:handler", To: "g:service"},
			{From: "g:service", To: "g:store"},
			{From: "g:service", To: "g:config"},
			{From: "g:other", To: "g:store"},
		},
		CannotTell: map[string]string{"g:config": "built at run time"},
		FileUnits:  map[string][]string{"api.yaml": {"api:create", "api:delete"}},
		Providers:  map[string]string{"api:create": "api", "api:delete": "api", "g:handler": "go"},
	}
}

func byWidth(out []deps.Slice, name, width string) deps.Slice {
	for _, d := range out {
		if d.Name == name && d.Width == width {
			return d
		}
	}

	return deps.Slice{}
}

func TestBindWidthsAndRules(t *testing.T) {
	set := &Set{}

	out, err := set.Bind(t.Context(), t.TempDir(), bindTree(), []BindSlice{
		{Suite: "api", Name: "h_test.go", HasList: true, List: []string{"g:handler", "api:create"}, Data: []string{"//:api.yaml", "//fixtures:x"}},
		{Suite: "api", Name: "new_test.go"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ex := byWidth(out, "h_test.go", Executed)
	if want := []string{"api:create", "g:config", "g:handler", "g:service", "g:store"}; !slices.Equal(ex.Units, want) {
		t.Errorf("executed %v, want %v", ex.Units, want)
	}

	if !slices.Equal(ex.Groups, []string{"config"}) {
		t.Errorf("a unit that can't be told brings its group: %v", ex.Groups)
	}

	if !slices.Equal(ex.Data, []string{"//fixtures:x"}) || slices.Contains(ex.Units, "api:delete") {
		t.Errorf("data %v", ex.Data)
	}

	unobserved, err := set.Bind(t.Context(), t.TempDir(), bindTree(), []BindSlice{
		{Suite: "web", Name: "w_test.go", HasList: true, List: []string{"g:handler"}, Data: []string{"//:api.yaml", "//fixtures:x"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := byWidth(unobserved, "w_test.go", Executed).Data; !slices.Equal(got, []string{"//:api.yaml", "//fixtures:x"}) {
		t.Errorf("unobserved data %v", got)
	}

	if dep := byWidth(out, "h_test.go", Dependents); !slices.Contains(dep.Units, "g:other") || slices.Contains(ex.Units, "g:other") {
		t.Errorf("dependents reach other, executed doesn't: %v / %v", dep.Units, ex.Units)
	}

	if why := ex.Why["g:store"]; why.Kind != "closure" || why.Via != "g:service" {
		t.Errorf("why store: %+v", why)
	}

	if nl := byWidth(out, "new_test.go", Executed); !nl.Everything || len(nl.Units) != 0 {
		t.Errorf("a slice without a list binds everything: %+v", nl)
	}
}

func TestDependentsDepth(t *testing.T) {
	set := &Set{}

	out, err := set.Bind(t.Context(), t.TempDir(), bindTree(), []BindSlice{
		{Suite: "s", Name: "x", HasList: true, List: []string{"g:store"}, Depth: 1},
	})
	if err != nil {
		t.Fatal(err)
	}

	dep := byWidth(out, "x", Dependents)

	if !slices.Contains(dep.Units, "g:service") || slices.Contains(dep.Units, "g:handler") {
		t.Errorf("depth 1: %v", dep.Units)
	}
}

func TestABindCorrelatorAdds(t *testing.T) {
	s := newSetup(t, "binder")
	set := s.set(t)

	out, err := set.Bind(t.Context(), s.r.Dir, bindTree(), []BindSlice{
		{Suite: "api", Name: "h_test.go", HasList: true, List: []string{"g:handler"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ex := byWidth(out, "h_test.go", Executed)
	if !slices.Contains(ex.Units, "g:other") || ex.Why["g:other"].By != "uses" {
		t.Fatalf("executed %v, why %+v", ex.Units, ex.Why["g:other"])
	}
}

func TestBindSendsTheSetupAndTheNarrowings(t *testing.T) {
	s := newSetup(t, "setupbinder")
	set := s.set(t)

	tree := bindTree()
	tree.Narrowings = []string{"n1"}

	out, err := set.Bind(t.Context(), s.r.Dir, tree, []BindSlice{
		{Suite: "api", Name: "_setup.api", HasList: true, List: []string{"g:store"}, Setup: true},
		{Suite: "api", Name: "h_test.go", HasList: true, List: []string{"g:handler"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if su := byWidth(out, "_setup.api", Executed); !slices.Contains(su.Units, "g:other") || !slices.Contains(su.Units, "g:store") {
		t.Errorf("setup target: %v", su.Units)
	}

	if ex := byWidth(out, "h_test.go", Executed); slices.Contains(ex.Units, "g:other") {
		t.Errorf("the setup's binding reached a slice: %v", ex.Units)
	}
}

func TestBindKeepsReasonsAndDropped(t *testing.T) {
	s := newSetup(t, "dropper")
	set := s.set(t)

	out, err := set.Bind(t.Context(), s.r.Dir, bindTree(), []BindSlice{
		{Suite: "api", Name: "h_test.go", HasList: true, List: []string{"g:handler"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ex := byWidth(out, "h_test.go", Executed)

	if w := ex.Why["g:other"]; w.Kind != "references" || w.Via != "g:handler" || !slices.Equal(w.Vias, []string{"g:handler"}) || w.By != "uses" {
		t.Errorf("why other: %+v", w)
	}

	if !slices.Equal(ex.Dropped["n1"], []string{"g:store"}) || !slices.Equal(ex.DroppedGroups["n1"], []string{"grp"}) {
		t.Errorf("dropped %v, groups %v", ex.Dropped, ex.DroppedGroups)
	}
}

func TestCoreDropped(t *testing.T) {
	tree := bindTree()
	tree.RuledOut = map[string][]plugin.Link{"wide": {{From: "g:handler", To: "g:other"}}}
	tree.ByUnits = map[string]string{"api.yaml": "document-units"}

	out, err := (&Set{}).Bind(t.Context(), t.TempDir(), tree, []BindSlice{
		{Suite: "api", Name: "h_test.go", HasList: true, List: []string{"g:handler", "api:create"}, Data: []string{"//:api.yaml"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	ex := byWidth(out, "h_test.go", Executed)

	if !slices.Equal(ex.Dropped["wide"], []string{"g:other"}) || slices.Contains(ex.Units, "g:other") {
		t.Errorf("wide: %v, units %v", ex.Dropped, ex.Units)
	}

	if !slices.Equal(ex.Dropped["document-units"], []string{"api:delete"}) {
		t.Errorf("document units: %v", ex.Dropped)
	}
}
