package lock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
)

func (l *Lock) dir() string { return "locks/" + l.Name }

func (l *Lock) slotPath(n int) string { return l.dir() + "/" + strconv.Itoa(n) + ".json" }

func (l *Lock) branchLeases(ctx context.Context, v *branchstore.View) ([]Slot, error) {
	slots, _, err := l.branchAll(ctx, v)

	return slots, err
}

func (l *Lock) branchAll(ctx context.Context, v *branchstore.View) ([]Slot, []Slot, error) {
	names, err := v.List(ctx, l.dir())
	if err != nil {
		return nil, nil, err
	}

	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, l.dir()+"/"+name)
	}

	data, err := v.Read(ctx, paths)
	if err != nil {
		return nil, nil, err
	}

	var out, marks []Slot

	for _, p := range paths {
		if !strings.HasSuffix(p, ".json") {
			continue
		}

		var lease Lease

		// an unreadable lease has no expiry, so it counts as expired and
		// its slot is taken over
		if raw, ok := data[p]; ok && json.Unmarshal(raw, &lease) != nil {
			lease = Lease{}
		}

		base := strings.TrimSuffix(path.Base(p), ".json")

		if strings.HasPrefix(base, markPrefix) {
			marks = append(marks, Slot{Ref: p, Lease: lease})

			continue
		}

		n, err := strconv.Atoi(base)
		if err != nil {
			continue
		}

		out = append(out, Slot{N: n, Ref: p, Lease: lease})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })

	return out, marks, nil
}

func encodeLease(lease Lease) ([]byte, error) {
	data, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode lease: %w", err)
	}

	return append(data, '\n'), nil
}

// branchTry looks once and takes a free or expired slot in one write; a
// write applied again after a lost race decides again, and finds the lease
// there when an earlier try of its own landed.
func (l *Lock) branchTry(ctx context.Context, holder, jobURL string) (Slot, bool, []Slot, error) {
	now := l.now()
	lease := Lease{Holder: holder, JobURL: jobURL, Acquired: now, Expires: now.Add(l.TTL), Weight: l.Weight}

	data, err := encodeLease(lease)
	if err != nil {
		return Slot{}, false, nil, err
	}

	var (
		took Slot
		ok   bool
		live []Slot
	)

	err = l.Branch.Update(ctx, "lease "+holder, func(ctx context.Context, v *branchstore.View) (branchstore.Change, error) {
		took, ok, live = Slot{}, false, nil

		slots, marks, err := l.branchAll(ctx, v)
		if err != nil {
			return branchstore.Change{}, err
		}

		taken, liveNow := byNumber(slots, now)
		live = liveNow

		for _, s := range live {
			if mine, err := encodeLease(s.Lease); err == nil && bytes.Equal(mine, data) {
				took, ok = Slot{N: s.N, Ref: s.Ref, Lease: lease}, true

				return branchstore.Change{}, nil
			}
		}

		if waitFor := l.yields(live, marks, now); waitFor != nil {
			live = waitFor

			return branchstore.Change{}, nil
		}

		free := l.freeSlots(taken, now)
		if len(free) < l.weight() {
			if live == nil {
				live = []Slot{}
			}

			return l.branchMark(holder, marks, now), nil
		}

		// all its slots in one write, or none
		change := branchstore.Change{Files: map[string][]byte{}}
		for _, n := range free[:l.weight()] {
			change.Files[l.slotPath(n)] = data
		}

		took, ok = Slot{N: free[0], Ref: l.slotPath(free[0]), Lease: lease}, true

		// its waiting mark goes in the same write
		if m, had := markOf(marks, holder); had {
			change.Deletes = []string{m.Ref}
		}

		return change, nil
	})
	if err != nil {
		return Slot{}, false, nil, err
	}

	if ok {
		return took, true, nil, nil
	}

	return Slot{}, false, live, nil
}

func (l *Lock) branchMark(holder string, marks []Slot, now time.Time) branchstore.Change {
	up, due := l.markUpdate(holder, marks, now)
	if !due {
		return branchstore.Change{}
	}

	return branchstore.Change{Files: map[string][]byte{l.dir() + "/" + path.Base(up.Ref) + ".json": up.Files[leaseFile]}}
}

// branchRelease frees what holder's first acquire took, chosen once: a
// release applied again after a lost race frees that same lease while it is
// still there, never another of the holder's.
func (l *Lock) branchRelease(ctx context.Context, holder string) (int, error) {
	var (
		target []byte
		at     string
		freed  int
	)

	err := l.Branch.Update(ctx, "release "+holder, func(ctx context.Context, v *branchstore.View) (branchstore.Change, error) {
		freed = 0

		slots, err := l.branchLeases(ctx, v)
		if err != nil {
			return branchstore.Change{}, err
		}

		if target == nil {
			var mine []Slot

			for _, s := range slots {
				if s.Lease.Holder == holder {
					mine = append(mine, s)
				}
			}

			if len(mine) == 0 {
				return branchstore.Change{}, nil
			}

			sort.SliceStable(mine, func(i, j int) bool { return mine[i].Lease.Acquired.Before(mine[j].Lease.Acquired) })

			if target, err = encodeLease(mine[0].Lease); err != nil {
				return branchstore.Change{}, err
			}

			at = mine[0].Ref
		}

		// every slot the same acquire took holds the same lease: they go
		// together, while the first is still there
		var gone []string

		for _, s := range slots {
			if data, err := encodeLease(s.Lease); err == nil && bytes.Equal(data, target) {
				gone = append(gone, s.Ref)
			}
		}

		if !slices.Contains(gone, at) {
			return branchstore.Change{}, nil
		}

		freed = 1

		return branchstore.Change{Deletes: gone}, nil
	})
	if err != nil {
		return 0, err
	}

	if freed == 0 {
		l.logf("lock %s: %s held no slot (its lease expired and was taken, or it never had one)", l.Name, holder)
	}

	return freed, nil
}

func (l *Lock) branchUnlock(ctx context.Context, n int) ([]Slot, error) {
	var removed []Slot

	err := l.Branch.Update(ctx, "unlock by hand", func(ctx context.Context, v *branchstore.View) (branchstore.Change, error) {
		removed = nil

		slots, err := l.branchLeases(ctx, v)
		if err != nil {
			return branchstore.Change{}, err
		}

		var c branchstore.Change

		for _, s := range slots {
			if n == 0 || s.N == n {
				removed = append(removed, s)
				c.Deletes = append(c.Deletes, s.Ref)
			}
		}

		return c, nil
	})

	return removed, err
}
