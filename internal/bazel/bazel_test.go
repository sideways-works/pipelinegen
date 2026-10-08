package bazel_test

import (
	"reflect"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestSlicesFromFixture(t *testing.T) {
	r := testrepo.New(t)
	c := testrepo.Bazel(t, r.Dir)

	got, err := c.Slices(t.Context(), "slice-e2e")
	if err != nil {
		t.Fatal(err)
	}

	want := []bazel.Slice{
		{Name: "e2e.checkout", Label: "//e2e:checkout", Tags: []string{"slice-e2e"}, Kind: "filegroup", Sources: []string{"e2e/checkout.spec.ts"}},
		{Name: "e2e.orders", Label: "//e2e:orders", Tags: []string{"slice-e2e"}, Kind: "filegroup", Sources: []string{"e2e/orders.spec.ts"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("slices:\n got %+v\nwant %+v", got, want)
	}

	// manual targets are slices too: a query isn't a build pattern
	got, err = c.Slices(t.Context(), "slice-backend")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Label != "//backendtests:money_test" {
		t.Fatalf("backend slices: %+v", got)
	}

	// a tag is matched whole, never as a prefix
	got, err = c.Slices(t.Context(), "slice")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatalf("prefix matched: %+v", got)
	}
}

func TestLabelPath(t *testing.T) {
	cases := map[string]string{
		"//e2e:checkout.spec.ts": "e2e/checkout.spec.ts",
		"//:MODULE.bazel":        "MODULE.bazel",
		"@@//a/b:c/d.go":         "a/b/c/d.go",
	}
	for label, want := range cases {
		got, ok := bazel.LabelPath(label)
		if !ok || got != want {
			t.Errorf("LabelPath(%q) = %q, %v; want %q", label, got, ok, want)
		}
	}

	for _, label := range []string{"@rules_go//go:def.bzl", "e2e:x", "//nocolon"} {
		if _, ok := bazel.LabelPath(label); ok {
			t.Errorf("LabelPath(%q) has a path", label)
		}
	}
}

func TestTagExprQuotes(t *testing.T) {
	if got := bazel.TagExpr("slice.e2e"); got != `attr(tags, "[\[ ]slice\.e2e[,\]]", //...)` {
		t.Fatal(got)
	}
}
