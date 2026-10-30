package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/depsrepo"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func graphBoundRepo(t *testing.T, deps bool) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)

	if !deps {
		r.Write("MODULE.bazel", "module(name = \"fixture\")\n")
	}

	r.Write("rules/defs.bzl", `def _impl(ctx):
    pass

fake_test = rule(_impl, test = True, attrs = {
    "srcs": attr.label_list(allow_files = True),
    "data": attr.label_list(allow_files = True),
})
`)
	r.Write("rules/BUILD.bazel", "")
	r.Write("texts/BUILD.bazel", "filegroup(name = \"texts\", srcs = glob([\"*.txt\"]))\n")
	r.Write("texts/g.txt", "g\n")
	r.Write("texts/c.txt", "c\n")
	r.Write("lib/BUILD.bazel", "filegroup(name = \"lib\", srcs = [\"lib.txt\"], data = [\"//lib/deep\"])\n")
	r.Write("lib/lib.txt", "lib\n")
	r.Write("lib/deep/BUILD.bazel", "filegroup(name = \"deep\", srcs = [\"deep.txt\"])\n")
	r.Write("lib/deep/deep.txt", "deep\n")
	r.Write("lib2/BUILD.bazel", "filegroup(name = \"lib2\", srcs = [\"lib2.txt\"])\n")
	r.Write("lib2/lib2.txt", "lib2\n")
	r.Write("docsx/BUILD.bazel", "filegroup(name = \"docsx\", srcs = [\"x.txt\"])\n")
	r.Write("docsx/x.txt", "x\n")
	r.Write("extra/BUILD.bazel", "filegroup(name = \"extra\", srcs = [\"e.txt\"])\n")
	r.Write("extra/e.txt", "e\n")
	r.Write("gspecs/BUILD.bazel", gspecsBuild(false))
	r.Write("gspecs/one.spec.ts", "test('one')\n-> t:g\n")
	r.Write("gspecs/two.spec.ts", "test('two')\n")
	r.Write("gspecs/three.spec.ts", "// pipelinegen:data //docsx\ntest('three')\n")
	r.Write("wide/BUILD.bazel", "filegroup(name = \"w\", srcs = [\"w.spec.ts\"], data = [\"//lib\"])\n")
	r.Write("wide/w.spec.ts", "test('w')\n-> t:g\n")
	r.Commit("graph-bound suites")

	return r
}

func gspecsBuild(withExtra bool) string {
	data := `["//lib"]`
	if withExtra {
		data = `["//lib", "//extra"]`
	}

	return `load("//rules:defs.bzl", "fake_test")

filegroup(name = "one", srcs = ["one.spec.ts"], data = ` + data + `)
fake_test(name = "two_runner", srcs = ["two.spec.ts"], data = ["//lib2"])
filegroup(name = "three", srcs = ["three.spec.ts"])
`
}

