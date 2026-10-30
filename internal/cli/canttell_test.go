package cli

import (
	"os"
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

func TestAPluginThatCantTellNeverNarrows(t *testing.T) {
	testrepo.Parallel(t)

	differ := bazeldifftest.Tool(t)
	r := pluginsRepo(t)
	pdir := pluginsDirFor(t)

	conf := readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml"))
	r.Write("pipelinegen.yaml", strings.Replace(conf, "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v1, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: v1, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
suites:
`, 1))
	r.Commit("plugins")

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	cfg.EverythingOn = nil
	cfg.Narrowing = 3
	cfg.Suites["dspecs"] = config.Suite{Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a"},
		"dspecs/specs/orders.spec.ts":   {"t:b"},
		"dspecs/specs/fresh.spec.ts":    {},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	for _, c := range []struct {
		name, cantTell, change string
		want                   []string
	}{
		{"every plugin tells", "", "texts/b.txt", []string{"specs/checkout.spec.ts", "specs/orders.spec.ts"}},
		{"the sbom can't tell", "texts", "texts/b.txt", []string{"specs/checkout.spec.ts", "specs/orders.spec.ts"}},
		{"every plugin tells, a used text", "", "texts/n.txt", []string{"specs/checkout.spec.ts"}},
		{"the correlator can't tell", "arrows", "texts/n.txt", []string{"specs/checkout.spec.ts"}},
	} {
		for _, name := range []string{"texts", "arrows"} {
			if name == c.cantTell {
				r.Write("cant-tell/"+name, "")
			} else if err := os.Remove(filepath.Join(r.Dir, "cant-tell", name)); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}

		base := r.Commit(c.name + ": base")

		r.Write(c.change, readFile(t, filepath.Join(r.Dir, c.change))+"changed\n")
		head := r.Commit(c.name)

		// nothing cached from another case's trees.
		set, err := plugins.Load(t.Context(), plugins.Options{
			Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: t.TempDir()}, Work: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		book := newPlanBook(set)

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, nil),
			Follow: followFor(cfg, ""), Narrowed: book.narrowedHook(), Why: book.whyHook(),
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All {
			t.Fatalf("%s: everything: %s", c.name, p.Reason)
		}

		for _, sp := range p.Suites {
			if sp.Suite != "dspecs" {
				continue
			}

			got := selectedNames(sp)
			for _, want := range c.want {
				if !strings.Contains(","+got+",", ","+want+",") {
					t.Errorf("%s: %s selected %q, want %s among them", c.name, sp.Suite, got, want)
				}
			}

			if c.cantTell == "" && got != strings.Join(c.want, ",") {
				t.Errorf("%s: %s selected %q, want exactly %v", c.name, sp.Suite, got, c.want)
			}
		}
	}
}
