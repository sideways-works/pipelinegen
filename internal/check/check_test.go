package check

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

type memTree map[string]string

func (m memTree) tree() Tree {
	return Tree{
		Files: slices.Sorted(keys(m)),
		Read: func(p string) ([]byte, error) {
			s, ok := m[p]
			if !ok {
				return nil, fs.ErrNotExist
			}

			return []byte(s), nil
		},
	}
}

func good(t *testing.T) memTree {
	t.Helper()

	m := memTree{}
	root := filepath.Join("testdata", "good")

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(p) //nolint:gosec // the test's own fixture
		if err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		m[filepath.ToSlash(rel)] = string(data)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func cfg() *config.Config {
	return &config.Config{
		Suites: map[string]config.Suite{
			"e2e":     {Tests: []string{"e2e/**/*.spec.ts"}},
			"backend": {Tests: []string{"**/*_test.go"}},
			"lint":    {Tag: "slice-lint"},
		},
		Untested: []config.Untested{
			{Paths: []string{"docs/**"}, Reason: "prose"},
			{Paths: []string{".gitignore", ".bazelignore"}, Reason: "repository settings"},
		},
		EverythingOn: []string{"MODULE.bazel", ".bazelversion"},
	}
}

func kinds(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Kind+" "+f.Path)
	}

	return out
}

func expect(t *testing.T, got []Finding, want ...string) {
	t.Helper()

	if k := kinds(got); !slices.Equal(k, want) {
		t.Fatalf("got %q\nwant %q\n%v", k, want, got)
	}
}

func TestAWellSetUpTreeHasNoFindings(t *testing.T) {
	expect(t, Find(cfg(), good(t).tree(), nil))
}

func TestAFileNoSliceDependsOnIsUncovered(t *testing.T) {
	m := good(t)
	m["web/unused.ts"] = ""
	m["scripts/deploy.sh"] = ""

	got := Find(cfg(), m.tree(), nil)
	expect(t, got, "uncovered-file scripts/deploy.sh", "uncovered-file web/unused.ts")

	if !strings.Contains(got[0].String(), "scripts/deploy.sh: no slice depends on this file") || !strings.Contains(got[0].String(), "\n  fix: ") {
		t.Fatalf("hook output: %s", got[0])
	}
}

func TestUntestedAndEverythingOnCover(t *testing.T) {
	m := good(t)
	m["web/unused.ts"] = ""
	m["ci/task.yml"] = ""

	c := cfg()
	c.Untested = append(c.Untested, config.Untested{Paths: []string{"web/unused.ts"}, Reason: "kept for later"})
	c.EverythingOn = append(c.EverythingOn, "ci/**")

	expect(t, Find(c, m.tree(), nil))
}

func TestATestFilesOwnersAreCovered(t *testing.T) {
	m := good(t)
	m["api/BUILD.bazel"] = strings.Replace(m["api/BUILD.bazel"], `srcs = ["api.go"],`, `srcs = ["api.go", "extra.go"],`, 1)
	m["api/extra.go"] = "package api\n"

	c := cfg()
	c.Suites["lint"] = config.Suite{Tag: "slice-lint"}
	m["tools/BUILD.bazel"] = strings.Replace(m["tools/BUILD.bazel"], `data = ["//api"],`, `data = ["//web"],`, 1)

	expect(t, Find(c, m.tree(), nil))
}

func TestATaggedTargetBoundToNothingIsReported(t *testing.T) {
	m := good(t)
	m["tools/BUILD.bazel"] = `sh_test(
    name = "lint-go",
    srcs = ["lint.sh"],
    tags = ["slice-lint"],
)
`

	got := Find(cfg(), m.tree(), nil)
	expect(t, got, "unbound-slice tools/BUILD.bazel")

	if got[0].Target != "//tools:lint-go" {
		t.Fatalf("target %q", got[0].Target)
	}
}

func TestATaggedSuiteWithoutTargetsIsReported(t *testing.T) {
	c := cfg()
	c.Suites["types"] = config.Suite{Tag: "slice-types"}

	expect(t, Find(c, good(t).tree(), nil), "untagged-slice pipelinegen.yaml")
}

func TestAHandWrittenSelectorWithATwinIsBound(t *testing.T) {
	m := good(t)
	m["tools/BUILD.bazel"] = `filegroup(
    name = "lint-go",
    srcs = ["lint.sh"],
    tags = ["slice-lint", "pipelinegen-run://tools:lint-go-run"],
)

sh_test(
    name = "lint-go-run",
    srcs = ["lint.sh"],
    data = ["//api"],
    tags = ["manual"],
)
`

	expect(t, Find(cfg(), m.tree(), nil))
}

