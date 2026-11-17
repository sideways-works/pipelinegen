package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
)

const reportConfig = `
pipeline: shop
suites:
  e2e:
    tag: slice-e2e
    run_slice: ci/tasks/e2e-slice.yml
    lock: {name: browsers, minutes: 90, slots: 1}
github: {repository: o/r, token: gh}
concourse: {url: "https://ci", repository: {uri: x}, planner: {image: {repository: p}, digest: "sha256:x", bootstrap: {release: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}, bazelisk: {version: v1, sha256: 0000000000000000000000000000000000000000000000000000000000000000}}}}
runs:
  pr:
    trigger: {pull_requests: {base: main, draft: false}}
    compare: merged_target
    select: affected
    suites: {e2e: {}}
    report: {comment: {format: list}}
`

type githubAPI struct {
	mu       sync.Mutex
	statuses map[string]string // context: state description
	comments map[int]map[int64]string
	next     int64
}

func (g *githubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()

	var body struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}

	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.Method != http.MethodGet && r.Method != http.MethodDelete {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}
	}

	var pr int

	switch {
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/statuses/"):
		g.statuses[body.Context] = body.State + " " + body.Description
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status"):
		out := []map[string]string{}

		for ctx, st := range g.statuses {
			state, desc, _ := strings.Cut(st, " ")
			out = append(out, map[string]string{"context": ctx, "state": state, "description": desc})
		}

		if err := json.NewEncoder(w).Encode(map[string]any{"statuses": out}); err != nil {
			panic(err)
		}
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments"):
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/%d/comments", &pr); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)

			return
		}

		out := []map[string]any{}
		for id, b := range g.comments[pr] {
			out = append(out, map[string]any{"id": id, "body": b})
		}

		if err := json.NewEncoder(w).Encode(out); err != nil {
			panic(err)
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/%d/comments", &pr); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)

			return
		}

		if g.comments[pr] == nil {
			g.comments[pr] = map[int64]string{}
		}

		g.next++
		g.comments[pr][g.next] = body.Body
	case r.Method == http.MethodDelete:
		var id int64
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/comments/%d", &id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)

			return
		}

		for _, cs := range g.comments {
			delete(cs, id)
		}

		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPatch:
		var id int64
		if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/comments/%d", &id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)

			return
		}

		for _, cs := range g.comments {
			if _, ok := cs[id]; ok {
				cs[id] = body.Body
			}
		}
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func (g *githubAPI) comment(pr int) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, b := range g.comments[pr] {
		return b
	}

	return ""
}

func (g *githubAPI) status(ctx string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.statuses[ctx]
}

func runEnv(tb testing.TB) (string, *githubAPI, *gitstore.Store) {
	tb.Helper()

	root, api, s, env := runEnvFor(tb)

	for k, v := range env {
		tb.Setenv(k, v)
	}

	return root, api, s
}

func runEnvFor(tb testing.TB) (string, *githubAPI, *gitstore.Store, map[string]string) {
	tb.Helper()

	root := tb.TempDir()

	cfg := reportConfig
	if onBranchStore {
		cfg += "store: {backend: branch}\n"
	}

	if err := os.WriteFile(filepath.Join(root, "pipelinegen.yaml"), []byte(cfg), 0o600); err != nil {
		tb.Fatal(err)
	}

	git := tb.TempDir()
	bare := filepath.Join(git, "o", "r.git")

	// the branch store reads by partial fetches, which the host serves.
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", bare},
		{"-C", bare, "config", "uploadpack.allowFilter", "true"},
		{"-C", bare, "config", "uploadpack.allowAnySHA1InWant", "true"},
	} {
		if out, err := exec.CommandContext(tb.Context(), "git", args...).CombinedOutput(); err != nil {
			tb.Fatalf("%v: %s", err, out)
		}
	}

	api := &githubAPI{statuses: map[string]string{}, comments: map[int]map[int64]string{}}
	srv := httptest.NewServer(api)
	tb.Cleanup(srv.Close)

	env := map[string]string{
		envGitURL: "file://" + git, envAPIURL: srv.URL, envToken: "t",
		envRun: "pr", envChange: "pr-7", envCommit: "0123456789abcdef0123456789abcdef01234567",
		envPR: "7", envAlsoTo: "", envSuite: "e2e", envBatch: "1", envJobURL: "https://ci/jobs/e2e",
		envSlice: "", envLabel: "",
	}

	s := &gitstore.Store{Remote: "file://" + git + "/o/r.git", Dir: filepath.Join(tb.TempDir(), "scratch")}
	if onBranchStore {
		s.Branch = &branchstore.Store{Remote: s.Remote, Branch: config.DefaultStoreBranch, Dir: filepath.Join(tb.TempDir(), "branch")}
	}

	tb.Cleanup(func() {
		if err := s.Close(); err != nil {
			tb.Error(err)
		}
	})

	return root, api, s, env
}

