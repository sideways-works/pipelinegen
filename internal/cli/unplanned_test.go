package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

type openPulls []map[string]any

type statusPulls struct {
	pulls openPulls
	set   chan string
}

func (o statusPulls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/repos/o/r/statuses/") {
		var st struct {
			State       string `json:"state"`
			Context     string `json:"context"`
			Description string `json:"description"`
		}

		if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		o.set <- strings.TrimPrefix(r.URL.Path, "/repos/o/r/statuses/") + " " + st.State + " " + st.Context + " " + st.Description

		w.WriteHeader(http.StatusCreated)

		if _, err := w.Write([]byte("{}")); err != nil {
			panic(err)
		}

		return
	}

	o.pulls.ServeHTTP(w, r)
}

func (o openPulls) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body any

	switch {
	case r.URL.Path == "/repos/o/r/pulls" && r.URL.Query().Get("base") == "main" && r.URL.Query().Get("state") == "open":
		body = o
	case strings.HasPrefix(r.URL.Path, "/repos/o/r/pulls/"):
		for _, p := range o {
			if r.URL.Path == fmt.Sprintf("/repos/o/r/pulls/%v", p["number"]) {
				body = p
			}
		}
	}

	if body == nil {
		http.Error(w, "unexpected "+r.URL.String(), http.StatusNotFound)

		return
	}

	if err := json.NewEncoder(w).Encode(body); err != nil {
		panic(err)
	}
}

// openPull is a pull request onto main of o/r, whose repository id is 1:
// a head in another repository has another id.
func openPull(number int, sha string, updated time.Time, draft bool, repo string) map[string]any {
	id := 1
	if repo != "o/r" {
		id = 2
	}

	return map[string]any{
		"number": number, "state": "open", "draft": draft, "updated_at": updated, "created_at": updated,
		"head": map[string]any{"sha": sha, "ref": fmt.Sprintf("topic-%d", number), "repo": map[string]any{"id": id, "full_name": repo}},
		"base": map[string]any{"ref": "main", "repo": map[string]any{"id": 1, "full_name": "o/r"}},
	}
}

