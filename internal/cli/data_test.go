package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func dataRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)
	r.Write("rules/defs.bzl", `def _impl(ctx):
    pass

fake_test = rule(_impl, test = True, attrs = {
    "srcs": attr.label_list(allow_files = True),
    "data": attr.label_list(allow_files = True),
})
`)
	r.Write("rules/BUILD.bazel", "")
	r.Write("specs/BUILD.bazel", `filegroup(name = "checkout", srcs = ["checkout.spec.ts"], data = ["fixtures.json"])
filegroup(name = "orders", srcs = ["orders.spec.ts"])
`)
	r.Write("specs/fixtures.json", "{}\n")
	r.Write("specs/checkout.spec.ts", "// pipelinegen:data //web\ntest('checkout')\n")
	r.Write("specs/orders.spec.ts", "test('orders')\n")
	r.Write("gotests/BUILD.bazel", `load("//rules:defs.bzl", "fake_test")

fake_test(name = "access_test", srcs = ["access_test.go", "matrix_test.go"], data = ["//api/money"])
`)
	r.Write("gotests/access_test.go", "package gotests\n\nfunc TestAccess(t *testing.T) {}\n")
	r.Write("gotests/matrix_test.go", "// pipelinegen:data //api/orders\npackage gotests\n\nfunc TestMatrix(t *testing.T) {}\n")
	r.Commit("data")

	return r
}

func TestSliceDataFromEverySource(t *testing.T) {
	testrepo.Parallel(t)

	r := dataRepo(t)
	bz := testrepo.Bazel(t, r.Dir)

	specs := config.Suite{
		Bind:  config.BindRecorded,
		Owner: "//{dir}:{stem}",
		ExtraDeps: []config.ExtraDeps{
			{Tests: []string{"specs/orders.spec.ts"}, Deps: []string{"//api/orders", "api:createOrder"}},
		},
	}

	got, err := sliceData(t.Context(), bz, r.Dir, "s", specs, []string{"specs/checkout.spec.ts", "specs/orders.spec.ts"})
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]string{
		"specs/checkout.spec.ts": {"//specs:fixtures.json", "//web"},
		"specs/orders.spec.ts":   {"//api/orders"},
	}
	expectData(t, got, want)

	recorded := config.Suite{Bind: config.BindRecorded}

	got, err = sliceData(t.Context(), bz, r.Dir, "s", recorded, []string{"gotests/access_test.go", "gotests/matrix_test.go"})
	if err != nil {
		t.Fatal(err)
	}

	expectData(t, got, map[string][]string{
		"gotests/access_test.go": {"//api/money:money"},
		"gotests/matrix_test.go": {"//api/money:money", "//api/orders"},
	})

	got, err = sliceData(t.Context(), bz, r.Dir, "s", config.Suite{Bind: config.BindGraph}, []string{"gotests/access_test.go"})
	if err != nil {
		t.Fatal(err)
	}

	expectData(t, got, map[string][]string{"gotests/access_test.go": {"//gotests:access_test"}})

	missing := config.Suite{Bind: config.BindRecorded, Owner: "//{dir}:nope_{stem}"}

	got, err = sliceData(t.Context(), bz, r.Dir, "s", missing, []string{"gotests/access_test.go"})
	if err != nil {
		t.Fatalf("a missing owner failed planning: %v", err)
	}

	expectData(t, got, map[string][]string{"gotests/access_test.go": {"//api/money:money"}})
}

func expectData(t *testing.T, got, want map[string][]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("data %v, want %v", got, want)
	}

	for f, w := range want {
		if !slices.Equal(got[f], w) {
			t.Errorf("%s: %v, want %v", f, got[f], w)
		}
	}
}

func TestCommentDataReadsOnlyTheTop(t *testing.T) {
	r := testrepo.New(t)

	body := "# pipelinegen:data //a:b\n" + strings.Repeat("\n", config.DataLines)

	r.Write("x.sh", body+"# pipelinegen:data //late:c\n")

	if got := commentData(r.Dir + "/x.sh"); !slices.Equal(got, []string{"//a:b"}) {
		t.Fatalf("comment data %v", got)
	}
}