func writeFile(tb testing.TB, p, body string) {
	tb.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		tb.Fatal(err)
	}

	if err := os.WriteFile(filepath.Clean(p), []byte(body), 0o600); err != nil {
		tb.Fatal(err)
	}
}

var onBranchStore bool

func onBranch(t *testing.T, test func(t *testing.T)) {
	t.Helper()

	onBranchStore = true

	t.Cleanup(func() { onBranchStore = false })

	test(t)
}

func TestARunReportsThroughItsCommands(t *testing.T) { reportsThroughItsCommands(t) }

func TestARunReportsThroughItsCommandsOnABranch(t *testing.T) {
	onBranch(t, reportsThroughItsCommands)
}

func reportsThroughItsCommands(t *testing.T) {
	t.Helper()

	root, api, s := runEnv(t)
	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}

	plan := `{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}], "untouched": ["//e2e:c"], "whole_suite": []}]}`
	if err := report.WritePlan(t.Context(), s, id, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "report", "start", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}

	if got := api.status("pipelinegen/pr/e2e"); got != "pending 0 of 2 slices done, 1 untouched" {
		t.Fatalf("pending: %q", got)
	}

	build := t.TempDir()
	t.Chdir(build)
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)

	if _, stderr, code := run(t, "slice", "start", "e2e.a", "-root", root); code != 0 {
		t.Fatalf("slice start: %d %s", code, stderr)
	}

	if body := api.comment(7); strings.Contains(body, "running") {
		t.Fatalf("running before a flush:\n%s", body)
	}

	if _, err := os.Stat(filepath.Join("result", ".pipelinegen", "journal.jsonl")); err != nil {
		t.Fatalf("no journal: %v", err)
	}

	writeFile(t, "result/e2e.a.json", `{"passed": false, "failures": [{"test": "pays"}]}`)
	writeFile(t, "result/e2e.a/screens.json", `["shot"]`)

	if stdout, stderr, code := run(t, "slice", "end", "e2e.a", "-root", root); code != 0 || stdout != "slice e2e.a: failed\n" {
		t.Fatalf("slice end: %d %q %s", code, stdout, stderr)
	}

	if body := api.comment(7); strings.Contains(body, "pays") {
		t.Fatalf("written by the slice's end:\n%s", body)
	}

	if stdout, stderr, code := run(t, "slice", "flush", "-root", root); code != 0 || stdout != "flushed 1 slice\n" {
		t.Fatalf("slice flush: %d %q %s", code, stdout, stderr)
	}

	if body := api.comment(7); !strings.Contains(body, "e2e.a") || !strings.Contains(body, "pays") {
		t.Fatalf("failed slice not shown:\n%s", body)
	}

	if stdout, stderr, code := run(t, "slice", "flush", "-root", root); code != 0 || stdout != "flushed 0 slices\n" {
		t.Fatalf("slice flush: %d %q %s", code, stdout, stderr)
	}

	if _, stderr, code := run(t, "report", "batch-end", "-root", root); code != 1 || !strings.Contains(stderr, "e2e.a failed, e2e.b errored") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if _, _, code := run(t, "report", "suite", "-root", root); code != 1 {
		t.Fatalf("suite: %d", code)
	}

	if _, stderr, code := run(t, "report", "collect", "-root", root, "-out", "slices"); code != 0 {
		t.Fatalf("collect: %d %s", code, stderr)
	}

	if got, err := os.ReadFile(filepath.Join("slices", "e2e.a", "extra", "screens.json")); err != nil || string(got) != `["shot"]` {
		t.Fatalf("collected extras: %q %v", got, err)
	}

	if got := api.status("pipelinegen/pr"); !strings.HasPrefix(got, "error ") {
		t.Fatalf("run status: %q", got)
	}

	if _, _, code := run(t, "report", "finish", "-root", root); code != 1 {
		t.Fatalf("finish: %d", code)
	}

	if _, stderr, code := run(t, "also-to", "-root", root, "-run", "pr", "-change", "pr-7", "8", "9"); code != 0 {
		t.Fatalf("also-to: %d %s", code, stderr)
	}

	if _, _, code := run(t, "report", "finish", "-root", root); code != 1 || api.comment(9) == "" {
		t.Fatal("no comment on the pull request named later")
	}
}

