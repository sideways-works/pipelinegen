package plugins

import (
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestWhatAPluginCantTellIsBoundWide(t *testing.T) {
	for _, call := range []string{plugin.CallDetect, plugin.CallUnits} {
		t.Run(call, func(t *testing.T) {
			s := newSetup(t, "links")
			s.r.Write("cant-tell/"+call, "")
			s.r.Commit("can't tell")

			set := s.set(t)

			tree, err := set.Tree(t.Context(), s.r.Dir, "c", 3)
			if err != nil {
				t.Fatal(err)
			}

			files := []string{"texts/a.txt", "texts/b.txt", "texts/doc.txt"}
			if got := tree.Untold["texts"]; !slices.Equal(got, files) {
				t.Fatalf("untold %v", tree.Untold)
			}

			for _, f := range files {
				if u := tree.Units[deps.FileID(f)]; u.Digest == "" || tree.Providers[u.ID] != "texts" {
					t.Errorf("%s: unit %+v by %q", f, u, tree.Providers[u.ID])
				}
			}

			bound, err := set.Bind(t.Context(), s.r.Dir, tree, []BindSlice{
				{Suite: "e2e", Name: "reached", List: []string{"t:a.txt"}, HasList: true},
				{Suite: "e2e", Name: "elsewhere", List: []string{"x:other"}, HasList: true},
			})
			if err != nil {
				t.Fatal(err)
			}

			for _, d := range bound {
				has := slices.Contains(d.Units, deps.FileID("texts/b.txt"))
				if d.Everything || has != (d.Name == "reached") {
					t.Errorf("%s (%s): everything %v, units %v", d.Name, d.Width, d.Everything, d.Units)
				}
			}
		})
	}

	t.Run("a correlator", func(t *testing.T) {
		tree := &deps.Tree{Units: map[string]deps.Unit{}, Untold: map[string][]string{"uses": {"texts/a.txt"}}}
		set := &Set{Plugins: []*Loaded{{Desc: plugin.Description{Kind: plugin.KindCorrelator}}}}
		set.Plugins[0].Conf.Name = "uses"

		d := &deps.Slice{Why: map[string]deps.Why{}}
		set.bindUntold(tree, d, BindSlice{List: []string{"x:other"}, HasList: true}, newLinkGraph(tree), unitIndex{})

		if !d.Everything {
			t.Error("a slice a correlator couldn't tell about isn't bound to everything")
		}
	})

	t.Run("a bind correlator", func(t *testing.T) {
		ss := []BindSlice{{Name: "recorded", HasList: true}, {Name: "graph", Graph: true}}
		bound := []map[string]*deps.Slice{
			{Executed: {}, Dependents: {}},
			{Executed: {}, Dependents: {}},
		}

		untoldBinder(ss, bound)

		for i, sl := range ss {
			for w, d := range bound[i] {
				if d.Everything != !sl.Graph {
					t.Errorf("%s (%s): everything %v", sl.Name, w, d.Everything)
				}
			}
		}
	})
}