func TestMissingSetupIsReported(t *testing.T) {
	m := good(t)
	m["MODULE.bazel"] = "module(name = \"fixture\")\n"
	m[".bazelignore"] = "node_modules\n"
	delete(m, ".gitignore")

	c := cfg()
	c.Untested = c.Untested[:1]

	expect(t, Find(c, m.tree(), nil), "setup .bazelignore", "uncovered-file .bazelignore", "setup .gitignore", "setup MODULE.bazel")
}

func TestLeftoverSetupWarns(t *testing.T) {
	o := Options{NoDepsRepo: true}

	got := FindWith(cfg(), good(t).tree(), nil, o)
	expect(t, got, "setup .bazelignore", "setup .gitignore", "setup MODULE.bazel")

	for _, f := range got {
		if !f.Warning || !strings.HasPrefix(f.String(), "warning: "+f.Path+": ") || !strings.Contains(f.Problem, "the next release refuses") {
			t.Fatalf("not a warning: %s", f)
		}
	}

	if failed := Failures(got); len(failed) > 0 {
		t.Fatalf("warnings fail: %v", failed)
	}

	m := good(t)
	m["MODULE.bazel"] = "module(name = \"fixture\")\n"
	m[".bazelignore"] = "node_modules\n"
	delete(m, ".gitignore")

	c := cfg()
	c.Untested = c.Untested[:1]

	expect(t, FindWith(c, m.tree(), nil, o), "uncovered-file .bazelignore")
}

func TestPatternsMatchingNothingAreReported(t *testing.T) {
	c := cfg()
	c.Untested = append(c.Untested, config.Untested{Paths: []string{"vendor/**"}, Reason: "gone"})
	c.Suites["unit"] = config.Suite{Tests: []string{"**/*.test.ts"}}

	got := Find(c, good(t).tree(), nil)
	expect(t, got, "bad-config pipelinegen.yaml", "bad-config pipelinegen.yaml")
}

func TestASuiteNoRunListsFailsTheCheck(t *testing.T) {
	c := cfg()

	expect(t, Find(c, good(t).tree(), nil))

	c.Runs = map[string]config.Run{
		"pr":      {Suites: map[string]config.RunSuite{"e2e": {}}},
		"nightly": {Suites: map[string]config.RunSuite{"e2e": {}, "lint": {}}},
	}

	got := Find(c, good(t).tree(), nil)
	expect(t, got, "bad-config pipelinegen.yaml")

	f := got[0]
	if f.Problem != "suite backend is in no run: its tests never run" || f.Fix != "list it under a run's suites (runs.<run>.suites), or remove it" ||
		len(Failures(got)) != 1 {
		t.Errorf("%+v", f)
	}
}

func TestBatchesFarAboveParallelWarn(t *testing.T) {
	for batches, want := range map[int]bool{0: false, 16: false, 17: true} {
		c := cfg()
		e2e := c.Suites["e2e"]
		e2e.Batches = batches
		c.Suites["e2e"] = e2e

		got := Find(c, good(t).tree(), nil)
		if len(Failures(got)) != 0 || (len(got) == 1) != want {
			t.Fatalf("batches %d: %v", batches, got)
		}

		if want && !strings.Contains(got[0].Problem, "suite e2e: batches (17) is more than 4 times parallel (4): every batch of a build runs at once, so a build of everything runs 17 batch tasks at the same time") {
			t.Errorf("the warning: %+v", got[0])
		}
	}

	for batches, want := range map[int]bool{0: true, 8: false} {
		c := cfg()
		e2e := c.Suites["e2e"]
		e2e.BatchMinutes, e2e.Parallel, e2e.Batches = 10, 2, batches
		c.Suites["e2e"] = e2e

		got := Find(c, good(t).tree(), nil)
		if len(Failures(got)) != 0 || (len(got) == 1) != want {
			t.Fatalf("batch_minutes, batches %d: %v", batches, got)
		}

		if want && !strings.Contains(got[0].Problem, "up to one per slice, and every one runs at once") {
			t.Errorf("the warning: %+v", got[0])
		}
	}
}

func TestBatchesOnAnOldLockDontWarn(t *testing.T) {
	cf := cfg()
	e2e := cf.Suites["e2e"]
	e2e.Batches, e2e.BatchMinutes, e2e.Lock = 12, 30, &config.Lock{Name: "browsers", Slots: 1, Per: "batch"}
	cf.Suites["e2e"] = e2e

	if got := Find(cf, good(t).tree(), nil); len(got) != 0 {
		t.Fatalf("an old lock still warns: %v", got)
	}
}

func TestAnUnreadableBuildFileIsReported(t *testing.T) {
	m := good(t)
	m["web/BUILD.bazel"] = "filegroup(name = "

	got := Find(cfg(), m.tree(), nil)
	if len(got) == 0 || got[0].Kind != KindUnreadable || got[0].Path != "web/BUILD.bazel" {
		t.Fatalf("got %v", got)
	}
}

