package cli

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/bazeldifftest"
	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

const noHostConfig = `
pipeline: shop
suites:
  e2e:
    tag: slice-e2e
    run_slice: ci/tasks/e2e-slice.yml
    lock: {name: browsers, minutes: 90, slots: 1}
git: {remote: "REMOTE"}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {e2e: {}}
`

func TestABranchRunNeedsNoHost(t *testing.T) { branchRunNeedsNoHost(t) }

func TestABranchRunNeedsNoHostOnABranch(t *testing.T) { onBranch(t, branchRunNeedsNoHost) }

func branchRunNeedsNoHost(t *testing.T) {
	t.Helper()

	remote := storetest.Daemon(t)

	cfgText := strings.Replace(noHostConfig, "REMOTE", remote, 1)
	if onBranchStore {
		cfgText += "store: {backend: branch}\n"
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfgText)

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "one"},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-q", "--allow-empty", "-m", "two"},
	} {
		if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	first, second := revParse(t, repo, "HEAD~1"), revParse(t, repo, "HEAD")

	api := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("a repository with no host asked an API: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(api.Close)

	for k, v := range map[string]string{
		envGitURL: "", envAPIURL: api.URL, envToken: "",
		envRun: "main", envChange: "main", envCommit: first,
		envPR: "", envAlsoTo: "", envSuite: "e2e", envBatch: "1", envJobURL: "https://ci/jobs/e2e",
		envSlice: "", envLabel: "",
	} {
		t.Setenv(k, v)
	}

	s := &gitstore.Store{Remote: remote, Dir: filepath.Join(t.TempDir(), "scratch")}
	if onBranchStore {
		s.Branch = &branchstore.Store{Remote: remote, Branch: config.DefaultStoreBranch, Dir: filepath.Join(t.TempDir(), "branch")}
	}

	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	id := report.ID{Run: "main", Change: "main", Commit: first}

	plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}], "untouched": [], "whole_suite": []}]}`
	if err := report.WritePlan(t.Context(), s, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 || strings.Contains(stderr, envToken) {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if stdout, stderr, code := run(t, "lock", "acquire", "browsers", "-root", root); code != 0 || !strings.Contains(stdout, "lock browsers: slot 1 until") {
		t.Fatalf("acquire: %d %s %s", code, stdout, stderr)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)

	for _, slice := range []string{"e2e.a", "e2e.b"} {
		if _, stderr, code := run(t, "slice", "start", slice, "-root", root); code != 0 {
			t.Fatalf("slice start %s: %d %s", slice, code, stderr)
		}

		writeFile(t, "result/"+slice+".json", `{"passed": true}`)

		if stdout, stderr, code := run(t, "slice", "end", slice, "-root", root); code != 0 || stdout != "slice "+slice+": passed\n" {
			t.Fatalf("slice end %s: %d %q %s", slice, code, stdout, stderr)
		}
	}

	for _, step := range [][]string{{"report", "batch-end"}, {"report", "suite"}, {"lock", "release", "browsers"}, {"report", "finish"}} {
		if stdout, stderr, code := run(t, append(step, "-root", root)...); code != 0 {
			t.Fatalf("%v: %d %s %s", step, code, stdout, stderr)
		}
	}

	if got, err := report.RunState(t.Context(), s, nil, id); err != nil || got != "success" {
		t.Fatalf("the run's state in the store: %q %v", got, err)
	}

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	base, inFlight, err := lastPassed(t.Context(), cfg, runs.Change{Run: "main", ID: "main", Commit: second, Branch: "main"}, openStore(cfg), repo)
	if err != nil || base != first || inFlight {
		t.Fatalf("last passed: %s %v %v, want %s", base, inFlight, err, first)
	}
}

func revParse(t *testing.T, repo, rev string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", rev).Output()
	if err != nil {
		t.Fatal(err)
	}

	return strings.TrimSpace(string(out))
}

func TestRunsCheckBeforeTheyHaveAHost(t *testing.T) {
	r := testrepo.New(t)

	conf := readFile(t, filepath.Join(r.Dir, config.FileName))
	start, end := strings.Index(conf, "github:"), strings.Index(conf, "suites:")

	if start < 0 || end < start || !strings.Contains(conf, "\nruns:") {
		t.Fatalf("the fixture's config changed:\n%s", conf)
	}

	r.Write(config.FileName, conf[:start]+conf[end:])

	if out, stderr, code := run(t, "check", "--root", r.Dir, "--staged"); code != 0 || out != "" {
		t.Fatalf("check: %d %s %s", code, out, stderr)
	}
}

func TestAffectedReadsListsOnlyFromARealRemote(t *testing.T) {
	t.Setenv(envGitURL, "")
	t.Setenv(envToken, "")

	repo := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	cfg := &config.Config{
		Git:    config.Git{Origin: originOf(t.Context(), repo)},
		Suites: map[string]config.Suite{"e2e": {Tests: []string{"*.spec.ts"}, Bind: config.BindRecorded}},
	}
	if cfg.Git.Origin != "" {
		t.Fatalf("an origin out of nowhere: %s", cfg.Git.Origin)
	}

	var warn strings.Builder

	if l, _ := baseLists(t.Context(), cfg, repo, "0123456789abcdef0123456789abcdef01234567", listsRemote, &warn); len(l) != 0 ||
		!strings.Contains(warn.String(), "no remote to read the recordings from") || strings.Contains(warn.String(), "github.com") {
		t.Fatalf("no remote: %v %s", l, warn.String())
	}

	remote := storetest.Bare(t)
	if out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "remote", "add", "origin", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if got := originOf(t.Context(), repo); got != remote {
		t.Fatalf("origin: %q, want %q", got, remote)
	}

	cfg.Git.Origin = ""
	withOrigin(t.Context(), cfg, repo)

	if got := codeRemote(cfg); got != remote {
		t.Fatalf("code remote: %q", got)
	}

	warn.Reset()

	if _, _ = baseLists(t.Context(), cfg, repo, "0123456789abcdef0123456789abcdef01234567", listsRemote, &warn); strings.Contains(warn.String(), "no remote") {
		t.Fatalf("with an origin: %s", warn.String())
	}

	if out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "remote", "set-url", "origin", "https://u:secret@git.example.test/r.git").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if got := originOf(t.Context(), repo); got != "https://git.example.test/r.git" {
		t.Fatalf("origin with a password: %q", got)
	}
}

func TestWhyNeedsNoRemote(t *testing.T) {
	testrepo.Parallel(t)

	bd := bazeldifftest.Tool(t).Path

	r := testrepo.New(t)

	conf := readFile(t, filepath.Join(r.Dir, config.FileName))
	r.Write(config.FileName, strings.Replace(conf, "github: {repository: owner/fixture, token: github_token}\n", "", 1))
	base := r.Commit("no host yet")

	r.Write("api/money/money.go", "package money // changed\n")
	r.Commit("money")

	env := map[string]string{envGitURL: "", envToken: ""}

	if _, stderr, code := runIn(t, r.Dir, env, "why", "-root", r.Dir, "-run", "pr", "-change", "pr-7", "-slice", "e2e.orders"); code != 1 ||
		!strings.Contains(stderr, "a kept plan is read from the repository's remote") || strings.Contains(stderr, "github.com") {
		t.Fatalf("kept plan with no remote: %d %s", code, stderr)
	}

	cache := t.TempDir()
	testrepo.Bazel(t, filepath.Join(cache, "tree"))

	stdout, stderr, code := runIn(t, r.Dir, env, "why", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-slice", "e2e/checkout.spec.ts")
	if code != 0 || !strings.HasPrefix(stdout, "specs e2e/checkout.spec.ts was selected from "+base[:12]+" to ") ||
		strings.Contains(stderr, "no remote") || strings.Contains(stderr, "github.com") {
		t.Fatalf("no remote: %d %s\n%s", code, stderr, stdout)
	}

	stdout, stderr, code = runIn(t, r.Dir, env, "why", "-root", r.Dir, "-base", base, "-cache", cache, "-bazel-diff", bd, "-slice", "e2e/checkout.spec.ts", "-no-lists")
	if code != 0 || !strings.HasPrefix(stdout, "specs e2e/checkout.spec.ts was selected from ") || strings.Contains(stderr, "no remote") {
		t.Fatalf("--no-lists: %d %s\n%s", code, stderr, stdout)
	}
}
