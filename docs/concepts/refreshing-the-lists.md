# Refreshing the lists

A plan is as exact as the lists it reads, and a list is what one slice
executed the last time it was recorded. Between two recordings of a slice
the code moves on. This page says what each kind of run does to the lists,
why they drift wide between runs of everything, how such a run makes them
exact again (some call this reseeding the lists), and how to tell from a
plan that they are due.

## Two kinds of recording

Every run with `record: true` records the slices it ran. What happens to
those recordings depends on whether the run ran everything.

A run that selected some slices adds. Once its last suite has ended, each
recorded slice's list gets the units the slice executed this time, and
keeps every unit it held before. Nothing leaves a list this way, because the run saw
only part of the suite under one change and can't know that an older unit
is no longer run. This is what keeps the lists safe between runs of
everything: the change that makes a test call new code is tested by a run
that records the test, so the new code is in its list before the next
change is planned.

A run of everything replaces. It is a run that compares with nothing and
selects every slice: a nightly, a round of a continuous run, a manual run
declared the same way, or the first run of a branch that has no passed
commit yet. Once its last suite has ended, the recording of each slice
that passed becomes its list. Units the slice no longer executes drop
out, a slice that was marked "couldn't tell" is told again, and a slice
that never had a list gets one.

Each slice is judged by its own result, not by the run's. A slice that
passed has a whole recording of the commit whatever its neighbours did,
so a run of 200 slices in which two fail still replaces 198 lists. A
slice that failed, never ran, or couldn't be told keeps what the lists
held for it: its older list, or its older mark. What a failed slice
reached before it stopped is added to its list, never put in its place,
and its own "couldn't tell" stays in the run's records, which plans read
while they are kept, and out of the lists. The suite's setup list is
replaced only when every slice of the suite passed: a batch that never
ran its setup may have set up more than the others. The run stays red,
and a rerun of the failed suite's build that passes the rest attaches
those slices too.

So the lists of one commit can hold slices recorded at different
commits. Each list records where a run of everything last replaced it,
and `pipelinegen lists show --slice` names that commit for a slice the
newest run of everything didn't replace. A plan assumes no more of such a
list than of any list: it is what the slice executed when it was last
recorded whole, with what later runs added, and the runs that change the
code in between record the slices they reach, as they always do.

## How lists drift wide

Three things make a plan select more than the change needs, and each lasts
until the next run of everything records the slice passing.

Code moves. A test stops calling a function, or the function is replaced
by another. The runs in between add the new function to the test's list
and keep the old one, so a later change to the old one still selects the
test.

A recording could not be told. When a correlator can't say what a slice
executed (its coverage was missing, a log was cut short), the slice's list
stays as it was and gets a mark. A marked slice is bound to everything: it
runs on every change. Any later run of the branch in which the slice
passes and the correlator can tell clears the mark, so a run of everything
clears them all at once.

A slice has no list. A new test file, or a slice whose only recordings so
far failed, is bound to everything until a run of the branch records it
passing. A pull request's run never makes a list: it may only add to lists
that exist, since its code has not landed.

What does not happen is a list that misses code its test runs, as long as
the branch's own run records. That is why `record: true` belongs on every
run that tests code that lands, and why a pull request's recordings are
[attached after the merge](../how-to/attaching-lists.md).

## One list through four runs

The notes service's Go suite, run through
[the steps by hand](../how-to/any-ci-by-hand.md) with two runs declared:
`main` compares each commit with the last one that passed and selects what
the change reaches, `nightly` runs everything.

The first run of `main` has nothing to compare with, runs all five slices,
and makes the lists. The search test executed three functions:

```
$ pipelinegen lists show --commit d166a0975e99 --slice search/search_test.go
lists of d166a0975e99: its own lists
no live runs' recordings added
go search/search_test.go: 3 units
  go:search#func:Find
  go:search#func:containsAll
  go:text#func:Words
```

The second commit moves code: `search.Find` stops calling `text.Words` and
splits the words itself, in a new function `fields`. The run of that
commit selects three slices, the search tests among them, and their
recordings are added to their lists. The new function is in, and `Words`
is still there, though the test no longer runs it:

```
$ tools/pipelinegen plan --run main --trigger trigger --root trigger --cache /home/you/ci/cache
main main: go 3 of 5
$ tools/pipelinegen report suite --root trigger
lists of 7d5ad7c2e21f: main main 7d5ad7c2e21f merged into the lists of its parent d166a0975e99; 5 slices, 2 changed
$ pipelinegen lists show --commit 7d5ad7c2e21f --slice search/search_test.go
lists of 7d5ad7c2e21f: its own lists
with the recordings of the live runs:
  main main 7d5ad7c2e21f (3 slices)
go search/search_test.go: 4 units
  go:search#func:Find
  go:search#func:containsAll
  go:search#func:fields
  go:text#func:Words
```

The third commit changes `Words`. Only `text/text_test.go` still runs it,
but the two search tests' lists say otherwise, so three slices run where
one would do:

```
$ tools/pipelinegen plan --run main --trigger trigger --root trigger --cache /home/you/ci/cache
main main: go 3 of 5
```

Then the run of everything, on that same commit. All five slices run, and
when the run has passed, the lists that still held `Words` lose it:

```
$ tools/pipelinegen plan --run nightly --trigger trigger --root trigger --cache /home/you/ci/cache
pipelinegen plan: the cold rebuild of the graph equals the cache
nightly main: every slice: every slice runs in this run
$ tools/pipelinegen report suite --root trigger
lists of 8cdfade2ff2e: nightly main 8cdfade2ff2e merged into its own lists; 5 slices, 2 changed
$ pipelinegen lists show --commit 8cdfade2ff2e --slice search/search_test.go
lists of 8cdfade2ff2e: its own lists
no live runs' recordings added
go search/search_test.go: 3 units
  go:search#func:Find
  go:search#func:containsAll
  go:search#func:fields
```

The fourth commit changes `Words` again, and now the plan is exact:

```
$ tools/pipelinegen plan --run main --trigger trigger --root trigger --cache /home/you/ci/cache
main main: go 1 of 5
```

## What a plan reads in between

A plan reads the lists of the commit it compares with. They are that
commit's own lists, or, when it has none yet, those of its nearest
ancestor along the first parents, up to `keep.lists.commits` back (200 by
default).

To those it adds the recordings of every run whose records are still in
the store: a pull request's run that was merged and never attached, a run
still going, a run that failed. Such a recording can only add units to a
list that exists, so it can only make a plan select more. This closes the
gap between a merge and the lists of the commit it made.

One kind of recording is left out. Once a run of everything has replaced a
slice's list at some commit, the recordings that older runs made of that
slice, at that commit or at one before it, are no longer added. The newer
recording is whole and was made on a tree that holds theirs, so whatever
they saw and it did not is what the test stopped running. Runs of later
commits, and runs of commits that are not in the branch's history (a pull
request's merge commit), still add.

## Telling that the lists are due

`pipelinegen lists show --commit <base>` prints what a plan against that
commit reads. Its first line says how far back the lists come from: `its
own lists`, `the lists of its parent`, or `the lists of <commit>, 14
commits back`. `no earlier lists (none within 200 commits)` means every
recorded slice is bound to everything, and a plan warns that the lists are
gone. That is the state before the first lists are made, and again when
the nearest lists lie more than `keep.lists.commits` first parents back.

A marked slice shows in the same output as `bound to everything until a
recording tells again`, with the plugin, the day it first couldn't tell
and the reason the newest recording that couldn't tell gave, and every
plan warns of it. `no list for <slice>` says the slice has
never been recorded passing.

For a slice you believe should not have run, `pipelinegen why --run
<run> --change <change> --commit <commit> --suite <suite> --slice <slice>`
names the unit that selected it: `it executed text.Words during its
recording`. When the test no longer calls that code, the list is stale and
the next run of everything drops it.

The commit status of the run of everything tells when the lists were last
replaced whole: the run is green only once its lists are attached, so its
newest green commit is where every list was last made exact. A red run
since then replaced the lists of the slices that passed in it, and `lists
show --slice` names, for a slice it didn't replace, the commit its list
was last recorded whole at. If that is days back, look at why that slice
keeps failing.

## What to declare

A nightly replaces the lists once a day:

```yaml
runs:
  nightly:
    trigger:
      timer: {start: "3:00", stop: "4:00", location: Europe/Berlin}
      branch: main
    compare: none
    select: all
    suites: {go: {}, e2e: {}}
    record: true
```

A [continuous run](../how-to/declaring-runs.md#continuous-runs) replaces
them with every round, each of the newest commit, so on a busy branch the
lists are never more than one round old. A nightly beside it adds little:

```yaml
runs:
  full:
    trigger: {branch: main, continuous: true}
    compare: none
    select: all
    suites: {go: {}, e2e: {}}
    record: true
```

Both replace only the lists of the suites they name. A suite left out of
every run of everything is never refreshed.

The branch's own run and the pull requests' run keep `record: true` as
well. They are what adds new code to the lists between two runs of
everything, and the run of the branch is what gives a new test its first
list.

To refresh on demand, start the planning job of the run of everything by
hand, or declare a second one with `manual: true` beside `branch`. That is
worth doing after a change that moves much code at once, since every list
that names the old code keeps selecting its test until then.

## A first adoption, and after a break

Before the first run of everything has passed there are no lists, and
every recorded slice runs on every change. That first run is the first
refresh. Where it would take too long to wait for,
[`pipelinegen seed`](../how-to/starting-narrow.md) puts lists recorded on
your own machine onto a commit of the default branch, and the first runs
compare with that commit. A seed only adds, like any run that didn't run
everything; `pipelinegen seed --replace` replaces each list whose slice
passed on your machine, as a run of everything does. Seeded lists are as
good as your machine is like the CI system, so the first run of everything
on CI replaces them.

After a break (the pipeline paused for weeks, a branch whose runs were all
red) two things may be missing. With no passed commit among the recent
ones, the branch's run has nothing to compare with and runs everything
once, which is itself a refresh. Where the tree is known to be good and
the lists still exist, [`pipelinegen baseline`](../how-to/starting-narrow.md)
declares a commit the base instead, and the next run compares with it. If
the lists were swept in the meantime, seed them first, or let the one full
run make them.

Next: [the nightly](the-nightly.md).