func TestFindWithStagedKeepsTheirFindings(t *testing.T) {
	m := good(t)
	m["web/unused.ts"] = ""
	m["web/other.ts"] = ""

	expect(t, Find(cfg(), m.tree(), []string{"web/other.ts", "web/app.ts"}), "uncovered-file web/other.ts")
}

func repo(t *testing.T) *testrepo.Repo {
	t.Helper()

	return testrepo.NewFrom(t, filepath.Join("testdata", "good"))
}

func runStaged(t *testing.T, r *testrepo.Repo) []Finding {
	t.Helper()

	got, err := Run(context.Background(), cfg(), r.Dir, true, Options{})
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestStagedReportsWhatTheCommitBrings(t *testing.T) {
	r := repo(t)

	r.Write("web/old.ts", "")
	r.Commit("an uncovered file")

	r.Write("web/new.ts", "")
	r.Git("add", "web/new.ts")
	r.Write("web/unstaged.ts", "")

	expect(t, runStaged(t, r), "uncovered-file web/new.ts")

	r.Write("web/BUILD.bazel", `filegroup(name = "web", srcs = glob(["*.ts"]))`)
	expect(t, runStaged(t, r), "uncovered-file web/new.ts")
}

func TestStagedBuildChangesReportWhatTheyUncoverElsewhere(t *testing.T) {
	r := repo(t)

	r.Write("e2e/BUILD.bazel", `filegroup(name = "specs", srcs = glob(["*.spec.ts"]))`)
	r.Git("add", "e2e/BUILD.bazel")

	expect(t, runStaged(t, r), "uncovered-file web/BUILD.bazel", "uncovered-file web/app.ts")
}

func TestStagedOnAFirstCommitReportsEverything(t *testing.T) {
	dir := t.TempDir()

	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "good"))); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "web", "unused.ts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}} {
		if _, err := git(context.Background(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Run(context.Background(), cfg(), dir, true, Options{})
	if err != nil {
		t.Fatal(err)
	}

	expect(t, got, "uncovered-file web/unused.ts")
}

func TestWholeTreeRunSeesUntrackedFiles(t *testing.T) {
	r := repo(t)
	r.Write("web/new.ts", "")

	got, err := Run(context.Background(), cfg(), r.Dir, false, Options{})
	if err != nil {
		t.Fatal(err)
	}

	expect(t, got, "uncovered-file web/new.ts")
}

func TestStagedIsFastOnALargeTree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a large repository")
	}

	r := repo(t)

	for i := range 220 {
		pkg := fmt.Sprintf("pkg%03d", i)
		r.Write(pkg+"/BUILD.bazel", `filegroup(name = "files", srcs = glob(["**/*.go"]))

sh_test(name = "lint", srcs = ["lint.sh"], data = [":files"], tags = ["slice-lint"])
`)
		r.Write(pkg+"/lint.sh", "")

		for j := range 20 {
			r.Write(fmt.Sprintf("%s/f%02d_test.go", pkg, j), "package p\n")
		}
	}

	r.Commit("large")
	r.Write("pkg000/BUILD.bazel", `filegroup(name = "files", srcs = glob(["*.go"]))

sh_test(name = "lint", srcs = ["lint.sh"], data = [":files"], tags = ["slice-lint"])
`)
	r.Git("add", "-A")

	start := time.Now()

	runStaged(t, r)

	took := time.Since(start)
	t.Logf("staged check of %d files took %v", strings.Count(r.Git("ls-files"), "\n")+1, took)

	// twice the target, so a busy machine doesn't fail it at random
	if took > 2*time.Second*raceSlowdown {
		t.Fatalf("took %v", took)
	}
}

func TestAnOwnerTemplateNamesTheTargetToWalk(t *testing.T) {
	m := good(t)
	m["e2e/BUILD.bazel"] = `filegroup(name = "checkout", srcs = ["checkout.spec.ts"], data = ["//web"])

filegroup(name = "unrelated", srcs = ["unrelated.txt"])
`
	m["e2e/unrelated.txt"] = ""

	c := cfg()
	c.Suites["e2e"] = config.Suite{Tests: []string{"e2e/**/*.spec.ts"}, Owner: "//{dir}:{stem}"}

	expect(t, Find(c, m.tree(), nil), "uncovered-file e2e/unrelated.txt")

	c.Suites["e2e"] = config.Suite{Tests: []string{"e2e/**/*.spec.ts"}, Owner: "//{dir}:{base}_run"}

	got := Find(c, m.tree(), nil)
	if len(got) == 0 || got[0].Kind != KindUnbound || got[0].Path != "e2e/checkout.spec.ts" || got[0].Target != "//e2e:checkout.spec.ts_run" {
		t.Fatalf("got %v", got)
	}
}

