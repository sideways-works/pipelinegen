package gitstore

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sideways-works/pipelinegen/internal/testenv"
)

// noAtomicRemote is a bare remote whose receive-pack takes no atomic
// pushes, as some git servers are set up.
func noAtomicRemote(t *testing.T) string {
	t.Helper()

	remote := bare(t)
	if out, err := exec.CommandContext(t.Context(), "git", "-C", strings.TrimPrefix(remote, "file://"), "config", "receive.advertiseAtomic", "false").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	return remote
}

// branchesOnly is a bare remote that takes branches and tags only, and
// declines any other ref in the words of a host that does so.
func branchesOnly(t *testing.T) string {
	t.Helper()

	remote := bare(t)

	hook := `#!/bin/sh
while read -r old new ref; do
	case "$ref" in
	refs/heads/*|refs/tags/*) ;;
	*) echo "Unexpected ref: $ref" >&2; exit 1 ;;
	esac
done
`
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(strings.TrimPrefix(remote, "file://"), "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	return remote
}

func TestARemoteWithoutAtomicPushesSaysWhatToSet(t *testing.T) {
	s := newStore(t, noAtomicRemote(t))
	s.Retries = 3
	syncBranch(s)

	plain := write(t, s, 0)
	_, record := s.Write(t.Context(), "slice", Update{Ref: stateRef("a.spec.ts"), Any: true, Files: files("state.json", "passed")})

	if onBranch {
		if err := errors.Join(plain, record); err != nil {
			t.Fatalf("on a branch: %v", err)
		}

		return
	}

	for name, err := range map[string]error{"refs": plain, "a run's records": record} {
		if !errors.Is(err, ErrNoAtomic) || !strings.Contains(err.Error(), "store.backend: branch") {
			t.Errorf("%s: %v", name, err)
		}
	}

	if s.Retried() != 0 {
		t.Errorf("a remote without atomic pushes was tried %d times again", s.Retried())
	}
}

func TestARemoteThatTakesOnlyBranchesSaysWhatToSet(t *testing.T) {
	s := newStore(t, branchesOnly(t))
	s.Retries = 1
	syncBranch(s)

	plain := write(t, s, 0)
	_, record := s.Write(t.Context(), "slice", Update{Ref: stateRef("a.spec.ts"), Any: true, Files: files("state.json", "passed")})

	if onBranch {
		if err := errors.Join(plain, record); err != nil {
			t.Fatalf("on a branch: %v", err)
		}

		return
	}

	for name, err := range map[string]error{"refs": plain, "a run's records": record} {
		if err == nil || !strings.Contains(err.Error(), "Unexpected ref") || !strings.Contains(err.Error(), "store.backend: branch") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
