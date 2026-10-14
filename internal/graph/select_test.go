package graph

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func selectedNames(t *testing.T, b, h *deps.Tree, ss []plugins.BindSlice, o Options) map[string]bool {
	t.Helper()

	got, err := Select(context.Background(), b, h, ss, []string{Executed}, o)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]bool{}
	for k := range got.Selected {
		out[k.Name] = true
	}

	return out
}

func TestASliceWithoutAListRunsWhenAnyUnitChangedAndOnlyThen(t *testing.T) {
	ss := []plugins.BindSlice{{Suite: "s", Name: "new_test.go"}}
	same := map[string]string{"go:a#func:A": "1"}

	if got := selectedNames(t, tree(same), tree(same), ss, Options{}); got["new_test.go"] {
		t.Fatal("an unchanged tree selected a slice without a list")
	}

	if got := selectedNames(t, tree(same), tree(map[string]string{"go:a#func:A": "2"}), ss, Options{}); !got["new_test.go"] {
		t.Fatal("a changed unit didn't select a slice without a list")
	}

	sel, err := Select(context.Background(), tree(same), tree(map[string]string{"go:a#func:A": "2"}), ss, []string{Executed, Dependents}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	for k, paths := range sel.Selected {
		if len(paths) > 0 {
			t.Fatalf("%v has the reasons %+v, want none", k, paths)
		}
	}
}

func TestARecordedSliceRootsAtItsOwnFilesUnitsAndTheEntriesItRuns(t *testing.T) {
	units := func(d string) *deps.Tree {
		tr := tree(nil)
		tr.Units = map[string]deps.Unit{
			"go:a#func:TestOne_test":  {ID: "go:a#func:TestOne_test", File: "a/a_test.go", Entry: "TestOne", Digest: "1"},
			"go:a#func:TestTwo_test":  {ID: "go:a#func:TestTwo_test", File: "a/a_test.go", Entry: "TestTwo", Digest: d},
			"go:a#func:helper_test":   {ID: "go:a#func:helper_test", File: "a/a_test.go", Digest: "1"},
			"go:a#type:fake_test":     {ID: "go:a#type:fake_test", File: "a/a_test.go", NotRoot: true, Digest: d},
			"go:a#typedecl:fake_test": {ID: "go:a#typedecl:fake_test", File: "a/a_test.go", Digest: "1"},
			"ts:web/x.spec.ts#fn:f":   {ID: "ts:web/x.spec.ts#fn:f", Group: "web/x.spec.ts", Digest: d},
		}

		return tr
	}

	one := plugins.BindSlice{Suite: "s", Name: "one", Sources: []string{"a/a_test.go"}, Tests: []string{"TestOne"}, HasList: true}
	all := plugins.BindSlice{Suite: "s", Name: "all", Sources: []string{"a/a_test.go"}, HasList: true}
	spec := plugins.BindSlice{Suite: "e2e", Name: "web/x.spec.ts", Sources: []string{"web/x.spec.ts"}, HasList: true}

	got := selectedNames(t, units("1"), units("2"), []plugins.BindSlice{one, all, spec}, Options{})

	if got["one"] {
		t.Fatal("a slice running TestOne was selected by TestTwo or by a type that is no root")
	}

	if !got["all"] || !got["web/x.spec.ts"] {
		t.Fatalf("selected %v, want all and web/x.spec.ts", got)
	}
}

func TestAChangedCorrelatorPinSelectsEveryRecordedSliceAndNoGraphOne(t *testing.T) {
	b, h := tree(map[string]string{"plugin:sql": "v1"}), tree(map[string]string{"plugin:sql": "v2"})
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "rec", HasList: true, List: []string{"go:a#func:A"}},
		{Suite: "g", Name: "graph", Graph: true},
	}

	got := selectedNames(t, b, h, ss, Options{Plugins: []Plugin{{Name: "sql"}}})
	if !got["rec"] || got["graph"] {
		t.Fatalf("selected %v, want rec only", got)
	}
}

