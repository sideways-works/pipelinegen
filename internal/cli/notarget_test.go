package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestANewTestFileInNoTargetRunsEverything(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path
	r := testrepo.New(t)

	conf := readText(t, filepath.Join(r.Dir, "pipelinegen.yaml"))
	r.Write("pipelinegen.yaml", strings.Replace(conf, "\nsuites:\n", "\nsuites:\n  unit:\n    tests: [\"text/*_test.go\"]\n    run_slice: ci/backend-slice.yml\n", 1))
	r.Write("text/BUILD.bazel", "filegroup(name = \"text\", srcs = [\"title.go\"])\n")
	r.Write("text/title.go", "package text\n")
	base := r.Commit("text")

	r.Write("text/title_test.go", "package text\n\nimport \"testing\"\n\nfunc TestTitle(t *testing.T) {}\n")
	r.Commit("a new test")

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := run(t, "affected", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-no-lists")
	if code != 0 || !strings.HasPrefix(stdout, "every slice: text/title_test.go changed and no Bazel target owns it: it is in no target yet (run gazelle)") {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
}
