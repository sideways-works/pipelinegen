# The lock

`internal/lock`, called by `pipelinegen lock` and `pipelinegen unlock`. The
contract is in [contracts.md](../contracts.md#locks-between-runs); the
user's side in [sharing machines](../how-to/sharing-machines.md).

## Why a lease, and only between repositories

Concourse's serial groups and `max_in_flight` hold inside one pipeline, and
each repository has one, so inside a repository they are enough. A machine
shared with another repository's pipeline needs something both can see: a
ref in a repository both write to.

## Slots and leases

A lock with `slots: n` is the refs `refs/pipelinegen/locks/<name>/1` to
`<name>/n` in the lock's repository. A held slot points at a commit holding
`lease.json`: holder, job URL, when it was taken and when it expires (the
suite's time limit in the run, `minutes`, plus 15).

## Acquire

1. Read every slot.
2. If as many leases are live (not expired) as there are slots, wait.
3. Otherwise take the first free or expired slot: a free one with a
   create-only write, an expired one with a write that names the commit it
   was read at. If another waiter was first, the write fails with
   `ErrStale` and nothing changed; try again soon.
4. Wait a random pause up to 15 seconds between looks (a tenth of that
   after a lost race), and log who holds the lock each time that changes.
5. Give up after `--wait` (4 hours by default), naming the holders.

Two holders can never share a slot: a slot changes hands only by a
compare-and-swap on its ref. Acquiring is never re-entrant.

## Release

`Release` deletes one slot this holder holds, the one it took first, with
a write that names the commit it holds, so a lease taken over meanwhile is
left alone, and two builds under one holder name each free their own.
The suite job releases in an `ensure`, whatever happened.

## A holder that dies

Its lease expires on its own, and the next waiter takes it over. Nothing
removes it by hand unless someone runs `pipelinegen unlock`, which deletes
a slot, or every slot, whoever holds it.

A lowered `slots` count leaves leases above the new count in place until
they expire or are released; while they live, waiters count them.
