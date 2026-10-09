package gitstore

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/testenv"
	"github.com/sideways-works/pipelinegen/internal/tries"
)

// refusing gives a bare remote a pre-receive hook that refuses pushes the
// way a host busy with another push to the same repository does: with
// firstK > 0, the first firstK pushes it gets; with firstK 0, the first push
// of every writer (told apart by the first ref it pushes).
func refusing(t *testing.T, firstK int) string {
	t.Helper()

	remote := bare(t)
	dir := strings.TrimPrefix(remote, "file://")
	state := t.TempDir()

	hook := `#!/bin/sh
read -r old new ref
if [ ` + strconv.Itoa(firstK) + ` -gt 0 ]; then
	n=$(( $(cat "` + state + `/count" 2>/dev/null || echo 0) + 1 ))
	echo "$n" > "` + state + `/count"
	[ "$n" -gt ` + strconv.Itoa(firstK) + ` ] && exit 0
else
	mark="` + state + `/$(printf %s "$ref" | tr / _)"
	[ -e "$mark" ] && exit 0
	: > "$mark"
fi
echo "busy with another push" >&2
exit 1
`
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	return remote
}

// dropping gives a bare remote a pre-receive hook that drops the first k
// pushes' connections, as a host busy with other pushes does: nothing of
// such a push is stored.
func dropping(t *testing.T, k int) string {
	t.Helper()

	remote := bare(t)
	state := filepath.Join(t.TempDir(), "count")

	hook := `#!/bin/sh
n=$(( $(cat "` + state + `" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "` + state + `"
[ "$n" -gt ` + strconv.Itoa(k) + ` ] && exit 0
kill -9 $PPID
exit 1
`
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(strings.TrimPrefix(remote, "file://"), "hooks", "pre-receive"), []byte(hook)); err != nil {
		t.Fatal(err)
	}

	return remote
}

func write(t *testing.T, s *Store, writer int) error {
	t.Helper()

	var ups []Update
	for i := range 3 {
		ups = append(ups, Update{Ref: fmt.Sprintf("%sw%d/s%d", prefix, writer, i), Any: true, Files: files("state.json", "passed")})
	}

	_, err := s.Write(t.Context(), "slices", ups...)

	return err
}

