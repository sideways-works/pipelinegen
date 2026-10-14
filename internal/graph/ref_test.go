package graph

import (
	"context"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestAReferencedNewNameCountsAsBoundWhileReferencedNewIsOn(t *testing.T) {
	b := goTree(map[string]string{"go:a#func:A": "1", "go:b#func:Old": "1"})
	h := goTree(map[string]string{"go:a#func:A": "2", "go:b#func:Old": "1", "go:b#func:New": "1"})
	h.Typed = append(h.Typed, plugin.Link{From: "go:a#func:A", To: "go:b#func:New", Kind: KindDependentsOnly, Ref: true})

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "a", HasList: true, List: []string{"go:a#func:A"}},
		{Suite: "s", Name: "old", HasList: true, List: []string{"go:b#func:Old"}},
	}

	on := Options{On: func(n string) bool { return n == narrowReferencedNew }}
	if got := selectedNames(t, b, h, ss, on); !got["a"] || got["old"] {
		t.Fatalf("referenced-new on: selected %v, want a only (its own change)", got)
	}

	off := Options{On: func(string) bool { return false }}
	if got := selectedNames(t, b, h, ss, off); !got["a"] || !got["old"] {
		t.Fatalf("referenced-new off: selected %v, want both", got)
	}

	sel, err := Select(context.Background(), b, h, ss, []string{Executed}, on)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := sel.LeftOut[narrowReferencedNew][Key{Suite: "s", Name: "old", Width: Executed}]; !ok {
		t.Fatalf("referenced-new left out %v, want old", sel.LeftOut)
	}
}
