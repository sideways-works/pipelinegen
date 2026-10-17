package affected

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
)

func TestUnionKeepsWhatTheBaseSelects(t *testing.T) {
	a, b, c := bazel.Slice{Label: "//a"}, bazel.Slice{Label: "//b"}, bazel.Slice{Label: "//c"}

	own := &Plan{Suites: []SuitePlan{
		{Suite: "s", Selected: []bazel.Slice{a}, Untouched: []string{"//b", "//c"}, Reasons: map[string][]Reason{"//a": {{Kind: KindTarget}}}},
	}}
	byBase := &Plan{Suites: []SuitePlan{
		{Suite: "s", Selected: []bazel.Slice{a, b}, Untouched: []string{"//c"}, WholeSuite: []string{"reconcile"}, Removed: []string{"//gone"}},
		{Suite: "old", Selected: []bazel.Slice{c}},
	}}

	got := Union(own, byBase)

	s := got.Suites[0]
	if len(s.Selected) != 2 || s.Selected[1].Label != "//b" || strings.Join(s.Untouched, ",") != "//c" {
		t.Fatalf("suite s: %+v", s)
	}

	if rs := s.Reasons["//b"]; len(rs) != 1 || rs[0].Kind != KindBaseConfig {
		t.Fatalf("reasons of //b: %+v", rs)
	}

	if rs := s.Reasons["//a"]; len(rs) != 1 || rs[0].Kind != KindTarget {
		t.Fatalf("reasons of //a: %+v", rs)
	}

	if strings.Join(s.WholeSuite, ",") != "reconcile" || strings.Join(s.Removed, ",") != "//gone" {
		t.Fatalf("whole suite %v, removed %v", s.WholeSuite, s.Removed)
	}

	if len(got.Suites) != 2 || got.Suites[1].Suite != "old" || !strings.Contains(got.Suites[1].Reason, "base's configuration") {
		t.Fatalf("removed suite: %+v", got.Suites)
	}

	if len(own.Suites[0].Selected) != 1 || len(own.Suites) != 1 {
		t.Fatal("Union changed its argument")
	}
}