func pushHead(t *testing.T, remote string, pr int) string {
	t.Helper()

	dir := t.TempDir()
	gitIn := func(args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir

		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	gitIn("init", "-q")
	writeFile(t, filepath.Join(dir, "f.txt"), fmt.Sprintf("head of %d\n", pr))
	gitIn("add", "f.txt")
	gitIn("commit", "-q", "-m", "head")
	gitIn("push", "-q", remote, fmt.Sprintf("HEAD:refs/pull/%d/head", pr))

	return gitIn("rev-parse", "HEAD")
}

func TestUnplannedListsMissedAndStalledHeads(t *testing.T) { unplannedListsMissedAndStalledHeads(t) }

func TestUnplannedListsMissedAndStalledHeadsOnABranch(t *testing.T) {
	onBranch(t, unplannedListsMissedAndStalledHeads)
}

func unplannedListsMissedAndStalledHeads(t *testing.T) {
	t.Helper()

	root, _, s := runEnv(t)

	cfg := strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"pull_requests: {base: main, draft: false}", "pull_requests: {base: main, draft: false, missed_after: 1h}", 1)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)

	remote := strings.TrimPrefix(s.Remote, "file://")
	long, now := time.Now().Add(-3*time.Hour), time.Now().Add(time.Hour)

	heads := map[int]string{}
	for _, pr := range []int{7, 8, 9, 10, 11, 12} {
		heads[pr] = pushHead(t, remote, pr)
	}

	api := httptest.NewServer(openPulls{
		openPull(7, heads[7], long, false, "o/r"),   // missed: plan it
		openPull(8, heads[8], now, false, "o/r"),    // pushed just now
		openPull(9, heads[9], long, false, "o/r"),   // planned, no suite: again
		openPull(10, heads[10], long, false, "o/r"), // planned and run
		openPull(11, heads[11], long, true, "o/r"),  // a draft
		openPull(12, heads[12], long, false, "f/r"), // a fork's
	})
	t.Cleanup(api.Close)
	t.Setenv(envAPIURL, api.URL)

	plan := []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)

	for _, pr := range []int{9, 10} {
		id := report.ID{Run: "pr", Change: fmt.Sprintf("pr-%d", pr), Commit: heads[pr], PR: pr}
		if err := report.WritePlan(t.Context(), pastStore(t, s), id, plan); err != nil {
			t.Fatal(err)
		}
	}

	id10 := report.ID{Run: "pr", Change: "pr-10", Commit: heads[10], PR: 10}
	if _, err := s.Write(t.Context(), "end", gitstore.Update{Ref: id10.EndRef("e2e"), Any: true, Files: map[string][]byte{"end.json": []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "sweep")

	stdout, stderr, code := run(t, "unplanned", "-root", root, "-run", "pr", "-out", out, "-cache", t.TempDir())
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}

	for _, want := range []string{
		"#7 " + heads[7][:12] + ": no plan: plan it",
		"#8 " + heads[8][:12] + ": waits for its own plan",
		"#9 " + heads[9][:12] + ": planned, no suite began: hand it off again",
		"#10 " + heads[10][:12] + ": planned, a suite began",
	} {
		if !strings.Contains(stdout, want+"\n") {
			t.Errorf("no %q in\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, "#11") || strings.Contains(stdout, "#12") {
		t.Errorf("a draft or a fork listed:\n%s", stdout)
	}

	entries := func(dir string) string {
		list, _ := os.ReadDir(filepath.Join(out, dir)) //nolint:errcheck // none is empty
		names := make([]string, 0, len(list))

		for _, e := range list {
			names = append(names, e.Name())
		}

		return strings.Join(names, ",")
	}

	if got := entries("plan") + " " + entries("handoff"); got != "7 9" {
		t.Fatalf("directories %q", got)
	}

	ch, err := runsFromTrigger(t, root, filepath.Join(out, "plan", "7"))
	if err != nil || ch.PR != 7 || ch.Commit != heads[7] || ch.Branch != "topic-7" || ch.ID != "pr-7" {
		t.Errorf("checkout of 7: %+v %v", ch, err)
	}

	if got := gitOut(t, filepath.Join(out, "plan", "7"), "rev-parse", "HEAD"); got != heads[7] {
		t.Errorf("checkout of 7 at %s", got)
	}

	if got := gitOut(t, filepath.Join(out, "plan", "7"), "remote", "get-url", "origin"); got != s.Remote {
		t.Errorf("origin of 7: %s", got)
	}

	var v map[string]string
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, "handoff", "9", ".git", "resource", "version.json"))), &v); err != nil ||
		v["pr"] != "9" || v["commit"] != heads[9] {
		t.Errorf("hand-off of 9: %v %v", v, err)
	}

	var meta []map[string]string
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(out, "handoff", "9", ".git", "resource", "metadata.json"))), &meta); err != nil || len(meta) == 0 {
		t.Errorf("metadata of 9: %v %v", meta, err)
	}
}

func TestUnplannedNeedsAPullRequestRun(t *testing.T) {
	root, _, _ := runEnv(t)

	if _, stderr, code := run(t, "unplanned", "-root", root, "-run", "nightly"); code != 2 || !strings.Contains(stderr, "no pull request run") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}

	return strings.TrimSpace(string(out))
}

func runsFromTrigger(t *testing.T, root, dir string) (runs.Change, error) {
	t.Helper()

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	return runs.FromTrigger(t.Context(), cfg, "pr", dir)
}

