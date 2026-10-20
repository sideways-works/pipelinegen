// Package lock is a lease between runs: Concourse's serial groups hold only
// inside one pipeline, and each run is a pipeline of its own, so a suite
// that needs a machine to itself takes a slot of a lock. Taking a free slot
// is a create-only push and taking over an expired one a push that names the
// old value, so two holders never share a slot.
package lock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Lease is a slot's content.
type Lease struct {
	Holder   string    `json:"holder"`
	JobURL   string    `json:"job_url,omitempty"`
	Acquired time.Time `json:"acquired"`
	Expires  time.Time `json:"expires"`
	// Weight is how many slots the holder took at once (a waiting mark:
	// how many it waits for); 0 is 1.
	Weight int `json:"weight,omitempty"`
}

// Slot is one held slot of a lock.
type Slot struct {
	N      int
	Ref    string
	Commit string
	Lease  Lease
}

// Lock is one named lock in a store.
type Lock struct {
	Store *gitstore.Store
	// Branch, when set, keeps the slots as files of a branch store instead
	// of refs: locks/<name>/<n>.json, each holding its lease.
	Branch *branchstore.Store
	Name   string
	Slots  int
	TTL    time.Duration
	Poll   time.Duration
	Now    func() time.Time
	Log    io.Writer
	// Weight is how many slots each acquire takes, all at once or none
	// (default 1): a suite that needs a whole machine takes every slot of
	// its lock.
	Weight int
}

// ErrName wraps a lock name or holder that can't be used.
var ErrName = errors.New("bad lock")

// ErrTimeout is returned when the lock stayed held past the wait.
var ErrTimeout = errors.New("the lock stayed held")

const (
	leaseFile   = "lease.json"
	refPrefix   = "refs/pipelinegen/locks/"
	defaultPoll = 15 * time.Second
)

func (l *Lock) now() time.Time {
	if l.Now != nil {
		return l.Now().UTC()
	}

	return time.Now().UTC()
}

func (l *Lock) slots() int { return max(l.Slots, 1) }

func (l *Lock) prefix() string { return refPrefix + l.Name + "/" }

// SlotRef is a slot's ref; slots count from 1.
func (l *Lock) SlotRef(n int) string { return l.prefix() + strconv.Itoa(n) }

func (l *Lock) check(holder string) error {
	if l.Name == "" || strings.ContainsAny(l.Name, "/\\ ~^:?*[\n\t") || strings.HasPrefix(l.Name, ".") ||
		strings.Contains(l.Name, "..") || strings.HasSuffix(l.Name, ".lock") {
		return fmt.Errorf("%w: name %q", ErrName, l.Name)
	}

	if strings.TrimSpace(holder) == "" {
		return fmt.Errorf("%w: no holder", ErrName)
	}

	return nil
}

// Leases reads every slot of the lock, held or expired.
func (l *Lock) Leases(ctx context.Context) ([]Slot, error) {
	if l.Branch != nil {
		v, err := l.Branch.Snapshot(ctx)
		if err != nil {
			return nil, err
		}

		return l.branchLeases(ctx, v)
	}

	slots, _, err := l.all(ctx)

	return slots, err
}

func (l *Lock) all(ctx context.Context) ([]Slot, []Slot, error) {
	refs, err := l.Store.List(ctx, l.prefix())
	if err != nil {
		return nil, nil, err
	}

	commits := make([]string, 0, len(refs))
	for _, ref := range refs {
		commits = append(commits, ref.Commit)
	}

	data, err := l.Store.FileEach(ctx, commits, leaseFile)
	if err != nil {
		return nil, nil, err
	}

	var out, marks []Slot

	for name, ref := range refs {
		var lease Lease

		// an unreadable lease has no expiry, so it counts as expired and
		// its slot is taken over
		if raw, ok := data[ref.Commit]; ok {
			if err := json.Unmarshal(raw, &lease); err != nil {
				lease = Lease{}
			}
		}

		part := strings.TrimPrefix(name, l.prefix())

		if strings.HasPrefix(part, markPrefix) {
			marks = append(marks, Slot{Ref: name, Commit: ref.Commit, Lease: lease})

			continue
		}

		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}

		out = append(out, Slot{N: n, Ref: name, Commit: ref.Commit, Lease: lease})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })

	return out, marks, nil
}

// Acquire waits until a slot is free or expired and takes it for holder,
// for at most wait (0: until ctx ends). It is never re-entrant: a slot
// already held by the same holder is someone else's to the caller.
func (l *Lock) Acquire(ctx context.Context, holder, jobURL string, wait time.Duration) (Slot, error) {
	if err := l.check(holder); err != nil {
		return Slot{}, err
	}

	start := time.Now()
	said := ""

	for {
		slot, ok, held, err := l.try(ctx, holder, jobURL)
		if err != nil {
			return Slot{}, err
		}

		if ok {
			return slot, nil
		}

		if who := describe(held); who != said && held != nil {
			l.logf("lock %s: waiting for %s", l.Name, who)
			said = who
		}

		if wait > 0 && time.Since(start) >= wait {
			return Slot{}, fmt.Errorf("lock %s: %w for %s by %s", l.Name, ErrTimeout, wait, describe(held))
		}

		pause := l.Poll
		if pause <= 0 {
			pause = defaultPoll
		}

		// a lost race (held == nil) tries again soon
		if held == nil {
			pause /= 10
		}

		t := time.NewTimer(time.Duration(rand.Int64N(int64(pause))) + 1) //nolint:gosec // jitter, not a secret

		select {
		case <-ctx.Done():
			t.Stop()

			return Slot{}, fmt.Errorf("lock %s: %w", l.Name, ctx.Err())
		case <-t.C:
		}
	}
}

