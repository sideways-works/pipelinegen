package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/sideways-works/pipelinegen/plugin"
)

func fakePages(describe func(plugin.Description) plugin.Handler) map[string]plugin.Handler {
	return map[string]plugin.Handler{
		plugin.CallDescribe: describe(plugin.Description{
			Kind: plugin.KindSBOM, Name: "pages", Namespaces: []string{"page"},
			Capabilities: []string{plugin.CallDetect, plugin.CallUnits},
			Candidates:   []string{"pages/*.md"},
			Inputs:       map[string]string{plugin.CallDetect: plugin.PerFile, plugin.CallUnits: plugin.PerFile},
			Narrowings:   []plugin.Narrowing{{Name: "page-parts", OffAt: 3, Note: "a page binds the sections read"}},
		}),
		plugin.CallDetect: func(in []byte) (any, error) {
			var req plugin.DetectRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.DetectAnswer
			for _, p := range req.Paths {
				a.Claims = append(a.Claims, plugin.Claim{Path: p, Role: "source", ByUnits: "page-parts"})
			}

			return a, nil
		},
		plugin.CallUnits: func(in []byte) (any, error) {
			var req plugin.TreeRequest
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, fmt.Errorf("request: %w", err)
			}

			var a plugin.UnitsAnswer

			for _, c := range req.Claimed {
				data, err := os.ReadFile(filepath.Join(req.Tree, c.Path))
				if err != nil {
					return nil, fmt.Errorf("units: %w", err)
				}

				for _, part := range strings.Split(string(data), "## ")[1:] {
					head, _, _ := strings.Cut(part, "\n")
					sum := sha256.Sum256([]byte(part))
					a.Units = append(a.Units, plugin.Unit{ID: "page:" + c.Path + "#" + head, File: c.Path, Group: c.Path, Digest: hex.EncodeToString(sum[:8])})
				}
			}

			return a, nil
		},
	}
}

func byUnitsRepo(t *testing.T) *testrepo.Repo {
	t.Helper()

	r := testrepo.New(t)
	r.Write("pages/BUILD.bazel", "filegroup(name = \"pages\", srcs = [\"guide.md\"], visibility = [\"//visibility:public\"])\n")
	r.Write("pages/guide.md", "## Start\nPress start.\n## Stop\nPress stop.\n")
	r.Write("texts/BUILD.bazel", "filegroup(name = \"texts\", srcs = glob([\"*.txt\"]))\n")
	r.Write("texts/a.txt", "a\n")
	r.Write("browser/BUILD.bazel", "filegroup(name = \"a\", srcs = [\"a.spec.ts\"])\nfilegroup(name = \"b\", srcs = [\"b.spec.ts\"])\n")
	r.Write("browser/a.spec.ts", "test('a')\n")
	r.Write("browser/b.spec.ts", "test('b')\n")
	r.Commit("a browser suite that reads a page")

	return r
}

func byUnitsConfig(t *testing.T, r *testrepo.Repo) *config.Config {
	t.Helper()

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	release := &config.Release{Version: "v0", SHA256: strings.Repeat("a", 64)}

	cfg.Narrowing = 2
	cfg.EverythingOn = nil
	cfg.Suites = map[string]config.Suite{
		"browser": {
			Tests: []string{"browser/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded,
			ExtraDeps: []config.ExtraDeps{{Tests: []string{"browser/*.spec.ts"}, Deps: []string{"//pages:guide.md"}}},
		},
	}
	cfg.Plugins = []config.Plugin{
		{Name: "texts", Kind: config.KindSBOM, Repository: "o/texts", Release: release, Include: []string{"texts/*.txt"}},
		{Name: "pages", Kind: config.KindSBOM, Repository: "o/pages", Release: release},
		{Name: "arrows", Kind: config.KindCorrelator, Repository: "o/arrows", Release: release, Include: []string{"texts/*.txt"}},
	}

	return cfg
}

func planByUnits(t *testing.T, r *testrepo.Repo, cfg *config.Config, lists recording.Lists, base, head string) *affected.Plan {
	t.Helper()

	cache, pdir := t.TempDir(), pluginsDirFor(t)
	bz := testrepo.Bazel(t, filepath.Join(cache, "tree"))
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	set, err := plugins.Load(t.Context(), plugins.Options{
		Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: filepath.Join(cache, "answers")}, Work: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	book := newPlanBook(set)
	walk := walkTreesFor(gitx.Repo{Dir: r.Dir}, base, head, set, narrowingLevel(cfg))

	p, err := affected.Compute(t.Context(), affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: bazeldifftest.Tool(t),
		Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, walk),
		Follow: followFor(cfg, ""), Narrowed: narrowedHook(book, walk, set), Why: book.whyHook(), Reasons: book.reasonsHook(),
		Sample: sampleRule(cfg, set), Select: walk.selection(), Inputs: inputsHook(book, walk), Through: throughHook(book, walk), Unobservable: unobservableHook(walk), Declared: book.declaredHook(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.All {
		t.Fatalf("everything: %s", p.Reason)
	}

	rep := set.Report()
	p.Plugins = &rep

	return p
}

func TestAPageNoCorrelatorRecordsStaysWhole(t *testing.T) {
	testrepo.Parallel(t)

	r := byUnitsRepo(t)
	cfg := byUnitsConfig(t, r)

	base := r.Git("rev-parse", "HEAD")
	r.Write("pages/guide.md", "## Start\nPress start twice.\n## Stop\nPress stop.\n")
	head := r.Commit("the guide's start")

	scripts := recording.Lists{"browser/browser/a.spec.ts": {"t:a"}, "browser/browser/b.spec.ts": {"t:a"}}

	p := planByUnits(t, r, cfg, scripts, base, head)
	if got := browserNames(p); got != "browser/a.spec.ts,browser/b.spec.ts" {
		t.Errorf("a changed section selects %q, want both specs", got)
	}

	warned := 0

	for _, w := range p.Plugins.Warnings {
		if strings.HasPrefix(w, "by-units-unobserved: suite browser: ") && strings.Contains(w, "page-parts") {
			warned++
		}
	}

	if warned != 1 {
		t.Errorf("warned %d times: %q", warned, p.Plugins.Warnings)
	}

	sections := recording.Lists{
		"browser/browser/a.spec.ts": {"t:a", "page:pages/guide.md#Start"},
		"browser/browser/b.spec.ts": {"t:a", "page:pages/guide.md#Stop"},
	}

	p = planByUnits(t, r, cfg, sections, base, head)
	if got := browserNames(p); got != "browser/a.spec.ts" {
		t.Errorf("recorded sections: a changed section selects %q, want the spec that read it", got)
	}
}

func browserNames(p *affected.Plan) string {
	var names []string

	for _, sp := range p.Suites {
		if sp.Suite != "browser" {
			continue
		}

		for _, sl := range sp.Selected {
			names = append(names, sl.Name)
		}
	}

	return strings.Join(names, ",")
}
