package affected

import (
	"context"
	"fmt"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
)

type countingDiffer struct {
	fakeDiffer

	explained int
}

func (c *countingDiffer) Explain(ctx context.Context, base, head, edges, target string) ([]RootCause, error) {
	c.explained++

	return c.fakeDiffer.Explain(ctx, base, head, edges, target)
}

func TestReasonsAskBazelDiffOnlyWhenTheBindingsCantTell(t *testing.T) {
	differ := &countingDiffer{}
	differ.causes = map[string][]RootCause{
		"//e2e:graph1": {{Label: "//lib:lib"}}, "//e2e:graph2": {{Label: "//lib:lib"}}, "//e2e:graph3": {{Label: "//lib:lib"}},
	}

	var warned []string

	o, done := withExplainBudget(Options{
		Differ: differ, ExplainLimit: 2,
		Reasons: func(_, _, target string) ([]Reason, bool) {
			if target == "@pipelinegen_deps//slices:e2e/a.spec.ts" {
				return []Reason{{Kind: KindExecuted, What: "t:a"}}, true
			}

			return nil, false
		},
		Warn: func(format string, _ ...any) { warned = append(warned, format) },
	})

	b, h := &Snapshot{Commit: "b"}, &Snapshot{Commit: "h"}
	before := map[string]map[string]bool{}
	reasonsOf := map[string][]Reason{}

	for _, label := range []string{"@pipelinegen_deps//slices:e2e/a.spec.ts", "//e2e:graph1", "//e2e:graph2", "//e2e:graph3"} {
		before[label] = map[string]bool{}
		reasonsOf[label] = sliceReasons(t.Context(), o, b, h, bazel.Slice{Label: label}, label, before, map[string]bool{})
	}

	done()

	if differ.explained != 2 {
		t.Fatalf("bazel-diff explained %d slices, want 2", differ.explained)
	}

	if r := reasonsOf["@pipelinegen_deps//slices:e2e/a.spec.ts"]; len(r) != 1 || r[0].What != "t:a" {
		t.Errorf("a recorded slice: %+v", r)
	}

	if r := reasonsOf["//e2e:graph3"]; len(r) != 1 || r[0].Kind != KindTarget || len(r[0].Through) != 1 || r[0].Through[0] != unexplained {
		t.Errorf("past the limit: %+v", r)
	}

	if len(warned) != 1 {
		t.Errorf("warned %q", warned)
	}

	differ.explained = 0
	o, done = withExplainBudget(Options{Differ: differ})

	for _, label := range []string{"//e2e:graph1", "//e2e:graph2", "//e2e:graph3"} {
		sliceReasons(t.Context(), o, b, h, bazel.Slice{Label: label}, label, before, map[string]bool{})
	}

	done()

	if differ.explained != 3 {
		t.Fatalf("without a limit bazel-diff explained %d of 3", differ.explained)
	}
}

func TestSlicesSelectedThroughOneLabelShareItsExplanation(t *testing.T) {
	differ := &countingDiffer{}
	differ.causes = map[string][]RootCause{
		"//runner:runner": {{Label: "//lib:lib", Path: []string{"//lib:lib", "//runner:runner"}}},
		"//msgs:de":       {{Label: "//msgs:de", Path: []string{"//msgs:de"}}},
	}

	o, done := withExplainBudget(Options{Differ: differ, ExplainLimit: 1})
	defer done()

	b, h := &Snapshot{Commit: "b"}, &Snapshot{Commit: "h"}

	for i := range 5 {
		rs := o.explainInputs(t.Context(), b, h, []Reason{{Kind: KindTarget, What: "//runner:runner"}})
		if len(rs) != 1 || rs[0].What != "//lib:lib" || len(rs[0].Through) != 2 {
			t.Fatalf("slice %d: %+v", i, rs)
		}
	}

	if differ.explained != 1 {
		t.Fatalf("bazel-diff explained %d times, want once", differ.explained)
	}

	rs := o.explainInputs(t.Context(), b, h, []Reason{{Kind: KindTarget, What: "//msgs:de"}})
	if len(rs) != 1 || rs[0].What != "//msgs:de" || len(rs[0].Through) != 1 || rs[0].Through[0] != unexplained {
		t.Fatalf("past the limit: %+v", rs)
	}

	o, done2 := withExplainBudget(Options{Differ: differ})
	defer done2()

	rs = o.explainInputs(t.Context(), b, h, []Reason{{Kind: KindTarget, What: "//msgs:de"}})
	if len(rs) != 1 || rs[0].What != "//msgs:de" || len(rs[0].Through) != 0 {
		t.Fatalf("its own cause: %+v", rs)
	}
}

func TestCausesGroupWhatSlicesShare(t *testing.T) {
	label := func(n string) string { return "//b:" + n }
	runner := Reason{Kind: KindTarget, What: "//runner:reporter.js", Through: []string{"//runner:runner"}}
	en := Reason{Kind: KindTarget, What: "//msgs:en.json", Through: []string{"//msgs:en"}}

	sp := SuitePlan{Reasons: map[string][]Reason{
		label("a"): {runner, en, {Kind: KindTestChanged, What: "b/a.spec.ts"}, {Kind: KindDepends, What: "t:x", Through: []string{"t:y"}}},
		label("b"): {runner, en, {Kind: KindTestChanged, What: "b/a.spec.ts"}, {Kind: KindDepends, What: "t:x", Through: []string{"t:z"}}},
		label("c"): {runner, {Kind: KindTarget, What: "//msgs:de.json", Through: []string{"//msgs:de"}}},
	}}

	for _, n := range []string{"a", "b", "c"} {
		sp.Selected = append(sp.Selected, bazel.Slice{Label: label(n)})
	}

	got := causesOf(sp)
	want := []string{
		"3: runner/reporter.js changed, through //runner:runner",
		"2: t:x changed",
		"2: msgs/en.json changed, through //msgs:en",
	}

	var texts []string
	for _, c := range got {
		texts = append(texts, fmt.Sprintf("%d: %s", c.Slices, c.Text()))
	}

	if fmt.Sprint(texts) != fmt.Sprint(want) {
		t.Fatalf("causes %q, want %q", texts, want)
	}

	if !got[1].Of(Reason{Kind: KindDepends, What: "t:x", Through: []string{"t:q"}}) || got[0].Of(Reason{Kind: KindTarget, What: "//runner:reporter.js"}) {
		t.Fatal("Of matches the wrong reasons")
	}
}
