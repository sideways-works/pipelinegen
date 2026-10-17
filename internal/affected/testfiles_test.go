package affected_test

import (
	"context"
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
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func stub(cfg *config.Config) func(context.Context, string, string, map[string][]bazel.Slice) (string, error) {
	return func(_ context.Context, tree, _ string, tests map[string][]bazel.Slice) (string, error) {
		dir := filepath.Join(tree, ".pipelinegen", "deps", "slices")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return "", fmt.Errorf("stub: %w", err)
		}

		var b strings.Builder

		for suite, slices := range tests {
			for _, sl := range slices {
				file := sl.Sources[0]
				pkg, name := filepath.Split(file)
				owner, _ := cfg.Suites[suite].OwnerOf(file)

				// both widths, as the deps repository's writers do
				for _, target := range []string{sl.Name, sl.Name + ".dependents"} {
					b.WriteString("filegroup(\n    name = \"" + target + "\",\n")
					b.WriteString("    srcs = [\"@@//" + strings.TrimSuffix(pkg, "/") + ":" + name + "\", \"@@" + owner + "\"],\n)\n\n")
				}
			}
		}

		if err := os.WriteFile(filepath.Join(dir, "BUILD.bazel"), []byte(b.String()), 0o600); err != nil {
			return "", fmt.Errorf("stub: %w", err)
		}

		if err := os.WriteFile(filepath.Join(tree, ".pipelinegen", "deps", "REPO.bazel"), nil, 0o600); err != nil {
			return "", fmt.Errorf("stub: %w", err)
		}

		return "stub", nil
	}
}

func TestTestFilesAreSlices(t *testing.T) {
	differ := bazeldifftest.Tool(t)
	r := testrepo.New(t)

	cfg, err := config.Load(r.Dir + "/pipelinegen.yaml")
	if err != nil {
		t.Fatal(err)
	}

	base := r.Git("rev-parse", "HEAD")

	r.Write("e2e/checkout.spec.ts", "test('pays')\n")
	head := r.Commit("checkout spec changed")

	cache := t.TempDir()
	bz := testrepo.Bazel(t, cache+"/tree")

	p, err := affected.Compute(t.Context(), affected.Options{
		Config: cfg, Repo: gitx.Repo{Dir: r.Dir}, Cache: cache, Base: base, Head: head, Differ: differ,
		Bazel:   func(dir string) *bazel.Client { c := *bz; c.Dir = dir; return &c },
		Prepare: stub(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}

	if p.All {
		t.Fatalf("everything: %s", p.Reason)
	}

	for _, sp := range p.Suites {
		if sp.Suite != "specs" {
			continue
		}

		names := []string{}
		for _, sl := range sp.Selected {
			names = append(names, sl.Name)
		}

		if got := strings.Join(names, ","); got != "e2e/checkout.spec.ts" {
			t.Fatalf("selected %q (untouched %v)", got, sp.Untouched)
		}

		if got := strings.Join(sp.Untouched, ","); got != bazel.SelectorLabel("e2e/orders.spec.ts") {
			t.Fatalf("untouched %q", got)
		}

		if rs := sp.Reasons[bazel.SelectorLabel("e2e/checkout.spec.ts")]; len(rs) == 0 || rs[0].Kind != affected.KindTestChanged {
			t.Fatalf("reasons %+v", rs)
		}

		return
	}

	t.Fatal("no specs suite in the plan")
}

func TestATestFileBazelIgnoresIsNoSlice(t *testing.T) {
	r := testrepo.New(t)

	ignore, err := os.ReadFile(filepath.Join(r.Dir, ".bazelignore"))
	if err != nil {
		t.Fatal(err)
	}

	r.Write(".bazelignore", string(ignore)+"examples/repo\n")
	r.Write("examples/repo/money_test.go", "package money\n\nimport \"testing\"\n\nfunc TestPays(t *testing.T) {}\n")
	r.Write("examples/other/money_test.go", "package money\n\nimport \"testing\"\n\nfunc TestPays(t *testing.T) {}\n")
	r.Commit("examples")

	got, err := affected.TestFileSlices(t.Context(), r.Dir, config.Suite{Tests: []string{"examples/**/*_test.go"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Name != "examples/other/money_test.go" {
		t.Fatalf("got %+v", got)
	}
}
