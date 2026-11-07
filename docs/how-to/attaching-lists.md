# Merging and attaching the lists

What a slice touched is recorded in every recording run, and has to
reach the branch's commits, as `refs/pipelinegen/lists/<commit>`, so that
later plans find it. `lists attach` does that: it merges the run's recordings
into the target commit's lists. A commit with no lists yet starts from its
nearest ancestor's.

```sh
pipelinegen lists attach --run pr --change pr-12 --commit <tested sha> --to <merge commit sha>
```

Lists only grow, except that a run that selected every slice replaces the
list of each slice that passed with its new recording, so code a test no
longer touches drops out at night. A slice whose correlator couldn't tell gets a
mark beside its list instead, and a later recording that tells clears it.
[Refreshing the lists](../concepts/refreshing-the-lists.md) shows both on
one list.

Attaching twice is safe. The same run attached again writes nothing. When a
pull request's run and the branch's own run both attach to the same merge
commit, the second merges into what the first wrote. Run it from a checkout
with history: it refuses a shallow clone, since it couldn't find the
ancestors' lists there.

## A branch's run

A recording run on a branch (a `branch`, `timer` or `manual` trigger, with
`record: true`) attaches its own recordings to the commit it tested as soon
as the whole run has passed. Nothing to set up. If the attach fails, the
run's status says so and is an error, never green.

A run in which some slices failed attaches as well, once its last suite
has ended: each slice that passed has a whole recording whatever its
neighbours did, so its recording goes in, and each slice that failed, never
ran or couldn't be told keeps what the lists held for it. When a rerun of a
suite's build passes the rest, those slices' recordings follow, and the
run turns green. `pipelinegen end` attaches the same way when its checkout
holds the run's commit and its history.

## Pull requests

A pull request run tests the head merged into the target's tip, which is
not the commit that lands. So whatever merges your pull requests calls
`lists attach` right after the merge, with the new commit on the branch:

```sh
# after merging pull request 12 into main
pipelinegen lists attach --run pr --change pr-12 \
  --commit "$TESTED_SHA" --to "$(git rev-parse origin/main)"
```

`--commit` is the commit the run tested, `--to` the commit now on the
branch.

Merge automation that lands several pull requests at once, such as a merge
train that tests a batch on its own branch, calls it once, for the batch's
run, onto the last merge commit. Name the batch's member pull requests with
`pipelinegen also-to` so each gets the batch's comment.

## When nobody attaches

Something that reached the branch without the pipeline (a direct push, a
merge with no attach) gets its lists from the branch's own run. Until then,
planning against that commit uses the nearest ancestor's lists, and adds
the recordings of every run whose refs still exist: the window between a
merge and its lists can only make plans select more, never less. A
recording older than the run of everything that last replaced its slice's
list, on the same history, is left out: that list already holds what the
test runs.
