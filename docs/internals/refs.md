# Refs

This page is for people changing what pipelinegen stores. pipelinegen keeps
no state store and no state branch. Everything that
outlives a job is in refs of the repository under `refs/pipelinegen/`,
each pointing at a commit whose tree holds records' files.
`internal/gitstore` reads and writes them; the formats are in
[contracts.md](../contracts.md#d-run-state-temporary-refs).

## The layout

```
refs/pipelinegen/lists/<full commit>                         what each slice touched, its marks, in _replaced.json where a run of everything last replaced each list, and under _attached/ the runs that went into them
refs/pipelinegen/run/<run>/<change>/<c12>                    a run's plan, its suites' end marks, and its slices once their suite ended
refs/pipelinegen/batch/<run>/<change>/<c12>/<suite>/<batch>  a live batch's slices
refs/pipelinegen/also-to/<run>/<change>                      more pull requests for a comment
refs/pipelinegen/locks/<name>/<n>                            a lock's slot (lease.json); wait-<id> a waiter's mark
refs/pipelinegen/superseded/<run>/<c12>                      a continuous run's build that found a newer commit and ran nothing (superseded.json), swept after 14 days
refs/pipelinegen/rounds/<run>/<change>/<c12>/started         a continuous run's round a build began (round.json), create-only, swept after 14 days
refs/pipelinegen/rounds/<run>/<change>/<c12>/planning        the same round's build stopped waiting and plans (round.json)
refs/pipelinegen/cache/<plugin>/<version>-<sha12>.<week>     one plugin release's answers used in an ISO week, by content
refs/pipelinegen/replaced/<plugin>/<version>-<sha12>         the keys a cold rebuild replaced, each with the digest of what it left
```

Every push to a repository is told of every ref it has, hidden ones
included, so a run keeps few: one per batch while it goes, one for the
whole run after. Readers still see each record under the name it had when
it was a ref of its own:

```
refs/pipelinegen/plans/<run>/<change>/<c12>                  the plan (plan.json): _plan/ in the run's ref
refs/pipelinegen/runs/<run>/<change>/<c12>/<suite>/<slice>/=  a slice's state.json, recording.txt and extras: <suite>/<slice>/=/
refs/pipelinegen/runs/<run>/<change>/<c12>/_end/<suite>      a suite's end mark (end.json): _end/<suite>/
refs/pipelinegen/runs/<run>/<change>/<c12>/_began/<suite>    the newest start of a build of the suite (began.json): _began/<suite>/
```

In a run's ref or a batch's, each record is a directory with, beside it,
an empty marker `<dir>:<unix>.<nonce>` that says when it was written; a
record that a batch's ref and the run's ref both hold is read from the
newer. A batch's steps (`PIPELINEGEN_BATCH` set) write their slices into
the batch's ref, so batches never wait on one another; everything else
goes into the run's ref. A suite's end moves its batches' records into
the run's ref and deletes the batches' refs, in one push guarded by what
it read: a batch that writes meanwhile makes it read again, and one that
writes after it starts its ref anew, which readers read and the next
compaction or the sweep takes. Writes and the compaction read the refs'
trees only and make each commit on the commits whose files it keeps, so
git sends nothing the remote has and nothing is downloaded. Their pushes
leave out every file a parent holds, at whatever path (git's sparse walk,
which proves that path by path only, is off there). They are thin, as
git's pushes over HTTP always are, so each new file may be sent as a delta
of the file it replaces: a write fetches the files it replaces, those
alone, before it pushes; it learns which of them it lacks from the
objects it holds, since a git before 2.45 dies when asked about one a
partial clone lacks. A push that would still need a file the scratch
repository never fetched fails at once, naming the file, as a defect of
pipelinegen, and is not tried again. A run planned
before this layout (its plan has a ref of its own) keeps a ref per record
until the sweep removes it; a run is never written in both.

`<c12>` is a commit's first 12 hex characters; `<change>` is `pr-<n>` or a
branch's name, written as one ref part (a `/` becomes `%2F`, so
`release/1.2` stays one part). A slice's name, a test file's path, keeps
its parts as ref path parts, each byte a ref can't hold written `%XX`, and
so is each dot that would start or end a part, double it or make it end in
`.lock`. A slice's ref ends in a part `=`, which no encoded name is, so one
slice's ref is never another's directory (`e2e/a` and `e2e/a/b.ts`). A
name with an empty, `.` or `..` part is refused. A lists ref's tree is `<suite>/<slice>.txt`, with
`<suite>/<slice>.cannot-tell` beside it for a marked slice and
`_attached/<run>/<change>/<commit>.json` for each run attached to it, and
git shares unchanged files between those trees, so a new ref costs only
what changed. Lists written before kept the attached runs in
`refs/pipelinegen/attached/<full commit>`, still read until the sweep
removes it with its lists.
Suite names start with a letter, so `_end` can't clash with one.

