package lock

import (
	"testing"
	"time"
)

func TestARenewedLeaseOutlivesItsTTL(t *testing.T) { bothStores(t, testARenewedLeaseOutlivesItsTTL) }

func testARenewedLeaseOutlivesItsTTL(t *testing.T, branch bool) {
	t.Helper()

	remote := bare(t)
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	c := &clock{now: t0}

	l := newLock(t, remote, 1, branch)
	l.Now, l.TTL = c.Now, 20*time.Minute

	if _, err := l.Acquire(t.Context(), "run main abc", "", 0); err != nil {
		t.Fatal(err)
	}

	expiry := func() time.Time {
		t.Helper()

		slots, err := l.Leases(t.Context())
		if err != nil || len(slots) != 1 {
			t.Fatalf("%+v %v", slots, err)
		}

		return slots[0].Lease.Expires
	}

	c.set(t0.Add(time.Minute))

	if held, err := l.Renew(t.Context(), "run main abc"); err != nil || !held || !expiry().Equal(t0.Add(20*time.Minute)) {
		t.Fatalf("early renew: %v %v %s", held, err, expiry())
	}

	c.set(t0.Add(6 * time.Minute))

	if held, err := l.Renew(t.Context(), "run main abc"); err != nil || !held || !expiry().Equal(t0.Add(26*time.Minute)) {
		t.Fatalf("renew: %v %v %s", held, err, expiry())
	}

	c.set(t0.Add(25 * time.Minute))

	if _, err := l.Acquire(t.Context(), "another", "", 200*time.Millisecond); err == nil {
		t.Fatal("a renewed lease was taken at its first expiry")
	}

	if held, err := l.Renew(t.Context(), "another"); err != nil || held {
		t.Fatalf("another holder renewed: %v %v", held, err)
	}

	c.set(t0.Add(27 * time.Minute))

	if held, err := l.Renew(t.Context(), "run main abc"); err != nil || held {
		t.Fatalf("an expired lease renewed: %v %v", held, err)
	}

	if _, err := l.Acquire(t.Context(), "another", "", 5*time.Second); err != nil {
		t.Fatalf("the next holder: %v", err)
	}
}
