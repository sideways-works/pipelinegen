package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/affected"
	"github.com/sideways-works/pipelinegen/internal/bazel"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestAQuietFileBesideAnExtensionsInput(t *testing.T) {
	testrepo.Parallel(t)

	r := sharedCauseRepo(t)
	r.Write("MODULE.bazel", readFile(t, filepath.Join(r.Dir, "MODULE.bazel"))+"\ngen = use_extension(\"//ext:gen.bzl\", \"gen\")\nuse_repo(gen, \"gen\")\n")
	r.Write("ext/BUILD.bazel", "filegroup(name = \"inputs\", srcs = [\"versions.txt\"], visibility = [\"//visibility:public\"])\n")
	r.Write("ext/versions.txt", "one\n")
	r.Write("ext/gen.bzl", `def _repo_impl(rctx):
    rctx.file("BUILD.bazel", 'filegroup(name = "files", srcs = ["versions.txt"], visibility = ["//visibility:public"])\n')
    rctx.file("versions.txt", rctx.attr.content)

_repo = repository_rule(implementation = _repo_impl, attrs = {"content": attr.string()})

def _gen_impl(mctx):
    _repo(name = "gen", content = mctx.read(Label("//ext:versions.txt")))

gen = module_extension(implementation = _gen_impl)
`)
	r.Write("runner/BUILD.bazel", "filegroup(name = \"runner\", srcs = [\"reporter.js\", \"runner.conf.js\"], data = [\"@gen//:files\"], visibility = [\"//visibility:public\"])\n")
	r.Commit("the runner depends on a generated repository")

	cfg := sharedCauseConfig(t, r)

	base := r.Git("rev-parse", "HEAD")
	r.Write("runner/reporter.js", "const marker = () =>\n  process.env.OUT ? join(process.env.OUT, '.complete') : undefined;\n")
	r.Write("ext/versions.txt", "two\n")
	head := r.Commit("format the reporter, bump the versions")

	p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
	t.Logf("plan: %s", summary(p))
	t.Logf("warnings: %q", p.Warnings)

	if got := selectedNames(suiteOf(p, "browser")); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
		t.Errorf("browser selects %q, want every spec: the generated repository changed", got)
	}

	if p.LabelGroups != 1 {
		t.Errorf("label groups %d: bazel-diff hashes the head once more, the reporter put back", p.LabelGroups)
	}

	if got := summary(p); !strings.HasPrefix(got, "browser 3 of 3 (3: external/gen changed, through //runner:runner), lint 1 of 1") {
		t.Errorf("summary %q", got)
	}

	base = head

	r.Write("ext/versions.txt", "three\n")
	head = r.Commit("bump the versions")

	p, _ = planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
	t.Logf("alone: %s", summary(p))

	if got := selectedNames(suiteOf(p, "browser")); got != "browser/a.spec.ts,browser/b.spec.ts,browser/c.spec.ts" {
		t.Errorf("alone: browser selects %q, want every spec", got)
	}
}

func TestAQuietFileBesideAnotherChange(t *testing.T) {
	testrepo.Parallel(t)

	r := sharedCauseRepo(t)
	cfg := sharedCauseConfig(t, r)

	base := r.Git("rev-parse", "HEAD")
	r.Write("runner/reporter.js", "const marker = () =>\n  process.env.OUT ? join(process.env.OUT, '.complete') : undefined;\n")
	r.Write("msgs/de.json", "{\"hello\": \"Servus\"}\n")
	head := r.Commit("format the reporter, a German greeting")

	p, _ := planSharedCause(t, r, cfg, t.TempDir(), pluginsDirFor(t), base, head, 25)
	t.Logf("plan: %s", summary(p))

	if got := selectedNames(suiteOf(p, "browser")); got != "browser/c.spec.ts" {
		t.Errorf("browser selects %q, want the spec behind the catalog", got)
	}

	if p.LabelGroups != 1 || len(p.LabelHashSeconds) != 1 || !strings.Contains(summary(p), "; label groups 1, hashed again in ") {
		t.Errorf("label groups %d in %v: %q", p.LabelGroups, p.LabelHashSeconds, summary(p))
	}
}

func TestAPlanCutOffWithTheBaseCopiesInPlace(t *testing.T) {
	testrepo.Parallel(t)

	r := sharedCauseRepo(t)
	cfg := sharedCauseConfig(t, r)
	cache, pdir := t.TempDir(), pluginsDirFor(t)

	base := r.Git("rev-parse", "HEAD")
	r.Write("runner/reporter.js", "const marker = () =>\n  process.env.OUT ? join(process.env.OUT, '.complete') : undefined;\n")
	r.Write("msgs/de.json", "{\"hello\": \"Servus\"}\n")
	head := r.Commit("format the reporter, a German greeting")

	first, _ := planSharedCause(t, r, cfg, cache, pdir, base, head, 25)

	tree := testrepo.Repo{Dir: filepath.Join(cache, "tree")}
	tree.GitIn(t, "checkout", base, "--", "runner/reporter.js", "msgs/de.json")

	if got := tree.GitIn(t, "status", "--porcelain"); got == "" {
		t.Fatal("the base's copies didn't change the tree")
	}

	if err := os.RemoveAll(filepath.Join(cache, "snapshots")); err != nil {
		t.Fatal(err)
	}

	left := filepath.Join(cache, "without-123")
	if err := os.MkdirAll(left, 0o750); err != nil {
		t.Fatal(err)
	}

	second, _ := planSharedCause(t, r, cfg, cache, pdir, base, head, 25)

	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Errorf("the cut-off plan's hashing directory is still there: %v", err)
	}

	for _, suite := range []string{"browser", "lint"} {
		if a, b := selectedNames(suiteOf(first, suite)), selectedNames(suiteOf(second, suite)); a != b || a == "" && suite == "lint" {
			t.Errorf("%s after a cut-off plan %q, before %q", suite, b, a)
		}
	}

	if got := tree.GitIn(t, "status", "--porcelain"); got != "" {
		t.Errorf("the tree isn't the head after the next plan: %q", got)
	}

	if got := tree.GitIn(t, "rev-parse", "HEAD"); got != head {
		t.Errorf("the tree is at %s, not the head %s", got, head)
	}
}

func TestTheSummaryNamesEachNarrowingAndWhatItLeftOut(t *testing.T) {
	p := &affected.Plan{Suites: []affected.SuitePlan{{
		Suite: "e2e", Selected: []bazel.Slice{{Name: "a", Label: "//e2e:a"}}, Untouched: []string{"//e2e:b", "//e2e:c"},
		Narrowed: []affected.Narrowed{{Kind: affected.NarrowedBy, Narrowing: "plugin-entry", Dropped: []string{"b", "c"}}},
	}}}

	if got := summary(p); got != "e2e 1 of 3 [plugin-entry left out 2]" {
		t.Errorf("%q", got)
	}
}
