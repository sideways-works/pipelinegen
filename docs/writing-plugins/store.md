# Writing a store

> **A design, not yet a protocol.** Store plugins are being designed.
> Today pipelinegen keeps its records itself, as hidden refs of the
> repository or on a branch of their own (`store.backend`), and you can't
> plug in a store of your own. This page describes the design so that
> someone planning, say, a database store knows what it will be asked; the
> calls and fields can still change.

A store keeps what pipelinegen learns between runs: plans, the state of
each run's slices, recordings and the lists made from them, the plugins'
cached answers, and the leases that let repositories share a machine.
Today's hidden refs become the first store plugin, git-ref-store. A team
that wants its records in a database, or in a store built for many teams
writing at once, writes a store plugin instead of changing pipelinegen.

## Records

The core speaks of records, never of refs, branches or tables. A record
has a name (a path such as `runs/<run>/<change>/<commit>/<suite>/<slice>`),
files by path with their bytes, a version (an opaque token for exactly
that content: a name never has the same version twice, and a version's
files never change), and the time it was written. Lists, plans, slice
states and recordings, a suite's end, a suite build's start, a continuous run's round started
and planning (written create-only, read by listing the run's rounds),
the plugins' answers and leases are all records, under names of their own
kind.

## The calls

A store plugin is one long-lived process per pipelinegen step, speaking
JSON lines on stdin and stdout, so a database store connects once per
step rather than once per call.

| Call | What it does |
|---|---|
| `describe` | the kind, the environment variable of its `store` credential, its limits (largest record, largest batch), and its consistency in words an adopter can read |
| `open` / `close` | a session, with a cache directory of its own |
| `get {names}` | the records present among the names, with version, time and files, in one round trip |
| `list {prefixes}` | the name, version and time of every record under the prefixes, and on request some of their files |
| `read {name, version}` | one version's files |
| `write {batch}` | each entry a name, what it expects (a version, absent, or any) and its files or a delete; all of the batch applies or none, and a mismatch answers which names were stale |
| `settle {prefix}` | a hint that a suite has ended; a store may ignore it, and no reader's result may depend on it |
| `answers.get`, `answers.use`, `answers.put`, `answers.replace`, `answers.drop` | the plugins' answers by key: written once, a hit counts as a use, the nightly's cold rebuild may replace them, and answers unused since a time may go |
| `lease.take`, `lease.renew`, `lease.release` | a slot of a lock, granted with a token; only the token's holder renews or releases it |

Versions never change, so the core caches records by name and version and
reads only the versions it lacks.

## What a store promises

The core relies on seven promises, and the conformance kit tests each:

1. A write that answered ok is durable, and every later read sees it,
   from any process on any machine. An eventually consistent store is not
   a store.
2. A guarded write is a compare-and-swap on each record it names.
3. A batch is atomic.
4. Many writers of different records lose none of them; two writes of one
   record keep the last.
5. A listing is consistent for each record, not across records.
6. Answers are written once per key, and a missing answer is always safe:
   the core computes it again.
7. A lease slot has at most one holder among takers whose clocks agree
   within seconds.

git-ref-store keeps them with guarded atomic pushes. A database keeps
them with transactions and row locks.

## Trusted and proposed records

A plan binds only with records that trusted runs wrote: runs not started
by a proposed change. A proposed change's run writes its own run records
and nothing later plans bind with, and its planning job, which runs the
change's build files, never holds a credential that writes shared
records. The config will name a store per role:

```yaml
store:
  trusted:  {plugin: git-ref-store, config: {...}, tokens: {store: <ci var>}}
  proposed: {plugin: git-ref-store, config: {...}, tokens: {store: <ci var>}}
```

With one store, both roles are that store and the core applies the rules
itself; with two, a stolen credential of a proposed change can't touch
what later plans bind with.

## Purge

A store can purge at every grain: everything, one proposed change's
runs, one run, everything older than a duration, the lists, the answer
cache, or the leases, with a dry run that prints what would go. Purging
only ever makes later plans select more: a purged answer is computed
again, a change whose runs are gone runs all its slices again, and lists
purged in the middle of a branch's history leave a tombstone, so plans
don't walk past it to older, narrower lists.

## Bounds

A store must reach its backend (a git remote, a database), so unlike an
sbom plugin it isn't a pure program. It is bounded instead: it gets only
its own `store` credential, runs only in steps that run nothing but
pipelinegen (never in a task that runs a change's code), and is pinned by
version and checksum like every plugin.

## A database store, in brief

As proof the design has no git in it: records and their files in tables
(`records`, `record_files`, and `blobs` keyed by hash, so recordings that
repeat across runs are kept once), answers and leases in tables of their
own; a guarded batch is one transaction that selects the named records for
update, compares each expectation and writes new versions or rolls back;
a lease is an insert that, on conflict, updates only a row whose expiry
is past, by the database's clock.