func TestLockCommands(t *testing.T) { lockCommands(t) }

func TestLockCommandsOnABranch(t *testing.T) { onBranch(t, lockCommands) }

func lockCommands(t *testing.T) {
	t.Helper()

	root, _, _ := runEnv(t)

	stdout, stderr, code := run(t, "lock", "acquire", "browsers", "-root", root)
	if code != 0 || !strings.Contains(stdout, "lock browsers: slot 1 until") {
		t.Fatalf("acquire: %d %s %s", code, stdout, stderr)
	}

	t.Setenv(envBatch, "2")

	if _, stderr, code := run(t, "lock", "acquire", "browsers", "-wait", "100ms", "-root", root); code != 1 || !strings.Contains(stderr, "o/r pr pr-7 0123456789ab e2e batch 1") {
		t.Fatalf("second acquire: %d %s", code, stderr)
	}

	if stdout, _, code := run(t, "lock", "release", "browsers", "-root", root); code != 0 || !strings.Contains(stdout, "0 slots freed") {
		t.Fatalf("release by another: %d %s", code, stdout)
	}

	if stdout, _, code := run(t, "unlock", "browsers", "1", "-root", root); code != 0 || !strings.Contains(stdout, "slot 1 freed, held by o/r pr pr-7 0123456789ab e2e batch 1") {
		t.Fatalf("unlock: %d %s", code, stdout)
	}

	if _, _, code := run(t, "lock", "acquire", "nope", "-root", root); code != 2 {
		t.Fatalf("unknown lock: %d", code)
	}
}

func TestARecordingBatchEndsItsSetupFirst(t *testing.T) {
	root, _, s := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(
		strings.Replace(reportConfig, "    tag: slice-e2e\n", "    tag: slice-e2e\n    bind: recorded\n", 1),
		"    report: {comment: {format: list}}\n", "    report: {comment: {format: list}}\n    record: true\n", 1)+"plugins:\n  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}\n")

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}]`)

	if _, stderr, code := run(t, "slice", "start", "e2e.a", "-root", root); code != 2 || !strings.Contains(stderr, "slice setup-end") {
		t.Fatalf("start before the setup's end: %d %s", code, stderr)
	}

	if stdout, stderr, code := run(t, "slice", "setup-end", "-root", root); code != 0 || stdout != "setup ended\n" {
		t.Fatalf("setup-end: %d %q %s", code, stdout, stderr)
	}

	if _, stderr, code := run(t, "slice", "start", "e2e.a", "-root", root); code != 0 {
		t.Fatalf("start: %d %s", code, stderr)
	}
}

func TestAStackNeedsACollector(t *testing.T) {
	root, _, s := runEnv(t)
	recording := strings.Replace(
		strings.Replace(reportConfig, "    tag: slice-e2e\n", "    tag: slice-e2e\n    bind: recorded\n    stack: true\n", 1),
		"    report: {comment: {format: list}}\n", "    report: {comment: {format: list}}\n    record: true\n", 1) + "plugins:\n  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}\n"
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), recording)

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}]`)

	for _, args := range [][]string{{"slice", "setup-end", "-root", root}, {"slice", "start", "e2e.a", "-root", root}} {
		if _, stderr, code := run(t, args...); code != 1 || !strings.Contains(stderr, "suite e2e runs a stack (stack: true), but no configured correlator collects") {
			t.Fatalf("%v: %d %s", args, code, stderr)
		}
	}

	plugins := "  - {name: go, kind: correlator, repository: example/go-correlator, commit: 0123456789abcdef0123456789abcdef01234567}\n"
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), recording+plugins)
	writeFile(t, "tools/plugins/go", "#!/bin/sh\ncat >/dev/null\ncase $1 in\n"+
		"describe) echo '{\"protocol\": 1, \"kind\": \"correlator\", \"name\": \"go\", \"capabilities\": [\"collect\"]}' ;;\n"+
		"collect) echo '{}' ;;\nesac\n")

	if err := os.Chmod("tools/plugins/go", 0o700); err != nil { //nolint:gosec // the test's own plugin
		t.Fatal(err)
	}

	if stdout, stderr, code := run(t, "slice", "setup-end", "-root", root); code != 0 || stdout != "setup ended\n" {
		t.Fatalf("setup-end with a collector: %d %s", code, stderr)
	}

	if _, stderr, code := run(t, "slice", "start", "e2e.a", "-root", root); code != 0 {
		t.Fatalf("start with a collector: %d %s", code, stderr)
	}
}

