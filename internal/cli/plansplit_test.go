package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
)

type prAndStatuses struct {
	pulls openPulls
	api   *githubAPI
}

func (h prAndStatuses) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/repos/o/r/pulls") {
		h.pulls.ServeHTTP(w, r)

		return
	}

	h.api.ServeHTTP(w, r)
}

func proposedChange(t *testing.T) (string, *gitstore.Store, string, runs.Change, *githubAPI) {
	t.Helper()

	root, api, s := runEnv(t)
	remote := strings.TrimPrefix(s.Remote, "file://")

	work := t.TempDir()
	gitIn := func(args ...string) string {
		t.Helper()

		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = work

		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@a", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@a")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	gitIn("init", "-q", "-b", "main")
	writeFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitIn("add", "base.txt")
	gitIn("commit", "-q", "-m", "base")
	gitIn("push", "-q", remote, "HEAD:refs/heads/main")
	writeFile(t, filepath.Join(work, "change.txt"), "change\n")
	gitIn("add", "change.txt")
	gitIn("commit", "-q", "-m", "change")
	gitIn("push", "-q", remote, "HEAD:refs/pull/7/head")

	head := runs.Head{PR: 7, Commit: gitIn("rev-parse", "HEAD"), Branch: "topic-7"}
	trigger := filepath.Join(t.TempDir(), "trigger")

	if err := runs.CheckoutHead(t.Context(), trigger, s.Remote, head, runs.Auth{}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(prAndStatuses{pulls: openPulls{openPull(7, head.Commit, time.Now(), false, "o/r")}, api: api})
	t.Cleanup(srv.Close)
	t.Setenv(envAPIURL, srv.URL)

	ch := runs.Change{Run: "pr", ID: "pr-7", Commit: head.Commit, PR: 7, Branch: "topic-7", Base: "main"}

	return root, s, trigger, ch, api
}

func loadConfig(t *testing.T, root string) *config.Config {
	t.Helper()

	cfg, err := config.Load(filepath.Join(root, config.FileName))
	if err != nil {
		t.Fatal(err)
	}

	return cfg
}

func TestTheSecondStepWritesOnlyThisChangesPlan(t *testing.T) {
	root, s, trigger, ch, api := proposedChange(t)
	cfg := loadConfig(t, root)

	abs, err := filepath.Abs(trigger)
	if err != nil {
		t.Fatal(err)
	}

	head, base, _, err := compareWith(t.Context(), cfg, ch, s, abs)
	if err != nil {
		t.Fatal(err)
	}

	plan := func(head, base string) string {
		return fmt.Sprintf(`{"base": %q, "head": %q, "all": false, "reason": "", "suites": [{"suite": "e2e", "all": false, "reason": "", "selected": [{"label": "//e2e:a", "sources": []}], "untouched": [], "whole_suite": []}]}`, base, head)
	}

	forged := t.TempDir()
	writeFile(t, filepath.Join(forged, planOutFile), plan(base, base))

	handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, forged, os.Stdout)
	if !errors.Is(err, errNotThisChange) || handOn {
		t.Fatalf("a forged plan: %v", err)
	}

	if _, ok, err := readPlan(t.Context(), s, reportID(ch)); err != nil || ok {
		t.Fatalf("a forged plan was written: %v %v", ok, err)
	}

	if got := api.status("pipelinegen/pr"); !strings.HasPrefix(got, "error ") {
		t.Errorf("run status after a forged plan: %q", got)
	}

	own := t.TempDir()
	writeFile(t, filepath.Join(own, planOutFile), plan(head, base))

	if handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, own, os.Stdout); err != nil || !handOn {
		t.Fatal(handOn, err)
	}

	if got, ok, err := readPlan(t.Context(), s, reportID(ch)); err != nil || !ok || got.Head != head {
		t.Fatalf("the plan written: %+v %v %v", got, ok, err)
	}

	if got := api.status("pipelinegen/pr/e2e"); !strings.HasPrefix(got, "pending ") {
		t.Errorf("suite status after the plan: %q", got)
	}

	if handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, forged, os.Stdout); err != nil || handOn {
		t.Fatal(handOn, err)
	}
}

