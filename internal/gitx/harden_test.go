package gitx_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/gitx"
)

func TestNoHookFsmonitorOrFilterRuns(t *testing.T) {
	for _, pipeline := range []bool{true, false} {
		t.Run(map[bool]string{true: "pipeline", false: "person"}[pipeline], func(t *testing.T) {
			dir := t.TempDir()
			marks := filepath.Join(dir, "marks")
			hooks := filepath.Join(dir, "hooks")
			mark := func(name string) string { return "#!/bin/sh\necho " + name + " >> '" + marks + "'\ncat\n" }

			for name, body := range map[string]string{
				"hooks/post-checkout": mark("hook"),
				"fsmonitor":           mark("fsmonitor"),
				"smudge":              mark("filter"),
			} {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			system := filepath.Join(dir, "gitconfig")

			config := "[core]\n\thooksPath = " + hooks + "\n\tfsmonitor = " + filepath.Join(dir, "fsmonitor") + "\n" +
				"[filter \"lfs\"]\n\tsmudge = " + filepath.Join(dir, "smudge") + "\n\trequired = true\n"
			if err := os.WriteFile(system, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}

			repo := filepath.Join(dir, "repo")
			for _, args := range [][]string{
				{"init", "-q", "-b", "main", repo},
				{"-C", repo, "config", "user.email", "t@example.test"},
				{"-C", repo, "config", "user.name", "t"},
			} {
				if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			}

			for name, body := range map[string]string{".gitattributes": "*.bin filter=lfs\n", "a.bin": "data\n"} {
				if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			for _, args := range [][]string{{"-C", repo, "add", "-A"}, {"-C", repo, "commit", "-q", "-m", "one"}} {
				if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
					t.Fatalf("%v: %s", err, out)
				}
			}

			// the tests' own isolation reads no system config: this one does
			t.Setenv("GIT_CONFIG_SYSTEM", system)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "")

			if err := os.Unsetenv("GIT_CONFIG_NOSYSTEM"); err != nil {
				t.Fatal(err)
			}

			if pipeline {
				t.Setenv("PIPELINEGEN_JOB_URL", "https://ci.example.test/job/1")
			} else {
				t.Setenv("PIPELINEGEN_JOB_URL", "")
				// a person's own config doesn't ask for the filter here
				if err := os.WriteFile(system, []byte("[core]\n\thooksPath = "+hooks+"\n\tfsmonitor = "+filepath.Join(dir, "fsmonitor")+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			r := gitx.Repo{Dir: repo}

			head, err := r.Git(t.Context(), "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}

			if err := r.Checkout(t.Context(), filepath.Join(dir, "tree"), head[:40]); err != nil {
				t.Fatal(err)
			}

			if _, err := (gitx.Repo{Dir: filepath.Join(dir, "tree")}).Git(t.Context(), "status", "--porcelain"); err != nil {
				t.Fatal(err)
			}

			if data, err := os.ReadFile(marks); err == nil {
				t.Fatalf("ran: %s", data)
			}
		})
	}
}
