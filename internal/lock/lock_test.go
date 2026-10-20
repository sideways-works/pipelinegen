package lock

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

func bare(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.CommandContext(t.Context(), "git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	// a branch store's reads are partial fetches, which the host serves
	for _, kv := range [][2]string{{"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}} {
		if out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}

	return "file://" + dir
}

func newLock(t *testing.T, remote string, slots int, branch bool) *Lock {
	t.Helper()

	l := &Lock{Name: "browsers", Slots: slots, TTL: time.Hour, Poll: 40 * time.Millisecond}

	if branch {
		b := &branchstore.Store{Remote: remote, Branch: "pipelinegen-locks", Dir: filepath.Join(t.TempDir(), "scratch"), Roots: true}
		t.Cleanup(func() {
			if err := b.Close(); err != nil {
				t.Error(err)
			}
		})

		l.Branch = b

		return l
	}

	s := &gitstore.Store{Remote: remote, Dir: filepath.Join(t.TempDir(), "scratch")}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})

	l.Store = s

	return l
}

func bothStores(t *testing.T, test func(t *testing.T, branch bool)) {
	t.Helper()

	for _, branch := range []bool{false, true} {
		t.Run(map[bool]string{false: "refs", true: "branch"}[branch], func(t *testing.T) { test(t, branch) })
	}
}

func TestNeverMoreHoldersThanSlots(t *testing.T) { bothStores(t, testNeverMoreHoldersThanSlots) }

func testNeverMoreHoldersThanSlots(t *testing.T, branch bool) {
	t.Helper()

	for _, slots := range []int{1, 3} {
		t.Run(fmt.Sprintf("slots=%d", slots), func(t *testing.T) {
			remote := bare(t)

			const acquirers = 20

			var (
				wg            sync.WaitGroup
				holding, most atomic.Int64
				turns         atomic.Int64
			)

			for i := range acquirers {
				wg.Go(func() {
					l := newLock(t, remote, slots, branch)
					holder := fmt.Sprintf("o/r pr pr-%d 0123456789ab e2e", i)

					if _, err := l.Acquire(t.Context(), holder, "", 10*time.Minute); err != nil {
						t.Error(err)

						return
					}

					n := holding.Add(1)

					for {
						m := most.Load()
						if n <= m || most.CompareAndSwap(m, n) {
							break
						}
					}

					time.Sleep(150 * time.Millisecond)
					holding.Add(-1)
					turns.Add(1)

					if freed, err := l.Release(t.Context(), holder); err != nil || freed != 1 {
						t.Errorf("release: %d %v", freed, err)
					}
				})
			}

			wg.Wait()

			if most.Load() > int64(slots) || turns.Load() != acquirers {
				t.Fatalf("%d holders at once with %d slots, %d turns", most.Load(), slots, turns.Load())
			}

			t.Logf("at most %d holders at once", most.Load())

			if left, err := newLock(t, remote, slots, branch).Leases(t.Context()); err != nil || len(left) != 0 {
				t.Fatalf("leases left: %v %v", left, err)
			}
		})
	}
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = t
}

func TestADeadHoldersLeaseIsTakenOverAtItsExpiryOnly(t *testing.T) {
	bothStores(t, testADeadHoldersLeaseIsTakenOverAtItsExpiryOnly)
}

func testADeadHoldersLeaseIsTakenOverAtItsExpiryOnly(t *testing.T, branch bool) {
	t.Helper()

	remote := bare(t)
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	c := &clock{now: t0}

	dead := newLock(t, remote, 1, branch)
	dead.Now = c.Now

	if _, err := dead.Acquire(t.Context(), "dead holder", "https://ci/jobs/1", 0); err != nil {
		t.Fatal(err)
	}

	waiter := newLock(t, remote, 1, branch)
	waiter.Now = c.Now

	var log strings.Builder

	waiter.Log = &log

	c.set(t0.Add(59 * time.Minute))

	if _, err := waiter.Acquire(t.Context(), "waiter", "", 300*time.Millisecond); !errors.Is(err, ErrTimeout) || !strings.Contains(err.Error(), "dead holder") {
		t.Fatalf("took a live lease: %v", err)
	}

	if !strings.Contains(log.String(), "waiting for dead holder (until 2026-01-01T10:00:00Z, https://ci/jobs/1)") {
		t.Fatalf("log: %q", log.String())
	}

	c.set(t0.Add(61 * time.Minute))

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	slot, err := waiter.Acquire(ctx, "waiter", "", 0)
	if err != nil || slot.N != 1 || slot.Lease.Holder != "waiter" {
		t.Fatalf("takeover: %+v %v", slot, err)
	}

	if freed, err := dead.Release(t.Context(), "dead holder"); err != nil || freed != 0 {
		t.Fatalf("dead release: %d %v", freed, err)
	}

	leases, err := waiter.Leases(t.Context())
	if err != nil || len(leases) != 1 || leases[0].Lease.Holder != "waiter" {
		t.Fatalf("%+v %v", leases, err)
	}
}

func TestNotReentrantAndUnlockByHand(t *testing.T) { bothStores(t, testNotReentrantAndUnlockByHand) }

func testNotReentrantAndUnlockByHand(t *testing.T, branch bool) {
	t.Helper()

	remote := bare(t)
	l := newLock(t, remote, 2, branch)

	for range 2 {
		if _, err := l.Acquire(t.Context(), "same", "", time.Second); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := l.Acquire(t.Context(), "same", "", 200*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("re-entered: %v", err)
	}

	if freed, err := l.Release(t.Context(), "same"); err != nil || freed != 1 {
		t.Fatalf("release: %d %v", freed, err)
	}

	if left, err := l.Leases(t.Context()); err != nil || len(left) != 1 || left[0].N != 2 {
		t.Fatalf("left: %+v %v", left, err)
	}

	if _, err := l.Acquire(t.Context(), "same", "", time.Second); err != nil {
		t.Fatal(err)
	}

	removed, err := l.Unlock(t.Context(), 2)
	if err != nil || len(removed) != 1 || removed[0].N != 2 {
		t.Fatalf("unlock 2: %+v %v", removed, err)
	}

	if removed, err := l.Unlock(t.Context(), 0); err != nil || len(removed) != 1 {
		t.Fatalf("unlock all: %+v %v", removed, err)
	}

	for _, bad := range []string{"", "a/b", "x.lock", "..", "has space"} {
		l.Name = bad
		if _, err := l.Acquire(t.Context(), "h", "", time.Second); !errors.Is(err, ErrName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
