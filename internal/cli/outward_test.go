package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/deps"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/plugincache"
	"github.com/sideways-works/pipelinegen/internal/plugins"
	"github.com/sideways-works/pipelinegen/internal/recording"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

func TestWalkingSelectsWhatBindingSelects(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	r.Write("gspecs/BUILD.bazel", "filegroup(name = \"one\", srcs = [\"one.spec.ts\"], data = [\"//texts:texts\"])\n")
	r.Write("gspecs/one.spec.ts", "test('one')\n")
	r.Commit("graph specs")

	pdir := pluginsDirFor(t)
	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	cfg.Narrowing = 3
	cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}
	cfg.Suites["gspecs"] = config.Suite{Tests: []string{"gspecs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindGraph}
	cfg.Plugins = []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt"}},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
	}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a", "t:k"},
		"dspecs/specs/orders.spec.ts":   {"t:b", "t:k"},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	plan := func(base, head string, walking bool) map[string]string {
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

		for _, w := range p.Warnings {
			if strings.Contains(w, "can't be told") {
				t.Errorf("walking %v: %s", walking, w)
			}
		}

		out := map[string]string{}
		for _, sp := range p.Suites {
			out[sp.Suite] = selectedNames(sp)
		}

		return out
	}

	files := []string{"texts/a.txt", "texts/b.txt", "texts/m.txt", "texts/c.txt", "texts/s.txt", "gspecs/one.spec.ts", "web/app.ts"}
	for _, file := range files {
		base := r.Git("rev-parse", "HEAD")

		r.Write(file, readFile(t, filepath.Join(r.Dir, file))+"changed\n")
		head := r.Commit(file)

		bound, walked := plan(base, head, false), plan(base, head, true)

		for _, suite := range []string{"dspecs", "gspecs"} {
			if bound[suite] != walked[suite] {
				t.Errorf("%s: %s bound selects %q, walked %q", file, suite, bound[suite], walked[suite])
			}
		}

		if hit := strings.Contains(walked["dspecs"], "specs/checkout.spec.ts"); file == "web/app.ts" && !hit || file == "texts/c.txt" && hit {
			t.Errorf("%s: dspecs selects %q", file, walked["dspecs"])
		}
	}

	base := r.Git("rev-parse", "HEAD")
	head := r.Commit("nothing")

	for _, walking := range []bool{false, true} {
		for suite, got := range plan(base, head, walking) {
			if got != "" {
				t.Errorf("an empty commit: %s selects %q (walking %v)", suite, got, walking)
			}
		}
	}

	base = head

	r.Write("specs/orders.spec.ts", readFile(t, filepath.Join(r.Dir, "specs/orders.spec.ts"))+"test('more')\n")
	head = r.Commit("orders grows")

	bound, walked := plan(base, head, false), plan(base, head, true)
	t.Logf("own file changed: bound %v, walked %v", bound, walked)

	if want := "specs/fresh.spec.ts,specs/orders.spec.ts"; walked["dspecs"] != want || bound["dspecs"] != want {
		t.Errorf("its own file changed: bound selects %q, walked %q, want %s", bound["dspecs"], walked["dspecs"], want)
	}
}

func TestAnUntestedFileATestReadsIsNamed(t *testing.T) {
	cfg := &config.Config{Untested: []config.Untested{{Paths: []string{"docs/**", "fixtures/*.json"}, Reason: "prose"}}}
	tree := &deps.Tree{Units: map[string]deps.Unit{
		"go:docs/example#func:Run": {ID: "go:docs/example#func:Run", File: "docs/example/run.go"},
		"go:api#func:Serve":        {ID: "go:api#func:Serve", File: "api/serve.go"},
	}}

	w := &walkTrees{head: "h", trees: map[string]walkTree{"h": {tree: tree, slices: []plugins.BindSlice{
		{Suite: "unit", Name: "a_test.go", HasList: true, List: []string{"go:docs/example#func:Run", "go:api#func:Serve"}},
		{Suite: "unit", Name: "b_test.go", HasList: true, List: []string{"go:docs/example#func:Run"}},
		{Suite: "e2e", Name: "c.spec.ts", Units: []string{"file:fixtures/orders.json"}},
		{Suite: "e2e", Name: "d.spec.ts", HasList: true, List: []string{"file:web/app.ts"}},
	}}}}

	want := []string{
		"docs/example/run.go is listed as untested (prose) but slice unit/a_test.go and 1 other slices reads it: remove it from untested",
		"fixtures/orders.json is listed as untested (prose) but slice e2e/c.spec.ts reads it: remove it from untested",
	}

	if got := w.untestedRead(cfg); !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}

	if got := (*walkTrees)(nil).untestedRead(cfg); got != nil {
		t.Fatalf("no walk: %q", got)
	}
}

func TestAWalkedTreeMarksACorrelatorWithoutLinks(t *testing.T) {
	set := &plugins.Set{Plugins: []*plugins.Loaded{
		{Conf: config.Plugin{Name: "old-go"}, Desc: plugin.Description{Kind: plugin.KindCorrelator, Version: "v0.1.6", Capabilities: []string{plugin.CallBind, plugin.CallObserve}}},
		{Conf: config.Plugin{Name: "new-go"}, Desc: plugin.Description{Kind: plugin.KindCorrelator, Capabilities: []string{plugin.CallBind, plugin.CallLinks}}},
	}}

	tree := &deps.Tree{}
	unwalkable(tree, set)

	if _, ok := tree.Untold["old-go"]; !ok || len(tree.Untold) != 1 {
		t.Fatalf("untold %v", tree.Untold)
	}

	ws := noLinksWarnings(planning{set: set, walk: &walkTrees{}})
	if len(ws) != 1 || !strings.Contains(ws[0], "old-go v0.1.6 offers no links") || !strings.Contains(ws[0], "pin a release of old-go that offers links") {
		t.Fatalf("warnings %q", ws)
	}
}
