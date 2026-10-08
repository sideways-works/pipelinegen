package depsrepo

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

var stubGroup = regexp.MustCompile(`filegroup\(name = "([^"]+)"`)

func TestStubBindsEveryNamedSelector(t *testing.T) {
	root := t.TempDir()

	for p, s := range map[string]string{
		"shop/BUILD.bazel":           `filegroup(name = "a", data = ["@pipelinegen_deps//slices:shop.a"])`,
		"shop/slices.bzl":            `x = "@pipelinegen_deps//slices:shop.b.dependents"`,
		"node_modules/x/BUILD.bazel": `"@pipelinegen_deps//slices:never"`,
	} {
		writeFileIn(t, root, p, s)
	}

	if err := Stub(root); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(root, Dir, slicesDir, "BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, m := range stubGroup.FindAllStringSubmatch(string(b), -1) {
		names = append(names, m[1])
	}

	if want := []string{"shop.a", "shop.a.dependents", "shop.b", "shop.b.dependents"}; !slices.Equal(names, want) {
		t.Errorf("stub selectors %v, want %v", names, want)
	}

	if _, err := ReadKey(root); err == nil {
		t.Error("the stub left a key a plan would trust")
	}
}

func TestBazelLoadsAFreshCloneThroughTheStub(t *testing.T) {
	dir := workspace(t)
	writeFileIn(t, dir, "e2e/BUILD.bazel", `filegroup(name = "checkout", srcs = ["checkout.spec.ts"], data = ["@pipelinegen_deps//slices:e2e/checkout.spec.ts"])
`)
	writeFileIn(t, dir, "e2e/checkout.spec.ts", "test('checkout')\n")

	if err := Stub(dir); err != nil {
		t.Fatal(err)
	}

	out, err := testrepo.Bazel(t, dir).Run(t.Context(), "query", "deps(//e2e:checkout)")
	if err != nil {
		t.Fatalf("bazel query on a fresh clone: %v", err)
	}

	if !strings.Contains(string(out), "@pipelinegen_deps//slices:stub") {
		t.Fatalf("the slice doesn't reach the stub:\n%s", out)
	}
}

func writeFileIn(t *testing.T, root, p, s string) {
	t.Helper()

	full := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(full, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