func TestExtraDepsAndExcludes(t *testing.T) {
	m := good(t)
	m["e2e/BUILD.bazel"] = `filegroup(name = "specs", srcs = glob(["*.spec.ts"]))`
	m["e2e/draft.spec.ts"] = ""

	c := cfg()
	c.Suites["e2e"] = config.Suite{
		Tests:     []string{"e2e/**/*.spec.ts"},
		Exclude:   []string{"e2e/draft.spec.ts"},
		ExtraDeps: []config.ExtraDeps{{Tests: []string{"e2e/checkout.spec.ts"}, Deps: []string{"//web"}}},
	}

	expect(t, Find(c, m.tree(), nil))

	c.Suites["e2e"] = config.Suite{Tests: []string{"e2e/**/*.spec.ts"}, Exclude: []string{"e2e/draft.spec.ts"}}
	expect(t, Find(c, m.tree(), nil), "uncovered-file web/BUILD.bazel", "uncovered-file web/app.ts")
}

func TestATestFileOnlyItsPackageHoldsIsHeldByNothing(t *testing.T) {
	m := good(t)
	m["e2e/BUILD.bazel"] = `filegroup(name = "app", data = ["//web"])`

	expect(t, Find(cfg(), m.tree(), nil), "unbound-slice pipelinegen.yaml", "uncovered-file web/BUILD.bazel", "uncovered-file web/app.ts")
}

func configText(t *testing.T, untested []map[string]any) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "workspace", config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	doc["suites"] = map[string]any{
		"e2e":     map[string]any{"tests": []any{"e2e/**/*.spec.ts"}, "run_slice": "ci/e2e-slice.yml"},
		"backend": map[string]any{"tests": []any{"**/*_test.go"}, "run_slice": "ci/backend-slice.yml"},
		"lint":    map[string]any{"tag": "slice-lint", "run_slice": "ci/lint.yml"},
	}
	doc["untested"] = untested
	doc["everything_on"] = []any{"MODULE.bazel", ".bazelversion"}

	runs, ok := doc["runs"].(map[string]any)
	if !ok {
		t.Fatal("the workspace's config has no runs")
	}

	for _, r := range runs {
		run, ok := r.(map[string]any)
		if !ok {
			t.Fatal("a run of the workspace's config isn't a mapping")
		}

		run["suites"] = map[string]any{"e2e": map[string]any{}, "backend": map[string]any{}, "lint": map[string]any{}}
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := config.Parse(out); err != nil {
		t.Fatalf("the test's config: %v", err)
	}

	return string(out)
}

func TestStagedUsesTheConfigTheCommitHolds(t *testing.T) {
	settings := map[string]any{"paths": []any{"docs/**", ".gitignore", ".bazelignore"}, "reason": "prose and settings"}
	unused := map[string]any{"paths": []any{"web/unused.ts"}, "reason": "kept for later"}

	r := repo(t)
	r.Write(config.FileName, configText(t, []map[string]any{settings, unused}))
	r.Write("web/unused.ts", "")
	r.Commit("an untested file")

	r.Write(config.FileName, configText(t, []map[string]any{settings}))
	r.Git("add", config.FileName)
	expect(t, runStaged(t, r), "uncovered-file web/unused.ts")

	r.Git("commit", "-q", "-m", "off untested")
	r.Write("web/new.ts", "")
	r.Git("add", "web/new.ts")
	r.Write(config.FileName, configText(t, []map[string]any{settings, unused, {"paths": []any{"web/new.ts"}, "reason": "later"}}))
	expect(t, runStaged(t, r), "uncovered-file web/new.ts")
}

func TestStagedMovesAConfigTheLastCommitWroteByRetiredKeys(t *testing.T) {
	settings := map[string]any{"paths": []any{"docs/**", ".gitignore", ".bazelignore"}, "reason": "prose and settings"}
	unused := map[string]any{"paths": []any{"web/unused.ts"}, "reason": "kept for later"}
	moved := configText(t, []map[string]any{settings, unused})
	retired := strings.Replace(moved, "    backend:\n", "    backend:\n        run_on:\n            tags: [big]\n", 1)

	if retired == moved {
		t.Fatal("the test's config has no backend suite to tag")
	}

	if _, err := config.Parse([]byte(retired)); err == nil || !strings.Contains(err.Error(), "suites.backend.run_on.tags") {
		t.Fatalf("the retired key should be refused, got %v", err)
	}

	r := repo(t)
	r.Write(config.FileName, retired)
	r.Write("web/unused.ts", "")
	r.Commit("a config by retired keys")

	r.Write(config.FileName, moved)
	r.Git("add", config.FileName)
	expect(t, runStaged(t, r))

	r.Write(config.FileName, configText(t, []map[string]any{settings}))
	r.Git("add", config.FileName)
	expect(t, runStaged(t, r), "uncovered-file web/unused.ts")
}

