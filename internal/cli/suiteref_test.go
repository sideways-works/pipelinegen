package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/report"
)

// releaseBefore is the release whose readers a store's layout must keep serving.
const releaseBefore = "v0.3.1"

// toolOf builds pipelinegen as the repository's tag has it; without the tag in this checkout the test can't run.
func toolOf(t *testing.T, tag string) string {
	t.Helper()

	top, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("not in a git checkout: %v", err)
	}

	repo := strings.TrimSpace(string(top))
	if err := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "--verify", "--quiet", tag+"^{commit}").Run(); err != nil {
		t.Skipf("this checkout has no tag %s", tag)
	}

	src := t.TempDir()

	archive := exec.CommandContext(t.Context(), "sh", "-c", `git -C "$1" archive "$2" | tar -x -C "$3"`, "sh", repo, tag, src)
	if out, err := archive.CombinedOutput(); err != nil {
		t.Fatalf("%s's tree: %v: %s", tag, err, out)
	}

	bin := filepath.Join(t.TempDir(), "pipelinegen")

	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/pipelinegen")
	build.Dir = src

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v: %s", tag, err, out)
	}

	return bin
}

func TestTheReleaseBeforeReadsASuitesOwnRef(t *testing.T) {
	if storetest.OnBranch {
		t.Skip("a suite's own ref is the refs store's layout")
	}

	old := toolOf(t, releaseBefore)
	ctx := t.Context()

	root, env := recordRepo(t)
	remote := storetest.Bare(t)

	writeFile(t, filepath.Join(root, config.FileName), strings.Replace(recordConfig, "suites:\n",
		"suites:\n  unit:\n    tests: [\"e2e/pay.spec.ts\"]\n    bind: recorded\n    run_slice: ci/e2e-slice.yml\n", 1)+
		"git: {remote: \""+remote+"\"}\n"+
		"runs:\n  main:\n    trigger: {branch: main}\n    compare: last_passed\n    select: affected\n    suites: {e2e: {}, unit: {}}\n")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "two suites and a remote")

	head := gitT(t, root, "rev-parse", "HEAD")
	id := report.ID{Run: "main", Change: "main", Commit: head}

	plan := `{"base": "", "head": "` + head + `", "suites": [` +
		`{"suite": "e2e", "selected": [{"label": "e2e/cart.spec.ts", "name": "e2e/cart.spec.ts"}], "untouched": [], "whole_suite": []},` +
		`{"suite": "unit", "selected": [{"label": "e2e/pay.spec.ts", "name": "e2e/pay.spec.ts"}], "untouched": [], "whole_suite": []}]}`
	if err := report.WritePlan(ctx, storetest.Open(t, remote), id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	for _, suite := range []string{"e2e", "unit"} {
		if err := report.RecordBegan(ctx, storetest.Open(t, remote), id, suite, ""); err != nil {
			t.Fatal(err)
		}
	}

	batch := storetest.Open(t, remote)
	batch.Batch = "1"

	if _, err := batch.Write(ctx, "state", gitstore.Update{Ref: id.StateRef("e2e", "e2e/cart.spec.ts"), Any: true,
		Files: map[string][]byte{"state.json": []byte(`{"state": "passed"}`)}}); err != nil {
		t.Fatal(err)
	}

	r := &report.Reporter{Config: loadConfig(t, root), ID: id, Store: storetest.Open(t, remote), GitHub: report.Nowhere{}, Throttle: -1}
	if _, err := r.Suite(ctx, "e2e"); err != nil {
		t.Fatal(err)
	}

	refs := refsOf(t, remote)
	if !strings.Contains(refs, "/e2e/"+gitstore.SuiteBatch+"\n") || !strings.Contains(refs, "/unit/"+gitstore.SuiteBatch+"\n") {
		t.Fatalf("the suites' own refs aren't there: %s", refs)
	}

	now, _, _ := runIn(t, root, env, "status", head, "--run", "main", "--json", "--root", root)

	cmd := exec.CommandContext(ctx, old, "status", head, "--run", "main", "--json", "--root", root)
	cmd.Dir = root
	cmd.Env = os.Environ()

	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	then, _ := cmd.Output() //nolint:errcheck // a pending run exits 2; the output is compared

	t.Logf("%s: %s", releaseBefore, then)

	if !strings.Contains(now, `"description": "1 of 2 suites done`) || string(then) != now {
		t.Errorf("%s reads %s, this build %s", releaseBefore, then, now)
	}
}

func refsOf(t *testing.T, remote string) string {
	t.Helper()

	out, err := exec.CommandContext(context.WithoutCancel(t.Context()), "git", "-C", strings.TrimPrefix(remote, "file://"), "for-each-ref", "--format=%(refname)").Output()
	if err != nil {
		t.Fatal(err)
	}

	return string(out)
}
