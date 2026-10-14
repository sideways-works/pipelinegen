package runs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testrepo"
)

func TestMergeIsTheSameEveryTime(t *testing.T) {
	r := testrepo.New(t)
	ctx := t.Context()

	base := r.Git("rev-parse", "HEAD")

	r.Git("checkout", "-q", "-b", "feature")
	r.Write("web/app.ts", "feature\n")
	head := r.Commit("feature")

	r.Git("checkout", "-q", "main")
	r.Write("docs/readme.md", "main moved\n")
	tip := r.Commit("main moved")

	first, err := Merge(ctx, r.Dir, head, tip)
	if err != nil {
		t.Fatal(err)
	}

	second, err := Merge(ctx, r.Dir, head, tip)
	if err != nil || second != first {
		t.Fatalf("merges differ: %s %s %v", first, second, err)
	}

	out := filepath.Join(t.TempDir(), "source")
	if err := Checkout(ctx, r.Dir, first, out); err != nil {
		t.Fatal(err)
	}

	if got, err := git(ctx, out, "rev-parse", "HEAD"); err != nil || got != first {
		t.Fatalf("HEAD %s, want %s", got, first)
	}

	for file, want := range map[string]string{"web/app.ts": "feature\n", "docs/readme.md": "main moved\n"} {
		data, err := os.ReadFile(filepath.Join(out, file))
		if err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", file, data, err)
		}
	}

	r.Git("checkout", "-q", "-b", "clash", base)
	r.Write("docs/readme.md", "other\n")
	clash := r.Commit("clash")

	if _, err := Merge(ctx, r.Dir, clash, tip); !errors.Is(err, ErrNoMerge) {
		t.Fatalf("conflict: %v", err)
	}
}

func TestFromTrigger(t *testing.T) {
	r := testrepo.New(t)
	ctx := t.Context()

	cfg, err := config.Load(filepath.Join(r.Dir, "pipelinegen.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ch, err := FromTrigger(ctx, cfg, "main", r.Dir)
	if err != nil || ch.ID != "main" || ch.Commit != r.Git("rev-parse", "HEAD") || ch.PR != 0 {
		t.Fatalf("branch: %+v %v", ch, err)
	}

	res := filepath.Join(r.Dir, ".git", "resource")
	if err := os.MkdirAll(res, 0o750); err != nil {
		t.Fatal(err)
	}

	r.Write(".git/resource/version.json", `{"pr":"42","commit":"0123456789abcdef0123456789abcdef01234567","committed":"x"}`)
	r.Write(".git/resource/head_name", "feature/x\n")

	ch, err = FromTrigger(ctx, cfg, "pr", r.Dir)
	if err != nil || ch.ID != "pr-42" || ch.PR != 42 || ch.Branch != "feature/x" || ch.Base != "main" {
		t.Fatalf("pull request: %+v %v", ch, err)
	}

	if _, err := FromTrigger(ctx, cfg, "nope", r.Dir); err == nil {
		t.Fatal("an unknown run")
	}

	dir := t.TempDir()
	if err := WriteRunFile(dir, ch); err != nil {
		t.Fatal(err)
	}

	back, err := ReadRunFile(filepath.Join(dir, RunFile))
	if err != nil || back != ch {
		t.Fatalf("run file: %+v %v", back, err)
	}
}