func TestACorrelatorThatCantTellInOneTreeSelectsRecordedSlicesEvenWithNothingChanged(t *testing.T) {
	same := map[string]string{"go:a#func:A": "1"}
	b, h := tree(same), tree(same)
	h.Untold = map[string][]string{"go-uses": nil}
	ss := []plugins.BindSlice{{Suite: "s", Name: "rec", HasList: true, List: []string{"go:a#func:A"}}}
	o := Options{Plugins: []Plugin{{Name: "go-uses"}}}

	if got := selectedNames(t, b, h, ss, o); !got["rec"] {
		t.Fatal("a correlator untold in the head only didn't select the recorded slice")
	}

	sel, err := Select(context.Background(), b, h, ss, []string{Executed}, o)
	if err != nil {
		t.Fatal(err)
	}

	want := []Path{{Changed: "plugin:go-uses", Steps: []Step{{To: "plugin:go-uses", Kind: KindEverything}}}}
	if got := sel.Selected[Key{Suite: "s", Name: "rec", Width: Executed}]; !reflect.DeepEqual(got, want) {
		t.Fatalf("the reason is %+v, want one step naming the correlator", got)
	}

	b.Untold = h.Untold
	if got := selectedNames(t, b, h, ss, o); got["rec"] {
		t.Fatal("a correlator untold in both unchanged trees selected the slice")
	}
}

func TestAnUntoldSBOMFileSelectsTheSlicesThatReachItsNamespace(t *testing.T) {
	b := tree(map[string]string{"file:db/m.sql": "1", "pg:table:public.notes": "1", "go:a#func:A": "1", "go:b#func:B": "1"},
		plugin.Link{From: "go:a#func:A", To: "pg:table:public.notes", Kind: plugin.LinkReads})
	h := tree(map[string]string{"file:db/m.sql": "2", "pg:table:public.notes": "1", "go:a#func:A": "1", "go:b#func:B": "1"}, b.Links...)
	b.Untold = map[string][]string{"pg": {"db/m.sql"}}
	h.Untold = b.Untold

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "reads", HasList: true, List: []string{"go:a#func:A"}},
		{Suite: "s", Name: "other", HasList: true, List: []string{"go:b#func:B"}},
	}

	got := selectedNames(t, b, h, ss, Options{Plugins: []Plugin{{Name: "pg", SBOM: true, Namespaces: []string{"pg"}}}})
	if !got["reads"] || got["other"] {
		t.Fatalf("selected %v, want reads only", got)
	}
}

func TestASetupSliceKeepsItsName(t *testing.T) {
	b, h := tree(map[string]string{"go:s#func:Start": "1"}), tree(map[string]string{"go:s#func:Start": "2"})
	ss := []plugins.BindSlice{{Suite: "e2e", Name: "_setup.e2e", Setup: true, HasList: true, List: []string{"go:s#func:Start"}}}

	if got := selectedNames(t, b, h, ss, Options{}); !got["_setup.e2e"] {
		t.Fatalf("selected %v, want _setup.e2e", got)
	}
}

func TestAUnitThatCantBeToldInOneTreeOnlySelectsTheSlicesThatBindIt(t *testing.T) {
	units := map[string]string{"x:a#L": "1", "x:w#A": "1", "x:w#W": "1", "x:b#B": "1"}
	trees := func() (*deps.Tree, *deps.Tree) {
		b, h := tree(units), tree(units)
		delete(h.Units, "x:b#B")

		for _, tr := range []*deps.Tree{b, h} {
			for id, u := range tr.Units {
				u.Group = id[2:3]
				tr.Units[id] = u
			}

			tr.Typed = []plugin.Link{{From: "x:a#L", To: "x:w#A", Kind: KindBind}, {From: "x:w#A", To: "x:b#B", Kind: KindFollow}}
			tr.CannotTell = map[string]string{}
		}

		h.CannotTell["x:w#A"] = "links to x:b#B, which no plugin describes"

		return b, h
	}

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "binds", HasList: true, List: []string{"x:a#L"}},
		{Suite: "s", Name: "other", HasList: true, List: []string{"x:w#W"}},
	}

	b, h := trees()
	if got := selectedNames(t, b, h, ss, Options{}); !got["binds"] || got["other"] {
		t.Fatalf("selected %v, want binds only", got)
	}

	b, h = trees()
	b.CannotTell["x:w#A"] = "generated"

	if got := selectedNames(t, b, h, ss, Options{}); len(got) > 0 {
		t.Fatalf("a unit that can't be told in both trees selected %v", got)
	}
}

