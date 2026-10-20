package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// A waiting mark says that a holder of several slots at once waits for
// the lock: while one is live, no taker of fewer slots takes one (each
// keeps what it holds until it ends), so the waiter gets them. A waiter
// renews its mark while it waits and removes it when it takes the slots; a
// waiter that died leaves a mark that expires after markTTL.
const (
	markPrefix = "wait-"
	markTTL    = 2 * time.Minute
)

func byNumber(slots []Slot, now time.Time) (map[int]Slot, []Slot) {
	taken := map[int]Slot{}

	var live []Slot

	for _, s := range slots {
		taken[s.N] = s

		if s.Lease.Expires.After(now) {
			live = append(live, s)
		}
	}

	return taken, live
}

func markName(holder string) string {
	sum := sha256.Sum256([]byte(holder))

	return markPrefix + hex.EncodeToString(sum[:6])
}

func markOf(marks []Slot, holder string) (Slot, bool) {
	for _, m := range marks {
		if m.Lease.Holder == holder {
			return m, true
		}
	}

	return Slot{}, false
}

// yields are the live waiting marks of others a taker gives way to though
// slots may be free: every taker gives way to a waiter that needs more slots
// at once, or a heavy waiter would wait for ever behind light takers.
func (l *Lock) yields(_, marks []Slot, now time.Time) []Slot {
	var waiting []Slot

	for _, m := range marks {
		if !m.Lease.Expires.After(now) {
			continue
		}

		if weightOf(m.Lease) > l.weight() {
			waiting = append(waiting, m)
		}
	}

	return waiting
}

func (l *Lock) weight() int { return max(l.Weight, 1) }

func weightOf(lease Lease) int { return max(lease.Weight, 1) }

func (l *Lock) freeSlots(taken map[int]Slot, now time.Time) []int {
	var out []int

	for n := 1; n <= l.slots(); n++ {
		if old, isTaken := taken[n]; !isTaken || !old.Lease.Expires.After(now) {
			out = append(out, n)
		}
	}

	return out
}

// markUpdate puts or renews holder's waiting mark when due: a waiter that
// takes more than one slot leaves one, and renews it once half its TTL
// passed.
func (l *Lock) markUpdate(holder string, marks []Slot, now time.Time) (gitstore.Update, bool) {
	if l.weight() == 1 {
		return gitstore.Update{}, false
	}

	old, had := markOf(marks, holder)
	if had && old.Lease.Expires.Sub(now) > markTTL/2 {
		return gitstore.Update{}, false
	}

	data, err := encodeLease(Lease{Holder: holder, Acquired: now, Expires: now.Add(markTTL), Weight: l.Weight})
	if err != nil {
		return gitstore.Update{}, false
	}

	up := gitstore.Update{Ref: l.prefix() + markName(holder), Files: map[string][]byte{leaseFile: data}}
	if had {
		up.Old = old.Commit
	}

	return up, true
}

func groups(mine []Slot) [][]Slot {
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].Lease.Acquired.Before(mine[j].Lease.Acquired) })

	var out [][]Slot

	for _, s := range mine {
		if n := len(out); n > 0 && out[n-1][0].Lease.Acquired.Equal(s.Lease.Acquired) {
			out[n-1] = append(out[n-1], s)

			continue
		}

		out = append(out, []Slot{s})
	}

	return out
}
