package affected_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazeldiff"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestInputLabelsMatchImpactedTargets(t *testing.T) {
	testrepo.NeedBazel(t)

	tool := bazeldiff.Tool{Path: os.Getenv("PIPELINEGEN_TEST_BAZEL_DIFF")}
	if tool.Path == "" {
		t.Skip("PIPELINEGEN_TEST_BAZEL_DIFF names no bazel-diff")
	}

	r := testrepo.New(t)
	r.Write("inputs/BUILD.bazel", `filegroup(
    name = "owner",
    srcs = ["a.spec.ts"],
    data = [
        "fixture.json",
        ":gen",
        "//api/orders",
        "@bazel_tools//tools/genrule:genrule-setup.sh",
    ],
)

genrule(
    name = "gen",
    srcs = ["gen.in"],
    outs = ["gen.txt"],
    cmd = "cp $< $@",
)
`)
	r.Write("inputs/a.spec.ts", "test\n")
	r.Write("inputs/fixture.json", "{}\n")
	r.Write("inputs/gen.in", "one\n")
	r.Commit("inputs")

	bz := testrepo.Bazel(t, r.Dir)

	rules, err := bz.Rules(t.Context(), "//inputs:owner")
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules %v: %v", rules, err)
	}

	data := affected.MainLabels(rules[0].Data)
	want := []string{"//api/orders:orders", "//inputs:fixture.json", "//inputs:gen", "@bazel_tools//tools/genrule:genrule-setup.sh"}

	if !slices.Equal(data, want) {
		t.Fatalf("data %v, want %v", data, want)
	}

	out := t.TempDir()
	hash := func(name string) string {
		p := filepath.Join(out, name)
		if err := tool.Hashes(t.Context(), bz, r.Dir, p, p+".edges"); err != nil {
			t.Fatal(err)
		}

		return p
	}

	base := hash("base")

	r.Write("inputs/fixture.json", "{\"a\": 1}\n")
	r.Write("inputs/gen.in", "two\n")
	r.Write("api/orders/orders.go", "package orders // changed\n")

	head := hash("head")

	labels, err := tool.Impacted(t.Context(), bz, base, head)
	if err != nil {
		t.Fatal(err)
	}

	impacted := map[string]bool{}
	for _, l := range affected.MainLabels(labels) {
		impacted[l] = true
	}

	for _, l := range data[:3] {
		if !impacted[l] {
			t.Errorf("%s changed, impacted %v lacks it", l, labels)
		}
	}

	hashed, err := affected.HashedLabels(head)
	if err != nil {
		t.Fatal(err)
	}

	if !hashed["//inputs:fixture.json"] || hashed[data[3]] {
		t.Fatalf("hashed: fixture %v, other repository %v", hashed["//inputs:fixture.json"], hashed[data[3]])
	}
}