func TestStagedNamesTheAloneFindingByItsFiles(t *testing.T) {
	settings := map[string]any{"paths": []any{"docs/**", ".gitignore", ".bazelignore"}, "reason": "prose and settings"}

	var doc map[string]any
	if err := yaml.Unmarshal([]byte(configText(t, []map[string]any{settings})), &doc); err != nil {
		t.Fatal(err)
	}

	suites, ok := doc["suites"].(map[string]any)
	if !ok {
		t.Fatalf("suites: %v", doc["suites"])
	}

	suites["unit"] = map[string]any{"tests": []any{"web/**/*.test.ts"}, "run_slice": "ci/e2e-slice.yml"}

	text, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	r := repo(t)
	r.Write(config.FileName, string(text))
	r.Write("web/src/a.test.ts", "")
	r.Commit("a test file nothing holds")

	r.Write("web/src/b.test.ts", "")
	r.Git("add", "web/src/b.test.ts")

	got := runStaged(t, r)
	expect(t, got, "unbound-slice pipelinegen.yaml")

	if !slices.Equal(got[0].Files, []string{"web/src/a.test.ts", "web/src/b.test.ts"}) {
		t.Fatalf("got %+v", got[0])
	}

	r.Git("commit", "-q", "-m", "another")

	r.Write("docs/more.md", "")
	r.Git("add", "docs/more.md")
	expect(t, runStaged(t, r))
	r.Git("commit", "-q", "-m", "prose")

	build, err := os.ReadFile(filepath.Join(r.Dir, "money", "BUILD.bazel"))
	if err != nil {
		t.Fatal(err)
	}

	r.Write("money/BUILD.bazel", string(build)+"\n# the same targets\n")
	r.Git("add", "money/BUILD.bazel")
	expect(t, runStaged(t, r))
}

func TestCommitChecksTheCommitNotTheWorkTree(t *testing.T) {
	r := repo(t)
	r.Write(config.FileName, configText(t, []map[string]any{{"paths": []any{"docs/**", ".gitignore", ".bazelignore"}, "reason": "prose"}}))
	head := r.Commit("config")
	r.Write("web/leftover.ts", "")

	got, err := Commit(context.Background(), r.Dir, head, Options{})
	if err != nil {
		t.Fatal(err)
	}

	expect(t, got)

	if _, err := Commit(context.Background(), repo(t).Dir, "HEAD", Options{}); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("a commit without config: %v", err)
	}
}

func TestAHelperIsNoSliceOfItsOwn(t *testing.T) {
	m := good(t)
	m["e2e/fixtures.spec.ts"] = ""
	m["e2e/BUILD.bazel"] = `filegroup(name = "specs", srcs = ["checkout.spec.ts"], data = ["//web"])`

	c := cfg()
	c.Suites["e2e"] = config.Suite{Tests: []string{"e2e/**/*.spec.ts"}, Helpers: []string{"e2e/fixtures.spec.ts"}}

	expect(t, Find(c, m.tree(), nil), "uncovered-file e2e/fixtures.spec.ts")

	m["e2e/BUILD.bazel"] = `filegroup(name = "specs", srcs = ["checkout.spec.ts", "fixtures.spec.ts"], data = ["//web"])`
	expect(t, Find(c, m.tree(), nil))
}

func TestARecordedSuitesHelperIsCoveredByItsRecordings(t *testing.T) {
	m := good(t)
	m["e2e/fixtures.spec.ts"] = ""
	m["e2e/BUILD.bazel"] = `filegroup(name = "specs", srcs = ["checkout.spec.ts"], data = ["//web"])`

	c := cfg()
	c.Suites["e2e"] = config.Suite{Tests: []string{"e2e/**/*.spec.ts"}, Helpers: []string{"e2e/fixtures.spec.ts"}, Bind: config.BindRecorded}

	expect(t, Find(c, m.tree(), nil))
}

func readerTree(t *testing.T) memTree {
	t.Helper()

	return fixtureTree(t, "reader")
}

func fixtureTree(t *testing.T, name string) memTree {
	t.Helper()

	m := memTree{}
	root := filepath.Join("testdata", name)

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		data, err := os.ReadFile(p) //nolint:gosec // the test's own fixture
		if err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return fmt.Errorf("fixture: %w", err)
		}

		m[filepath.ToSlash(rel)] = string(data)

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func lintCfg() *config.Config {
	return &config.Config{
		Suites:       map[string]config.Suite{"lint": {Tag: "slice-lint", RunSlice: "ci/lint.yml"}},
		Untested:     []config.Untested{{Paths: []string{".gitignore", ".bazelignore"}, Reason: "repository settings"}},
		EverythingOn: []string{"MODULE.bazel"},
	}
}

func TestTheReaderFollowsLoadsComprehensionsAndSpaces(t *testing.T) {
	expect(t, Find(lintCfg(), readerTree(t).tree(), nil))
}

