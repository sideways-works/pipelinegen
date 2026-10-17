package affected_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestSampleOnRunsASample(t *testing.T) {
	differ := bazeldifftest.Tool(t)
	r := testrepo.New(t)

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.EverythingOn = slices.DeleteFunc(cfg.EverythingOn, func(p string) bool { return p == "ci/**" })
	cfg.EverythingOn = append(cfg.EverythingOn, "ci/reconcile.yml")
	cfg.SampleOn = []string{"ci/**"}

	e2e := cfg.Suites["e2e"]
	e2e.Sample = 1
	e2e.SampleOn = []string{"docs/**"}
	cfg.Suites["e2e"] = e2e

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")

	r.Write("e2e/api.tests", "TestOrders\n")
	e2eBuild(r, map[string][]string{
		"checkout": {"checkout.spec.ts"},
		"orders":   {"orders.spec.ts"},
		"api":      {"api.tests"},
	})
	base := r.Commit("base")

	plan := func(head string) *affected.Plan {
		t.Helper()

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c },
		})
		if err != nil {
			t.Fatal(err)
		}

		return p
	}

	summary := func(p *affected.Plan) string {
		for _, sp := range p.Suites {
			if sp.Suite != "e2e" {
				continue
			}

			var b strings.Builder

			fmt.Fprintf(&b, "%d run", len(sp.Selected))

			for _, n := range sp.Narrowed {
				fmt.Fprintf(&b, "; %s %s %d of %d: %s", n.Kind, n.Narrowing, len(n.Dropped), n.Reached, n.Reason)
			}

			return b.String()
		}

		return "no e2e"
	}

	cases := []struct {
		name string
		edit func()
		want string
	}{
		{"a task file", func() {
			r.Write("ci/e2e-slice.yml", "changed\n")
		}, "1 run; sampled sample_on 2 of 3: ci/e2e-slice.yml changed (sample_on ci/**)"},
		{"a suite's own sample_on", func() {
			r.Write("docs/readme.md", "changed\n")
		}, "1 run; sampled sample_on 2 of 3: docs/readme.md changed (sample_on docs/**)"},
		{"a task file and a test", func() {
			r.Write("ci/e2e-slice.yml", "changed\n")
			r.Write("e2e/api.tests", "TestOrders\nTestRefunds\n")
		}, "1 run; sampled sample_on 2 of 3: ci/e2e-slice.yml changed (sample_on ci/**)"},
	}

	for _, c := range cases {
		r.Git("checkout", "-q", "-B", strings.ReplaceAll(c.name, " ", "-"), base)
		c.edit()
		p := plan(r.Commit(c.name))

		if p.All {
			t.Fatalf("%s: everything: %s", c.name, p.Reason)
		}

		if got := summary(p); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}

		if strings.Contains(c.name, "test") {
			for _, sp := range p.Suites {
				if sp.Suite == "e2e" && !slices.ContainsFunc(sp.Selected, func(sl bazel.Slice) bool { return sl.Label == "//e2e:api" }) {
					t.Errorf("%s: the changed test's slice isn't in the sample: %+v", c.name, sp.Selected)
				}
			}
		}
	}

	r.Git("checkout", "-q", "-B", "docs-only", base)
	r.Write("docs/readme.md", "again\n")

	for _, sp := range plan(r.Commit("docs only")).Suites {
		if sp.Suite == "backend" && (len(sp.Narrowed) > 0 || len(sp.Selected) > 0) {
			t.Errorf("backend sampled by e2e's sample_on: %+v", sp)
		}
	}

	r.Git("checkout", "-q", "-B", "reconcile", base)
	r.Write("ci/reconcile.yml", "changed\n")

	if p := plan(r.Commit("reconcile")); !p.All || !strings.Contains(p.Reason, "everything_on") {
		t.Errorf("everything_on: all %v, %s", p.All, p.Reason)
	}
}
