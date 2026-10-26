package driver

import (
	"slices"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/plugin"
)

func TestADriversEnvironmentHoldsNoTokenAndNoTrustedKeys(t *testing.T) {
	e := Env{
		Base: []string{
			"PATH=/usr/bin", "HOME=/h", "GITHUB_TOKEN=t0k", "PIPELINEGEN_TRUSTED={}", "PIPELINEGEN_STORE_TOKEN=s",
			"PIPELINEGEN_JOB_URL=https://ci/x", "PIPELINEGEN_MARKS=/b/work/marks", "PIPELINEGEN_SUITE=go", "PIPELINEGEN_RECORD=true", "NPM_TOKEN=declared",
		},
		Tools:   "/b/tools",
		Secrets: []string{"NPM_TOKEN"},
		Params:  map[string]string{"GOFLAGS": "-trimpath", "XDG_CACHE_HOME": "{cache}/xdg"},
		Cache:   "/b/cache/tests",
	}

	got := e.For(map[string]string{"WORDS": "http://127.0.0.1:8080"})

	for _, gone := range []string{"GITHUB_TOKEN=", "PIPELINEGEN_TRUSTED=", "PIPELINEGEN_STORE_TOKEN=", "PIPELINEGEN_JOB_URL=", "PIPELINEGEN_MARKS="} {
		if slices.ContainsFunc(got, func(kv string) bool { return strings.HasPrefix(kv, gone) }) {
			t.Errorf("%s reached the driver: %v", gone, got)
		}
	}

	for _, kept := range []string{
		"PATH=/b/tools:/usr/bin", "HOME=/h", "PIPELINEGEN_SUITE=go", "PIPELINEGEN_RECORD=true", "NPM_TOKEN=declared",
		"GOFLAGS=-trimpath", "XDG_CACHE_HOME=/b/cache/tests/xdg", "WORDS=http://127.0.0.1:8080",
	} {
		if !slices.Contains(got, kept) {
			t.Errorf("%s missing: %v", kept, got)
		}
	}

	vars := e.Vars(map[string]string{"WORDS": "w"})
	if vars["GOFLAGS"] != "-trimpath" || vars["NPM_TOKEN"] != "declared" || vars["WORDS"] != "w" || vars["HOME"] != "" {
		t.Fatalf("vars: %v", vars)
	}
}

func TestOnlyAMarkedCallGetsTheMarksDirectory(t *testing.T) {
	env := []string{"HOME=/h", "PIPELINEGEN_MARKS=/old/marks"}

	if got := withMarks(env, plugin.DriverRequest{}); !slices.Equal(got, []string{"HOME=/h"}) {
		t.Errorf("unmarked: %q", got)
	}

	marked := plugin.DriverRequest{Marks: &plugin.Marks{Command: []string{"mark"}, Dir: "/b/work/marks"}}
	if got := withMarks(env, marked); !slices.Equal(got, []string{"HOME=/h", "PIPELINEGEN_MARKS=/b/work/marks"}) {
		t.Errorf("marked: %q", got)
	}

	if !slices.Equal(env, []string{"HOME=/h", "PIPELINEGEN_MARKS=/old/marks"}) {
		t.Errorf("the driver's own environment changed: %q", env)
	}
}

func TestADriverSeesTheRunsCommitAndNoOtherOfTheRunsVariables(t *testing.T) {
	base := []string{
		"PATH=/usr/bin", "PIPELINEGEN_COMMIT=stale", "PIPELINEGEN_CHANGE=pr-12", "PIPELINEGEN_READ_TOKEN=r",
		"PIPELINEGEN_CHANGES_TOKEN=c", "PIPELINEGEN_REPORT_TOKEN=p", "PIPELINEGEN_STORE_TOKEN=s", "GITHUB_TOKEN=g", "GH_TOKEN=h",
	}

	got := Env{Base: base, Commit: "0123abcd"}.For(nil)

	var commits []string

	for _, kv := range got {
		name, value, _ := strings.Cut(kv, "=")

		switch {
		case name == plugin.EnvCommit:
			commits = append(commits, value)
		case strings.HasPrefix(name, "PIPELINEGEN_") || strings.HasSuffix(name, "_TOKEN"):
			t.Errorf("%s reached the driver", kv)
		}
	}

	if !slices.Equal(commits, []string{"0123abcd"}) {
		t.Errorf("the driver's %s: %q", plugin.EnvCommit, commits)
	}

	if _, ok := (Env{Base: base, Commit: "0123abcd"}).Vars(nil)[plugin.EnvCommit]; ok {
		t.Errorf("%s is among the vars a driver passes its tests by name", plugin.EnvCommit)
	}

	if slices.ContainsFunc(Env{Base: base}.For(nil), func(kv string) bool { return strings.HasPrefix(kv, plugin.EnvCommit+"=") }) {
		t.Errorf("without a commit, %s was set", plugin.EnvCommit)
	}
}
