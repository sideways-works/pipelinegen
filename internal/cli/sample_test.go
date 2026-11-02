package cli

import (
	"maps"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/plugin"
)

func sampleSlices() (*deps.Tree, []deps.Slice) {
	t := &deps.Tree{Links: []plugin.Link{
		{From: "u:start", To: "u:cfg"}, {From: "u:start", To: "u:helper"}, {From: "u:own", To: "u:helper"},
	}}

	slice := func(name string, extra map[string]deps.Why) deps.Slice {
		why := map[string]deps.Why{
			"u:start":  {Kind: whyExecuted},
			"u:cfg":    {Kind: whyClosure, Via: "u:start"},
			"u:clock":  {Kind: "references", Via: "u:start", Vias: []string{"u:start"}, By: "go"},
			"u:boot":   {Kind: whyInit, By: "go"},
			"u:opaque": {Kind: "references", Via: "u:start", By: "go"},
			"u:helper": {Kind: whyClosure, Via: "u:start"},
		}

		maps.Copy(why, extra)

		d := deps.Slice{Suite: "s", Name: name, Width: "executed", Why: why}
		for id := range why {
			d.Units = append(d.Units, id)
		}

		slices.Sort(d.Units)

		return d
	}

	// the dependents width too, which sorts first: what the sample takes
	// out must leave both.
	dependents := func(d deps.Slice) deps.Slice {
		d.Width = "dependents"
		d.Units = slices.Clone(d.Units)

		return d
	}

	a, b := slice("a", map[string]deps.Why{"u:own": {Kind: whyExecuted}}), slice("b", nil)

	return t, []deps.Slice{
		a, b, {Suite: "s", Name: "new", Width: "executed", Everything: true},
		dependents(a), dependents(b),
	}
}

func unitsOf(ss []deps.Slice, name string) []string {
	for _, d := range ss {
		if d.Name == name && d.Width == "executed" {
			return d.Units
		}
	}

	return nil
}

func TestToSample(t *testing.T) {
	cases := []struct {
		name          string
		sample, bound bool
		sampled       []string
		boundOnly     []string
		b             []string
	}{
		{"off", false, false, nil, nil, []string{"u:boot", "u:cfg", "u:clock", "u:helper", "u:opaque", "u:start"}},
		{"sample", true, false, []string{"u:start"}, nil, []string{"u:boot", "u:cfg", "u:clock", "u:helper", "u:opaque"}},
		{"sample-bound", true, true, []string{"u:start"}, []string{"u:boot", "u:cfg", "u:clock"}, []string{"u:helper", "u:opaque"}},
	}

	for _, c := range cases {
		tree, ss := sampleSlices()
		out := toSample(tree, ss, c.sample, c.bound)

		if got := unitsOf(out, affected.SampleTarget("s")); !slices.Equal(got, c.sampled) {
			t.Errorf("%s: sample %v, want %v", c.name, got, c.sampled)
		}

		if got := unitsOf(out, affected.SampleBoundTarget("s")); !slices.Equal(got, c.boundOnly) {
			t.Errorf("%s: sample-bound %v, want %v", c.name, got, c.boundOnly)
		}

		if got := unitsOf(out, "b"); !slices.Equal(got, c.b) {
			t.Errorf("%s: b keeps %v, want %v", c.name, got, c.b)
		}

		if got := unitsOf(out, "new"); len(got) != 0 {
			t.Errorf("%s: new %v", c.name, got)
		}
	}
}
