package secret

import (
	"slices"
	"strings"
	"testing"
)

func TestAChildGetsNoCredential(t *testing.T) {
	for name, value := range map[string]string{
		"GITHUB_TOKEN": "write", "GH_TOKEN": "write", "PIPELINEGEN_STORE_TOKEN": "store", "PIPELINEGEN_REPORT_TOKEN": "report",
		"PIPELINEGEN_READ_TOKEN": "read", "PIPELINEGEN_SSH_KEY_STORE": "key", "PIPELINEGEN_RUN": "pr", "GIT_READ": "gitread", "GOPRIVATE": "example.com",
	} {
		t.Setenv(name, value)
	}

	names := func(env []string) []string {
		var out []string

		for _, kv := range env {
			name, _, _ := strings.Cut(kv, "=")
			out = append(out, name)
		}

		return out
	}

	got := names(ChildEnv("GIT_READ"))
	for _, gone := range []string{"GITHUB_TOKEN", "GH_TOKEN", "PIPELINEGEN_STORE_TOKEN", "PIPELINEGEN_REPORT_TOKEN", "PIPELINEGEN_READ_TOKEN", "PIPELINEGEN_SSH_KEY_STORE", "PIPELINEGEN_RUN"} {
		if slices.Contains(got, gone) {
			t.Errorf("%s reached the child", gone)
		}
	}

	for _, kept := range []string{"GIT_READ", "GOPRIVATE", "PATH"} {
		if !slices.Contains(got, kept) {
			t.Errorf("%s was dropped", kept)
		}
	}

	if !slices.Contains(names(ChildEnv("GITHUB_TOKEN")), "GITHUB_TOKEN") {
		t.Error("the declared git token was dropped")
	}
}

func TestHardenedGitKeepsTheCallersConfig(t *testing.T) {
	t.Setenv("PIPELINEGEN_JOB_URL", "")
	t.Setenv(PipelineStepVar, "")

	env := HardenGit([]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=x", "HOME=/h"})
	want := []string{
		"GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=x", "HOME=/h",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null", "GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_COUNT=3", "GIT_LFS_SKIP_SMUDGE=1",
	}

	if !slices.Equal(env, want) {
		t.Fatalf("got %q", env)
	}

	t.Setenv("PIPELINEGEN_JOB_URL", "https://ci.example.test/job/1")

	if env := HardenGit(nil); !slices.Contains(env, "GIT_CONFIG_NOSYSTEM=1") || !slices.Contains(env, "GIT_CONFIG_GLOBAL=/dev/null") {
		t.Fatalf("a pipeline's step: %q", env)
	}
}

func TestAStepSaysItIsAPipelines(t *testing.T) {
	for _, tc := range []struct {
		step, url string
		want      bool
	}{
		{"1", "", true},
		{"1", "https://ci.example.test/job/1", true},
		{"", "https://ci.example.test/job/1", true},
		{"", "", false},
	} {
		t.Setenv(PipelineStepVar, tc.step)
		t.Setenv("PIPELINEGEN_JOB_URL", tc.url)

		if got := InPipeline(); got != tc.want {
			t.Errorf("mark %q, link %q: in a pipeline %v", tc.step, tc.url, got)
		}

		if got := slices.Contains(HardenGit(nil), "GIT_CONFIG_GLOBAL=/dev/null"); got != tc.want {
			t.Errorf("mark %q, link %q: git without the global config %v", tc.step, tc.url, got)
		}
	}
}

func TestAFillGetsNoCredential(t *testing.T) {
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "PIPELINEGEN_STORE_TOKEN", "PIPELINEGEN_SSH_KEY_READ", "PIPELINEGEN_RUN"} {
		t.Setenv(name, "x")
	}

	for _, kv := range FillEnv(nil) {
		if name, _, _ := strings.Cut(kv, "="); name == "GITHUB_TOKEN" || name == "GH_TOKEN" || strings.HasPrefix(name, "PIPELINEGEN_") {
			t.Errorf("%s reached the fill", name)
		}
	}
}
