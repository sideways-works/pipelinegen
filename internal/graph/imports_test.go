package graph

import (
	"context"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

// importTrees are package c gaining an import of d, which its changed function Lock calls; d imports
// e when effects, whose variable Init runs at start-up.
func importTrees(effects bool) (b, h *deps.Tree) {
	units := map[string]string{"go:c#func:Lock": "1", "go:c#type:P": "1", "go:d#func:Kind": "1", "go:e#var:Init": "1"}
	b = goTree(units)

	units["go:c#func:Lock"] = "2"
	h = goTree(units)
	h.Typed = append(h.Typed,
		plugin.Link{From: GroupPrefix + "c", To: GroupPrefix + "d", Kind: KindFollow},
		plugin.Link{From: "go:c#func:Lock", To: "go:d#func:Kind", Kind: KindFollow})

	if effects {
		for _, t := range []*deps.Tree{b, h} {
			t.Typed = append(t.Typed, plugin.Link{From: GroupPrefix + "d", To: "go:e#var:Init", Kind: KindFollow})
		}
	}

	return b, h
}

func importSlices() []plugins.BindSlice {
	return []plugins.BindSlice{
		{Suite: "s", Name: "lock", HasList: true, List: []string{"go:c#func:Lock"}},
		{Suite: "s", Name: "problem", HasList: true, List: []string{"go:c#type:P"}},
		{Suite: "s", Name: "linked-d", HasList: true, List: []string{"go:c#type:P", "go:d#func:Kind"}},
	}
}

func TestAnImportOfCodeThatRunsNothingAtStartSelectsNoSliceOfTheImporter(t *testing.T) {
	b, h := importTrees(false)

	on := Options{On: func(n string) bool { return n == narrowLinkedImports }}
	if got := selectedNames(t, b, h, importSlices(), on); !got["lock"] || got["problem"] || got["linked-d"] {
		t.Fatalf("linked-imports on: selected %v, want lock only", got)
	}

	off := Options{On: func(string) bool { return false }}
	if got := selectedNames(t, b, h, importSlices(), off); !got["lock"] || !got["problem"] || !got["linked-d"] {
		t.Fatalf("linked-imports off: selected %v, want every slice that links c", got)
	}

	sel, err := Select(context.Background(), b, h, importSlices(), []string{Executed}, on)
	if err != nil {
		t.Fatal(err)
	}

	left := sel.LeftOut[narrowLinkedImports]
	if _, ok := left[Key{Suite: "s", Name: "problem", Width: Executed}]; !ok || len(left) != 2 {
		t.Fatalf("left out by linked-imports: %v, want problem and linked-d", left)
	}
}

func TestAnImportSelectsTheSlicesThatNowLinkWhatRunsAtStart(t *testing.T) {
	b, h := importTrees(true)

	on := Options{On: func(n string) bool { return n == narrowLinkedImports }}

	// linked-d reached d's start-up in both trees; problem reaches it only now
	if got := selectedNames(t, b, h, importSlices(), on); !got["lock"] || !got["problem"] || got["linked-d"] {
		t.Fatalf("linked-imports on: selected %v, want lock and problem", got)
	}
}