func TestALeaseLastsTheRunsLockMinutes(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		run     string
		minutes int
	}{
		{"{e2e: {}}", 90},
		{"{e2e: {lock_minutes: 600}}", 600},
	} {
		root, _, _, env := runEnvFor(t)
		writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(reportConfig, "    suites: {e2e: {}}\n", "    suites: "+c.run+"\n", 1))

		before := time.Now()

		stdout, stderr, code := runIn(t, root, env, "lock", "acquire", "browsers", "-root", root)
		if code != 0 {
			t.Fatalf("acquire: %d %s", code, stderr)
		}

		_, until, ok := strings.Cut(strings.TrimSpace(stdout), " until ")
		if !ok {
			t.Fatalf("acquire said %q", stdout)
		}

		expires, err := time.Parse(time.RFC3339, until)
		if err != nil {
			t.Fatal(err)
		}

		want := before.Add(time.Duration(c.minutes)*time.Minute + leaseMargin)
		if d := expires.Sub(want); d < -time.Minute || d > time.Minute {
			t.Errorf("run suites %s: the lease ends %s, want about %s", c.run, expires, want)
		}

		env[envBatch] = "2"
		if _, stderr, code := runIn(t, root, env, "lock", "acquire", "browsers", "-wait", "100ms", "-root", root); code != 1 {
			t.Errorf("run suites %s: a second holder took the lock: %d %s", c.run, code, stderr)
		}
	}
}

