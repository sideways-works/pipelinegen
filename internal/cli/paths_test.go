package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestRelativePathsFromAnotherDirectory(t *testing.T) {
	r := testrepo.New(t)
	build := t.TempDir()
	bz := testrepo.Bazel(t, filepath.Join(build, "rel-cache", "tree"))

	cfg := "bazel:\n  startup_flags: [" + strings.Join(bz.StartupFlags, ", ") + "]\n"

	data, err := os.ReadFile(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	r.Write("pipelinegen.yaml", cfg+string(data))
	r.Commit("bazel flags")

	if err := os.Rename(r.Dir, filepath.Join(build, "trigger")); err != nil {
		t.Fatal(err)
	}

	t.Chdir(build)

	stdout, stderr, code := run(t, "affected", "--root", "trigger", "--head", "HEAD", "--cache", "rel-cache")
	if code != 0 {
		t.Fatalf("affected: %d\n%s", code, stderr)
	}

	if !strings.Contains(stdout, "e2e") {
		t.Fatalf("no plan: %s", stdout)
	}

	if _, err := os.Stat(filepath.Join(build, "rel-cache", "tree")); err != nil {
		t.Fatalf("the work tree isn't under the cache named: %v", err)
	}

	if _, err := os.Stat(filepath.Join(build, "trigger", "rel-cache")); err == nil {
		t.Fatal("the cache landed inside the checkout")
	}
}

func TestAPathFlagIsAbsoluteOnceRead(t *testing.T) {
	t.Chdir(t.TempDir())

	var c common

	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	c.register(fs)

	if err := parse(t.Context(), fs, []string{"--root", "trigger"}); err != nil {
		t.Fatal(err)
	}

	if !filepath.IsAbs(c.root) || filepath.Base(c.root) != "trigger" {
		t.Fatalf("root %q", c.root)
	}
}
