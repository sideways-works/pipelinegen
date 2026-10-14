package runs

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestATokenGoesOnlyToItsScope(t *testing.T) {
	env := tokenEnv(Auth{Scope: "https://git.example.test/group/r.git", Token: "t0ken"})
	if !slices.Contains(env, "GIT_CONFIG_KEY_0=http.https://git.example.test/group/r.git.extraHeader") {
		t.Fatalf("scope: %v", env)
	}

	for _, e := range env {
		if strings.Contains(e, "t0ken") {
			t.Fatalf("the token in the clear: %s", e)
		}
	}

	if env := tokenEnv(Auth{Token: "t0ken"}); env != nil {
		t.Fatalf("no scope: %v", env)
	}

	if env := tokenEnv(Auth{Scope: GitHubScope}); env != nil {
		t.Fatalf("no token: %v", env)
	}
}

func TestTheOriginIsTheScope(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	if got := OriginScope(t.Context(), repo); got != "" {
		t.Fatalf("no origin: %q", got)
	}

	for origin, want := range map[string]string{
		"https://u:p@git.example.test/group/r.git": "https://git.example.test/group/r.git",
		"https://git.example.test/r":               "https://git.example.test/r",
		"git://git.example.test/r.git":             "",
		"file:///srv/r.git":                        "",
		"git@git.example.test:r.git":               "",
	} {
		if out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}

		if got := OriginScope(t.Context(), repo); got != want {
			t.Errorf("%s: %q, want %q", origin, got, want)
		}

		if out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "remote", "remove", "origin").CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
}