func TestAnExpressionTheReaderCantFollowFails(t *testing.T) {
	m := readerTree(t)
	m["b/BUILD.bazel"] = `load(":defs.bzl", "inputs")

filegroup(
    name = "lint",
    srcs = ["//:lint.sh"],
    data = inputs(),
    tags = ["slice-lint"],
)
`
	m["b/defs.bzl"] = "def inputs():\n    return native.glob([\"*.txt\"])\n"

	got := Find(lintCfg(), m.tree(), nil)
	expect(t, got, "unreadable b/BUILD.bazel")

	if got[0].Target != "//b:lint" || !strings.Contains(got[0].Problem, "data: inputs(...) cannot be read") {
		t.Fatalf("got %+v", got[0])
	}
}

func TestAnUnknownConstantFails(t *testing.T) {
	m := readerTree(t)
	m["a/lists.bzl"] = "OTHER = []\n"

	got := Find(lintCfg(), m.tree(), nil)
	expect(t, got, "unreadable a/BUILD.bazel")

	if !strings.Contains(got[0].Problem, "INPUTS is no top-level value of //a:lists.bzl") {
		t.Fatalf("got %+v", got[0])
	}
}

func TestWhatBazelIgnoresHoldsNoSlice(t *testing.T) {
	fixture := func() (memTree, *config.Config) {
		m := good(t)
		m["fixtures/testdata/x/BUILD.bazel"] = "filegroup(name = \"x\", srcs = [\"x.sh\"], tags = [\"slice-lint\"])\n"
		m["fixtures/testdata/x/x.sh"] = "true\n"
		m["REPO.bazel"] = "# the repository's settings\n"

		c := cfg()
		c.Untested = append(c.Untested, config.Untested{Paths: []string{"fixtures/**", "REPO.bazel"}, Reason: "fixtures"})

		return m, c
	}

	m, c := fixture()
	expect(t, Find(c, m.tree(), nil), "unbound-slice fixtures/testdata/x/BUILD.bazel")

	m, c = fixture()
	m["REPO.bazel"] = "ignore_directories([\n    \"**/testdata\",\n])\n"
	expect(t, Find(c, m.tree(), nil))

	m, c = fixture()
	m[".bazelignore"] += "fixtures/testdata\n"
	expect(t, Find(c, m.tree(), nil))

	m, c = fixture()
	lint := c.Suites["lint"]
	lint.Exclude = []string{"**/testdata/**"}
	c.Suites["lint"] = lint
	expect(t, Find(c, m.tree(), nil))
}

func TestALabelNamedTwiceIsReported(t *testing.T) {
	m := good(t)
	m["tools/BUILD.bazel"] = `filegroup(
    name = "lint-go",
    srcs = ["lint.sh"],
    data = ["//docs", "//docs:docs"],
    tags = ["slice-lint"],
)
`
	m["docs/BUILD.bazel"] = "filegroup(name = \"docs\", srcs = glob([\"**/*.md\"]))\n"

	got := Find(cfg(), m.tree(), nil)
	if !slices.ContainsFunc(got, func(f Finding) bool {
		return f.Kind == KindDuplicate && f.Path == "tools/BUILD.bazel" && strings.Contains(f.Problem, "//docs:docs twice in data")
	}) {
		t.Fatalf("no duplicate reported: %v", kinds(got))
	}
}

func outsideCfg() *config.Config {
	return &config.Config{
		Suites:       map[string]config.Suite{"web": {Tag: "slice-web", RunSlice: "ci/web.yml"}},
		Untested:     []config.Untested{{Paths: []string{".gitignore"}, Reason: "repository settings"}},
		EverythingOn: []string{"MODULE.bazel"},
	}
}

func findOutside(c *config.Config, m memTree) []Finding {
	return FindWith(c, m.tree(), nil, Options{NoDepsRepo: true})
}

func TestNamesFromOtherRepositoriesAreTools(t *testing.T) {
	expect(t, findOutside(outsideCfg(), fixtureTree(t, "outside")))

	m := fixtureTree(t, "outside")
	m["web/BUILD.bazel"] = strings.Replace(m["web/BUILD.bazel"], `    srcs = glob(["src/**"]),
`, "", 1)
	expect(t, findOutside(outsideCfg(), m), "uncovered-file web/src/main.ts", "uncovered-file web/src/util.ts")
}

func TestWhatAnOutsideNameCantTellStillFails(t *testing.T) {
	for _, attr := range []string{
		"srcs = [f for f in eslint_bin]",
		"srcs = list(eslint_bin)",
		"srcs = eslint_bin.sources()",
		`srcs = web_project("x")`,
	} {
		m := fixtureTree(t, "outside")
		m["web/BUILD.bazel"] = strings.Replace(m["web/BUILD.bazel"], `srcs = glob(["src/**"])`, attr, 1)

		got := findOutside(outsideCfg(), m)
		expect(t, got, "unreadable web/BUILD.bazel")

		if got[0].Target != "//web:web" {
			t.Errorf("%s: %+v", attr, got[0])
		}
	}
}