func TestABatchWhoseSetupFailedIsToldOnce(t *testing.T) {
	root, api, s := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(
		strings.Replace(reportConfig, "    tag: slice-e2e\n", "    tag: slice-e2e\n    bind: recorded\n", 1),
		"    report: {comment: {format: list}}\n", "    report: {comment: {format: list}}\n    record: true\n", 1)+
		"plugins:\n  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}\n"+
		"  - {name: go, kind: correlator, repository: example/go-correlator, commit: 0123456789abcdef0123456789abcdef01234567}\n")

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}], "untouched": ["//e2e:c"]}]}`)); err != nil {
		t.Fatal(err)
	}

	build := t.TempDir()
	t.Chdir(build)
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)
	writeFile(t, "tools/plugins/go", "#!/bin/sh\ncat >/dev/null\ncase $1 in\n"+
		"describe) echo '{\"protocol\": 1, \"kind\": \"correlator\", \"name\": \"go\", \"capabilities\": [\"observe\"]}' ;;\n"+
		"observe) echo observed >> '"+filepath.Join(build, "observed.log")+"'; echo 'no coverprofile' >&2; exit 3 ;;\nesac\n")

	if err := os.Chmod("tools/plugins/go", 0o700); err != nil { //nolint:gosec // the test's own plugin
		t.Fatal(err)
	}

	if err := os.MkdirAll("coverage", 0o750); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := run(t, "report", "batch-end", "-root", root, "-coverage", "coverage")
	if code != 1 || strings.Count(stderr, "batch 1 recorded nothing: its setup failed before any slice ran") != 1 ||
		!strings.Contains(stderr, "none of its 2 slices ran") || strings.Contains(stderr, "could not tell") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if _, err := os.Stat("observed.log"); err == nil {
		t.Fatal("a correlator observed a batch that ran nothing")
	}

	if _, _, code := run(t, "report", "suite", "-root", root); code != 1 {
		t.Fatalf("suite: %d", code)
	}

	if got := api.status("pipelinegen/pr/e2e"); got != "error 0 of 3 slices ran, 2 not run (a batch's setup failed)" {
		t.Fatalf("status: %q", got)
	}

	if body := api.comment(7); !strings.Contains(body, "**not run** 2 slices of e2e: its batch ended before any slice started") || strings.Contains(body, "**failed**") {
		t.Fatalf("comment:\n%s", body)
	}
}

func TestABatchThatRecordedNothingIsToldOnce(t *testing.T) {
	root, _, s := runEnv(t)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(
		strings.Replace(reportConfig, "    tag: slice-e2e\n", "    tag: slice-e2e\n    bind: recorded\n", 1),
		"    report: {comment: {format: list}}\n", "    report: {comment: {format: list}, flush: 1h}\n    record: true\n", 1)+
		"plugins:\n  - {name: units, kind: sbom, repository: example/units, commit: 0123456789abcdef0123456789abcdef01234567}\n"+
		"  - {name: go, kind: correlator, repository: example/go-correlator, commit: 0123456789abcdef0123456789abcdef01234567}\n")

	id := report.ID{Run: "pr", Change: "pr-7", Commit: "0123456789abcdef0123456789abcdef01234567"}
	if err := report.WritePlan(t.Context(), s, id, []byte(`{"suites": [{"suite": "e2e", "selected": [{"label": "//e2e:a"}, {"label": "//e2e:b"}]}]}`)); err != nil {
		t.Fatal(err)
	}

	build := t.TempDir()
	t.Chdir(build)
	writeFile(t, "batches/1/slices.json", `[{"name": "e2e.a", "label": "//e2e:a"}, {"name": "e2e.b", "label": "//e2e:b"}]`)
	writeFile(t, "tools/plugins/go", "#!/bin/sh\ncat >/dev/null\ncase $1 in\n"+
		"describe) echo '{\"protocol\": 1, \"kind\": \"correlator\", \"name\": \"go\", \"capabilities\": [\"observe\"]}' ;;\n"+
		"observe) echo observed >> '"+filepath.Join(build, "observed.log")+"'; echo 'no coverprofile' >&2; exit 3 ;;\nesac\n")

	if err := os.Chmod("tools/plugins/go", 0o700); err != nil { //nolint:gosec // the test's own plugin
		t.Fatal(err)
	}

	if _, stderr, code := run(t, "slice", "setup-end", "-root", root); code != 0 {
		t.Fatalf("setup-end: %d %s", code, stderr)
	}

	for _, name := range []string{"e2e.a", "e2e.b"} {
		if _, stderr, code := run(t, "slice", "start", name, "-root", root); code != 0 {
			t.Fatalf("start: %d %s", code, stderr)
		}

		writeFile(t, "result/"+name+".json", `{"passed": true}`)

		if _, stderr, code := run(t, "slice", "end", name, "-root", root); code != 0 && code != 1 {
			t.Fatalf("end: %d %s", code, stderr)
		}
	}

	if _, err := os.Stat("observed.log"); err == nil {
		t.Skip("the slices' ends were flushed before the batch's end")
	}

	_, stderr, code := run(t, "report", "batch-end", "-root", root, "-coverage", "coverage")
	if code != 1 || strings.Count(stderr, "batch 1 recorded nothing: no slice left coverage in ") != 1 || strings.Contains(stderr, "could not tell") {
		t.Fatalf("batch-end: %d %s", code, stderr)
	}

	if _, err := os.Stat("observed.log"); err == nil {
		t.Fatal("a correlator observed a batch that recorded nothing")
	}
}
