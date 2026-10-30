package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// childCredentials are what a step may hold and no program the tool starts on a tree may see.
var childCredentials = []string{"GITHUB_TOKEN", "GH_TOKEN", "PIPELINEGEN_STORE_TOKEN", "PIPELINEGEN_REPORT_TOKEN", "PIPELINEGEN_CHANGES_TOKEN", "PIPELINEGEN_SSH_KEY_STORE", "PIPELINEGEN_RUN"}

func dumper(t *testing.T) (exe, dump string) {
	t.Helper()

	dir := t.TempDir()
	dump = filepath.Join(dir, "env")
	exe = filepath.Join(dir, "stand-in")
	script := "#!/bin/sh\nenv | cut -d= -f1 > '" + dump + "'\n" +
		`while [ $# -gt 0 ]; do [ "$1" = --output ] && echo '{"rootCauses": []}' > "$2"; shift; done` + "\n"

	if err := testenv.WriteExecutable(t.Context(), exe, []byte(script)); err != nil {
		t.Fatal(err)
	}

	return exe, dump
}

func sawNone(t *testing.T, child, dump string) {
	t.Helper()

	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("%s never ran: %v", child, err)
	}

	names := strings.Fields(string(data))

	for _, name := range childCredentials {
		for _, seen := range names {
			if seen == name {
				t.Errorf("%s saw %s", child, name)
			}
		}
	}

	if !strings.Contains(" "+strings.Join(names, " ")+" ", " GIT_READ_TOKEN ") {
		t.Errorf("%s lost the declared git token", child)
	}
}

func TestNoChildSeesACredential(t *testing.T) {
	for _, name := range childCredentials {
		t.Setenv(name, "secret-"+strings.ToLower(name))
	}

	t.Setenv("GIT_READ_TOKEN", "read-only")

	cfg := &config.Config{}
	cfg.Concourse.Planner.GitToken = "GIT_READ_TOKEN"

	t.Run("bazel", func(t *testing.T) {
		exe, dump := dumper(t)
		t.Setenv("PIPELINEGEN_BAZELISK", exe)

		if _, err := bazelFor(cfg, t.TempDir(), "")(t.TempDir()).Run(t.Context(), "query", "//..."); err != nil {
			t.Fatal(err)
		}

		sawNone(t, "bazel", dump)
	})

	t.Run("bazel for a command", func(t *testing.T) {
		exe, dump := dumper(t)
		t.Setenv("PIPELINEGEN_BAZELISK", exe)

		if _, err := (&common{root: t.TempDir()}).bazel(cfg).Run(t.Context(), "query", "//..."); err != nil {
			t.Fatal(err)
		}

		sawNone(t, "bazel", dump)
	})

	t.Run("bazel-diff", func(t *testing.T) {
		exe, dump := dumper(t)

		tool, err := differFor(t.Context(), cfg, t.TempDir(), exe)
		if err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		if _, err := tool.Explain(t.Context(), filepath.Join(dir, "base.json"), filepath.Join(dir, "head.json"), filepath.Join(dir, "edges.json"), "//a:b"); err != nil {
			t.Fatal(err)
		}

		sawNone(t, "bazel-diff", dump)
	})
}

func TestAPipelineStepsBazelIdlesBriefly(t *testing.T) {
	exe, _ := dumper(t)
	t.Setenv("PIPELINEGEN_BAZELISK", exe)

	cfg := &config.Config{}
	cfg.Bazel.StartupFlags = []string{"--output_user_root=/r"}

	t.Setenv(envPipelineStep, "")
	t.Setenv("PIPELINEGEN_JOB_URL", "https://ci.example.test/job/1")

	if got := bazelFor(cfg, t.TempDir(), "")(t.TempDir()).StartupFlags; !slices.Equal(got, []string{"--output_user_root=/r", "--max_idle_secs=600"}) {
		t.Fatalf("a pipeline's step: %q", got)
	}

	own := &config.Config{}
	own.Bazel.StartupFlags = []string{"--max_idle_secs=30"}

	if got := (&common{root: t.TempDir()}).bazel(own).StartupFlags; !slices.Equal(got, []string{"--max_idle_secs=30"}) {
		t.Fatalf("the config's own idle time: %q", got)
	}

	t.Setenv("PIPELINEGEN_JOB_URL", "")

	if got := bazelFor(cfg, t.TempDir(), "")(t.TempDir()).StartupFlags; !slices.Equal(got, []string{"--output_user_root=/r"}) {
		t.Fatalf("a person's machine: %q", got)
	}
}

func TestAPipelineStepsBazelKeepsItsOutputInTheCache(t *testing.T) {
	exe, _ := dumper(t)
	t.Setenv("PIPELINEGEN_BAZELISK", exe)
	t.Setenv(envPipelineStep, "")
	t.Setenv("PIPELINEGEN_JOB_URL", "https://ci.example.test/job/1")
	t.Setenv("BAZELISK_HOME", "")

	cache := t.TempDir()
	bz := bazelFor(&config.Config{}, t.TempDir(), cache)(t.TempDir())

	want := []string{"--max_idle_secs=600", "--output_user_root=" + filepath.Join(cache, "bazel", "root"), "--output_base=" + filepath.Join(cache, "bazel", "base")}
	if !slices.Equal(bz.StartupFlags, want) || !slices.Contains(bz.Env, "BAZELISK_HOME="+filepath.Join(cache, "bazel", "bazelisk")) {
		t.Fatalf("a pipeline's step: %q %q", bz.StartupFlags, bz.Env)
	}

	for _, own := range []string{"--output_user_root=/r", "--output_base=/b"} {
		cfg := &config.Config{}
		cfg.Bazel.StartupFlags = []string{own}

		if got := bazelFor(cfg, t.TempDir(), cache)(t.TempDir()).StartupFlags; !slices.Equal(got, []string{own, "--max_idle_secs=600"}) {
			t.Errorf("the config's own %s: %q", own, got)
		}
	}

	t.Setenv("BAZELISK_HOME", "/somewhere")

	if env := bazelFor(&config.Config{}, t.TempDir(), cache)(t.TempDir()).Env; slices.Contains(env, "BAZELISK_HOME="+filepath.Join(cache, "bazel", "bazelisk")) {
		t.Errorf("the step's own BAZELISK_HOME was replaced: %q", env)
	}

	t.Setenv("PIPELINEGEN_JOB_URL", "")

	if got := bazelFor(&config.Config{}, t.TempDir(), cache)(t.TempDir()).StartupFlags; len(got) != 0 {
		t.Errorf("a person's machine: %q", got)
	}
}

func TestAPlanningStepKeepsBazelInItsCache(t *testing.T) {
	exe, _ := dumper(t)
	t.Setenv("PIPELINEGEN_BAZELISK", exe)
	t.Setenv("PIPELINEGEN_JOB_URL", "")
	t.Setenv(envPipelineStep, "1")
	t.Setenv("BAZELISK_HOME", "")

	cache := t.TempDir()
	bz := bazelFor(&config.Config{}, t.TempDir(), cache)(t.TempDir())

	want := []string{"--max_idle_secs=600", "--output_user_root=" + filepath.Join(cache, "bazel", "root"), "--output_base=" + filepath.Join(cache, "bazel", "base")}
	if !slices.Equal(bz.StartupFlags, want) || !slices.Contains(bz.Env, "BAZELISK_HOME="+filepath.Join(cache, "bazel", "bazelisk")) {
		t.Fatalf("a planning step: %q %q", bz.StartupFlags, bz.Env)
	}
}
