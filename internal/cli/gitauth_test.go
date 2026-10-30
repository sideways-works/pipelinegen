package cli

import (
	"os/exec"
	"strings"
	"testing"
)

func TestGitAuthNeverPrintsTheToken(t *testing.T) {
	const token = "not-a-real-token-but-secret"

	t.Setenv("TEST_GIT_TOKEN", token)
	t.Setenv("GIT_CONFIG_COUNT", "")

	stdout, stderr, code := run(t, "git-auth", "TEST_GIT_TOKEN")
	if code != 0 {
		t.Fatalf("git-auth: %d %s", code, stderr)
	}

	if strings.Contains(stdout, token) {
		t.Fatal("git-auth printed the token")
	}

	fill := func(host string) string {
		t.Helper()

		script := stdout + `printf 'protocol=https\nhost=` + host + `\n\n' | git credential fill`

		cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
		// only what the lines set: no helper of the machine's answers.
		cmd.Env = []string{"TEST_GIT_TOKEN=" + token, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}

		out, _ := cmd.Output() //nolint:errcheck // a host without credentials fails, which the output shows

		return string(out)
	}

	if got := fill("github.com"); !strings.Contains(got, "username=x-access-token\n") || !strings.Contains(got, "password="+token+"\n") {
		t.Errorf("git didn't get the token for the host")
	}

	if strings.Contains(fill("example.com"), token) {
		t.Error("another host got the token")
	}

	for _, bad := range [][]string{{"git-auth"}, {"git-auth", "NOT-A-NAME"}, {"git-auth", "X", "-host", "a/b"}} {
		if _, _, code := run(t, bad...); code != 2 {
			t.Errorf("%v: exit %d, want 2", bad, code)
		}
	}
}