func (l *Lock) mark(ctx context.Context, holder string, marks []Slot, now time.Time) error {
	up, due := l.markUpdate(holder, marks, now)
	if !due {
		return nil
	}

	if _, err := l.Store.Write(ctx, "waits "+holder, up); err != nil && !errors.Is(err, gitstore.ErrStale) {
		return err
	}

	return nil
}

func (l *Lock) try(ctx context.Context, holder, jobURL string) (Slot, bool, []Slot, error) {
	if l.Branch != nil {
		return l.branchTry(ctx, holder, jobURL)
	}

	slots, marks, err := l.all(ctx)
	if err != nil {
		return Slot{}, false, nil, err
	}

	now := l.now()
	taken, live := byNumber(slots, now)

	if waitFor := l.yields(live, marks, now); waitFor != nil {
		return Slot{}, false, waitFor, nil
	}

	free := l.freeSlots(taken, now)
	if len(free) < l.weight() {
		if live == nil {
			// no slot is live, yet fewer are free than it takes (a lease
			// beyond a count since lowered): wait like when all are held
			live = []Slot{}
		}

		return Slot{}, false, live, l.mark(ctx, holder, marks, now)
	}

	lease := Lease{Holder: holder, JobURL: jobURL, Acquired: now, Expires: now.Add(l.TTL), Weight: l.Weight}

	data, err := encodeLease(lease)
	if err != nil {
		return Slot{}, false, nil, err
	}

	// all its slots in one write, or none: a free slot is created, an
	// expired one taken over from the value it was read with; the push
	// fails when someone else was first
	var ups []gitstore.Update

	for _, n := range free[:l.weight()] {
		up := gitstore.Update{Ref: l.SlotRef(n), Files: map[string][]byte{leaseFile: data}}
		if old, isTaken := taken[n]; isTaken {
			up.Old = old.Commit
		}

		ups = append(ups, up)
	}

	// its waiting mark goes in the same write
	if m, ok := markOf(marks, holder); ok {
		ups = append(ups, gitstore.Update{Ref: m.Ref, Old: m.Commit, Delete: true})
	}

	written, err := l.Store.Write(ctx, "lease "+holder, ups...)

	switch {
	case errors.Is(err, gitstore.ErrStale):
		return Slot{}, false, nil, nil
	case err != nil:
		return Slot{}, false, nil, err
	}

	first := ups[0].Ref

	return Slot{N: free[0], Ref: first, Commit: written[first], Lease: lease}, true, nil, nil
}

func describe(held []Slot) string {
	parts := make([]string, 0, len(held))
	for _, s := range held {
		p := fmt.Sprintf("%s (until %s", s.Lease.Holder, s.Lease.Expires.Format(time.RFC3339))
		if s.Lease.JobURL != "" {
			p += ", " + s.Lease.JobURL
		}

		parts = append(parts, p+")")
	}

	return strings.Join(parts, "; ")
}

func (l *Lock) logf(format string, args ...any) {
	if l.Log == nil {
		return
	}

	if _, err := fmt.Fprintf(l.Log, format+"\n", args...); err != nil {
		return
	}
}

// Release frees what one acquire of holder's took, the first it took, and
// only while it still holds the value read: a holder that overran its lease
// never frees its successor's, and two builds under one holder name each
// free their own. It says how many it freed (0 or 1).
func (l *Lock) Release(ctx context.Context, holder string) (int, error) {
	if err := l.check(holder); err != nil {
		return 0, err
	}

	if l.Branch != nil {
		return l.branchRelease(ctx, holder)
	}

	slots, err := l.Leases(ctx)
	if err != nil {
		return 0, err
	}

	var mine []Slot

	for _, s := range slots {
		if s.Lease.Holder == holder {
			mine = append(mine, s)
		}
	}

	// the slots one acquire took go together, in one write
	for _, group := range groups(mine) {
		ups := make([]gitstore.Update, 0, len(group))
		for _, s := range group {
			ups = append(ups, gitstore.Update{Ref: s.Ref, Old: s.Commit, Delete: true})
		}

		_, err := l.Store.Write(ctx, "release "+holder, ups...)

		switch {
		case errors.Is(err, gitstore.ErrStale):
			continue
		case err != nil:
			return 0, err
		}

		return 1, nil
	}

	l.logf("lock %s: %s held no slot (its lease expired and was taken, or it never had one)", l.Name, holder)

	return 0, nil
}

// Unlock frees a slot by hand (n), or every slot (n 0), whoever holds it,
// and returns the leases it removed.
func (l *Lock) Unlock(ctx context.Context, n int) ([]Slot, error) {
	if err := l.check("by hand"); err != nil {
		return nil, err
	}

	if l.Branch != nil {
		return l.branchUnlock(ctx, n)
	}

	slots, err := l.Leases(ctx)
	if err != nil {
		return nil, err
	}

	var removed []Slot

	for _, s := range slots {
		if n != 0 && s.N != n {
			continue
		}

		if _, err := l.Store.Write(ctx, "unlock by hand", gitstore.Update{Ref: s.Ref, Any: true, Delete: true}); err != nil {
			return removed, err
		}

		removed = append(removed, s)
	}

	return removed, nil
}