func TestDataEdgesSelect(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := dataRepo(t)

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}
	cfg.Suites["dgo"] = config.Suite{Tests: []string{"gotests/*_test.go"}, RunSlice: "ci/backend-slice.yml", Bind: config.BindRecorded}

	lists := recording.Lists{"dgo/gotests/access_test.go": {}, "dgo/gotests/matrix_test.go": {}}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	cases := []struct {
		name, file, suite, want string
	}{
		{"owner data", "specs/fixtures.json", "dspecs", "specs/checkout.spec.ts"},
		{"comment", "api/orders/orders.go", "dgo", "gotests/matrix_test.go"},
		{"owner data of a test rule", "api/money/money.go", "dgo", "gotests/access_test.go,gotests/matrix_test.go"},
	}

	for _, c := range cases {
		base := r.Git("rev-parse", "HEAD")
		r.Write(c.file, "// changed "+c.name+"\n")
		head := r.Commit(c.name)

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: noPlugins(t, cfg, lists, bazelOf),
		})
		if err != nil {
			t.Fatal(err)
		}

		if got := selectedIn(p, c.suite); got != c.want {
			t.Errorf("%s: %s selected %q, want %q", c.name, c.suite, got, c.want)
		}
	}
}

func noPlugins(t *testing.T, cfg *config.Config, l recording.Lists, bz func(string) *bazel.Client) func(context.Context, string, string, map[string][]bazel.Slice) (string, error) {
	t.Helper()

	set, err := plugins.Load(t.Context(), plugins.Options{Config: cfg, Dir: t.TempDir(), Cache: t.TempDir(), Answers: &plugincache.Cache{Dir: t.TempDir()}, Work: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	return pluginsPrepare(cfg, l, nil, bz, set, depsWriter(), newPlanBook(set), nil)
}

func selectedIn(p *affected.Plan, suite string) string {
	for _, sp := range p.Suites {
		if sp.Suite == suite {
			names := []string{}
			for _, sl := range sp.Selected {
				names = append(names, sl.Name)
			}

			return strings.Join(names, ",")
		}
	}

	return "no suite"
}

func TestAChangeCannotDeselectItself(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := dataRepo(t)

	suite := "\n  dgo:\n    tests: [\"gotests/*_test.go\"]\n    run_slice: ci/backend-slice.yml\n"
	extra := "    extra_deps: [{tests: [gotests/access_test.go], deps: [\"//web\"]}]\n"

	data, err := os.ReadFile(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	plain := strings.Replace(string(data), "  - pipelinegen.yaml\n", "", 1)
	withSuite := strings.Replace(plain, "\nruns:", suite+extra+"runs:", 1)

	if withSuite == plain {
		t.Fatal("the fixture's config has no runs: section to put the suite before")
	}

	r.Write("pipelinegen.yaml", withSuite)
	r.Write("BUILD.bazel", "filegroup(name = \"config\", srcs = [\"pipelinegen.yaml\"])\n")
	base := r.Commit("dgo reads web")

	r.Write("pipelinegen.yaml", strings.Replace(withSuite, extra, "", 1))
	r.Write("web/app.ts", "// changed\n")
	head := r.Commit("drop the edge and change web")

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	// Plan without the check, which the fixture's loose files would turn into a run of everything.
	p, err := withBaseConfig(t.Context(), cfg, r.Dir, "", base, head, false, func(c *config.Config, _ string, _ *config.Meaning) (*affected.Plan, error) {
		return affected.Compute(t.Context(), affected.Options{
			Config: c, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: noPlugins(t, c, recording.Lists{"dgo/gotests/access_test.go": {}, "dgo/gotests/matrix_test.go": {}}, bazelOf),
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := selectedIn(p, "dgo"); got != "gotests/access_test.go" {
		t.Fatalf("dgo selected %q: %s", got, p.Reason)
	}

	for _, sp := range p.Suites {
		if sp.Suite != "dgo" {
			continue
		}

		rs := sp.Reasons[bazel.SelectorLabel("gotests/access_test.go")]
		if len(rs) == 0 || rs[len(rs)-1].Kind != affected.KindBaseConfig {
			t.Fatalf("reasons %+v", rs)
		}
	}
}

func TestABaseConfigIsHistory(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := dataRepo(t)

	data, err := os.ReadFile(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	plain := strings.Replace(string(data), "  - pipelinegen.yaml\n", "", 1)
	suite := "\n  dgo:\n    tests: [\"gotests/*_test.go\"]\n    run_slice: ci/backend-slice.yml\n"
	extra := "    extra_deps: [{tests: [gotests/access_test.go], deps: [\"//web\"]}]\n"
	history := "    params: {TOKEN: ((gh))}\n    from_the_future: 1\n"
	head := strings.Replace(plain, "\nruns:", suite+"runs:", 1)
	// The history keys sit on another suite: a suite whose params change runs whole, which would hide the
	// edge under test.
	old := strings.Replace(plain, "\nruns:", suite+extra+"runs:", 1)
	old = strings.Replace(old, "    parallel: 2\n", "    parallel: 2\n"+history, 1)
	old = strings.Replace(old, "concourse:\n", "concourse:\n  vars: [github_token]\n", 1)

	r.Write("BUILD.bazel", "filegroup(name = \"config\", srcs = [\"pipelinegen.yaml\"])\n")

	plan := func(base string) (*affected.Plan, string) {
		t.Helper()

		r.Write("pipelinegen.yaml", head)
		r.Write("web/app.ts", "// changed at "+base+"\n")
		h := r.Commit("the head")

		cache := t.TempDir()
		testrepo.Bazel(t, cache+"/tree")

		stdout, stderr, code := runIn(t, r.Dir, nil, "affected", "-root", r.Dir, "-base", base, "-head", h, "-cache", cache, "-bazel-diff", differ.Path, "-no-lists", "-json")
		if code != 0 {
			t.Fatalf("affected: %d %s", code, stderr)
		}

		var p affected.Plan
		if err := json.Unmarshal([]byte(stdout), &p); err != nil {
			t.Fatal(err)
		}

		return &p, stderr
	}

	r.Write("pipelinegen.yaml", old)
	p, _ := plan(r.Commit("an older config"))

	if got := selectedIn(p, "dgo"); got != "gotests/access_test.go" {
		t.Errorf("dgo selected %q (%s): the base's edge must still count", got, p.Reason)
	}

	warned := strings.Join(p.Warnings, "\n")
	for _, w := range []string{"concourse.vars", "from_the_future", "TOKEN"} {
		if !strings.Contains(warned, w) {
			t.Errorf("no warning of %s: %v", w, p.Warnings)
		}
	}

	r.Write("pipelinegen.yaml", strings.Replace(old, "    tests: [\"gotests/*_test.go\"]\n", "", 1))
	p, _ = plan(r.Commit("a config that can't say its tests"))

	if !p.All || !strings.Contains(p.Reason, "the base's configuration can't be read") {
		t.Errorf("an unreadable base: all %v, %q", p.All, p.Reason)
	}
}

func TestAShortOwnerLabelIsItsPackagesTarget(t *testing.T) {
	testrepo.Parallel(t)

	r := testrepo.New(t)
	r.Write("app/BUILD.bazel", `filegroup(name = "app", srcs = ["main.txt"])
`)
	r.Write("app/main.txt", "app\n")
	r.Write("e2e/run_test.sh", "#!/bin/sh\n")
	r.Commit("short owner")

	bz := testrepo.Bazel(t, r.Dir)

	for _, owner := range []string{"//app", "//app:app"} {
		s := config.Suite{Bind: config.BindGraph, Owner: owner}

		got, err := sliceData(t.Context(), bz, r.Dir, "e2e", s, []string{"e2e/run_test.sh"})
		if err != nil {
			t.Fatalf("owner %s: %v", owner, err)
		}

		expectData(t, got, map[string][]string{"e2e/run_test.sh": {"//app:app"}})
	}
}