func TestATestFileAnOutsideValueMayOwnIsUnknown(t *testing.T) {
	c := outsideCfg()
	c.Suites["unit"] = config.Suite{Tests: []string{"**/*.test.ts"}}

	m := fixtureTree(t, "outside")
	m["web/src/main.test.ts"] = "import { main } from './main';\n"
	m["web/BUILD.bazel"] = strings.Replace(m["web/BUILD.bazel"], `glob(["src/**"])`, `glob(["src/**"], exclude = ["**/*.test.ts"])`, 1)

	got := findOutside(c, m)
	expect(t, got, "unreadable web/src/main.test.ts")

	if got[0].Target != "//web:web" || !strings.Contains(got[0].Problem, "//web:web's eslint, lint_deps, tsc holds a value from another repository") {
		t.Fatalf("got %+v", got[0])
	}

	unit := c.Suites["unit"]
	unit.Owner = "//{dir}:web"
	c.Suites["unit"] = unit
	m["web/src/BUILD.bazel"] = "filegroup(name = \"web\", srcs = glob([\"*.ts\"]), data = [\"//web\"])\n"
	expect(t, findOutside(c, m))
}

func TestADeclaredMacroCoversWhatItReads(t *testing.T) {
	m := fixtureTree(t, "outside")
	m["web/BUILD.bazel"] = strings.Replace(m["web/BUILD.bazel"], `    srcs = glob(["src/**"]),
`, "", 1)

	c := outsideCfg()
	c.Macros = map[string]config.Macro{
		"@web_tools//macros:web.bzl%web_project": {Reads: []string{"src/**"}, Reason: "builds the package's sources"},
	}

	got := findOutside(c, m)
	expect(t, got, "declared-macro pipelinegen.yaml")

	if !got[0].Warning || !strings.Contains(got[0].Problem, `"@web_tools//macros:web.bzl%web_project" is trusted to read src/** (builds the package's sources): 2 files in //web:web`) {
		t.Fatalf("got %+v", got[0])
	}

	if len(Failures(got)) != 0 {
		t.Fatalf("a declaration fails nothing: %v", Failures(got))
	}

	c.Macros["@web_tools//macros:web.bzl%web_project"] = config.Macro{Reads: []string{"src/**", "public/**"}, Reason: "sources"}
	c.Macros["@web_tools//macros:web.bzl%other"] = config.Macro{Reads: []string{"*"}, Reason: "unused"}

	got = findOutside(c, m)
	expect(t, got, "bad-config pipelinegen.yaml", "bad-config pipelinegen.yaml", "declared-macro pipelinegen.yaml")

	if !strings.Contains(got[0].Problem, `"@web_tools//macros:web.bzl%other": no BUILD file calls it`) ||
		!strings.Contains(got[1].Problem, `reads "public/**" matches no file in any package that calls it`) {
		t.Fatalf("got %v", got)
	}
}

func TestWhatBazelIgnoresIsOneFinding(t *testing.T) {
	m := good(t)
	m[".bazelignore"] += "examples/repo\n"
	m["examples/repo/money_test.go"] = "package money\n"
	m["examples/repo/README.txt"] = "an example workspace\n"
	m["examples/repo/sub/BUILD.bazel"] = "filegroup(name = \"x\", srcs = [\"x.txt\"])\n"

	got := Find(cfg(), m.tree(), nil)
	expect(t, got, "uncovered-file examples/repo/")

	if !strings.Contains(got[0].Problem, "its 3 files") || !strings.Contains(got[0].Fix, `paths: ["examples/repo/**"]`) {
		t.Fatalf("got %+v", got[0])
	}

	expect(t, Find(cfg(), m.tree(), []string{"examples/repo/sub/BUILD.bazel"}), "uncovered-file examples/repo/")
	expect(t, Find(cfg(), m.tree(), []string{"docs/readme.md"}))
}

func TestDataPinnedByVersionOnlyWarns(t *testing.T) {
	m := good(t)
	m["MODULE.bazel"] += "bazel_dep(name = \"tools\", version = \"1.0\")\n"
	m["lint/BUILD.bazel"] = "filegroup(name = \"lint\", srcs = [\"lint.sh\"], data = [\"@tools//lint:catalog.yaml\"], tags = [\"slice-lint\"])\n"
	m["lint/lint.sh"] = "true\n"

	got := Find(cfg(), m.tree(), nil)
	if !slices.ContainsFunc(got, func(f Finding) bool {
		return f.Kind == KindVersionPinned && f.Warning && strings.Contains(f.Problem, "only the version of the module tools pins")
	}) {
		t.Fatalf("no warning: %v", kinds(got))
	}

	m["MODULE.bazel.lock"] = "{}\n"
	if got := Find(cfg(), m.tree(), nil); slices.ContainsFunc(got, func(f Finding) bool { return f.Kind == KindVersionPinned }) {
		t.Fatalf("a tracked lockfile still warned: %v", kinds(got))
	}
}

