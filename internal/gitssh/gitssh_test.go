package gitssh

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/plugin"
)

const (
	testKey   = "-----BEGIN OPENSSH PRIVATE KEY-----\nnot-a-real-key-0123456789\n-----END OPENSSH PRIVATE KEY-----"
	testHosts = "git.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItestkey"
)

// TestMain lets the test binary hold a key until a signal ends it, so the
// test can see the key's directory go with the process.
func TestMain(m *testing.M) {
	if os.Getenv("GITSSH_HOLD") != "" {
		k := &Keys{KnownHosts: []string{testHosts}}
		if _, err := k.Env(plugin.PurposeStore); err != nil {
			os.Exit(1)
		}

		println(k.Dir()) //nolint:forbidigo // the test reads the directory from stderr
		time.Sleep(time.Minute)
		os.Exit(0)
	}

	os.Exit(m.Run())
}

func TestCommand(t *testing.T) {
	dir := t.TempDir()

	c, err := Command(dir, testKey, []string{testHosts})
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{"key", "known_hosts"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, err, info)
		}
	}

	for _, want := range []string{
		"ssh -F /dev/null -i '" + filepath.Join(dir, "key") + "'", "IdentitiesOnly=yes", "IdentityAgent=none", "ForwardAgent=no",
		"PasswordAuthentication=no", "BatchMode=yes", "StrictHostKeyChecking=yes",
		"UserKnownHostsFile='" + filepath.Join(dir, "known_hosts") + "'", "GlobalKnownHostsFile=/dev/null",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("the command lacks %s: %s", want, c)
		}
	}

	if strings.Contains(c, "accept-new") || strings.Contains(c, "StrictHostKeyChecking=no") {
		t.Errorf("host keys accepted: %s", c)
	}

	if _, err := Command(t.TempDir(), testKey, nil); !errors.Is(err, ErrSSH) {
		t.Errorf("no known hosts: %v", err)
	}
}

func TestKeysByPurpose(t *testing.T) {
	k := &Keys{KnownHosts: []string{testHosts}}

	if env, err := k.Env(plugin.PurposeRead); err != nil || env != nil || k.Dir() != "" {
		t.Fatalf("no key: %v %v %q", env, err, k.Dir())
	}

	t.Setenv(KeyEnv(plugin.PurposeRead), testKey)
	t.Setenv(KeyEnv(plugin.PurposeStore), testKey+"-store")

	read, err := k.Env(plugin.PurposeRead)
	if err != nil || len(read) != 1 || !strings.HasPrefix(read[0], "GIT_SSH_COMMAND=ssh ") {
		t.Fatalf("read: %v %v", read, err)
	}

	store, err := k.Env(plugin.PurposeStore)
	if err != nil || store[0] == read[0] {
		t.Fatalf("the store's key is the read key's: %v %v", store, err)
	}

	info, err := os.Stat(k.Dir())
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the keys' directory: %v %v", err, info)
	}

	if cwd, _ := os.Getwd(); strings.HasPrefix(k.Dir(), cwd) { //nolint:errcheck // an empty cwd prefixes nothing
		t.Errorf("the keys are under the working directory: %s", k.Dir())
	}

	dir := k.Dir()
	if err := k.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Close left %s: %v", dir, err)
	}
}

func TestTheKeyGoesWithASignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}

		cmd := exec.CommandContext(t.Context(), self, "-test.run=^$")

		cmd.Env = append(os.Environ(), "GITSSH_HOLD=1", KeyEnv(plugin.PurposeStore)+"="+testKey)

		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}

		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}

		line, err := bufio.NewReader(stderr).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}

		dir := strings.TrimSpace(line)
		if _, err := os.Stat(filepath.Join(dir, plugin.PurposeStore, "key")); err != nil {
			t.Fatalf("no key while held: %v", err)
		}

		if err := cmd.Process.Signal(sig); err != nil {
			t.Fatal(err)
		}

		if err := cmd.Wait(); err == nil {
			t.Errorf("%v: the process ended as if nothing happened", sig)
		}

		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%v left %s: %v", sig, dir, err)
		}
	}
}

func TestGitReachesAnSSHRemote(t *testing.T) {
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "ssh.log")

	fake := `#!/bin/sh
echo "$@" > "` + log + `"
for a; do last="$a"; done
key=""
while [ $# -gt 0 ]; do [ "$1" = -i ] && key="$2"; shift; done
[ "$(stat -c %a "$key")" = 600 ] || { echo "key readable by others" >&2; exit 9; }
exec sh -c "$last"
`
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "ssh"), []byte(fake)); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(KeyEnv(plugin.PurposeRead), testKey)

	bare := filepath.Join(t.TempDir(), "r.git")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	k := &Keys{KnownHosts: []string{testHosts}}

	t.Cleanup(func() { _ = k.Close() }) //nolint:errcheck // the test's own scratch

	env, err := k.Env(plugin.PurposeRead)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(t.Context(), "git", "ls-remote", "ssh://git@git.example.test"+bare)

	cmd.Env = append(os.Environ(), env...)

	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ls-remote: %v: %s", err, out)
	}

	args, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(args), "StrictHostKeyChecking=yes") || !strings.Contains(string(args), "git@git.example.test") {
		t.Fatalf("ssh ran with %q: %v", args, err)
	}
}

func TestRemotesAndHostLines(t *testing.T) {
	for remote, want := range map[string]bool{
		"ssh://git@git.example.test/team/app.git":   true,
		"ssh://git.example.test:2222/team/app.git":  true,
		"git@git.example.test:team/app.git":         true,
		"ssh://git:secret@git.example.test/app.git": false,
		"ssh://git.example.test/":                   false,
		"https://git.example.test/team/app.git":     false,
		"file:///srv/git/app.git":                   false,
	} {
		if Valid(remote) != want {
			t.Errorf("Valid(%q) = %v", remote, !want)
		}
	}

	if p := KnownHostsProblem(testHosts); p != "" {
		t.Error(p)
	}

	if KnownHostsProblem("@cert-authority *.example.test ssh-ed25519 AAAA") != "" || KnownHostsProblem("git.example.test ssh-ed25519") == "" {
		t.Error("known_hosts lines")
	}
}
