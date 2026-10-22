package report

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/recording"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
	"github.com/sideways-works/pipelinegen/internal/lists"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func branchHistory(t *testing.T) (string, []string) {
	t.Helper()

	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")

		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git("init", "--quiet", "-b", "main")

	var out []string

	for range 2 {
		git("commit", "--quiet", "--allow-empty", "-m", "c")
		out = append(out, git("rev-parse", "HEAD"))
	}

	return dir, out
}

func testAttach(ctx context.Context, r *Reporter, end Ending) error {
	files, err := Recordings(ctx, r.Store, r.ID)
	if err != nil {
		return err
	}

	recs, marks, err := lists.Recordings(files)
	if err != nil {
		return err
	}

	states, err := SliceStates(ctx, r.Store, r.ID)
	if err != nil {
		return err
	}

	shrink, whole, told := map[string]bool{}, map[string]bool{}, lists.Marks{}

	for k := range recs {
		whole[k] = states[k] == Passed
		shrink[k] = end.All && whole[k]
	}

	for k, ms := range marks {
		if states[k] == Passed {
			told[k] = ms
		}
	}

	_, err = lists.Attach{Repo: r.Checkout, Run: r.ID.Run, Change: r.ID.Change, Commit: r.ID.Commit, To: r.ID.Commit,
		Recordings: recs, CannotTell: told, Shrinkable: shrink, Whole: whole, SuitesPassed: end.Suites, Trusted: true}.Do(ctx, r.Store)

	return err
}

func branchRun(t *testing.T) (*Reporter, *fakeGitHub, []string) {
	t.Helper()

	checkout, commits := branchHistory(t)
	gh := newFake()
	r := newReporter(t, bareRemote(t), gh, "")
	r.Config.Runs = map[string]config.Run{"nightly": {Trigger: config.Trigger{Branch: "main"}, Record: true}}
	r.ID = ID{Run: "nightly", Change: "main", Commit: commits[1]}
	r.Checkout = checkout
	r.Observer = fakeObserver{rec: []byte("a#func:New\n")}
	r.Attach = testAttach

	plan := `{"all": true, "suites": [{"suite": "go", "all": true, "selected": [{"label": "//a:a_test"}]}]}`
	if err := WritePlan(t.Context(), r.Store, r.ID, []byte(plan)); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Store.Write(t.Context(), "lists", gitstore.Update{Ref: lists.Ref(commits[0]), Files: lists.Encode(recording.Lists{
		"go/a.a_test": {"a#func:Old"}, "go/b.b_test": {"b#func:B"},
	})}); err != nil {
		t.Fatal(err)
	}

	return r, gh, commits
}

func TestAPassedBranchRunAttachesItsLists(t *testing.T) {
	ctx := t.Context()
	r, gh, commits := branchRun(t)

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if passed, err := r.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	if st, ok := gh.status(r.ID.Commit, "pipelinegen/nightly"); !ok || st.State != "success" || st.Description != "1 suite passed" {
		t.Fatalf("run status %+v", st)
	}

	got, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1])
	if err != nil || src.Back != 0 {
		t.Fatalf("lists of the run's commit: %v %+v", err, src)
	}

	if !slices.Equal(got["go/a.a_test"], []recording.ID{"a#func:New"}) || !slices.Equal(got["go/b.b_test"], []recording.ID{"b#func:B"}) {
		t.Fatalf("lists %v", got)
	}

	att, err := lists.Attachments(ctx, r.Store, commits[1])
	if err != nil || len(att) != 1 || att[0].Run != "nightly" {
		t.Fatalf("attachments %v %v", att, err)
	}

	r2 := *r
	r2.attachTried, r2.attachErr = false, nil

	if passed, err := r2.Finish(ctx); err != nil || !passed {
		t.Fatalf("finish: %v %v", passed, err)
	}

	if att, err := lists.Attachments(ctx, r.Store, commits[1]); err != nil || len(att) != 1 {
		t.Fatalf("attached twice: %v", att)
	}
}

func TestAFailedAttachIsNeverGreen(t *testing.T) {
	ctx := t.Context()
	r, gh, _ := branchRun(t)
	r.Checkout = filepath.Join(t.TempDir(), "nothing")

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Suite(ctx, "go"); !errors.Is(err, errAttach) {
		t.Fatalf("suite: %v", err)
	}

	st, ok := gh.status(r.ID.Commit, "pipelinegen/nightly")
	if !ok || st.State != "error" || !strings.HasPrefix(st.Description, "passed, but its lists weren't attached: ") || len(st.Description) > maxDescription {
		t.Fatalf("run status %+v", st)
	}
}

func TestPullRequestRunsAttachNothing(t *testing.T) {
	ctx := t.Context()
	r, _, commits := branchRun(t)
	r.Config.Runs["nightly"] = config.Run{Trigger: config.Trigger{PullRequests: &config.PullRequests{Base: "main"}}, Record: true}

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	if passed, err := r.Suite(ctx, "go"); err != nil || !passed {
		t.Fatalf("suite: %v %v", passed, err)
	}

	if _, src, err := lists.Nearest(ctx, r.Store, r.Checkout, commits[1]); err != nil || src.Back != 1 {
		t.Fatalf("a pull request's run attached: %v %+v", err, src)
	}
}

func TestASuiteEndThatCannotBeRecordedSaysSo(t *testing.T) {
	ctx := t.Context()
	r, gh, _ := branchRun(t)

	if _, err := r.Slice(ctx, SliceEvent{Suite: "go", Slice: "a.a_test", Label: "//a:a_test", Result: writeResult(t, `{"passed": true}`), Coverage: []string{"c"}}); err != nil {
		t.Fatal(err)
	}

	hook := "#!/bin/sh\necho \"pre-receive hook declined\" >&2\nexit 1\n"
	if err := testenv.WriteExecutable(ctx, filepath.Join(strings.TrimPrefix(r.Store.Remote, "file://"), "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	r.Store.Retries = 0
	if r.Store.Branch != nil {
		r.Store.Branch.Retries = 0
	}

	_, err := r.Suite(ctx, "go")
	if !errors.Is(err, ErrUnrecorded) {
		t.Fatalf("got %v", err)
	}

	for _, want := range []string{"the suite's end mark", "its recordings are not attached", "rerun the build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q says nothing of %q", err, want)
		}
	}

	for _, c := range []string{Context(r.Config, "nightly", "go"), RunContext(r.Config, "nightly")} {
		if st, ok := gh.status(r.ID.Commit, c); !ok || st.State != "error" || st.Description != unrecordedText {
			t.Errorf("status %s: %+v", c, st)
		}
	}
}
