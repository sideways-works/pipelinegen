package tries

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var errHungUp = errors.New("the remote end hung up unexpectedly")

func fake(p Policy, took time.Duration) (*Run, *[]time.Duration) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	now := start

	if !p.Until.IsZero() {
		p.Until = start.Add(p.Until.Sub(time.Time{}))
	}

	var waits []time.Duration

	r := &Run{p: p, start: start}
	r.now = func() time.Time { return now }
	r.sleep = func(_ context.Context, d time.Duration) {
		waits = append(waits, d)
		now = now.Add(d + took)
	}

	return r, &waits
}

func TestTheWaitsGrowWithJitter(t *testing.T) {
	r, waits := fake(Policy{Retries: 9, First: time.Second}, 0)

	for r.Again(t.Context(), Passing) {
	}

	full := []int{1, 2, 4, 8, 16, 30, 30, 30, 30}
	if len(*waits) != len(full) {
		t.Fatalf("waits %v", *waits)
	}

	same := true

	for i, w := range *waits {
		f := time.Duration(full[i]) * time.Second
		if w < f/2 || w > f {
			t.Errorf("wait %d: %s, want %s to %s", i, w, f/2, f)
		}

		same = same && w == f
	}

	if same {
		t.Error("no wait was cut: no jitter")
	}
}

func TestRetriesAreAMinimumAndTheDeadlineABound(t *testing.T) {
	r, waits := fake(Policy{Retries: 2, First: time.Second, Deadline: 5 * time.Minute}, 10*time.Second)
	for r.Again(t.Context(), Passing) {
	}

	// every retry starts within the deadline; the try itself may end past it
	var spent time.Duration
	for i, w := range *waits {
		spent += w
		if i > 0 {
			spent += 10 * time.Second
		}
	}

	if len(*waits) <= 2 || spent > 5*time.Minute {
		t.Errorf("%d retries over %s", len(*waits), spent)
	}

	r, waits = fake(Policy{Retries: 6, First: time.Second, Deadline: time.Second}, time.Minute)
	for r.Again(t.Context(), Passing) {
	}

	if len(*waits) != 6 {
		t.Errorf("a deadline cut the retries short: %d", len(*waits))
	}

	r, _ = fake(Policy{}, 0)
	if r.Again(t.Context(), Passing) {
		t.Error("no retries and no deadline went again")
	}
}

func TestNoWaitPassesTheHardEnd(t *testing.T) {
	r, waits := fake(Policy{Retries: 50, First: time.Second, Deadline: time.Hour, Until: time.Time{}.Add(time.Minute)}, 0)
	for r.Again(t.Context(), Passing) {
	}

	var spent time.Duration
	for _, w := range *waits {
		spent += w
	}

	if spent > time.Minute || len(*waits) == 0 {
		t.Errorf("%d retries over %s", len(*waits), spent)
	}

	if err := r.Err(errHungUp); !strings.Contains(err.Error(), "the step's time limit") {
		t.Errorf("says %v", err)
	}
}

func TestRefusalsByARuleAndDefects(t *testing.T) {
	r, waits := fake(Policy{Retries: 10, First: time.Second, Deadline: time.Hour}, 0)
	for r.Again(t.Context(), Ruled) {
	}

	if len(*waits) != RuledRetries {
		t.Errorf("a refusal by a rule went again %d times", len(*waits))
	}

	r, waits = fake(Policy{Retries: 1, First: time.Second}, 0)
	for r.Again(t.Context(), Ruled) {
	}

	if len(*waits) != 1 {
		t.Errorf("more than the retries asked for: %d", len(*waits))
	}

	r, waits = fake(Policy{Retries: 10, First: time.Second, Deadline: time.Hour}, 0)
	if r.Again(t.Context(), Defect) || len(*waits) != 0 {
		t.Error("a defect went again")
	}
}

func TestTheLastErrorSaysWhatWasTried(t *testing.T) {
	cause := errHungUp

	r, _ := fake(Policy{Retries: 3, First: time.Second}, 0)
	for r.Again(t.Context(), Passing) {
	}

	err := r.Err(cause)
	if !errors.Is(err, cause) {
		t.Fatalf("lost its cause: %v", err)
	}

	for _, want := range []string{"4 tries over", "its 3 retries are spent", "the last: the remote end hung up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q says nothing of %q", err, want)
		}
	}

	r, _ = fake(Policy{Retries: 3, First: time.Second}, 0)
	if got := r.Err(cause); got.Error() != cause.Error() {
		t.Errorf("a first try's failure wrapped: %v", got)
	}
}

func TestKindsOfGitFailures(t *testing.T) {
	for printed, want := range map[string]Kind{
		"! [remote rejected] x -> refs/a (pre-receive hook declined)":                       Ruled,
		"remote: Permission to o/r.git denied to someone.":                                  Ruled,
		"fatal: unable to access 'https://h/r.git/': The requested URL returned error: 403": Ruled,
		"fatal: the remote end hung up unexpectedly":                                        Passing,
		"send-pack: unexpected disconnect while reading sideband packet":                    Passing,
		"! [remote rejected] x -> refs/a (failed)":                                          Passing,
		"error: RPC failed; HTTP 502 curl 22 The requested URL returned error: 502":         Passing,
	} {
		if got := Of(printed); got != want {
			t.Errorf("%q: %d, want %d", printed, got, want)
		}
	}
}

func TestNoWaitPassesTheContextsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	slept := 0
	r := &Run{p: Policy{Retries: 10, First: 20 * time.Second}, start: time.Now(), now: time.Now}
	r.sleep = func(context.Context, time.Duration) { slept++ }

	if r.Again(ctx, Passing) || slept != 0 {
		t.Fatalf("waited past the deadline (%d)", slept)
	}

	if err := r.Err(errHungUp); !strings.Contains(err.Error(), "1 try over") || !strings.Contains(err.Error(), "the step's time limit is near") {
		t.Errorf("says %v", err)
	}

	if dl, _ := ctx.Deadline(); !HardEnd(ctx, time.Time{}).Equal(dl) || !HardEnd(t.Context(), dl).Equal(dl) {
		t.Error("HardEnd is not the earlier of the two")
	}
}