func TestAFirstPipelinesEarlierHandOffsGoAgain(t *testing.T) {
	root, _, s := runEnv(t)

	cfg := strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"pull_requests: {base: main, draft: false}", "pull_requests: {base: main, draft: false, missed_after: 1h}", 1)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)

	remote := strings.TrimPrefix(s.Remote, "file://")
	long := time.Now().Add(-3 * time.Hour)
	plan := []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)

	var pulls openPulls

	heads := map[int]string{}

	for _, pr := range []int{20, 21, 22, 23} {
		heads[pr] = pushHead(t, remote, pr)
		pulls = append(pulls, openPull(pr, heads[pr], long, false, "o/r"))

		id := report.ID{Run: "pr", Change: fmt.Sprintf("pr-%d", pr), Commit: heads[pr], PR: pr}
		if err := report.WritePlan(t.Context(), pastStore(t, s), id, plan); err != nil {
			t.Fatal(err)
		}
	}

	newest := report.ID{Run: "pr", Change: "pr-23", Commit: heads[23], PR: 23}
	if _, err := s.Write(t.Context(), "end", gitstore.Update{Ref: newest.EndRef("e2e"), Any: true, Files: map[string][]byte{"end.json": []byte("{}\n")}}); err != nil {
		t.Fatal(err)
	}

	api := httptest.NewServer(pulls)
	t.Cleanup(api.Close)
	t.Setenv(envAPIURL, api.URL)

	out := filepath.Join(t.TempDir(), "sweep")
	if _, stderr, code := run(t, "unplanned", "-root", root, "-run", "pr", "-out", out, "-cache", t.TempDir()); code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}

	list, err := os.ReadDir(filepath.Join(out, "handoff"))
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(list))
	for _, e := range list {
		got = append(got, e.Name())
	}

	if strings.Join(got, ",") != "20,21,22" {
		t.Fatalf("handed off again: %v", got)
	}

	if _, err := os.Stat(filepath.Join(out, "plan")); err == nil {
		t.Error("a planned head was planned again")
	}
}

func stalledHead(t *testing.T) (string, *gitstore.Store, string, chan string) {
	t.Helper()

	root, _, s := runEnv(t)

	cfg := strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"pull_requests: {base: main, draft: false}", "pull_requests: {base: main, draft: false, missed_after: 1h}", 1)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)

	head := pushHead(t, strings.TrimPrefix(s.Remote, "file://"), 9)
	id := report.ID{Run: "pr", Change: "pr-9", Commit: head, PR: 9}

	if err := report.WritePlan(t.Context(), pastStore(t, s), id, []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	set := make(chan string, 10)
	api := httptest.NewServer(statusPulls{pulls: openPulls{openPull(9, head, time.Now().Add(-3*time.Hour), false, "o/r")}, set: set})
	t.Cleanup(api.Close)
	t.Setenv(envAPIURL, api.URL)

	return root, s, head, set
}

// pastStore writes as if hours ago, so a plan has waited any missed_after with no sleep and no window a
// slow machine could miss.
func pastStore(t *testing.T, s *gitstore.Store) *gitstore.Store {
	t.Helper()

	past := func() time.Time { return time.Now().Add(-3 * time.Hour) }

	p := &gitstore.Store{Remote: s.Remote, Token: s.Token, Env: s.Env, Clock: past}
	if s.Branch != nil {
		p.Branch = &branchstore.Store{Remote: s.Branch.Remote, Branch: s.Branch.Branch, Dir: filepath.Join(t.TempDir(), "branch"), Clock: past}
	}

	t.Cleanup(func() { _ = p.Close() }) //nolint:errcheck // a scratch repository

	return p
}

func sweepOnce(t *testing.T, root, cache string) (string, string) {
	t.Helper()

	out := filepath.Join(t.TempDir(), "sweep")

	stdout, stderr, code := run(t, "unplanned", "-root", root, "-run", "pr", "-out", out, "-cache", cache)
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}

	return stdout, out
}