func TestANewSymlinkIsNoFileOfTheTree(t *testing.T) {
	r := repo(t)

	if err := os.MkdirAll(filepath.Join(r.Dir, "tools"), 0o750); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(t.TempDir(), filepath.Join(r.Dir, "tools", "plugins")); err != nil {
		t.Fatal(err)
	}

	r.Write("web/new.ts", "")

	tree, err := WorkTree(t.Context(), r.Dir)
	if err != nil {
		t.Fatal(err)
	}

	if slices.Contains(tree.Files, "tools/plugins") || !slices.Contains(tree.Files, "web/new.ts") {
		t.Fatalf("files %v", tree.Files)
	}
}

func aloneTree(t *testing.T) (memTree, *config.Config) {
	t.Helper()

	m := good(t)
	m["web/src/a.test.ts"] = "import { app } from '../app';\n"
	m["web/src/b.test.ts"] = "import { app } from '../app';\n"
	m["web/src/c.test.ts"] = "import { app } from '../app';\n"

	c := cfg()
	c.Suites["unit"] = config.Suite{Tests: []string{"web/**/*.test.ts"}}

	return m, c
}

func TestTestFilesNothingHoldsAreRefused(t *testing.T) {
	m, c := aloneTree(t)

	got := Find(c, m.tree(), nil)
	expect(t, got, "unbound-slice pipelinegen.yaml")

	want := Finding{
		Kind: KindUnbound, Path: "pipelinegen.yaml",
		Files: []string{"web/src/a.test.ts", "web/src/b.test.ts", "web/src/c.test.ts"},
		Problem: "suite unit names no owner, and no Bazel target holds 3 of its test files (web/src/a.test.ts, web/src/b.test.ts and 1 more), " +
			"so each is bound to nothing but itself: a change to anything these files import selects none of them",
		Fix: "give suite unit an owner pattern in pipelinegen.yaml, declare a Bazel target that holds the files, " +
			"or bind the suite by recording (bind: recorded) with a correlator that binds them",
	}
	if got[0].key() != want.key() {
		t.Fatalf("got  %+v\nwant %+v", got[0], want)
	}

	for _, staged := range [][]string{{"web/src/b.test.ts"}, {"pipelinegen.yaml"}} {
		expect(t, Find(c, m.tree(), staged), "unbound-slice pipelinegen.yaml")
	}

	expect(t, Find(c, m.tree(), []string{"docs/readme.md"}))

	for _, f := range want.Files {
		delete(m, f)
	}

	m["top.test.ts"] = ""
	c.Suites["unit"] = config.Suite{Tests: []string{"top.test.ts"}}

	got = Find(c, m.tree(), nil)
	expect(t, got, "unbound-slice pipelinegen.yaml")

	if !strings.Contains(got[0].Problem, "holds 1 of its test files (top.test.ts), so") {
		t.Fatalf("got %+v", got[0])
	}
}

func TestWhatBindsATestFileIsNoAloneFinding(t *testing.T) {
	m, c := aloneTree(t)

	held := maps.Clone(m)
	held["web/src/BUILD.bazel"] = "filegroup(name = \"tests\", srcs = glob([\"*.test.ts\"]), data = [\"//web\"])\n"
	expect(t, Find(c, held.tree(), nil))

	unit := c.Suites["unit"]
	unit.ExtraDeps = []config.ExtraDeps{{Tests: []string{"web/src/a.test.ts", "web/src/b.test.ts"}, Deps: []string{"//web"}}}
	c.Suites["unit"] = unit
	named := maps.Clone(m)
	named["web/src/c.test.ts"] = "// pipelinegen:data //web\nimport { app } from '../app';\n"
	expect(t, Find(c, named.tree(), nil))

	unit = config.Suite{Tests: []string{"web/**/*.test.ts"}, Bind: config.BindRecorded}
	c.Suites["unit"] = unit
	expect(t, Find(c, m.tree(), nil))

	c.Suites["unit"] = config.Suite{Tests: []string{"web/**/*.test.ts"}, Owner: "//{dir}:{stem}"}
	expect(t, Find(c, m.tree(), nil),
		"unbound-slice web/src/a.test.ts", "unbound-slice web/src/b.test.ts", "unbound-slice web/src/c.test.ts")
}

func TestATestFileNoTargetListsCoversNothing(t *testing.T) {
	m, c := aloneTree(t)
	m["web/BUILD.bazel"] += "\nfilegroup(name = \"styles\", srcs = [\"styles.css\"])\n"
	m["web/styles.css"] = ""

	c.Suites["unit"] = config.Suite{Tests: []string{"web/**/*.test.ts"}, Bind: config.BindRecorded}
	expect(t, Find(c, m.tree(), nil), "uncovered-file web/styles.css")
}
