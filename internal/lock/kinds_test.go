package lock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestADeadWaitersMarkExpires(t *testing.T) { bothStores(t, testADeadWaitersMarkExpires) }

func testADeadWaitersMarkExpires(t *testing.T, branch bool) {
	t.Helper()

	remote := bare(t)
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	c := &clock{now: t0}

	light := newLock(t, remote, 2, branch)
	light.Now = c.Now

	heavy := newLock(t, remote, 2, branch)
	heavy.Weight, heavy.Now = 2, c.Now

	if _, err := light.Acquire(t.Context(), "main batch 1", "", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	if _, err := heavy.Acquire(t.Context(), "pr-7 checks", "", 200*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("the waiter: %v", err)
	}

	if _, err := light.Acquire(t.Context(), "main batch 2", "", 300*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a live mark didn't hold the lighter taker back: %v", err)
	}

	c.set(t0.Add(markTTL + time.Second))

	if _, err := light.Acquire(t.Context(), "main batch 2", "", 5*time.Second); err != nil {
		t.Fatalf("after the mark expired: %v", err)
	}
}

func wait(t *testing.T, l *Lock, holder string) chan error {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	got := make(chan error, 1)

	go func() {
		_, err := l.Acquire(ctx, holder, "", 0)
		got <- err
	}()

	return got
}

func took(t *testing.T, got chan error) {
	t.Helper()

	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the waiter never took the free slot")
	}
}

func liveMarks(t *testing.T, l *Lock) []Slot {
	t.Helper()

	var marks []Slot

	if l.Branch != nil {
		v, err := l.Branch.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		if _, marks, err = l.branchAll(t.Context(), v); err != nil {
			t.Fatal(err)
		}
	} else {
		var err error
		if _, marks, err = l.all(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	var out []Slot

	for _, m := range marks {
		if m.Lease.Expires.After(l.now()) {
			out = append(out, m)
		}
	}

	return out
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()

	for range 200 {
		if ok() {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("waited in vain")
}

func TestAWeightTakesItsSlotsAtOnce(t *testing.T) { bothStores(t, testAWeightTakesItsSlotsAtOnce) }

func testAWeightTakesItsSlotsAtOnce(t *testing.T, branch bool) {
	t.Helper()

	remote := bare(t)

	heavy := newLock(t, remote, 4, branch)
	heavy.Weight = 4

	light := newLock(t, remote, 4, branch)

	if _, err := heavy.Acquire(t.Context(), "pr-7 bazel-checks", "", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	slots, err := light.Leases(t.Context())
	if err != nil || len(slots) != 4 {
		t.Fatalf("a weight of 4 took %d slots: %v", len(slots), err)
	}

	if _, err := light.Acquire(t.Context(), "pr-8 backend", "", 300*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a slot of a held weight was taken: %v", err)
	}

	if n, err := heavy.Release(t.Context(), "pr-7 bazel-checks"); err != nil || n != 1 {
		t.Fatalf("release: %d %v", n, err)
	}

	if slots, err := light.Leases(t.Context()); err != nil || len(slots) != 0 {
		t.Fatalf("after the release: %+v %v", slots, err)
	}
}

func TestAWeightedWaiterIsNotStarved(t *testing.T) { bothStores(t, testAWeightedWaiterIsNotStarved) }

func testAWeightedWaiterIsNotStarved(t *testing.T, branch bool) {
	t.Helper()

	remote := bare(t)

	light := newLock(t, remote, 4, branch)
	heavy := newLock(t, remote, 4, branch)
	heavy.Weight = 4

	arrival := newLock(t, remote, 4, branch)
	observer := newLock(t, remote, 4, branch)

	for _, h := range []string{"main backend 1", "main backend 2"} {
		if _, err := light.Acquire(t.Context(), h, "", 5*time.Second); err != nil {
			t.Fatalf("%s: %v", h, err)
		}
	}

	got := wait(t, heavy, "pr-7 bazel-checks")

	waitFor(t, func() bool { return len(liveMarks(t, observer)) == 1 })

	if _, err := arrival.Acquire(t.Context(), "pr-9 backend", "", 300*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a weight-1 arrival took a slot a weight-4 waiter needs: %v", err)
	}

	for _, h := range []string{"main backend 1", "main backend 2"} {
		if _, err := light.Release(t.Context(), h); err != nil {
			t.Fatal(err)
		}
	}

	took(t, got)

	if _, err := arrival.Acquire(t.Context(), "pr-9 backend", "", 300*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("an arrival got a slot of the held weight: %v", err)
	}

	if _, err := heavy.Release(t.Context(), "pr-7 bazel-checks"); err != nil {
		t.Fatal(err)
	}

	if _, err := arrival.Acquire(t.Context(), "pr-9 backend", "", 5*time.Second); err != nil {
		t.Fatalf("the arrival after the weight: %v", err)
	}
}
