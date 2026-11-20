package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/report"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// Content goes into the file too, so the commit differs from an earlier push's of the same branch
// within the same second.
func pushBranch(t *testing.T, remote, branch string, content ...string) string {
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
	writeFile(t, filepath.Join(dir, "f.txt"), "on "+branch+"\n"+strings.Join(content, ""))
	gitIn("add", "f.txt")
	gitIn("commit", "-q", "-m", branch)
	gitIn("push", "-q", "-f", remote, "HEAD:refs/heads/"+branch)

	return gitIn("rev-parse", "HEAD")
}

func gitForgeConfig(t *testing.T, root, remote string) {
	t.Helper()

	cfg := strings.NewReplacer(
		"github: {repository: o/r, token: gh}", "git: {remote: \""+remote+"\"}\nforges:\n  - {name: git, plugin: git, changes: {branches: [\"review/*\"]}}",
		"trigger: {pull_requests: {base: main, draft: false}}", "trigger: {changes: {base: main, head: \"review/c*\"}}",
		"\n    report: {comment: {format: list}}", "",
	).Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")))
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), cfg)
}

func TestTheSweepTakesChangesFromTheForge(t *testing.T) {
	root, _, s := runEnv(t)
	gitForgeConfig(t, root, s.Remote)

	heads := map[string]string{}
	for _, b := range []string{"main", "review/cart", "review/coupon", "review/docs", "personal/wip"} {
		heads[b] = pushBranch(t, s.Remote, b)
	}

	coupon := report.ID{Run: "pr", Change: "git/branch-review%2Fcoupon", Commit: heads["review/coupon"]}
	if err := report.WritePlan(t.Context(), s, coupon, []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "sweep")

	stdout, stderr, code := run(t, "unplanned", "-root", root, "-run", "pr", "-out", out, "-cache", t.TempDir())
	if code != 0 {
		t.Fatalf("%d %s", code, stderr)
	}

	if !strings.Contains(stdout, "git/branch-review%2Fcart "+heads["review/cart"][:12]+": no plan: plan it\n") {
		t.Fatalf("cart:\n%s", stdout)
	}

	for _, b := range []string{"review/docs", "personal/wip", "main"} {
		if strings.Contains(stdout, heads[b][:12]) {
			t.Errorf("%s listed:\n%s", b, stdout)
		}
	}

	cart := filepath.Join(out, "plan", "git%2Fbranch-review%252Fcart")

	ch, err := runs.ReadRunFile(filepath.Join(cart, runs.RunFile))
	want := runs.Change{
		Run: "pr", ID: "git/branch-review%2Fcart", Commit: heads["review/cart"], Branch: "review/cart", Base: "main",
		Forge: "git", Remote: s.Remote,
	}

	handed := ch.Handed
	ch.Handed = ""

	if err != nil || ch != want || handed == "" {
		t.Fatalf("hand-off of cart: %+v %q %v", ch, handed, err)
	}

	if _, err := os.Stat(filepath.Join(cart, ".git")); err == nil {
		t.Error("the hand-off is a checkout")
	}

	marks, err := s.List(t.Context(), report.SweptOf("pr"))
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := marks[report.SweptRef(report.ID{Run: "pr", Change: ch.ID, Commit: ch.Commit})]; !ok {
		t.Errorf("no mark under the change's id: %v", marks)
	}

	if !strings.Contains(stdout, "git/branch-review%2Fcoupon "+heads["review/coupon"][:12]+": planned, its suites on their way\n") {
		t.Errorf("coupon:\n%s", stdout)
	}
}

func TestTheSweepHandsAForgesHeadOffAgain(t *testing.T) {
	root, _, s := runEnv(t)
	gitForgeConfig(t, root, s.Remote)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"),
		strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")), `head: "review/c*"}`, `head: "review/c*", missed_after: 1h}`, 1))

	commit := pushBranch(t, s.Remote, "review/cart")
	id := report.ID{Run: "pr", Change: "git/branch-review%2Fcart", Commit: commit}

	if err := report.WritePlan(t.Context(), pastStore(t, s), id, []byte(`{"suites": [{"suite": "e2e", "selected": [], "untouched": [], "whole_suite": []}]}`)); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "sweep")

	stdout, stderr, code := run(t, "unplanned", "-root", root, "-run", "pr", "-out", out, "-cache", t.TempDir())
	if code != 0 || !strings.Contains(stdout, "git/branch-review%2Fcart "+commit[:12]+": planned, no suite began: hand it off again\n") {
		t.Fatalf("%d %s\n%s", code, stdout, stderr)
	}

	ch, err := runs.ReadRunFile(filepath.Join(out, "handoff", "git%2Fbranch-review%252Fcart", runs.RunFile))
	if err != nil || ch.ID != id.Change || ch.Commit != commit {
		t.Fatalf("hand-off: %+v %v", ch, err)
	}
}

func TestTheSweepReadsAnSSHServer(t *testing.T) {
	root, _, s := runEnv(t)

	bare := strings.TrimPrefix(s.Remote, "file://")
	sshRemote := "ssh://git@git.example.test" + bare
	gitForgeConfig(t, root, sshRemote)
	writeFile(t, filepath.Join(root, "pipelinegen.yaml"), strings.Replace(readFile(t, filepath.Join(root, "pipelinegen.yaml")),
		`git: {remote: "`+sshRemote+`"}`,
		`git: {remote: "`+sshRemote+`", ssh_key: deploy_key, known_hosts: ["git.example.test ssh-ed25519 AAAAtestkey"]}`, 1))

	commit := pushBranch(t, s.Remote, "review/cart")

	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "ssh.log")
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "ssh"),
		[]byte("#!/bin/sh\necho \"$@\" >> '"+log+"'\nfor a; do last=\"$a\"; done\nexec sh -c \"$last\"\n")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PIPELINEGEN_SSH_KEY_READ", "-----BEGIN OPENSSH PRIVATE KEY-----\nread\n-----END OPENSSH PRIVATE KEY-----")
	t.Setenv("PIPELINEGEN_SSH_KEY_STORE", "-----BEGIN OPENSSH PRIVATE KEY-----\nstore\n-----END OPENSSH PRIVATE KEY-----")

	out := filepath.Join(t.TempDir(), "sweep")

	stdout, stderr, code := run(t, "unplanned", "-root", root, "-run", "pr", "-out", out, "-cache", t.TempDir())
	if code != 0 || !strings.Contains(stdout, "git/branch-review%2Fcart "+commit[:12]+": no plan: plan it\n") {
		t.Fatalf("%d %s\n%s", code, stdout, stderr)
	}

	args := readFile(t, log)
	if strings.Count(args, "StrictHostKeyChecking=yes") < 2 || !strings.Contains(args, "git@git.example.test") {
		t.Fatalf("ssh ran with:\n%s", args)
	}
}