func graphBoundConfig(t *testing.T, r *testrepo.Repo, withPlugins bool, startup []string) *config.Config {
	t.Helper()

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	cfg.Bazel.Bazelisk, cfg.Bazel.StartupFlags = "bazelisk", startup
	cfg.Suites["gspecs"] = config.Suite{
		Tests: []string{"gspecs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindGraph,
		ExtraDeps: []config.ExtraDeps{{Tests: []string{"gspecs/three.spec.ts"}, Deps: []string{"//extra"}}},
	}
	cfg.Suites["wide"] = config.Suite{
		Tests: []string{"wide/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindGraph,
		Follow: config.FollowDependents,
	}

	if withPlugins {
		release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}
		described := []string{"texts/*.txt", "gspecs/*.spec.ts", "wide/*.spec.ts"}
		cfg.Plugins = []config.Plugin{
			{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: described},
			{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: described},
		}
	}

	return cfg
}

type graphBoundChange struct {
	name                 string
	edit                 func(r *testrepo.Repo)
	plugins, withoutThem map[string]string
	removedFromGSpecs    string
}

func appendTo(path, text string) func(r *testrepo.Repo) {
	return func(r *testrepo.Repo) {
		data, err := os.ReadFile(filepath.Join(r.Dir, path))
		if err != nil {
			r.Write(path, text)

			return
		}

		r.Write(path, string(data)+text)
	}
}

func graphBoundChanges() []graphBoundChange {
	same := func(m map[string]string) (map[string]string, map[string]string) { return m, m }

	var out []graphBoundChange

	add := func(name string, edit func(r *testrepo.Repo), plugins, withoutThem map[string]string) {
		out = append(out, graphBoundChange{name: name, edit: edit, plugins: plugins, withoutThem: withoutThem})
	}

	p, n := same(nil)
	add("a unit nothing names", appendTo("texts/c.txt", "changed\n"), p, n)
	// A graph-bound slice binds its own file's units but follows none of their links.
	add("a unit the specs' own units link to, no owner reaches", appendTo("texts/g.txt", "changed\n"), p, n)

	p, n = same(map[string]string{"gspecs": "gspecs/one.spec.ts", "wide": "wide/w.spec.ts"})
	add("a library two levels under the owners", appendTo("lib/deep/deep.txt", "changed\n"), p, n)

	p, n = same(map[string]string{"gspecs": "gspecs/two.spec.ts"})
	add("the data of the test rule that takes a file without an owner", appendTo("lib2/lib2.txt", "changed\n"), p, n)

	p, n = same(map[string]string{"gspecs": "gspecs/three.spec.ts"})
	add("a pipelinegen:data label", appendTo("docsx/x.txt", "changed\n"), p, n)
	add("an extra_deps label", appendTo("extra/e.txt", "changed\n"), p, n)

	p, n = same(map[string]string{"gspecs": "gspecs/one.spec.ts"})
	add("a label added to an owner's data", func(r *testrepo.Repo) { r.Write("gspecs/BUILD.bazel", gspecsBuild(true)) }, p, n)
	add("the spec itself", appendTo("gspecs/one.spec.ts", "test('more')\n"), p, n)

	p, n = same(map[string]string{"gspecs": "gspecs/one.spec.ts,gspecs/three.spec.ts"})
	add("the extra label, now also one's", appendTo("extra/e.txt", "again\n"), p, n)

	p, n = same(map[string]string{"gspecs": "gspecs/four.spec.ts"})
	add("a new spec", func(r *testrepo.Repo) {
		r.Write("gspecs/BUILD.bazel", gspecsBuild(true)+"filegroup(name = \"four\", srcs = [\"four.spec.ts\"])\n")
		r.Write("gspecs/four.spec.ts", "test('four')\n")
	}, p, n)

	p, n = same(map[string]string{"specs": "e2e/checkout.spec.ts"})
	add("code the fixture's own owners reach", appendTo("api/money/money.go", "// changed\n"), p, n)

	p, n = same(nil)
	add("a spec removed", func(r *testrepo.Repo) {
		r.Git("rm", "-q", "gspecs/three.spec.ts")
		r.Write("gspecs/BUILD.bazel", strings.Replace(gspecsBuild(true), "filegroup(name = \"three\", srcs = [\"three.spec.ts\"])\n", "", 1)+
			"filegroup(name = \"four\", srcs = [\"four.spec.ts\"])\n")
	}, p, n)

	out[len(out)-1].removedFromGSpecs = "gspecs/three.spec.ts"

	p, n = same(nil)
	add("nothing", func(*testrepo.Repo) {}, p, n)

	return out
}

func TestGraphBoundWalkedSelectsWhatSelectorsSelect(t *testing.T) {
	graphBoundEquivalence(t, true, 0, graphBoundHalf)
}

const graphBoundHalf = 7

// graphBoundIdle keeps both Bazel servers up between plans: each waits while the other side plans.
const graphBoundIdle = "--max_idle_secs=900"

var (
	errGraphBoundEverything = errors.New("everything selected")
	errGraphBoundDeps       = errors.New("a walked plan left a deps repository in its work tree")
)

func graphBoundEquivalence(t *testing.T, withPlugins bool, from, to int) {
	t.Helper()

	// The switch is package-wide: no other test may run while it is on.
	before := forward.Load()

	t.Cleanup(func() { forward.Store(before) })

	differ := bazeldifftest.Tool(t).Path
	pdir := pluginsDirFor(t)

	type side struct {
		r     *testrepo.Repo
		cache string
		flags []string
	}

	newSide := func(deps bool) side {
		r := graphBoundRepo(t, deps)
		cache := t.TempDir()

		testrepo.Bazel(t, filepath.Join(cache, "tree"))

		return side{r: r, cache: cache, flags: []string{graphBoundIdle}}
	}

	withLines, without := newSide(true), newSide(false)

	// The caller sets the switch between the selectors and the walk, once for plans that run side by side.
	plan := func(s side, cfg *config.Config, base, head string, walking bool) (*affected.Plan, error) {
		p, err := affectedPlan(t.Context(), cfg, s.r.Dir, base, head, false, s.cache, differ, listsNone, pdir)
		if err != nil {
			return nil, err
		}

		if p.All {
			return nil, fmt.Errorf("%w: %s", errGraphBoundEverything, p.Reason)
		}

		if _, err := os.Stat(filepath.Join(s.cache, "tree", filepath.FromSlash(depsrepo.Dir))); walking && err == nil {
			return nil, errGraphBoundDeps
		}

		return p, nil
	}

	describe := func(p *affected.Plan) map[string]string {
		out := map[string]string{}

		for _, sp := range p.Suites {
			if names := selectedNames(sp); names != "" {
				out[sp.Suite] = names
			}

			var removed []string
			for _, l := range sp.Removed {
				removed = append(removed, strings.TrimPrefix(l, bazel.SelectorLabel("")))
			}

			if len(removed) > 0 {
				out[sp.Suite+" removed"] = strings.Join(removed, ",")
			}
		}

		return out
	}

	changes := graphBoundChanges()

	for i, c := range changes[:to] {
		var bases [2]string

		for j, s := range []side{withLines, without} {
			bases[j] = s.r.Git("rev-parse", "HEAD")
			c.edit(s.r)
			s.r.Git("add", "-A")
			s.r.Git("commit", "-q", "--allow-empty", "-m", c.name)
		}

		if i < from {
			continue
		}

		want := c.withoutThem
		if withPlugins {
			want = c.plugins
		}

		hl, hw := withLines.r.Git("rev-parse", "HEAD"), without.r.Git("rev-parse", "HEAD")
		cl, cw := graphBoundConfig(t, withLines.r, withPlugins, withLines.flags), graphBoundConfig(t, without.r, withPlugins, without.flags)

		forward.Store(true)

		selectors, err := plan(withLines, cl, bases[0], hl, false)
		if err != nil {
			t.Fatalf("%s, selectors: %v", c.name, err)
		}

		forward.Store(false)

		var (
			walked, noDeps *affected.Plan
			errs           [2]error
			wg             sync.WaitGroup
		)

		walkedCfg := graphBoundConfig(t, withLines.r, withPlugins, withLines.flags)

		wg.Go(func() { walked, errs[0] = plan(withLines, walkedCfg, bases[0], hl, true) })
		wg.Go(func() { noDeps, errs[1] = plan(without, cw, bases[1], hw, true) })
		wg.Wait()

		if err := errors.Join(errs[:]...); err != nil {
			t.Fatalf("%s, walked: %v", c.name, err)
		}

		sides := map[string]map[string]string{
			"selectors":              describe(selectors),
			"walked":                 describe(walked),
			"walked, no deps module": describe(noDeps),
		}

		for how, got := range sides {
			for _, suite := range []string{"specs", "gspecs", "wide"} {
				if got[suite] != want[suite] {
					t.Errorf("%s (plugins %v), %s: %s selects %q, want %q", c.name, withPlugins, how, suite, got[suite], want[suite])
				}
			}

			if got["gspecs removed"] != c.removedFromGSpecs {
				t.Errorf("%s (plugins %v), %s: removed %q, want %q", c.name, withPlugins, how, got["gspecs removed"], c.removedFromGSpecs)
			}
		}

		if fmt.Sprint(sides["selectors"]) != fmt.Sprint(sides["walked"]) || fmt.Sprint(sides["walked"]) != fmt.Sprint(sides["walked, no deps module"]) {
			t.Errorf("%s (plugins %v): the ways differ: %v", c.name, withPlugins, sides)
		}
	}
}