func TestTheSweepTakesAHeadUpOnceThenWaits(t *testing.T) {
	root, _, head, _ := stalledHead(t)
	cache := t.TempDir()

	stdout, out := sweepOnce(t, root, cache)
	if _, err := os.Stat(filepath.Join(out, "handoff", "9")); err != nil {
		t.Fatalf("first sweep: no hand-off: %v\n%s", err, stdout)
	}

	stdout, out = sweepOnce(t, root, cache)
	if _, err := os.Stat(filepath.Join(out, "handoff", "9")); err == nil {
		t.Fatalf("second sweep handed off again:\n%s", stdout)
	}

	if !strings.Contains(stdout, "#9 "+head[:12]+": taken up 1 times") {
		t.Errorf("second sweep:\n%s", stdout)
	}
}

func TestTheSweepGivesUpOnceAndSays(t *testing.T) {
	root, s, head, set := stalledHead(t)

	id := report.ID{Run: "pr", Change: "pr-9", Commit: head, PR: 9}
	mark := fmt.Sprintf(`{"at": %q, "times": 3}`, time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339))

	if _, err := s.Write(t.Context(), "mark", gitstore.Update{Ref: report.SweptRef(id), Any: true, Files: map[string][]byte{"swept.json": []byte(mark)}}); err != nil {
		t.Fatal(err)
	}

	cache := t.TempDir()

	stdout, out := sweepOnce(t, root, cache)
	if _, err := os.Stat(filepath.Join(out, "handoff", "9")); err == nil {
		t.Fatalf("handed off after the last time:\n%s", stdout)
	}

	select {
	case got := <-set:
		if got != head+" error pipelinegen/pr planned, but no suite began after 3 hand-offs: see the suite jobs" {
			t.Errorf("status %q", got)
		}
	default:
		t.Fatalf("no status said so:\n%s", stdout)
	}

	stdout, _ = sweepOnce(t, root, cache)
	if !strings.Contains(stdout, "#9 "+head[:12]+": given up after 3 times") {
		t.Errorf("later sweep:\n%s", stdout)
	}

	select {
	case got := <-set:
		t.Errorf("said again: %q", got)
	default:
	}
}

func TestTheSweepDropsTheMarksOfClosedHeads(t *testing.T) {
	root, s, head, _ := stalledHead(t)

	gone := report.ID{Run: "pr", Change: "pr-5", Commit: head, PR: 5}
	if _, err := s.Write(t.Context(), "mark", gitstore.Update{Ref: report.SweptRef(gone), Any: true, Files: map[string][]byte{"swept.json": []byte(`{"times": 1}`)}}); err != nil {
		t.Fatal(err)
	}

	sweepOnce(t, root, t.TempDir())

	refs, err := s.List(t.Context(), report.SweptOf("pr"))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := refs[report.SweptRef(gone)]; ok || len(refs) != 1 {
		t.Errorf("marks after the sweep: %v", refs)
	}
}

func TestTwoSweepsAtOnceHandAHeadOffOnce(t *testing.T) {
	root, s, head, _ := stalledHead(t)
	cfg := loadConfig(t, root)

	h := runs.Head{PR: 9, Commit: head}
	id := headID("pr", h)

	var outs [2]bytes.Buffer

	sweeps := [2]sweep{}
	for i := range sweeps {
		sweeps[i] = sweep{cfg: cfg, out: t.TempDir(), stdout: &outs[i], now: time.Now()}
		writeFile(t, filepath.Join(sweeps[i].out, sweepHandOffDir, headDir(h), "version.json"), "{}")
	}

	for i := range sweeps {
		if err := sweeps[i].keep(t.Context(), s, h, sweeps[i].marked(report.SweptRef(id), swept{})); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(sweeps[0].out, sweepHandOffDir, headDir(h))); err != nil {
		t.Fatalf("the first sweep's hand-off: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sweeps[1].out, sweepHandOffDir, headDir(h))); err == nil || !strings.Contains(outs[1].String(), "another sweep or a planning build took it up first") {
		t.Fatalf("the second sweep hands the head off too: %v %q", err, outs[1].String())
	}
}