func TestAWriteLostInPassingGoesAgain(t *testing.T) {
	const retries = 3

	for k := 0; k <= retries+1; k++ {
		t.Run(fmt.Sprintf("dropped %d times", k), func(t *testing.T) {
			remote := bare(t)
			if k > 0 {
				remote = dropping(t, k)
			}

			s := newStore(t, remote)
			s.Retries = retries
			syncBranch(s)

			err := write(t, s, 0)

			switch {
			case k <= retries && err != nil:
				t.Fatalf("k=%d: %v", k, err)
			case k <= retries && s.Retried() != k:
				t.Fatalf("k=%d: %d retries counted", k, s.Retried())
			case k > retries && (err == nil || errors.Is(err, ErrStale) ||
				!strings.Contains(err.Error(), "4 tries over") || !strings.Contains(err.Error(), "hung up")):
				t.Fatalf("k=%d: got %v", k, err)
			}
		})
	}

	t.Run("a guarded write still refused reads again", func(t *testing.T) {
		s := newStore(t, refusing(t, 5))
		s.Retries = 1
		syncBranch(s)

		_, err := s.Write(t.Context(), "lease", Update{Ref: prefix + "lock/1", Files: files("lease.json", "{}")})
		if !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "remote rejected") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("no retries", func(t *testing.T) {
		s := newStore(t, refusing(t, 1))
		s.Retries = 0
		syncBranch(s)

		if err := write(t, s, 0); err == nil {
			t.Fatal("a write with no retries survived a refusal")
		}
	})
}

func TestARefusalByARuleGoesAgainOnlyAFewTimes(t *testing.T) {
	s := newStore(t, refusing(t, 100))
	s.Retries = 10
	syncBranch(s)

	err := write(t, s, 0)
	if err == nil || s.Retried() != tries.RuledRetries || !strings.Contains(err.Error(), "by a rule of its own") {
		t.Fatalf("%d retries: %v", s.Retried(), err)
	}
}

func TestAWriteGoesAgainUntilItsDeadline(t *testing.T) {
	s := newStore(t, dropping(t, 1000))
	s.Retries, s.Backoff, s.RetryFor = 1, 10*time.Millisecond, 2*time.Second
	syncBranch(s)

	start := time.Now()

	err := write(t, s, 0)
	if took := time.Since(start); err == nil || s.Retried() < 3 || took > 20*time.Second || !strings.Contains(err.Error(), "its deadline of 2s") {
		t.Fatalf("%d retries in %s: %v", s.Retried(), took, err)
	}
}

func TestWritersThatMeetAllLand(t *testing.T) {
	for _, backoff := range []time.Duration{0, 10 * time.Millisecond} {
		t.Run("backoff "+backoff.String(), func(t *testing.T) {
			remote := refusing(t, 0)

			var (
				wg   sync.WaitGroup
				errs = make([]error, 6)
			)

			for w := range 6 {
				s := newStore(t, remote)
				s.Retries, s.Backoff = 10, backoff
				syncBranch(s)

				wg.Go(func() { errs[w] = write(t, s, w) })
			}

			wg.Wait()

			if err := errors.Join(errs...); err != nil {
				t.Fatal(err)
			}

			if refs, err := newStore(t, remote).List(t.Context(), prefix); err != nil || len(refs) != 18 {
				t.Fatalf("%d refs, %v", len(refs), err)
			}
		})
	}
}

func TestOnlyALostLeaseIsStale(t *testing.T) {
	for line, stale := range map[string]bool{
		"!\t0123:refs/pipelinegen/a/=\t[rejected] (stale info)":                                                                  true,
		"!\t0123:refs/pipelinegen/a/=\t[rejected] (fetch first)":                                                                 true,
		"!\t0123:refs/pipelinegen/a/=\t[remote rejected] (cannot lock ref 'refs/a': is at 1 but expected 2)":                     true,
		"!\t624941f0:refs/pipelinegen/runs/main/main/23ce9ff4139f/smoke/count_smoke.sh/=\t[remote rejected] (failed)":            false,
		"!\t0123:refs/pipelinegen/runs/main/main/29213d02ebf4/unit/mul_test.sh/=\t[remote rejected] (missing necessary objects)": false,
		"!\t0123:refs/pipelinegen/a/=\t[remote rejected] (atomic transaction failed)":                                            false,
		"!\t0123:refs/pipelinegen/a/=\t[remote rejected] (pre-receive hook declined)":                                            false,
	} {
		if leaseLost(line) != stale {
			t.Errorf("%q: stale %v", line, !stale)
		}
	}
}

// hostRefusing puts a git first on PATH that answers the first k pushes
// as the hosting service did, with "[remote rejected] (<why>)" for every
// ref and nothing stored, and runs the real git for everything else.
func hostRefusing(t *testing.T, k int, why string) {
	t.Helper()

	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	count := filepath.Join(dir, "count")

	wrapper := `#!/bin/sh
for a in "$@"; do
	[ "$a" = push ] || continue
	n=$(( $(cat "` + count + `" 2>/dev/null || echo 0) + 1 ))
	echo "$n" > "` + count + `"
	[ "$n" -gt ` + strconv.Itoa(k) + ` ] && break
	for r in "$@"; do
		case $r in -*|push) ;; *:*) printf '!\t%s\t[remote rejected] (` + why + `)\n' "${r#+}" ;; esac
	done
	echo Done
	exit 1
done
exec '` + git + `' "$@"
`
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "git"), []byte(wrapper)); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestTheHostsRefusalsAreRetried(t *testing.T) {
	for _, why := range []string{"failed", "missing necessary objects"} {
		t.Run(why, func(t *testing.T) {
			remote := bare(t)
			hostRefusing(t, 2, why)

			s := newStore(t, remote)
			s.Retries = 3
			syncBranch(s)

			if err := write(t, s, 0); err != nil {
				t.Fatal(err)
			}

			if s.Retried() != 2 {
				t.Fatalf("%d retries", s.Retried())
			}

			if refs, err := newStore(t, remote).List(t.Context(), prefix); err != nil || len(refs) != 3 {
				t.Fatalf("%d refs, %v", len(refs), err)
			}
		})
	}
}

func syncBranch(s *Store) {
	if s.Branch != nil {
		s.Branch.Retries, s.Branch.Backoff, s.Branch.RetryFor = s.Retries, s.Backoff, s.RetryFor
	}
}

// readsDropping puts a git first on PATH whose reads of a batch's ref
// (ls-remote) lose their connection the first k times, and runs the real
// git for everything else.
func readsDropping(t *testing.T, k int) {
	t.Helper()

	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	count := filepath.Join(dir, "count")

	wrapper := `#!/bin/sh
case "$*" in
*ls-remote*` + BatchPrefix + `*)
	n=$(( $(cat "` + count + `" 2>/dev/null || echo 0) + 1 ))
	echo "$n" > "` + count + `"
	if [ "$n" -le ` + strconv.Itoa(k) + ` ]; then
		echo "fatal: the remote end hung up unexpectedly" >&2
		exit 128
	fi
	;;
esac
exec '` + git + `' "$@"
`
	if err := testenv.WriteExecutable(t.Context(), filepath.Join(dir, "git"), []byte(wrapper)); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAReadLostBeforeThePushGoesAgain(t *testing.T) {
	remote := bare(t)
	s := groupedStore(t, remote, "1")
	s.Retries = 3

	readsDropping(t, 4)

	if _, err := s.Write(t.Context(), "state", Update{Ref: stateRef("a.spec.ts"), Any: true, Files: files("state.json", "passed")}); err != nil {
		t.Fatal(err)
	}

	if s.Retried() != 1 {
		t.Errorf("%d retries counted", s.Retried())
	}

	if got := readAll(t, remote)[stateRef("a.spec.ts")]; got != "state.json=passed" {
		t.Errorf("reads %q", got)
	}
}