func TestAReasonTakesTheFewestHops(t *testing.T) {
	long := []plugin.Link{
		{From: "x:root", To: "x:a1", Kind: plugin.LinkUses},
		{From: "x:a1", To: "x:a2", Kind: plugin.LinkUses},
		{From: "x:a2", To: "x:a3", Kind: plugin.LinkUses},
		{From: "x:a3", To: "x:c", Kind: plugin.LinkUses},
	}
	direct := plugin.Link{From: "x:root", To: "x:c", Kind: plugin.LinkUses}
	units := map[string]string{"x:root": "1", "x:a1": "1", "x:a2": "1", "x:a3": "1", "x:c": "1"}
	changed := map[string]string{"x:root": "1", "x:a1": "1", "x:a2": "1", "x:a3": "1", "x:c": "2"}
	ss := []plugins.BindSlice{{Suite: "s", Name: "one", HasList: true, List: []string{"x:root"}}}

	for _, links := range [][]plugin.Link{append([]plugin.Link{direct}, long...), append(slices.Clone(long), direct)} {
		sel, err := Select(context.Background(), tree(units, links...), tree(changed, links...), ss, []string{Executed, Dependents}, Options{Paths: 1})
		if err != nil {
			t.Fatal(err)
		}

		for k, paths := range sel.Selected {
			if len(paths) != 1 || len(paths[0].Steps) != 1 {
				t.Fatalf("%v: the reason is %+v, want the one direct link", k, paths)
			}
		}
	}
}

func TestAGraphBoundSliceBindsItsOwnUnitsWithoutFollowingThem(t *testing.T) {
	units := func(own, far, sib string, cannotTell bool) *deps.Tree {
		tr := tree(map[string]string{"t:far": far, "t:named": "1"},
			plugin.Link{From: "t:own", To: "t:far", Kind: plugin.LinkUses},
			plugin.Link{From: "t:named", To: "t:far", Kind: plugin.LinkUses})
		tr.Units["t:own"] = deps.Unit{ID: "t:own", File: "g/g.spec.ts", Group: "grp", Digest: own}
		tr.Units["t:sib"] = deps.Unit{ID: "t:sib", File: "g/sib.txt", Group: "grp", Digest: sib}

		if cannotTell {
			tr.CannotTell = map[string]string{"t:own": "texts"}
		}

		return tr
	}

	plain := plugins.BindSlice{Suite: "g", Name: "plain", Sources: []string{"g/g.spec.ts"}, Graph: true}
	named := plugins.BindSlice{Suite: "g", Name: "named", Sources: []string{"g/g.spec.ts"}, Units: []string{"t:named"}, Graph: true}
	ss := []plugins.BindSlice{plain, named}

	for _, width := range []string{Executed, Dependents} {
		pick := func(b, h *deps.Tree) map[string]bool {
			got, err := Select(context.Background(), b, h, ss, []string{width}, Options{})
			if err != nil {
				t.Fatal(err)
			}

			out := map[string]bool{}
			for k := range got.Selected {
				out[k.Name] = true
			}

			return out
		}

		if got := pick(units("1", "1", "1", false), units("1", "2", "1", false)); got["plain"] || !got["named"] {
			t.Errorf("%s: a unit its own unit links to changed: selected %v, want named only", width, got)
		}

		if got := pick(units("1", "1", "1", false), units("2", "1", "1", false)); !got["plain"] || !got["named"] {
			t.Errorf("%s: its own unit changed: selected %v, want both", width, got)
		}

		if got := pick(units("1", "1", "1", false), units("1", "1", "2", false)); got["plain"] {
			t.Errorf("%s: a unit beside its own one changed: selected %v, want neither", width, got)
		}

		if got := pick(units("1", "1", "1", true), units("1", "1", "2", true)); !got["plain"] {
			t.Errorf("%s: a unit in the group of its own unit that can't be told changed: selected %v, want plain", width, got)
		}
	}
}