## The guarantees

### Compare-and-swap

Every write names the value the ref must have, and
the remote applies it only then (git's `--force-with-lease`). An empty
expected value means the ref must not exist, which is a create-only write.
A write whose expectation failed returns `ErrStale` and changes nothing.

### Atomic batches

`Store.Write` pushes all its updates with `--atomic`:
all land or none. A batch's slice states, or a whole run's refs in a sweep,
move together.

### One writer per ref

A slice's ref is written only by that slice's own
steps, one after another; a suite's end marker only by its suite job. Those
writes may skip the expectation (`Any`), since nothing else writes them,
and writers never contend. Refs written by several parties (lists, the
plan, lock slots) are always create-only or compare-and-swap.

### Create-only where it matters

A plan is written once per change and
commit. A second writer fails rather than overwrites. A lists ref and its
attached ref are written by compare-and-swap: a second attach to the same
commit reads what the first wrote and merges into it. A trusted run
attached before writes again only when it brings what the lists don't
hold: slices that passed in a rerun after its first attach, which replace
their lists in a run of everything (those a whole recording of its commit
replaced already stay) and add to them otherwise.

### Safe retries

Each write makes a new commit, so a retry after a push
that did land finds the ref already at its value and succeeds, rather than
failing its lease. A read fails over after three quick tries; a write that
failed in passing, at its push or at the reads before it, goes again with
waits that grow, as `writes` says ([config](../reference/config.md#writes)),
and a refusal by a rule of the remote's only twice. A lost
compare-and-swap is never retried there: a plain write returns
`ErrStale`, and a write of a run's records reads its refs again and
applies itself again.

### No token on disk or in a URL

`gitstore` works in a scratch repository
of its own and passes the token to git through the environment, as an
HTTP header, never in a URL or an argument; error messages are redacted.

## Reading

`Store.List` fetches every ref under some prefixes in one round trip into a
local mirror namespace, with `--prune`, and returns name, commit and commit
time; a run's records come back under their own names, with the tree of
their directory as the commit and their marker's time. The planning job's
scratch repository waits in its cache between plans, so a plan fetches
only what was written since the last one on that worker: a job takes it
by renaming it, checks it with `git fsck --connectivity-only` and starts
empty when it is missing or broken, and puts it back when it ends. `Files` and `File` read a record's files; `First` returns the first of
several refs that exists (the nearest ancestor's lists).

## Lifetimes

Nothing is deleted when a run ends; the sweep removes what `keep` no longer
holds ([reporter](reporter.md#the-sweep)): a run whole, its run ref and any
batch refs a suite that never ended left, in one atomic push; a run of
more than 300 refs (one of the layout that kept a ref per record) goes in
chunks of 300, each atomic and guarded, its plan last, so that a chunk
that moved leaves a run the next sweep takes. The lists of the default
branch's last `keep.lists.commits` first parents (200) stay whatever their
age, and those of its older commits go whatever their age; when the plan's
checkout is shallow or can't tell where the branch is, only the age rule
applies. Right after it, the planning job
removes the answer refs of weeks before last week (and those of the layout
without weeks), and the record of replaced keys of a plugin release that
has had no answer ref for 90 days. Losing the refs is safe: runs
select more until they record again ([backups](../how-to/backups.md)).
