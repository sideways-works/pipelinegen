package gitstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// sshRemote is a bare remote reached over "ssh" through a command of the store's environment, which
// runs git's remote end here and notes each call; without it no git call reaches the remote.
func sshRemote(t *testing.T) (remote string, env []string, calls string) {
	t.Helper()

	dir := strings.TrimPrefix(bare(t), "file://")
	calls = filepath.Join(t.TempDir(), "calls")
	ssh := filepath.Join(t.TempDir(), "ssh")

	script := `#!/bin/sh
for last; do :; done
echo "$last" >> "` + calls + `"
exec sh -c "$last"
`
	if err := testenv.WriteExecutable(t.Context(), ssh, []byte(script)); err != nil {
		t.Fatal(err)
	}

	return "ssh://store.invalid" + dir, []string{"GIT_SSH_COMMAND=" + ssh, "GIT_SSH_VARIANT=simple"}, calls
}

func TestTheRunsRecordsGoOverTheStoresSSH(t *testing.T) {
	ctx := t.Context()
	remote, env, calls := sshRemote(t)

	open := func(batch string) *Store {
		s := groupedStore(t, remote, batch)
		s.Env = env

		return s
	}

	state(t, open("1"), "a.spec.ts", "passed")

	if n, err := open("").Compact(ctx, runsRoot+run+"/", "e2e"); err != nil || n != 1 {
		t.Fatalf("compacted %d: %v", n, err)
	}

	r := open("")

	refs, err := r.List(ctx, runsRoot+run+"/")
	if err != nil || len(refs) != 1 {
		t.Fatalf("records %v: %v", refs, err)
	}

	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(got), "git-receive-pack") {
		t.Errorf("no push went through the store's ssh command:\n%s", got)
	}
}