func TestAUnitNoSBOMListsBindsItsSliceWide(t *testing.T) {
	ss := []plugins.BindSlice{{Suite: "e2e", Name: "a.spec.ts", HasList: true, List: []string{"ts:a#fn:f", "dep:npm:react"}}}
	base, head := tree(map[string]string{"go:b#func:B": "1", "ts:a#fn:f": "1"}), tree(map[string]string{"go:b#func:B": "2", "ts:a#fn:f": "1"})

	goOnly := Options{Plugins: []Plugin{{Name: "go-sbom", SBOM: true, Namespaces: []string{"go", "dep:go"}}, {Name: "ts", SBOM: true, Namespaces: []string{"ts"}}}}

	sel, err := Select(context.Background(), base, head, ss, []string{Executed}, goOnly)
	if err != nil {
		t.Fatal(err)
	}

	paths := sel.Selected[Key{Suite: "e2e", Name: "a.spec.ts", Width: Executed}]
	if len(paths) != 1 || paths[0].Changed != "dep:npm:react" || paths[0].Steps[0].Kind != KindUnlisted {
		t.Fatalf("paths %+v", paths)
	}

	withNPM := goOnly
	withNPM.Plugins = append(slices.Clone(goOnly.Plugins), Plugin{Name: "npm", SBOM: true, Namespaces: []string{"dep:npm"}})

	if got := selectedNames(t, base, head, ss, withNPM); got["a.spec.ts"] {
		t.Fatal("a change it never touched selected it once npm units are listed")
	}
}

func TestAPathNamesThePluginOfEachLinkAndItsUnits(t *testing.T) {
	mk := func(d string) *deps.Tree {
		tr := tree(nil)
		tr.Units = map[string]deps.Unit{
			"api:createNote":       {ID: "api:createNote", File: "api/notes.yaml", Label: "POST /notes", Digest: "1"},
			"go:api#func:validate": {ID: "go:api#func:validate", File: "api/api.go", Label: "api.validate", Digest: d},
		}
		tr.Providers = map[string]string{"api:createNote": "openapi", "go:api#func:validate": "go-sbom"}
		tr.Links = []plugin.Link{{From: "api:createNote", To: "go:api#func:validate", Kind: plugin.LinkUses}}
		tr.LinkBy = map[[2]string]string{{"api:createNote", "go:api#func:validate"}: "api-uses"}

		return tr
	}

	ss := []plugins.BindSlice{{Suite: "e2e", Name: "create.spec.ts", HasList: true, List: []string{"api:createNote"}}}

	sel, err := Select(context.Background(), mk("1"), mk("2"), ss, []string{Executed}, Options{})
	if err != nil {
		t.Fatal(err)
	}

	paths := sel.Selected[Key{Suite: "e2e", Name: "create.spec.ts", Width: Executed}]
	if len(paths) != 1 || len(paths[0].Steps) != 1 || paths[0].Steps[0].Plugin != "api-uses" {
		t.Fatalf("paths %+v", paths)
	}

	if n := paths[0].Nodes["api:createNote"]; n.Label != "POST /notes" || n.File != "api/notes.yaml" || n.Plugin != "openapi" {
		t.Fatalf("nodes %+v", paths[0].Nodes)
	}
}
