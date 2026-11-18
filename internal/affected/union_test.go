package affected

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/config"
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

func TestUnionTrustedAddsOnlyWhatTheHeadHas(t *testing.T) {
	file := func(f string) bazel.Slice { return bazel.Slice{Name: f, Label: bazel.SelectorLabel(f)} }
	a, b, x, y := file("e2e/a.spec.ts"), file("e2e/b.spec.ts"), file("e2e/x.spec.ts"), file("e2e/y.spec.ts")

	own := &Plan{Warnings: []string{"own"}, Suites: []SuitePlan{
		{Suite: "e2e", Selected: []bazel.Slice{a}, Untouched: []string{b.Label}, slices: []bazel.Slice{a, b}},
	}}
	byBase := &Plan{Suites: []SuitePlan{
		{Suite: "e2e", All: true, Reason: "MODULE.bazel changed", Selected: []bazel.Slice{a, b, x, y}},
		{Suite: "old", Selected: []bazel.Slice{a}},
		{Suite: "idle", Selected: []bazel.Slice{}},
	}}
	head := &config.Config{Suites: map[string]config.Suite{"e2e": {Tests: []string{"e2e/*.spec.ts"}, Exclude: []string{"e2e/x.spec.ts"}}}}

	got := UnionTrusted(own, byBase, head, "")

	if len(got.Suites) != 1 {
		t.Fatalf("suites %+v: a suite the head dropped came back", got.Suites)
	}

	s := got.Suites[0]
	if len(s.Selected) != 2 || s.Selected[1].Label != b.Label || len(s.Untouched) != 0 || !s.All {
		t.Fatalf("e2e: %+v", s)
	}

	if rs := s.Reasons[b.Label]; len(rs) != 1 || rs[0].Kind != KindBaseConfig {
		t.Fatalf("reasons of b: %+v", rs)
	}

	want := []string{
		"own",
		"e2e/x.spec.ts: the base's configuration had it in e2e; this commit's configuration excludes it, so it no longer runs there",
		"e2e/y.spec.ts: the base's configuration had it in e2e; this commit's configuration leaves it out, so it no longer runs there",
		"suite old: the base's configuration had it; this commit's configuration doesn't, so it no longer runs",
	}
	if strings.Join(got.Warnings, "\n") != strings.Join(want, "\n") {
		t.Fatalf("warnings:\n%s", strings.Join(got.Warnings, "\n"))
	}

	if len(own.Warnings) != 1 {
		t.Fatal("UnionTrusted changed its argument")
	}
}
