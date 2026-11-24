# Writing a store

A store keeps what pipelinegen learns between runs: plans, the state of
each run's slices, rounds, recordings and the lists made from them, the
plugins' cached answers. By default pipelinegen keeps them itself, as
hidden refs of the repository or on a branch of their own. A store plugin
keeps them anywhere else: s3-store keeps them in an S3 bucket.

```yaml
plugins:
  - name: s3
    kind: store
    repository: https://github.com/sideways-works/pipelinegen-plugins
    path: store/s3
    release: {version: <tag>, sums: <the checksum of its SHA256SUMS>}
    config: {endpoint: "https://s3.eu-central-1.amazonaws.com", region: eu-central-1, bucket: ci-records, prefix: shop/}
store: {backend: plugin, plugin: s3}
```

## Records

The core speaks of records, never of refs, branches or tables. A record
has a name (a path such as `refs/pipelinegen/plans/<run>/<change>/<c12>`),
files by path with their bytes, a version and the time it was written, by
the writer's clock. A version is an opaque token for exactly that content
of that record: a name never has the same version twice, a version's files
never change, and a read finds a version without its record's name. Lists,
plans, slice states and recordings, a suite's end, a round's records and
the plugins' answers are all records, under names of their own kind.

## The calls

Each call is one process, as for every plugin: the request on stdin, the
answer on stdout. The plugin's config block arrives in every request, and
its cache directory (`cache`) keeps what it learns between calls.

| Call | Request | Answer |
|---|---|---|
| `describe` | | `kind: store` |
| `store.list` | `prefixes` | `records`: the name, version and time of every record under any prefix |
| `store.get` | `names` | `record`: the first of the names that exists, or none |
| `store.read` | `versions`, and `paths` when only some files are wanted | `files`: each version's files by path; a version the store lacks is left out |
| `store.write` | `message`, `updates`, `time` | `versions`: each written record's new version; `stale`: the record whose expectation failed |

An update is a record's `name`, its new `files` or `delete`, and what it
expects: `expect`, the version the record must have, empty for a record
that must not exist; or `any`, whatever it holds. The store applies the
updates in order and stops at the first whose record holds something else:
it answers that name in `stale`, and the updates before it stand. The core
reads only versions its own calls listed or found, so a store may answer a
read of any other version as missing.

## What a store promises

`storetest.Conform` tests each of these on a store, and every store
pipelinegen knows passes it: the refs, a branch, and s3-store against an
S3 service (`go test ./internal/pluginstore -run External` with
`PIPELINEGEN_TEST_STORE_PLUGIN` naming the plugin's program and
`PIPELINEGEN_TEST_STORE_CONFIG` its config).

1. A write that answered is durable, and every later listing and read
   sees it, from any process on any machine. An eventually consistent
   store is not a store.
2. A guarded update is a compare-and-swap on its record: of two writers
   expecting the same version, or creating the same record, one wins and
   the other is stale.
3. Many writers of different records lose none of them.
4. A version's files never change, so a reader that found a version reads
   it whole after newer ones are written.
5. A listing names every record under its prefixes.

A write of several records is not atomic across them. The core writes
several at once only where a part that stands is safe: a batch's slices,
each written by its own steps alone, and sweeps of guarded deletes.

## Credentials

A store plugin reads its credentials from `PIPELINEGEN_STORE_ACCESS_KEY_ID`,
`PIPELINEGEN_STORE_SECRET_ACCESS_KEY` and, for a temporary key,
`PIPELINEGEN_STORE_SESSION_TOKEN`. They reach the plugin's calls and
nothing else pipelinegen runs, with the proxy and certificate settings
(`HTTPS_PROXY`, `SSL_CERT_FILE` and their kin) it needs to reach its
service. A runner gives a step that only reads records, such as a proposed
change's first planning step, a key that only reads, and pipelinegen's own
bookkeeping steps a key that writes; a step that runs the change's code
gets none.

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

That split is to come: today one store keeps both, and the core applies
the rules itself. With two, a stolen credential of a proposed change
can't touch what later plans bind with.

## Purge (to come)

A store will purge at every grain: everything, one proposed change's
runs, one run, everything older than a duration, the lists or the answer
cache, with a dry run that prints what would go. Purging
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
repeat across runs are kept once), answers in a table of their own; a
guarded batch is one transaction that selects the named records for
update, compares each expectation and writes new versions or rolls back.
