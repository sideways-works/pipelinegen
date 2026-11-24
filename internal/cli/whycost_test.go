package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
)

func TestTheCostNamesWhatSelectedTheMostAndWhatAlone(t *testing.T) {
	r := func(what string, through ...string) affected.Reason {
		return affected.Reason{Kind: affected.KindDepends, What: what, Through: through}
	}

	s := whySuite{
		Suite: "backend",
		Selected: []whySelected{
			{Name: "a", Label: "//a"}, {Name: "b", Label: "//b"}, {Name: "c", Label: "//c"},
		},
		Untouched: []string{"//d"},
		Reasons: map[string][]affected.Reason{
			// a and b reach the package only through the hub; c reaches the function too
			"//a": {r("group:core", "hub", "go:a")},
			"//b": {r("group:core", "hub", "go:b")},
			"//c": {r("group:core", "hub", "go:c"), r("go:core#func:Lock", "go:c")},
		},
	}

	c := suiteCost(s)

	if c.Selected != 3 || c.Of != 4 {
		t.Fatalf("selected %d of %d", c.Selected, c.Of)
	}

	if got := c.Changed[0]; got.What != "group:core" || got.Slices != 3 || got.Alone != 2 {
		t.Fatalf("first changed %+v", got)
	}

	if got := c.Changed[1]; got.What != "go:core#func:Lock" || got.Slices != 1 || got.Alone != 0 {
		t.Fatalf("second changed %+v", got)
	}

	// a slice's own unit ends its path and is no junction; the hub leads to every slice, two alone
	if len(c.Through) != 1 || c.Through[0].What != "hub" || c.Through[0].Slices != 3 || c.Through[0].Alone != 2 {
		t.Fatalf("through %+v", c.Through)
	}

	var out bytes.Buffer
	if err := writeCosts(&out, []Cost{c}, "at h", false, nil); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"backend: 3 of 4 selected at h", "group:core", "3 (2 alone)", "hub"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out.String())
		}
	}
}

func TestCostAndASliceAreTwoQuestions(t *testing.T) {
	w := whyFlags{cost: true, slice: "a", run: "r", change: "main"}
	if _, err := w.mode(); err == nil {
		t.Fatal("--cost with --slice")
	}

	w.slice = ""
	if kept, err := w.mode(); err != nil || !kept {
		t.Fatalf("--cost of a kept plan: %v %v", kept, err)
	}
}
