package graph

import (
	"context"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
)

func TestAUnitThatCantBeToldIsLabelledByThePluginThatSaidSo(t *testing.T) {
	units := map[string]string{"x:a#L": "1", "x:w#A": "1", "x:w#W": "1"}
	changed := map[string]string{"x:a#L": "1", "x:w#A": "1", "x:w#W": "2"}

	for _, c := range []struct {
		by, want string
	}{
		{"corr", "corr: package w does not build"},
		{"", "package w does not build"},
	} {
		b, h := tree(units), tree(changed)

		for _, tr := range []*deps.Tree{b, h} {
			for id, u := range tr.Units {
				u.Group = id[2:3]
				tr.Units[id] = u
				tr.Providers[id] = "sbom"
			}

			tr.CannotTell = map[string]string{"x:w#A": "package w does not build"}
			tr.CannotTellBy = map[string]string{}
		}

		if c.by != "" {
			b.CannotTellBy["x:w#A"], h.CannotTellBy["x:w#A"] = c.by, c.by
		}

		sel, err := Select(context.Background(), b, h, []plugins.BindSlice{{Suite: "s", Name: "binds", HasList: true, List: []string{"x:w#A"}}}, []string{Executed}, Options{Paths: 1})
		if err != nil {
			t.Fatal(err)
		}

		paths := sel.Selected[Key{Suite: "s", Name: "binds", Width: Executed}]
		if len(paths) != 1 || paths[0].Why != c.want {
			t.Errorf("by %q: paths %+v, want one whose why is %q", c.by, paths, c.want)
		}
	}
}
