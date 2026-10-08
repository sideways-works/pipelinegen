package gitx_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitx"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestResolveAncestorDiff(t *testing.T) {
	r := testrepo.New(t)
	g := gitx.Repo{Dir: r.Dir}
	ctx := t.Context()

	first := r.Git("rev-parse", "HEAD")

	r.Write("web/app.ts", "export const a = 1\n")
	r.Write("web/new file.ts", "x\n")
	r.Git("rm", "-q", "docs/readme.md")
	r.Git("mv", "e2e/helpers.ts", "e2e/helpers2.ts")
	second := r.Commit("change")

	got, ok, err := g.Resolve(ctx, "HEAD")
	if err != nil || !ok || got != second {
		t.Fatalf("Resolve HEAD: %q %v %v", got, ok, err)
	}

	if _, ok, err := g.Resolve(ctx, "0123456789012345678901234567890123456789"); ok || err != nil {
		t.Fatalf("Resolve unknown: %v %v", ok, err)
	}

	if _, ok, err := g.Resolve(ctx, "--help"); ok || err != nil {
		t.Fatalf("Resolve option-like: %v %v", ok, err)
	}

	if yes, err := g.IsAncestor(ctx, first, second); !yes || err != nil {
		t.Fatalf("first before second: %v %v", yes, err)
	}

	if yes, err := g.IsAncestor(ctx, second, first); yes || err != nil {
		t.Fatalf("second before first: %v %v", yes, err)
	}

	changes, err := g.Diff(ctx, first, second)
	if err != nil {
		t.Fatal(err)
	}

	want := []gitx.Change{
		{Path: "docs/readme.md", Status: "D"},
		{Path: "e2e/helpers.ts", Status: "D"},
		{Path: "e2e/helpers2.ts", Status: "A"},
		{Path: "web/app.ts", Status: "M"},
		{Path: "web/new file.ts", Status: "A"},
	}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("diff:\n got %+v\nwant %+v", changes, want)
	}

	if changes, err := g.Diff(ctx, second, second); err != nil || len(changes) != 0 {
		t.Fatalf("empty diff: %+v %v", changes, err)
	}
}

func TestCheckoutReusesItsDirectory(t *testing.T) {
	r := testrepo.New(t)
	g := gitx.Repo{Dir: r.Dir}
	ctx := t.Context()

	first := r.Git("rev-parse", "HEAD")

	r.Write("web/app.ts", "changed\n")
	second := r.Commit("change")

	dir := filepath.Join(t.TempDir(), "tree")

	if err := g.Checkout(ctx, dir, first); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := g.Checkout(ctx, dir, second); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "web", "app.ts"))
	if err != nil || string(data) != "changed\n" {
		t.Fatalf("app.ts: %q %v", data, err)
	}

	if _, err := os.Stat(filepath.Join(dir, "stray")); !os.IsNotExist(err) {
		t.Fatalf("stray file kept: %v", err)
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := g.Checkout(ctx, dir, first); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutReplacesAWorkTreeOfAGoneRepository(t *testing.T) {
	r := testrepo.New(t)
	commit := r.Git("rev-parse", "HEAD")

	tree := filepath.Join(t.TempDir(), "tree")
	if err := (gitx.Repo{Dir: r.Dir}).Checkout(t.Context(), tree, commit); err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.Rename(r.Dir, moved); err != nil {
		t.Fatal(err)
	}

	if err := (gitx.Repo{Dir: moved}).Checkout(t.Context(), tree, commit); err != nil {
		t.Fatalf("checkout after the repository moved: %v", err)
	}

	if _, ok, err := (gitx.Repo{Dir: tree}).Show(t.Context(), "HEAD", "pipelinegen.yaml"); err != nil || !ok {
		t.Fatalf("the work tree is broken: %v %v", ok, err)
	}
}
