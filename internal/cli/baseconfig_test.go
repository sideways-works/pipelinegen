package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
	"github.com/sideways-works/pipelinegen/plugin"
)

// specsRepo is the fixture with the specs suite in the main run, two more specs, and a config whose
// edits alone run nothing whole.
func specsRepo(t *testing.T) (*testrepo.Repo, string) {
	t.Helper()

	r := testrepo.New(t)

	data, err := os.ReadFile(filepath.Join(r.Dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	cfg := strings.Replace(string(data), "  - pipelinegen.yaml\n", "", 1)
	cfg = strings.Replace(cfg, "    compare: last_passed\n    select: affected\n    suites: {e2e: {}, backend: {}}\n",
		"    compare: last_passed\n    select: affected\n    suites: {e2e: {}, backend: {}, specs: {}}\n", 1)

	if !strings.Contains(cfg, "specs: {}}") {
		t.Fatal("the fixture's main run has no suites to add specs to")
	}

	r.Write("e2e/identity.spec.ts", "test('identity')\n")
	r.Write("e2e/sandbox.spec.ts", "test('sandbox')\n")

	return r, cfg
}

const specsTests = "    tests: [\"e2e/*.spec.ts\"]\n"

func withExclude(cfg string, excluded ...string) string {
	return strings.Replace(cfg, specsTests, specsTests+"    exclude: [\""+strings.Join(excluded, "\", \"")+"\"]\n", 1)
}

// planBoth plans base to head as the main run, trusted or as a proposed change.
func planBoth(t *testing.T, r *testrepo.Repo, base, head string, trusted bool) *affected.Plan {
	t.Helper()

	differ := bazeldifftest.Tool(t)

	cfg, err := config.Load(filepath.Join(r.Dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")
	bazelOf := func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c }

	p, err := withBaseConfig(t.Context(), cfg, r.Dir, "main", base, head, trusted, func(c *config.Config, _ string, _ *config.Meaning) (*affected.Plan, error) {
		return affected.Compute(t.Context(), affected.Options{
			Config: c, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ, Bazel: bazelOf,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	return p
}

func TestATrustedRunDoesNotRunWhatItsConfigExcludes(t *testing.T) {
	testrepo.Parallel(t)

	r, cfg := specsRepo(t)

	r.Write(config.FileName, withExclude(cfg, "e2e/sandbox.spec.ts"))
	base := r.Commit("the base excludes one spec")

	r.Write(config.FileName, withExclude(cfg, "e2e/sandbox.spec.ts", "e2e/identity.spec.ts"))
	r.Commit("exclude another spec")

	data, err := os.ReadFile(filepath.Join(r.Dir, "MODULE.bazel"))
	if err != nil {
		t.Fatal(err)
	}

	r.Write("MODULE.bazel", string(data)+"# bumped\n")
	head := r.Commit("a change that runs everything")

	p := planBoth(t, r, base, head, true)

	if got := selectedIn(p, "specs"); got != "e2e/checkout.spec.ts,e2e/orders.spec.ts" {
		t.Errorf("trusted: specs selected %q", got)
	}

	want := "e2e/identity.spec.ts: the base's configuration had it in specs; this commit's configuration excludes it, so it no longer runs there"
	if !strings.Contains(strings.Join(p.Warnings, "\n"), want) {
		t.Errorf("trusted: no notice of the excluded spec: %q", p.Warnings)
	}

	// a proposed change can't deselect its own tests by excluding them
	p = planBoth(t, r, base, head, false)

	if got := selectedIn(p, "specs"); got != "e2e/checkout.spec.ts,e2e/identity.spec.ts,e2e/orders.spec.ts" {
		t.Errorf("proposed: specs selected %q", got)
	}

	if rs := reasonsIn(p, "specs", "e2e/identity.spec.ts"); len(rs) == 0 || rs[len(rs)-1].Kind != affected.KindBaseConfig {
		t.Errorf("proposed: reasons of the excluded spec %+v", rs)
	}
}

func TestATrustedRunDoesNotBringBackARemovedSuite(t *testing.T) {
	testrepo.Parallel(t)

	r, cfg := specsRepo(t)

	r.Write(config.FileName, cfg)
	base := r.Commit("the base has the specs suite")

	removed := strings.Replace(cfg, "  specs:\n"+specsTests+"    owner: \"//{dir}:{stem}\"\n    run_slice: ci/e2e-slice.yml\n", "", 1)
	removed = strings.Replace(removed, ", specs: {}}", "}", 1)

	if removed == cfg {
		t.Fatal("the fixture's specs suite isn't where the test removes it")
	}

	r.Write(config.FileName, removed)
	r.Write("e2e/checkout.spec.ts", "test('checkout again')\n")
	head := r.Commit("remove the specs suite and change a spec")

	if p := planBoth(t, r, base, head, true); selectedIn(p, "specs") != "no suite" {
		t.Errorf("trusted: specs selected %q", selectedIn(p, "specs"))
	}

	p := planBoth(t, r, base, head, false)
	if got := selectedIn(p, "specs"); !strings.Contains(got, "e2e/checkout.spec.ts") {
		t.Errorf("proposed: specs selected %q", got)
	}

	if rs := reasonsIn(p, "specs", "e2e/checkout.spec.ts"); len(rs) == 0 || rs[len(rs)-1].Kind != affected.KindBaseConfig {
		t.Errorf("proposed: reasons %+v", rs)
	}
}

func reasonsIn(p *affected.Plan, suite, name string) []affected.Reason {
	for _, sp := range p.Suites {
		if sp.Suite == suite {
			return sp.Reasons[bazel.SelectorLabel(name)]
		}
	}

	return nil
}

func TestATrustedRunDoesNotEmitAWholeSuiteCheckItsConfigRemoved(t *testing.T) {
	testrepo.Parallel(t)

	r, cfg := specsRepo(t)

	r.Write(config.FileName, cfg)
	base := r.Commit("the base has the reconcile check")

	check := "    whole_suite:\n      - name: reconcile\n        task: ci/reconcile.yml\n        also_on: [\"//api/money:money\"]\n"
	if !strings.Contains(cfg, check) {
		t.Fatal("the fixture's e2e suite has no reconcile check")
	}

	r.Write(config.FileName, strings.Replace(cfg, check, "", 1))

	data, err := os.ReadFile(filepath.Join(r.Dir, "MODULE.bazel"))
	if err != nil {
		t.Fatal(err)
	}

	r.Write("MODULE.bazel", string(data)+"# bumped\n")
	head := r.Commit("remove the check, in a change that runs everything")

	headCfg, err := config.Load(filepath.Join(r.Dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	jobs := func(p *affected.Plan) []string {
		t.Helper()

		bare := t.TempDir()
		gitT(t, bare, "init", "-q", "--bare")

		out := t.TempDir()
		em := &emitter{dir: filepath.Join(out, "emit"), pools: filepath.Join(out, "pools"), cfg: headCfg, store: &gitstore.Store{Remote: "file://" + bare}, root: r.Dir}

		emitted, err := em.build(t.Context(), p, false)
		if err != nil {
			t.Fatal(err)
		}

		var e plugin.Emission
		if err := json.Unmarshal(emitted.data, &e); err != nil {
			t.Fatal(err)
		}

		var names []string
		for _, j := range e.Jobs {
			names = append(names, j.Name)
		}

		return names
	}

	p := planBoth(t, r, base, head, true)
	if got := jobs(p); slices.Contains(got, "e2e-whole-reconcile") {
		t.Errorf("trusted: emitted %v", got)
	}

	want := "whole-suite check reconcile: the base's configuration had it in e2e; this commit's configuration doesn't, so it no longer runs there"
	if !slices.Contains(p.Warnings, want) {
		t.Errorf("trusted: no notice of the removed check: %q", p.Warnings)
	}

	// a proposed change can't drop a check by removing it
	if got := jobs(planBoth(t, r, base, head, false)); !slices.Contains(got, "e2e-whole-reconcile") {
		t.Errorf("proposed: emitted %v", got)
	}
}
