package piperun

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestASuiteBecomesItsPreparePoolChecksAndEnd(t *testing.T) {
	em, err := Emit([]Suite{
		{Name: "e2e", Prepare: true, Whole: []string{"money"}, Slices: []Slice{
			{Name: "a", Seconds: 10, Measured: true}, {Name: "b", Seconds: 90, Measured: true}, {Name: "c"},
		}},
		{Name: "unit", Slices: []Slice{{Name: "x"}}},
		{Name: "lint"},
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := Encode(em)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"jobs":[` +
		`{"template":"e2e-prepare","name":"e2e-prepare","params":{}},` +
		`{"template":"e2e-whole-money","name":"e2e-whole-money","params":{},"after":["e2e-slices"]},` +
		`{"template":"e2e-end","name":"e2e-end","params":{},"after":[{"job":"e2e-prepare","ended":true},{"job":"e2e-slices","ended":true},{"job":"e2e-whole-money","ended":true}]},` +
		`{"template":"unit-end","name":"unit-end","params":{},"after":[{"job":"unit-slices","ended":true}]},` +
		`{"template":"lint-end","name":"lint-end","params":{}}],` +
		`"pools":[` +
		`{"template":"e2e-slices","name":"e2e-slices","items":["b","c","a"],"after":["e2e-prepare"]},` +
		`{"template":"unit-slices","name":"unit-slices","items":["x"]}]}`
	if string(data) != want {
		t.Fatalf("emission:\n%s\nwant\n%s", data, want)
	}

	var back plugin.Emission
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}

	if end := back.Jobs[2].After; !end[0].Ended || end[0].Job != "e2e-prepare" || back.Jobs[1].After[0].Ended {
		t.Fatalf("afters read back: %+v", back.Jobs)
	}
}

func TestChecksWithoutSlicesWaitForThePrepareAndNoPool(t *testing.T) {
	em, err := Emit([]Suite{{Name: "e2e", Prepare: true, Whole: []string{"money"}}})
	if err != nil {
		t.Fatal(err)
	}

	if len(em.Pools) != 0 || len(em.Jobs) != 3 || em.Jobs[1].After[0] != (plugin.EmitAfter{Job: "e2e-prepare"}) {
		t.Fatalf("%+v", em)
	}

	if names := []string{em.Jobs[2].After[0].Job, em.Jobs[2].After[1].Job}; !slices.Equal(names, []string{"e2e-prepare", "e2e-whole-money"}) {
		t.Fatalf("the end waits for %q", names)
	}
}

func TestUnmeasuredSlicesGoAtTheMedian(t *testing.T) {
	got := Longest([]Slice{{Name: "new"}, {Name: "a", Seconds: 5, Measured: true}, {Name: "b", Seconds: 30, Measured: true}, {Name: "c", Seconds: 60, Measured: true}})
	if !slices.Equal(got, []string{"c", "b", "new", "a"}) {
		t.Fatalf("%q", got)
	}

	if got := Longest([]Slice{{Name: "z"}, {Name: "y"}}); !slices.Equal(got, []string{"y", "z"}) {
		t.Fatalf("none measured: %q", got)
	}
}

func TestNamesPiperunWouldRefuseAreRefused(t *testing.T) {
	for _, s := range []Suite{
		{Name: "e2e", Whole: []string{"a/b"}},
		{Name: "e2e", Whole: []string{"x", "x"}},
	} {
		if _, err := Emit([]Suite{s}); !errors.Is(err, errName) {
			t.Errorf("%+v: %v", s, err)
		}
	}

	if _, err := Emit([]Suite{{Name: "a", Whole: []string{"end"}}, {Name: "a-whole"}}); !errors.Is(err, errName) || !strings.Contains(err.Error(), `"a-whole-end" names two instances`) {
		t.Fatalf("two suites' names meeting: %v", err)
	}
}

func items(n int, width int) []Slice {
	out := make([]Slice, n)
	for i := range out {
		out[i] = Slice{Name: fmt.Sprintf("%0*d", width, i)}
	}

	return out
}

func TestEachLimitIsRefusedByName(t *testing.T) {
	em, err := Emit([]Suite{{Name: "s", Slices: items(plugin.EmitMaxPoolItems, 1)}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Encode(em); err != nil {
		t.Fatalf("at the item limit: %v", err)
	}

	em, _ = Emit([]Suite{{Name: "s", Slices: items(plugin.EmitMaxPoolItems+1, 1)}}) //nolint:errcheck // checked above
	if _, err := Encode(em); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "pool s-slices holds 2001 slices, more than the 2000") {
		t.Fatalf("items: %v", err)
	}

	var suites []Suite
	for i := range plugin.EmitMaxInstances + 1 {
		suites = append(suites, Suite{Name: fmt.Sprintf("s%d", i)})
	}

	em, _ = Emit(suites) //nolint:errcheck // no name repeats
	if _, err := Encode(em); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "501 instances, more than the 500") {
		t.Fatalf("instances: %v", err)
	}

	em, _ = Emit([]Suite{{Name: "s", Slices: items(1000, 60)}}) //nolint:errcheck // no name repeats
	if _, err := Encode(em); !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "more than the 49152 piperun takes in one emission") {
		t.Fatalf("bytes: %v", err)
	}
}

// piperun refuses an emission whose after names a node it doesn't emit.
func TestEveryAfterNamesAnEmittedNode(t *testing.T) {
	for _, s := range []Suite{
		{Name: "a", Slices: []Slice{{Name: "x"}}, Whole: []string{"w"}},
		{Name: "b", Slices: []Slice{{Name: "x"}}},
		{Name: "c", Whole: []string{"w"}},
		{Name: "d", Prepare: true},
		{Name: "e", Prepare: true, Whole: []string{"w"}},
		{Name: "f"},
	} {
		em, err := Emit([]Suite{s})
		if err != nil {
			t.Fatal(err)
		}

		nodes := map[string]bool{}
		for _, j := range em.Jobs {
			nodes[j.Name] = true
		}

		for _, p := range em.Pools {
			nodes[p.Name] = true
		}

		var afters []plugin.EmitAfter
		for _, j := range em.Jobs {
			afters = append(afters, j.After...)
		}

		for _, p := range em.Pools {
			afters = append(afters, p.After...)
		}

		for _, a := range afters {
			if !nodes[a.Job] {
				t.Errorf("suite %s: after names %s, which isn't emitted: %+v", s.Name, a.Job, em)
			}
		}

		if !s.Prepare && nodes[plugin.PiperunPrepare(s.Name)] {
			t.Errorf("suite %s has no prepare, and one is emitted", s.Name)
		}
	}

	em, err := Emit([]Suite{{Name: "b", Slices: []Slice{{Name: "x"}}}})
	if err != nil {
		t.Fatal(err)
	}

	if end := em.Jobs[len(em.Jobs)-1]; end.Name != "b-end" || len(end.After) != 1 || end.After[0] != (plugin.EmitAfter{Job: "b-slices", Ended: true}) || len(em.Pools[0].After) != 0 {
		t.Fatalf("a suite without prepare: %+v", em)
	}
}
