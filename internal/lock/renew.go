package lock

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/sideways-works/pipelinegen/internal/branchstore"
	"github.com/sideways-works/pipelinegen/internal/gitstore"
)

// Renew moves the expiry of holder's live lease to TTL from now, so a holder
// that keeps renewing keeps its slot and one that stops frees it within TTL.
// It writes only once a quarter of the TTL has passed, so callers may renew
// often; held is false when holder has no live lease.
func (l *Lock) Renew(ctx context.Context, holder string) (held bool, err error) {
	if err := l.check(holder); err != nil {
		return false, err
	}

	if l.Branch != nil {
		return l.branchRenew(ctx, holder)
	}

	for range 5 {
		slots, err := l.Leases(ctx)
		if err != nil {
			return false, err
		}

		s, ok := l.liveOf(slots, holder)
		if !ok {
			return false, nil
		}

		lease, due := l.renewed(s.Lease)
		if !due {
			return true, nil
		}

		data, err := encodeLease(lease)
		if err != nil {
			return false, err
		}

		_, err = l.Store.Write(ctx, "renew "+holder, gitstore.Update{Ref: s.Ref, Old: s.Commit, Files: map[string][]byte{leaseFile: data}})
		if errors.Is(err, gitstore.ErrStale) {
			continue
		}

		return err == nil, err
	}

	return false, fmt.Errorf("lock %s: %w: the lease of %s kept moving while it was renewed", l.Name, gitstore.ErrStale, holder)
}

func (l *Lock) liveOf(slots []Slot, holder string) (Slot, bool) {
	now := l.now()

	var (
		out Slot
		ok  bool
	)

	for _, s := range slots {
		if s.Lease.Holder != holder || !s.Lease.Expires.After(now) {
			continue
		}

		if !ok || s.Lease.Acquired.Before(out.Lease.Acquired) {
			out, ok = s, true
		}
	}

	return out, ok
}

func (l *Lock) renewed(lease Lease) (Lease, bool) {
	now := l.now()
	if lease.Expires.Sub(now) > l.TTL*3/4 {
		return lease, false
	}

	lease.Expires = now.Add(l.TTL)

	return lease, true
}

func (l *Lock) branchRenew(ctx context.Context, holder string) (bool, error) {
	held := false

	err := l.Branch.Update(ctx, "renew "+holder, func(ctx context.Context, v *branchstore.View) (branchstore.Change, error) {
		held = false

		slots, err := l.branchLeases(ctx, v)
		if err != nil {
			return branchstore.Change{}, err
		}

		s, ok := l.liveOf(slots, holder)
		if !ok {
			return branchstore.Change{}, nil
		}

		held = true

		lease, due := l.renewed(s.Lease)
		if !due {
			return branchstore.Change{}, nil
		}

		data, err := encodeLease(lease)
		if err != nil {
			return branchstore.Change{}, err
		}

		if old, err := encodeLease(s.Lease); err == nil && bytes.Equal(old, data) {
			return branchstore.Change{}, nil
		}

		return branchstore.Change{Files: map[string][]byte{s.Ref: data}}, nil
	})

	return held, err
}
