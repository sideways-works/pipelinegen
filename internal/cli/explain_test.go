package cli

import (
	"context"
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

type explainCounter struct {
	affected.Differ

	n int
}

func (e *explainCounter) Explain(ctx context.Context, base, head, edges, target string) ([]affected.RootCause, error) {
	e.n++

	return e.Differ.Explain(ctx, base, head, edges, target)
}

func TestAPlanOfRecordedSlicesAsksBazelDiffToExplainNothing(t *testing.T) {
	testrepo.Parallel(t)

	r := pluginsRepo(t)
	pdir := pluginsDirFor(t)

	conf := readFile(t, filepath.Join(r.Dir, "pipelinegen.yaml"))
	r.Write("pipelinegen.yaml", strings.Replace(conf, "\nsuites:\n", `
plugins:
  - {name: texts, kind: sbom, repository: o/texts, release: {version: v1, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
  - {name: arrows, kind: correlator, repository: o/arrows, release: {version: v1, sha256: `+strings.Repeat("a", 64)+`}, include: ["texts/*.txt"]}
suites:
`, 1))
	base := r.Commit("plugins")

	r.Write("texts/b.txt", readFile(t, filepath.Join(r.Dir, "texts/b.txt"))+"changed\n")
	head := r.Commit("b changed")

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	cfg.EverythingOn = nil
	cfg.Suites = map[string]config.Suite{"dspecs": {Tests: []string{"specs/*.spec.ts"}, Owner: "//{dir}:{stem}", RunSlice: "ci/e2e-slice.yml", Bind: config.BindRecorded}}

	lists := recording.Lists{
		"dspecs/specs/checkout.spec.ts": {"t:a"},
		"dspecs/specs/orders.spec.ts":   {"t:b"},
		"dspecs/specs/fresh.spec.ts":    {},
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	var selections []string

	for _, limit := range []int{0, config.DefaultExplainLimit, -1} {
		set, err := plugins.Load(t.Context(), plugins.Options{
			Config: cfg, Dir: pdir, Cache: cache, Answers: &plugincache.Cache{Dir: t.TempDir()}, Work: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}

		book := newPlanBook(set)
		differ := &explainCounter{Differ: bazeldifftest.Tool(t)}

		p, err := affected.Compute(t.Context(), affected.Options{
			Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
			Bazel: bazelOf, Prepare: pluginsPrepare(cfg, lists, nil, bazelOf, set, depsWriter(), book, nil),
			Follow: followFor(cfg, ""), Narrowed: book.narrowedHook(), Why: book.whyHook(),
			Reasons: book.reasonsHook(), ExplainLimit: limit,
		})
		if err != nil {
			t.Fatal(err)
		}

		if p.All || len(p.Suites) != 1 || len(p.Suites[0].Selected) == 0 {
			t.Fatalf("limit %d: plan %+v", limit, p)
		}

		if differ.n != 0 {
			t.Errorf("limit %d: bazel-diff explained %d recorded slices", limit, differ.n)
		}

		selections = append(selections, selectedNames(p.Suites[0]))
	}

	if selections[0] != selections[1] || selections[0] != selections[2] {
		t.Fatalf("the selection moved with the limit: %q", selections)
	}
}
