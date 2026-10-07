package secret

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestRedactMasksEveryForm(t *testing.T) {
	t.Setenv("SOME_TOKEN", "tok-from-the-environment")
	load()
	Add("tok-from-the-environment") // the environment is read once per process

	tok := "tok-from-the-environment"
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))

	text := "fatal: Authorization: Basic " + basic + " and " + tok
	got := Redact(text)

	if strings.Contains(got, tok) || strings.Contains(got, basic) || strings.Count(got, Mask) != 2 {
		t.Fatalf("redacted: %q", got)
	}

	if Redact("short") != "short" {
		t.Fatal("redacted an ordinary word")
	}
}

func TestEnvironments(t *testing.T) {
	t.Setenv("GIT_TRACE_CURL", "1")
	t.Setenv("GITHUB_TOKEN", "a-token-value")
	t.Setenv("PIPELINEGEN_GIT_URL", "https://example.invalid")
	t.Setenv("GOPROXY", "https://user:pass@proxy.invalid")
	t.Setenv("GOMODCACHE", "/inherited")
	t.Setenv("GOFLAGS", "-mod=vendor")

	if slices.ContainsFunc(GitEnv(), func(kv string) bool { return strings.HasPrefix(kv, "GIT_TRACE") }) {
		t.Error("git keeps GIT_TRACE_CURL")
	}

	env := PluginEnv(map[string]string{"GOMODCACHE": "/cache/mod"})
	for _, kv := range env {
		for _, bad := range []string{"GITHUB_TOKEN=", "PIPELINEGEN_", "GOPROXY=https", "GIT_", "GOMODCACHE=/inherited", "GOFLAGS"} {
			if strings.HasPrefix(kv, bad) {
				t.Errorf("a plugin gets %s", kv)
			}
		}
	}

	for _, want := range []string{"GOMODCACHE=/cache/mod", "GOPROXY=off"} {
		if !slices.Contains(env, want) {
			t.Errorf("a plugin lacks %s", want)
		}
	}
}

func TestFillEnv(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "a-token-value")
	t.Setenv("NPM_TOKEN", "another-token-value")
	t.Setenv("DEPLOY_KEY", "a-key-value")
	t.Setenv("PIPELINEGEN_GIT_URL", "https://example.invalid")
	t.Setenv("GOPROXY", "https://proxy.example.test")
	t.Setenv("GOPRIVATE", "git.example.test/*")
	t.Setenv("GOMODCACHE", "/inherited")

	env := FillEnv(map[string]string{"GOMODCACHE": "/cache/mod"})

	for _, kv := range env {
		for _, bad := range []string{"GITHUB_TOKEN=", "NPM_TOKEN=", "DEPLOY_KEY=", "PIPELINEGEN_", "GOMODCACHE=/inherited"} {
			if strings.HasPrefix(kv, bad) {
				t.Errorf("a fill gets %s", kv)
			}
		}
	}

	for _, want := range []string{"GOMODCACHE=/cache/mod", "GOPROXY=https://proxy.example.test", "GOPRIVATE=git.example.test/*"} {
		if !slices.Contains(env, want) {
			t.Errorf("a fill lacks %s", want)
		}
	}
}

func TestNoSSHKeyReachesAPluginOrAFill(t *testing.T) {
	t.Setenv("PIPELINEGEN_SSH_KEY_READ", "-----BEGIN OPENSSH PRIVATE KEY----- read")
	t.Setenv("PIPELINEGEN_SSH_KEY_STORE", "-----BEGIN OPENSSH PRIVATE KEY----- store")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /tmp/pipelinegen-ssh-1/store/key")

	for name, env := range map[string][]string{"plugin": PluginEnv(nil), "fill": FillEnv(nil)} {
		for _, kv := range env {
			if strings.HasPrefix(kv, "PIPELINEGEN_SSH_KEY") || strings.HasPrefix(kv, "GIT_SSH_COMMAND=") {
				t.Errorf("a %s gets %s", name, kv)
			}
		}
	}
}