func TestTheSecondStepShowsWhyThereIsNoPlan(t *testing.T) {
	root, s, trigger, ch, api := proposedChange(t)
	cfg := loadConfig(t, root)

	if handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, t.TempDir(), os.Stdout); err != nil || handOn {
		t.Fatal(handOn, err)
	}

	if got := api.status("pipelinegen/pr"); got != "" {
		t.Fatalf("an empty first step showed %q", got)
	}

	failed := t.TempDir()
	writeFile(t, filepath.Join(failed, planFailedOut), "the change doesn't merge\n")

	handOn, err := planFrom(t.Context(), cfg, ch, s, trigger, failed, os.Stdout)
	if err == nil || handOn || !strings.Contains(err.Error(), "the change doesn't merge") {
		t.Fatalf("a failed first step: %v", err)
	}

	if got := api.status("pipelinegen/pr"); !strings.HasPrefix(got, "error ") {
		t.Errorf("run status after a failed first step: %q", got)
	}
}

func TestTheFirstStepWritesNothing(t *testing.T) {
	root, s, trigger, ch, api := proposedChange(t)
	cfg := loadConfig(t, root)

	if err := report.WritePlan(t.Context(), s, reportID(ch), []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if err := planOut(t.Context(), cfg, ch, s, trigger, t.TempDir(), "", out, os.Stdout); err != nil {
		t.Fatal(err)
	}

	if list, err := os.ReadDir(out); err != nil || len(list) != 0 {
		t.Fatalf("planned already, yet the first step left %v %v", list, err)
	}

	if !s.ReadOnly {
		t.Fatal("the first step's store writes")
	}

	if _, err := s.Write(t.Context(), "x", gitstore.Update{Ref: "refs/pipelinegen/x", Any: true, Files: map[string][]byte{"f": []byte("x")}}); !errors.Is(err, gitstore.ErrReadOnly) {
		t.Fatalf("a write in the first step: %v", err)
	}

	if answersCache(t.TempDir(), s).Push {
		t.Error("the first step pushes plugin answers")
	}

	if got := api.status("pipelinegen/pr"); got != "" {
		t.Errorf("the first step set the run status %q", got)
	}
}

func TestPlanTakesOneStepAtATime(t *testing.T) {
	if _, stderr, code := run(t, "plan", "-out", "a", "-from", "b"); code != 2 || !strings.Contains(stderr, "two steps") {
		t.Fatalf("%d %s", code, stderr)
	}
}

func TestTheFirstStepReadsWithTheReadTokenAlone(t *testing.T) {
	root, s, trigger, ch, _ := proposedChange(t)

	cfg := strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"github: {repository: o/r, token: gh}", "github: {repository: o/r, token: gh, tokens: {read: reader}}", 1)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)
	t.Setenv("PIPELINEGEN_READ_TOKEN", "the-read-token")
	t.Setenv(envToken, "")

	t.Setenv("PIPELINEGEN_STORE_TOKEN", "the-store-token")

	if err := planOut(t.Context(), loadConfig(t, root), ch, s, trigger, t.TempDir(), "", t.TempDir(), os.Stdout); !errors.Is(err, errWritingCredential) || !strings.Contains(err.Error(), "PIPELINEGEN_STORE_TOKEN") {
		t.Fatalf("a store token in the first step: %v", err)
	}

	t.Setenv("PIPELINEGEN_STORE_TOKEN", "")

	if err := planOut(t.Context(), loadConfig(t, root), ch, s, trigger, t.TempDir(), "", t.TempDir(), os.Stdout); err != nil {
		t.Fatal(err)
	}

	if s.Token != "the-read-token" || !s.ReadOnly {
		t.Fatalf("the first step's store reads with %q, read only %v", s.Token, s.ReadOnly)
	}
}

func TestTheFirstStepPlansWhenGitHubWontAnswerTheReadToken(t *testing.T) {
	root, s, trigger, ch, _ := proposedChange(t)

	cfg := strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		"github: {repository: o/r, token: gh}", "github: {repository: o/r, tokens: {read: r, changes: c, report: p, store: s}}", 1)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)
	t.Setenv("PIPELINEGEN_READ_TOKEN", "the-read-token")
	t.Setenv("PIPELINEGEN_CHANGES_TOKEN", "")
	t.Setenv(envToken, "")

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the token can't read pull requests", http.StatusForbidden)
	}))
	t.Cleanup(refusing.Close)
	t.Setenv(envAPIURL, refusing.URL)

	out := t.TempDir()
	if err := planOut(t.Context(), loadConfig(t, root), ch, s, trigger, t.TempDir(), "", out, os.Stdout); err != nil {
		t.Fatal(err)
	}

	if list, err := os.ReadDir(out); err != nil || len(list) != 1 {
		t.Fatalf("the first step left %v %v, want a plan or why there is none", list, err)
	}
}
