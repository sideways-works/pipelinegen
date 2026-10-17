package affected_test

import (
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func e2eBuild(r *testrepo.Repo, slices map[string][]string) {
	var b strings.Builder

	for _, name := range []string{"checkout", "orders", "search", "api"} {
		files, ok := slices[name]
		if !ok {
			continue
		}

		b.WriteString("filegroup(\n    name = \"" + name + "\",\n    srcs = [\n")

		for _, f := range files {
			b.WriteString("        \"" + f + "\",\n")
		}

		b.WriteString("        \"//web\",\n    ],\n    tags = [\"slice-e2e\"],\n)\n\n")
	}

	r.Write("e2e/BUILD.bazel", b.String())
}

func TestTestsAddedOrRemovedRunTheirSlice(t *testing.T) {
	differ := bazeldifftest.Tool(t)
	r := testrepo.New(t)

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")

	r.Write("e2e/orders2.spec.ts", "test\n")
	r.Write("e2e/api.tests", "TestOrders\nTestRefunds\n")
	e2eBuild(r, map[string][]string{
		"checkout": {"checkout.spec.ts"},
		"orders":   {"orders.spec.ts", "orders2.spec.ts"},
		"api":      {"api.tests"},
	})
	base := r.Commit("base")

	plan := func(head string) map[string]affected.SuitePlan {
		t.Helper()

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c },
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("everything: %s", p.Reason)
		}

		out := map[string]affected.SuitePlan{}
		for _, sp := range p.Suites {
			out[sp.Suite] = sp
		}

		return out
	}

	change := func(name string, edit func()) string {
		t.Helper()

		r.Git("checkout", "-q", "-B", name, base)
		edit()

		return r.Commit(name)
	}

	type want struct {
		selected string
		reasons  map[string]string // slice -> "kind what; kind what"
		removed  string
	}

	cases := []struct {
		name string
		edit func()
		want want
	}{
		{"a test file added to a slice", func() {
			r.Write("e2e/checkout2.spec.ts", "test\n")
			e2eBuild(r, map[string][]string{"checkout": {"checkout.spec.ts", "checkout2.spec.ts"}, "orders": {"orders.spec.ts", "orders2.spec.ts"}, "api": {"api.tests"}})
		}, want{"//e2e:checkout", map[string]string{"//e2e:checkout": "test-added e2e/checkout2.spec.ts"}, ""}},

		{"a test file removed from a slice", func() {
			r.Git("rm", "-q", "e2e/orders2.spec.ts")
			e2eBuild(r, map[string][]string{"checkout": {"checkout.spec.ts"}, "orders": {"orders.spec.ts"}, "api": {"api.tests"}})
		}, want{"//e2e:orders", map[string]string{"//e2e:orders": "test-removed e2e/orders2.spec.ts"}, ""}},

		{"a test added inside a file", func() {
			r.Write("e2e/orders2.spec.ts", "test\ntest('another')\n")
		}, want{"//e2e:orders", map[string]string{"//e2e:orders": "test-changed e2e/orders2.spec.ts"}, ""}},

		{"a file moved between slices", func() {
			e2eBuild(r, map[string][]string{"checkout": {"checkout.spec.ts", "orders2.spec.ts"}, "orders": {"orders.spec.ts"}, "api": {"api.tests"}})
		}, want{"//e2e:checkout,//e2e:orders", map[string]string{
			"//e2e:checkout": "test-added e2e/orders2.spec.ts",
			"//e2e:orders":   "test-removed e2e/orders2.spec.ts",
		}, ""}},

		{"a new slice", func() {
			r.Write("e2e/search.spec.ts", "test\n")
			e2eBuild(r, map[string][]string{"checkout": {"checkout.spec.ts"}, "orders": {"orders.spec.ts", "orders2.spec.ts"}, "search": {"search.spec.ts"}, "api": {"api.tests"}})
		}, want{"//e2e:search", map[string]string{"//e2e:search": "new-slice //e2e:search"}, ""}},

		{"a slice whose tests are all gone", func() {
			r.Git("rm", "-q", "e2e/orders.spec.ts", "e2e/orders2.spec.ts")
			e2eBuild(r, map[string][]string{"checkout": {"checkout.spec.ts"}, "api": {"api.tests"}})
		}, want{"", map[string]string{}, "//e2e:orders"}},

		{"a test added to a by-name list", func() {
			r.Write("e2e/api.tests", "TestOrders\nTestRefunds\nTestPayouts\n")
		}, want{"//e2e:api", map[string]string{"//e2e:api": "test-changed e2e/api.tests"}, ""}},
	}

	for _, c := range cases {
		e2e := plan(change(strings.ReplaceAll(c.name, " ", "-"), c.edit))["e2e"]

		labels := []string{}
		for _, sl := range e2e.Selected {
			labels = append(labels, sl.Label)
		}

		if got := strings.Join(labels, ","); got != c.want.selected {
			t.Errorf("%s: selected %q, want %q", c.name, got, c.want.selected)
		}

		for slice, want := range c.want.reasons {
			parts := []string{}

			for _, rs := range e2e.Reasons[slice] {
				if rs.Kind != affected.KindTarget {
					parts = append(parts, rs.Kind+" "+rs.What)
				}
			}

			if got := strings.Join(parts, "; "); got != want {
				t.Errorf("%s: %s reasons %q, want %q", c.name, slice, got, want)
			}
		}

		if got := strings.Join(e2e.Removed, ","); got != c.want.removed {
			t.Errorf("%s: removed %q, want %q", c.name, got, c.want.removed)
		}
	}
}
