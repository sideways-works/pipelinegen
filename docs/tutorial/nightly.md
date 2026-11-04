# Step 12: the nightly

The `nightly` run from step 9 starts once a night, between 3:00 and 4:00
in the time zone you named, on the tip of `main`. It runs every slice,
records them all, and rebuilds the graph cold ([the
nightly](../concepts/the-nightly.md)). When its last suite has ended, the
recording of each slice that passed replaces its list ([refreshing the
lists](../concepts/refreshing-the-lists.md)).

## The first nightly

You don't have to wait for the timer: trigger `plan-nightly` by hand in
Concourse. It runs once per commit of `main`: started again on a commit
the nightly has had, it says `nothing changed since the nightly of
<commit>: not run` and starts nothing, so push a commit first if the last
nightly ran on the tip.

> **Output to come.** The nightly's plan, the cold rebuild's report and
> the alarm will be filled in from a real run.

**You should see** every slice run, and the plan's log end with the cold
rebuild's comparison: no difference between the fresh graph and the cached
one.

## When it is red

A red nightly means one of three things:

- A test fails that the day's runs left out. The nightly's report counts,
  per narrowing, how many of its failures that narrowing left out of the
  day's runs. Past `narrowing_alarm` (2) it recommends a higher
  `narrowing` level; see [narrowing](../concepts/narrowing.md).
- A test fails that the day's runs did run: it broke on something outside
  the repository (a flaky test, a changed service). Fix it as you would
  any failure.
- The cold rebuild found a difference: a plugin's cached answer doesn't
  match what it says on a fresh read. The plan names the plugin, the file
  and the unit or link. Report it to the plugin; until it is fixed,
  `rebuild_check: warning` keeps the nightly green while still listing the
  difference.

Next: [step 13, upgrading](upgrading.md).
