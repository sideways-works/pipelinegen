package graph_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/graph"
	"github.com/sideways-works/pipelinegen/internal/graph/graphtest"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

// x keeps its digest while one of its links comes or goes, u keeps it too, t changes, n is new and r
// gone in the head; p binds x without expanding it; j is a junction leading to u.
const (
	movedX    = "u:x"
	movedU    = "u:u"
	movedT    = "u:t"
	movedN    = "u:n"
	movedR    = "u:r"
	movedP    = "u:p"
	movedJ    = graph.NodePrefix + "syn:impl:j"
	movedG    = graph.GroupPrefix + "gu"
	movedPlan = "uses"
)

type movedShape struct {
	name   string
	to     string
	inHead bool
}

var movedShapes = []movedShape{
	{"added to an unchanged unit", movedU, true},
	{"removed from an unchanged unit", movedU, false},
	{"added to a new unit", movedN, true},
	{"removed with its deleted unit", movedR, false},
	{"added to a changed unit", movedT, true},
	{"removed from a changed unit", movedT, false},
	{"added to a junction", movedJ, true},
	{"removed from a junction", movedJ, false},
	{"added to a group", movedG, true},
	{"removed from a group", movedG, false},
}

func movedTrees(s movedShape, kind string) (*deps.Tree, *deps.Tree) {
	tree := func(head bool) *deps.Tree {
		t := &deps.Tree{Units: map[string]deps.Unit{}, CannotTell: map[string]string{}}

		for _, id := range []string{movedX, movedU, movedT, movedP} {
			t.Units[id] = deps.Unit{ID: id, Group: "g" + strings.TrimPrefix(id, "u:"), Digest: "1"}
		}

		if head {
			t.Units[movedT] = deps.Unit{ID: movedT, Group: "gt", Digest: "2"}
			t.Units[movedN] = deps.Unit{ID: movedN, Group: "gn", Digest: "1"}
		} else {
			t.Units[movedR] = deps.Unit{ID: movedR, Group: "gr", Digest: "1"}
		}

		t.Typed = []plugin.Link{
			{From: movedP, To: movedX, Kind: graph.KindBind},
			{From: movedJ, To: movedU, Kind: graph.KindFollow},
		}

		if head == s.inHead {
			ln := plugin.Link{From: movedX, To: s.to, Kind: kind}
			if kind == movedPlan {
				t.Links = append(t.Links, ln)
			} else {
				t.Typed = append(t.Typed, ln)
			}
		}

		return t
	}

	return tree(false), tree(true)
}

func movedSlices(s movedShape) []plugins.BindSlice {
	return []plugins.BindSlice{
		{Suite: "s", Name: "x", HasList: true, List: []string{movedX}},
		{Suite: "s", Name: "p", HasList: true, List: []string{movedP}},
		{Suite: "s", Name: "end", HasList: true, List: []string{s.to}},
	}
}

func TestALinkThatComesOrGoesSelectsWhatItsWidthFollows(t *testing.T) {
	kinds := []string{movedPlan, graph.KindFollow, graph.KindBind, graph.KindDependentsOnly}

	for _, s := range movedShapes {
		for _, kind := range kinds {
			t.Run(fmt.Sprintf("%s, %s", s.name, kind), func(t *testing.T) {
				base, head := movedTrees(s, kind)
				ss := movedSlices(s)

				model := graphtest.Model{UnreachedNew: true}

				oracle, _, err := graphtest.ForwardWith(func(tr *deps.Tree) ([]deps.Slice, error) { return model.Bind(tr, ss), nil }, base, head)
				if err != nil {
					t.Fatal(err)
				}

				sel, err := graph.Select(t.Context(), base, head, ss, []string{graph.Executed, graph.Dependents}, graph.Options{})
				if err != nil {
					t.Fatal(err)
				}

				if c := graphtest.Compare(base, head, ss, oracle, sel.Selected, graphtest.Accept{}); len(c.Missing) > 0 || len(c.BadPaths) > 0 {
					t.Fatalf("the walk misses what binding forward selects:\n%s", c)
				}

				changedEnd := s.to == movedT || s.to == movedN || s.to == movedR

				for _, width := range []string{graph.Executed, graph.Dependents} {
					paths, got := sel.Selected[graph.Key{Suite: "s", Name: "x", Width: width}]
					want := width == graph.Dependents || kind != graph.KindDependentsOnly

					if got != want {
						t.Fatalf("%s: x selected %v, want %v: %v", width, got, want, paths)
					}

					if !got {
						continue
					}

					p := paths[0]

					switch {
					case changedEnd && (p.Changed != s.to || len(p.Steps) != 1 || p.Steps[0].From != movedX || p.Steps[0].To != s.to):
						t.Fatalf("%s: x's reason %+v, want the step %s -> %s", width, p, movedX, s.to)
					case !changedEnd && (p.Changed != movedX || len(p.Steps) != 0):
						t.Fatalf("%s: x's reason %+v, want x itself", width, p)
					}
				}
			})
		}
	}
}

func TestALinkToAChangedUnitSelectsNoSliceThatDoesntFollowIt(t *testing.T) {
	for _, s := range movedShapes {
		if s.to != movedT && s.to != movedN && s.to != movedR {
			continue
		}

		for kind, want := range map[string]bool{movedPlan: true, graph.KindFollow: false, graph.KindBind: false} {
			base, head := movedTrees(s, kind)

			sel, err := graph.Select(t.Context(), base, head, movedSlices(s), []string{graph.Executed}, graph.Options{})
			if err != nil {
				t.Fatal(err)
			}

			if _, got := sel.Selected[graph.Key{Suite: "s", Name: "p", Width: graph.Executed}]; got != want {
				t.Fatalf("%s, %s: p selected %v, want %v", s.name, kind, got, want)
			}
		}
	}
}
