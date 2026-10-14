package graph

import (
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func entryTrees(changed string, own bool) (*deps.Tree, *deps.Tree, []plugins.BindSlice) {
	units := map[string]string{"go:a": "1", "go:b": "1", "go:c": "1", "go:x": "1", "plugin:go": "1", "plugin:react": "1"}
	link := plugin.Link{From: "go:b", To: "go:x", Kind: plugin.LinkUses}

	tr := func(us map[string]string) *deps.Tree {
		t := tree(us, link)
		t.Pins = map[string]string{"go": "plugin:go", "react": "plugin:react"}
		t.CorrelatorEnds = map[string][]string{"react": {"go:b"}}

		for _, id := range []string{"go:a", "go:b", "go:c", "go:x"} {
			t.Providers[id] = "go"
		}

		return t
	}

	h := maps.Clone(units)
	h["plugin:"+changed] = "2"

	if own {
		h["go:c"] = "2"
	}

	ss := []plugins.BindSlice{
		{Suite: "web", Name: "w1", HasList: true, List: []string{"go:a"}},
		{Suite: "web", Name: "w2", HasList: true, List: []string{"go:a"}},
		{Suite: "api", Name: "one", HasList: true, List: []string{"go:b"}},
		{Suite: "api", Name: "two", HasList: true, List: []string{"go:c"}},
	}

	return tr(units), tr(h), ss
}

func entryOptions(entry bool) Options {
	return Options{
		Plugins:  []Plugin{{Name: "go", SBOM: true, Namespaces: []string{"go"}}, {Name: "react"}},
		On:       func(n string) bool { return entry && n == narrowPluginEntry },
		Observes: func(suite, _ string) bool { return suite == "web" },
	}
}

func TestAPluginsEntryReachesWhatItBindsAndSamplesWhileItsNarrowingIsOn(t *testing.T) {
	for _, c := range []struct {
		changed  string
		own      bool
		entry    bool
		selected []string
		left     []string
	}{
		{changed: "react", selected: []string{"api/one", "web/w1", "web/w2"}},
		{changed: "react", entry: true, selected: []string{"api/" + EntryTarget("api"), "web/" + EntryTarget("web")}, left: []string{"api/one", "web/w1", "web/w2"}},
		{changed: "go", selected: []string{"api/one", "api/two", "web/w1", "web/w2"}},
		{changed: "go", entry: true, selected: []string{"api/" + EntryTarget("api"), "web/" + EntryTarget("web")}, left: []string{"api/one", "api/two", "web/w1", "web/w2"}},
		{changed: "go", own: true, entry: true, selected: []string{"api/" + EntryTarget("api"), "api/two", "web/" + EntryTarget("web")}, left: []string{"api/one", "web/w1", "web/w2"}},
		{changed: "react", own: true, entry: true, selected: []string{"api/" + EntryTarget("api"), "api/two", "web/" + EntryTarget("web")}, left: []string{"api/one", "web/w1", "web/w2"}},
	} {
		name := fmt.Sprintf("%s entry, own change %v, plugin-entry on %v", c.changed, c.own, c.entry)
		b, h, ss := entryTrees(c.changed, c.own)

		sel, err := Select(t.Context(), b, h, ss, []string{Executed, Dependents}, entryOptions(c.entry))
		if err != nil {
			t.Fatal(err)
		}

		for _, width := range []string{Executed, Dependents} {
			var got, left []string

			for k := range sel.Selected {
				if k.Width == width {
					got = append(got, k.Suite+"/"+k.Name)
				}
			}

			for k := range sel.LeftOut[narrowPluginEntry] {
				if k.Width == width {
					left = append(left, k.Suite+"/"+k.Name)
				}
			}

			slices.Sort(got)
			slices.Sort(left)

			var entries []string

			for k := range sel.Entries {
				if k.Width == width {
					entries = append(entries, k.Suite+"/"+k.Name)
				}
			}

			slices.Sort(entries)

			if !c.entry && !slices.Equal(entries, c.selected) {
				t.Errorf("%s, %s: an entry reached %v, want %v", name, width, entries, c.selected)
			}

			if !slices.Equal(got, c.selected) || !slices.Equal(left, c.left) {
				t.Errorf("%s, %s: selects %v and leaves out %v, want %v and %v", name, width, got, c.selected, left, c.left)
			}
		}
	}
}

func TestACorrelatorsEntryLeavesAloneASliceOfASuiteItNeitherObservesNorAnswered(t *testing.T) {
	b, h, ss := entryTrees("react", false)
	b.CorrelatorEnds, h.CorrelatorEnds = nil, nil

	sel, err := Select(t.Context(), b, h, ss, []string{Executed, Dependents}, entryOptions(false))
	if err != nil {
		t.Fatal(err)
	}

	for k := range sel.Selected {
		if k.Suite == "api" {
			t.Errorf("selects %s/%s (%s), which neither the correlator observes nor binds through its answers", k.Suite, k.Name, k.Width)
		}
	}
}

func TestACorrelatorsEntryReachesAGraphBoundSliceThroughTheUnitsItsDataNames(t *testing.T) {
	b, h, _ := entryTrees("react", false)
	ss := []plugins.BindSlice{
		{Suite: "it", Name: "through", Graph: true, Units: []string{"go:b"}},
		{Suite: "it", Name: "apart", Graph: true, Units: []string{"go:c"}},
	}

	sel, err := Select(t.Context(), b, h, ss, []string{Executed, Dependents}, entryOptions(false))
	if err != nil {
		t.Fatal(err)
	}

	for _, width := range []string{Executed, Dependents} {
		_, through := sel.Selected[Key{Suite: "it", Name: "through", Width: width}]
		_, apart := sel.Selected[Key{Suite: "it", Name: "apart", Width: width}]

		if !through || apart {
			t.Errorf("%s: selects through %v, apart %v; want through only", width, through, apart)
		}
	}
}

func TestAnSBOMsEntryIsNamedOnItsPaths(t *testing.T) {
	for _, entry := range []bool{false, true} {
		b, h, ss := entryTrees("go", true)

		sel, err := Select(t.Context(), b, h, ss, []string{Executed}, entryOptions(entry))
		if err != nil {
			t.Fatal(err)
		}

		all := maps.Clone(sel.Selected)
		maps.Copy(all, sel.LeftOut[narrowPluginEntry])

		named := 0

		for k, paths := range all {
			for _, p := range paths {
				switch {
				case p.Changed == "go:c" && p.Entry != "":
					t.Errorf("entry %v: %s/%s names the entry %q for go:c, which changed in its own right", entry, k.Suite, k.Name, p.Entry)
				case p.Changed == "go:a" || p.Changed == "go:b":
					if p.Entry != "go" {
						t.Errorf("entry %v: %s/%s names %q for %s, which only go's entry changed", entry, k.Suite, k.Name, p.Entry, p.Changed)
					}

					named++
				}
			}
		}

		if named == 0 {
			t.Errorf("entry %v: no path to a unit only go's entry changed: the test proves nothing", entry)
		}
	}
}
