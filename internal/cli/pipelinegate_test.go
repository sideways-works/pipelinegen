package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitstore/storetest"
)

func TestThePipelineIsSetFromTheNewestConfigOnly(t *testing.T) {
	remote := storetest.Bare(t)
	work := t.TempDir()

	git := func(dir string, args ...string) string {
		t.Helper()

		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}

		return strings.TrimSpace(string(out))
	}

	git(work, "init", "-q", "-b", "main")

	commit := func(config string) string {
		t.Helper()

		writeFile(t, filepath.Join(work, "pipelinegen.yaml"), config)
		git(work, "add", "-A")
		git(work, "commit", "-q", "--allow-empty", "-m", "next")
		git(work, "push", "-q", remote, "HEAD:refs/heads/main")

		return git(work, "rev-parse", "HEAD")
	}

	conf := strings.Replace(reportConfig, "github: {repository: o/r, token: gh}", "git: {remote: \""+remote+"\"}", 1)
	older := commit(conf)
	newer := commit(conf + "everything_on: [pipelinegen.yaml]\n")

	cfg := loadConfigText(t, conf)

	for _, tc := range []struct {
		checkout string
		open     bool
	}{
		{older, false},
		{newer, true},
	} {
		repo := filepath.Join(t.TempDir(), "repo")
		git(filepath.Dir(repo), "clone", "-q", remote, repo)
		git(repo, "checkout", "-q", tc.checkout)

		gate := t.TempDir()

		var out bytes.Buffer

		open, err := pipelineGate(t.Context(), cfg, repo, gate, &out)
		if err != nil || open != tc.open || strings.Contains(readFile(t, filepath.Join(gate, "gate.json")), "1") != tc.open {
			t.Fatalf("checkout %s: open %v, %v, %q", tc.checkout[:12], open, err, out.String())
		}

		if !tc.open && !strings.Contains(out.String(), "superseded by "+newer[:12]) {
			t.Fatalf("the older config's build: %q", out.String())
		}
	}

	writeFile(t, filepath.Join(work, "README"), "x")
	git(work, "add", "-A")
	git(work, "commit", "-q", "-m", "docs")
	git(work, "push", "-q", remote, "HEAD:refs/heads/main")

	repo := filepath.Join(t.TempDir(), "repo")
	git(filepath.Dir(repo), "clone", "-q", remote, repo)
	git(repo, "checkout", "-q", newer)

	if open, err := pipelineGate(t.Context(), cfg, repo, t.TempDir(), &bytes.Buffer{}); err != nil || !open {
		t.Fatalf("the newest config, with a later commit beside it: %v %v", open, err)
	}
}
