package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/runs"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func fakeSSH(t *testing.T, serverKey string) string {
	t.Helper()

	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "ssh.log")
	script := `#!/bin/sh
echo "$@" >> '` + log + `'
hosts=""
for a; do case "$a" in UserKnownHostsFile=*) hosts="${a#UserKnownHostsFile=}"; hosts="${hosts#\'}"; hosts="${hosts%\'}";; esac; last="$a"; done
if ! grep -qF 'git.example.test ` + serverKey + `' "$hosts"; then
  if grep -q '^git.example.test ' "$hosts"; then
    echo '@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@' >&2
    echo '@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @' >&2
  else
    echo 'No ED25519 host key is known for git.example.test and you have requested strict checking.' >&2
  fi
  echo 'Host key verification failed.' >&2
  exit 255
fi
exec sh -c "$last"
`

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "ssh"), []byte(script)); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return log
}

func TestAHandOffComesOverSSHOnlyFromAKnownHost(t *testing.T) {
	_, _, s := runEnv(t)
	bare := strings.TrimPrefix(s.Remote, "file://")
	remote := "ssh://git@git.example.test" + bare

	commit := pushBranch(t, s.Remote, "review/cart")
	trigger := t.TempDir()

	if err := runs.WriteRunFile(trigger, runs.Change{
		Run: "pr", ID: "git/branch-review%2Fcart", Commit: commit, Branch: "review/cart", Base: "main", Forge: "git", Remote: remote,
	}); err != nil {
		t.Fatal(err)
	}

	log := fakeSSH(t, "ssh-ed25519 AAAAgood")

	keys := func(hosts ...string) string {
		data, err := json.Marshal(config.Trusted{KnownHosts: hosts})
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	}

	t.Setenv("PIPELINEGEN_SSH_KEY_READ", "-----BEGIN OPENSSH PRIVATE KEY-----\nread\n-----END OPENSSH PRIVATE KEY-----")
	t.Cleanup(closeSSH)

	for _, tc := range []struct {
		name, trusted, key, want string
	}{
		{"a known host", keys("git.example.test ssh-ed25519 AAAAgood"), "k", ""},
		{"an unknown host", keys("other.example.test ssh-ed25519 AAAAgood"), "k", "no host is trusted on first use"},
		{"a changed key", keys("git.example.test ssh-ed25519 AAAAold"), "k", "ssh-keyscan git.example.test"},
		{"no trusted keys", "", "k", "regenerate the pipeline"},
		{"no host keys", keys(), "k", "git.known_hosts lists no host key"},
		{"no read key", keys("git.example.test ssh-ed25519 AAAAgood"), "", "no read key in PIPELINEGEN_SSH_KEY_READ"},
	} {
		closeSSH()
		t.Setenv(envTrusted, tc.trusted)

		if tc.key == "" {
			t.Setenv("PIPELINEGEN_SSH_KEY_READ", "")
		}

		dir, err := handOffCheckout(t.Context(), "pr", trigger, true)

		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.want == "":
			if head := gitOut(t, dir, "rev-parse", "HEAD"); head != commit {
				t.Errorf("%s: checked out %q", tc.name, head)
			}
		case err == nil || !strings.Contains(err.Error(), tc.want):
			t.Errorf("%s: want %q, got %v", tc.name, tc.want, err)
		}
	}

	args := readFile(t, log)
	if !strings.Contains(args, "StrictHostKeyChecking=yes") || !strings.Contains(args, "GlobalKnownHostsFile=/dev/null") || !strings.Contains(args, "-F /dev/null") {
		t.Errorf("ssh ran with:\n%s", args)
	}
}
