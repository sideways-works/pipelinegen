package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldiff"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestANewTestFileRuns(t *testing.T) {
	for _, c := range []struct {
		name string
		// build is the new file's directory's BUILD before the change; added, when set, replaces it.
		build, added string
		listed       bool
	}{
		{name: "glob", build: globBuild},
		{name: "glob-listed", build: globBuild, listed: true},
		{name: "line-added", build: oldBuild, added: oldBuild + newLine},
		{name: "line-added-listed", build: oldBuild, added: oldBuild + newLine, listed: true},
		{name: "line-before", build: oldBuild + newLine},
		{name: "line-before-listed", build: oldBuild + newLine, listed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			testrepo.Parallel(t)

			for _, suite := range []string{"nspecs", "ngraph"} {
				got := newTestPlan(t, suite, c.build, c.added, c.listed)
				if got != suite+"/added.spec.ts" {
					t.Errorf("%s: a new test file selects %q, want %s/added.spec.ts", suite, got, suite)
				}
			}
		})
	}
}

const (
	globBuild = `[filegroup(name = f[:-len(".spec.ts")], srcs = [f]) for f in glob(["*.spec.ts"])]
`
	oldBuild = `filegroup(name = "old", srcs = ["old.spec.ts"], data = ["//texts:texts"])
`
	newLine = `filegroup(name = "added", srcs = ["added.spec.ts"], data = ["//texts:texts"])
`
)

func newTestPlan(t *testing.T, suite, build, added string, listed bool) string {
	t.Helper()

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write(suite+"/BUILD.bazel", build)
	r.Write(suite+"/old.spec.ts", "test('old')\n")
	base := r.Commit("old spec")

	if added != "" {
		r.Write(suite+"/BUILD.bazel", added)
	}

	r.Write(suite+"/added.spec.ts", "test('added')\n")
	head := r.Commit("added spec")

	lists := recording.Lists{suite + "/" + suite + "/old.spec.ts": {"t:a"}}
	if listed {
		lists[suite+"/"+suite+"/added.spec.ts"] = []recording.ID{"t:b"}
	}

	return planSuite(t, r.Dir, suite, base, head, lists, differ)
}

func planSuite(t *testing.T, dir, suite, base, head string, lists recording.Lists, differ bazeldiff.Tool) string {
	t.Helper()

	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfg, err := config.Load(dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Narrowing = 3

	bind := config.BindRecorded
	if suite == "ngraph" {
		bind = config.BindGraph
	}

	cfg.Suites[suite] = config.Suite{Tests: []string{suite + "/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: bind}
	cfg.Plugins = []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt"}},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	set, err := plugins.Load(t.Context(), plugins.Options{
		Config: cfg, Dir: pluginsDirFor(t), Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	book := newPlanBook(set)

	p, err := affected.Compute(t.Context(), affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: dir}, Cache: cache, Base: base, Head: head, Differ: differ,
		Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, nil),
		Follow: followFor(cfg, ""), Narrowed: book.narrowedHook(), Sample: sampleRule(cfg, set),
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.All {
		t.Fatalf("everything: %s", p.Reason)
	}

	for _, sp := range p.Suites {
		if sp.Suite == suite {
			t.Logf("%s: selected %q, reasons %v", suite, selectedNames(sp), sp.Reasons)

			return selectedNames(sp)
		}
	}

	t.Fatalf("no suite %s in the plan", suite)

	return ""
}

func TestAChangedOwnTestFileRunsItsSlice(t *testing.T) {
	for _, listed := range []bool{true, false} {
		t.Run(map[bool]string{true: "listed", false: "unlisted"}[listed], func(t *testing.T) {
			testrepo.Parallel(t)

			got := ownChangePlan(t, listed)
			if got != "nspecs/old.spec.ts" {
				t.Errorf("a changed own test file selects %q, want nspecs/old.spec.ts", got)
			}
		})
	}
}

func ownChangePlan(t *testing.T, listed bool) string {
	t.Helper()

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("nspecs/BUILD.bazel", oldBuild)
	r.Write("nspecs/old.spec.ts", "test('old')\n")
	base := r.Commit("old spec")

	r.Write("nspecs/old.spec.ts", "test('old')\ntest('more')\n")
	head := r.Commit("old spec grows")

	lists := recording.Lists{}
	if listed {
		lists["nspecs/nspecs/old.spec.ts"] = []recording.ID{"t:a"}
	}

	return planSuite(t, r.Dir, "nspecs", base, head, lists, differ)
}
