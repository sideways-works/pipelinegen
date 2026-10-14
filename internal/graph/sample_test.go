package graph

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestWhatEverySliceRanGoesToTheSampleAndWhatItBringsToSampleBound(t *testing.T) {
	links := []plugin.Link{
		{From: "go:s#func:Start", To: "go:s#type:Config", Kind: plugin.LinkUses},
		{From: "go:a#func:A", To: "go:a#type:Own", Kind: plugin.LinkUses},
	}
	units := map[string]string{"go:s#func:Start": "1", "go:s#type:Config": "1", "go:a#func:A": "1", "go:a#type:Own": "1", "go:b#func:B": "1"}

	ss := []plugins.BindSlice{
		{Suite: "s", Name: "a", HasList: true, List: []string{"go:s#func:Start", "go:a#func:A"}},
		{Suite: "s", Name: "b", HasList: true, List: []string{"go:s#func:Start", "go:b#func:B"}},
	}

	both := Options{On: func(n string) bool { return n == narrowSample || n == narrowSampleBound }}
	sampleOnly := Options{On: func(n string) bool { return n == narrowSample }}

	for _, c := range []struct {
		name    string
		changed string
		o       Options
		want    []string
	}{
		{"what every slice ran", "go:s#func:Start", both, []string{SampleTarget("s")}},
		{"what it brings, with sample-bound", "go:s#type:Config", both, []string{SampleBoundTarget("s")}},
		{"what it brings, without sample-bound", "go:s#type:Config", sampleOnly, []string{SampleTarget("s"), "a", "b"}},
		{"one slice's own", "go:a#type:Own", both, []string{"a"}},
	} {
		h := maps.Clone(units)

		h[c.changed] = "2"

		got, err := Select(context.Background(), tree(units, links...), tree(h, links...), ss, []string{Executed}, c.o)
		if err != nil {
			t.Fatal(err)
		}

		var names []string
		for _, hit := range got.Hits("s", Executed) {
			names = append(names, hit.Slice)
		}

		if !slices.Equal(names, c.want) {
			t.Fatalf("%s: selected %v, want %v", c.name, names, c.want)
		}
	}
}

func hitsOf(t *testing.T, b, h *deps.Tree, ss []plugins.BindSlice, width string, o Options) []string {
	t.Helper()

	got, err := Select(context.Background(), b, h, ss, []string{width}, o)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, hit := range got.Hits("s", width) {
		names = append(names, hit.Slice)
	}

	return names
}

var sampleOn = Options{On: func(n string) bool { return n == narrowSample }}

func TestASampledUnitsMovedLinkSelectsTheSlicesWhoseBindingMoved(t *testing.T) {
	units := map[string]string{"x:x": "1", "x:a": "1", "x:b": "1", "x:own": "1"}
	h := maps.Clone(units)
	h["x:x"] = "2"

	for _, width := range []string{Executed, Dependents} {
		b := tree(units, plugin.Link{From: "x:own", To: "x:a", Kind: plugin.LinkUses})
		hd := tree(h, plugin.Link{From: "x:own", To: "x:a", Kind: plugin.LinkUses})
		b.Typed = []plugin.Link{{From: "x:x", To: "x:a", Kind: KindFollow}}
		hd.Typed = []plugin.Link{{From: "x:x", To: "x:b", Kind: KindFollow}}

		ss := []plugins.BindSlice{
			{Suite: "s", Name: "one", HasList: true, List: []string{"x:x"}},
			{Suite: "s", Name: "two", HasList: true, List: []string{"x:x"}},
			{Suite: "s", Name: "three", HasList: true, List: []string{"x:x", "x:own"}},
		}

		if got, want := hitsOf(t, b, hd, ss, width, sampleOn), []string{SampleTarget("s"), "one", "three", "two"}; !slices.Equal(got, want) {
			t.Fatalf("%s: selected %v, want %v", width, got, want)
		}

		hd.Typed = nil
		if got, want := hitsOf(t, b, hd, ss, width, sampleOn), []string{SampleTarget("s"), "one", "two"}; !slices.Equal(got, want) {
			t.Fatalf("%s, a link gone: selected %v, want %v", width, got, want)
		}
	}
}

func TestPastTheCapASampledUnitWithMovedLinksSelectsEverySliceBindingIt(t *testing.T) {
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"x:x"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"x:x"}},
		{Suite: "s", Name: "three", HasList: true, List: []string{"x:x", "x:own"}},
	}

	for _, c := range []struct {
		moved int
		want  []string
	}{
		{movedCap, []string{SampleTarget("s"), "one", "two"}},
		{movedCap + 1, []string{SampleTarget("s"), "one", "three", "two"}},
	} {
		units := map[string]string{"x:x": "1", "x:own": "1"}

		var gone, kept []plugin.Link

		for i := range c.moved {
			id := fmt.Sprintf("x:b%03d", i)
			units[id] = "1"
			gone = append(gone, plugin.Link{From: "x:x", To: id, Kind: plugin.LinkUses})
			kept = append(kept, plugin.Link{From: "x:own", To: id, Kind: plugin.LinkUses})
		}

		h := maps.Clone(units)
		h["x:x"] = "2"

		got := hitsOf(t, tree(units, slices.Concat(gone, kept)...), tree(h, kept...), ss, Executed, sampleOn)
		if !slices.Equal(got, c.want) {
			t.Fatalf("%d moved: selected %v, want %v", c.moved, got, c.want)
		}
	}
}

func TestASampledChangeStillSelectsTheSetupSlice(t *testing.T) {
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "_setup.s", Setup: true, HasList: true, List: []string{"x:x"}},
		{Suite: "s", Name: "one", HasList: true, List: []string{"x:x", "x:a"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"x:x", "x:b"}},
	}
	units := map[string]string{"x:x": "1", "x:a": "1", "x:b": "1"}
	h := maps.Clone(units)
	h["x:x"] = "2"

	if got, want := hitsOf(t, tree(units), tree(h), ss, Executed, sampleOn), []string{"_sample.s", "_setup.s"}; !slices.Equal(got, want) {
		t.Fatalf("selected %v, want %v", got, want)
	}
}

func TestWhenEverySliceIsSelectedTheSampleTargetsAreToo(t *testing.T) {
	ss := []plugins.BindSlice{
		{Suite: "s", Name: "one", HasList: true, List: []string{"x:a"}},
		{Suite: "s", Name: "two", HasList: true, List: []string{"x:a", "x:b"}},
	}
	units := map[string]string{"x:a": "1", "x:b": "1"}
	h := maps.Clone(units)
	h["x:b"] = "2"

	both := Options{On: func(n string) bool { return n == narrowSample || n == narrowSampleBound }}

	if got, want := hitsOf(t, tree(units), tree(h), ss, Executed, both), []string{"two"}; !slices.Equal(got, want) {
		t.Fatalf("one slice: selected %v, want %v", got, want)
	}

	h["x:a"] = "2"
	ss[0].List = []string{"x:c"}
	units["x:c"], h["x:c"] = "1", "2"

	want := []string{SampleBoundTarget("s"), SampleTarget("s"), "one", "two"}
	if got := hitsOf(t, tree(units), tree(h), ss, Executed, both); !slices.Equal(got, want) {
		t.Fatalf("every slice: selected %v, want %v", got, want)
	}
}
