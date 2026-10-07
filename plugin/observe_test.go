package plugin_test

import (
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestObserveSeesOnlyTheTreeAndTheCoverage(t *testing.T) {
	for p, want := range map[string]bool{
		"source": true, "source/web/dist": true, "./source/dist": true, "coverage/build": true, "coverage": true,
		"prepared/tree": false, "dist": false, "/source/dist": false, "../source": false,
		"source/../prepared": false, "coverage/../../x": false, "sources/x": false,
	} {
		if got := plugin.Observable(p); got != want {
			t.Errorf("%s: %t, want %t", p, got, want)
		}
	}

	d := plugin.Description{ObserveReads: []string{"build_root", "maps", "depth"}}

	got := d.ObserveReadsOutside(map[string]any{
		"build_root": "prepared/tree", "maps": []any{"coverage/maps", "/srv"}, "depth": 3, "other": "/elsewhere",
	})

	want := []plugin.ObserveRead{{Key: "build_root", Value: "prepared/tree"}, {Key: "maps", Value: "/srv"}, {Key: "depth", Value: "3"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if got := d.ObserveReadsOutside(map[string]any{"build_root": ""}); len(got) != 0 {
		t.Fatalf("an empty value keeps the default: %v", got)
	}
}
