package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestASliceWithoutAListRunsWhenAnOwnTestFileWithoutUnitsChanges(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("specs/checkout.spec.ts", "test('checkout')\n")
	r.Write("docs/BUILD.bazel", "filegroup(name = \"docs\", srcs = [\"README.md\"])\n")
	r.Write("docs/README.md", "the fixture\n")
	r.Commit("readme")

	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Narrowing = 3
	cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}
	cfg.Plugins = []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt"}},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
	}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a", "t:k"},
		"dspecs/specs/orders.spec.ts":   {"t:b", "t:k"},
	}

	pdir := pluginsDirFor(t)
	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	plan := func(base, head string, walking bool) string {
		t.Helper()

		set, err := plugins.Load(t.Context(), plugins.Options{
			Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		book := newPlanBook(set)

		var walk *walkTrees
		if walking {
			walk = walkTreesFor(gitx.Repo{Dir: r.Dir}, base, head, set, narrowingLevel(cfg))
		}

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, walk),
			Follow: followFor(cfg, ""), Narrowed: narrowedHook(book, walk, set), Sample: sampleRule(cfg, set),
			Expect: book.expectHook(), Select: walk.selection(), Inputs: inputsHook(book, walk),
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("everything: %s", p.Reason)
		}

		for _, sp := range p.Suites {
			if sp.Suite == "dspecs" {
				return selectedNames(sp)
			}
		}

		return ""
	}

	for _, c := range []struct {
		file, want string
	}{
		{"specs/orders.spec.ts", "specs/fresh.spec.ts,specs/orders.spec.ts"},
		{"docs/README.md", ""},
		{"specs/fixtures.json", "specs/checkout.spec.ts"},
	} {
		base := r.Git("rev-parse", "HEAD")

		r.Write(c.file, readFile(t, filepath.Join(r.Dir, c.file))+"changed\n")
		head := r.Commit(c.file)

		for _, walking := range []bool{false, true} {
			if got := plan(base, head, walking); got != c.want {
				t.Errorf("%s, walked %v: selects %q, want %q", c.file, walking, got, c.want)
			}
		}
	}
}
