package affected

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
)

func TestOnlyAFileModifiedInPlaceChangesNothingObservable(t *testing.T) {
	rule := UnobservableRule{OffAt: 4, Files: []Unobservable{
		{Path: "r/a.js", Plugin: "ts", Units: 2}, {Path: "r/b.js", Plugin: "ts", Units: 1}, {Path: "r/c.js", Plugin: "ts", Units: 1},
	}}

	q := newQuiet(rule, []gitx.Change{{Path: "r/a.js", Status: "M"}, {Path: "r/b.js", Status: "A"}, {Path: "r/c.js", Status: "T"}, {Path: "r/d.js", Status: "D"}})
	if q == nil {
		t.Fatal("no rule")
	}

	if got := fmt.Sprint(q.list()); got != "[{r/a.js ts 2}]" {
		t.Errorf("files %s", got)
	}

	if got := fmt.Sprint(q.changed); got != "[r/a.js r/b.js r/c.js r/d.js]" {
		t.Errorf("changed %s", got)
	}

	if newQuiet(rule, []gitx.Change{{Path: "r/b.js", Status: "D"}}) != nil {
		t.Error("a rule without a file modified in place")
	}

	pure := newQuiet(rule, []gitx.Change{{Path: "r/a.js", Status: "M"}})

	impacted, err := pure.impactedWithout(t.Context(), Options{}, []string{"r/a.js"})
	if err != nil || len(impacted) != 0 {
		t.Errorf("a change of nothing but the file: %v %v", impacted, err)
	}

	if _, err := q.impactedWithout(t.Context(), Options{}, []string{"r/a.js"}); !errors.Is(err, errNoDiffer) {
		t.Errorf("mixed without bazel-diff: %v", err)
	}
}

func TestDeclaredTextReaders(t *testing.T) {
	q := newQuiet(UnobservableRule{Files: []Unobservable{{Path: "docs/a.md"}, {Path: "src/x.go"}}},
		[]gitx.Change{{Path: "docs/a.md", Status: "M"}, {Path: "src/x.go", Status: "M"}})

	s := config.Suite{TextInputs: []config.TextInputs{
		{Tests: []string{"check/docs_test.go"}, Paths: []string{"src/**"}},
		{Paths: []string{"docs/*.md"}},
	}}

	if got := fmt.Sprint(q.textRead(s, "check/docs_test.go")); got != "[docs/a.md src/x.go]" {
		t.Errorf("the docs check reads %s", got)
	}

	if got := fmt.Sprint(q.textRead(s, "other_test.go")); got != "[docs/a.md]" {
		t.Errorf("another test reads %s", got)
	}
}
