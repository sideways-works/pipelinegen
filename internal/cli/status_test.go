package cli

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/config"
	"github.com/sideways-works/pipelinegen/internal/testenv"
)

func TestStatusSaysWhatTheRecordsHold(t *testing.T) {
	root, env := recordRepo(t)

	if _, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root); code != 0 {
		t.Fatalf("record: %d %s", code, stderr)
	}

	head := gitT(t, root, "rev-parse", "HEAD")

	for _, what := range []string{"HEAD", "main", head} {
		stdout, stderr, code := runIn(t, root, env, "status", what, "--root", root)
		if code != 0 || !strings.HasPrefix(stdout, "pipelinegen: local local-") || !strings.Contains(stdout, " "+head[:12]+": passed") ||
			strings.Count(stdout, "\n") != 1 || !strings.Contains(stderr, "lists from the local store") {
			t.Fatalf("status %s: %d %q %q", what, code, stdout, stderr)
		}
	}

	stdout, _, code := runIn(t, root, env, "status", "HEAD", "--root", root, "--json")

	var lines []StatusLine
	if err := json.Unmarshal([]byte(stdout), &lines); code != 0 || err != nil || len(lines) != 1 || lines[0].State != "passed" || lines[0].Commit != head || lines[0].Run != "local" {
		t.Fatalf("--json: %d %v %q", code, err, stdout)
	}

	if _, _, code := runIn(t, root, env, "status", "HEAD", "--root", root, "--run", "nightly"); code != 2 {
		t.Errorf("an unknown run: %d", code)
	}

	writeFile(t, filepath.Join(root, "e2e/cart.spec.ts"), "test('cart, again')\n")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "unrecorded")

	if stdout, _, code := runIn(t, root, env, "status", "HEAD", "--root", root); code != 3 || !strings.HasPrefix(stdout, "pipelinegen: no run planned HEAD (") {
		t.Fatalf("a commit no run planned: %d %q", code, stdout)
	}

	if _, _, code := runIn(t, root, env, "status", "--root", root); code != 2 {
		t.Errorf("no commit: %d", code)
	}
}

func TestStatusOfARunThatDidNotEnd(t *testing.T) {
	root, env := recordRepo(t)

	writeFile(t, filepath.Join(root, "ci/e2e-slice.sh"), "exit 7\n")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "a failing task")

	if _, _, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root); code != 1 {
		t.Fatalf("record: %d", code)
	}

	stdout, _, code := runIn(t, root, env, "status", "HEAD", "--root", root)
	if code != 2 || !strings.Contains(stdout, ": pending") {
		t.Fatalf("%d %q", code, stdout)
	}
}

func TestStatusOnTheServer(t *testing.T) {
	root, env := recordRepo(t)

	store := filepath.Join(gitT(t, root, "rev-parse", "--path-format=absolute", "--git-common-dir"), "pipelinegen", "local.git")
	writeFile(t, filepath.Join(root, config.FileName), recordConfig+"git: {remote: \"file://"+store+"\"}\n")
	gitT(t, root, "-c", "user.name=t", "-c", "user.email=t@example.test", "commit", "-qam", "the records' remote")

	if _, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root); code != 0 {
		t.Fatalf("record: %d %s", code, stderr)
	}

	bare := filepath.Join(t.TempDir(), "server.git")
	gitT(t, root, "clone", "-q", "--bare", root, bare)

	stdout, stderr, code := runIn(t, bare, env, "status", "main", "--root", bare)
	if code != 0 || !strings.Contains(stdout, ": passed") {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}
}

func TestStatusOverSSH(t *testing.T) {
	root, env := recordRepo(t)

	store := filepath.Join(t.TempDir(), "records.git")
	if _, stderr, code := runIn(t, root, env, "record", "--suite", "e2e", "--root", root, "--store", store); code != 0 {
		t.Fatalf("record: %d %s", code, stderr)
	}

	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "ssh.log")
	fake := "#!/bin/sh\necho \"$@\" >> '" + log + "'\nfor a; do last=\"$a\"; done\nexec sh -c \"$last\"\n"

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "ssh"), []byte(fake)); err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()

	writeFile(t, filepath.Join(root, config.FileName), recordConfig+
		"git: {remote: \"ssh://git@git.example.test"+store+"\", ssh_key: records_key, known_hosts: [\"git.example.test ssh-ed25519 AAAAtestkey\"]}\n")

	e := maps.Clone(env)
	e["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	e["PIPELINEGEN_SSH_KEY_STORE"] = "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----"
	e["TMPDIR"] = tmp

	stdout, stderr, code := runIn(t, root, e, "status", "HEAD~0", "--root", root)
	if code != 0 || !strings.Contains(stdout, ": passed") || strings.Contains(stderr, "local store") {
		t.Fatalf("%d %q %q", code, stdout, stderr)
	}

	args := readFile(t, log)
	if !strings.Contains(args, "StrictHostKeyChecking=yes") || !strings.Contains(args, "IdentityAgent=none") || !strings.Contains(args, "git@git.example.test") {
		t.Fatalf("ssh ran with %q", args)
	}

	if left, _ := filepath.Glob(filepath.Join(tmp, "pipelinegen-ssh-*")); len(left) != 0 { //nolint:errcheck // a bad pattern finds nothing
		t.Fatalf("the key stayed: %v", left)
	}
}

func TestAFailedSSHShowsNoKeyPath(t *testing.T) {
	root, env := recordRepo(t)

	bin, tmp := t.TempDir(), t.TempDir()
	fake := "#!/bin/sh\nwhile [ $# -gt 0 ]; do [ \"$1\" = -i ] && key=\"$2\"; shift; done\n" +
		"echo \"Load key \\\"$key\\\": invalid format\" >&2\necho 'Host key verification failed.' >&2\nexit 255\n"

	if err := testenv.WriteExecutable(t.Context(), filepath.Join(bin, "ssh"), []byte(fake)); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(root, config.FileName), recordConfig+
		"git: {remote: \"ssh://git@git.example.test/srv/r.git\", ssh_key: records_key, known_hosts: [\"git.example.test ssh-ed25519 AAAAtestkey\"]}\n")

	e := maps.Clone(env)
	e["PATH"] = bin + string(os.PathListSeparator) + os.Getenv("PATH")
	e["PIPELINEGEN_SSH_KEY_STORE"] = "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----"
	e["TMPDIR"] = tmp

	_, stderr, code := runIn(t, root, e, "status", "HEAD", "--root", root)
	if code != 1 || !strings.Contains(stderr, "Host key verification failed.") || strings.Contains(stderr, "pipelinegen-ssh-") || strings.Contains(stderr, tmp) {
		t.Fatalf("%d %q", code, stderr)
	}

	if left, _ := filepath.Glob(filepath.Join(tmp, "pipelinegen-ssh-*")); len(left) != 0 { //nolint:errcheck // a bad pattern finds nothing
		t.Fatalf("the key stayed: %v", left)
	}
}
