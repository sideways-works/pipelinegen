# Sharing machines

Three things decide where and what runs at once: the runner's placement,
which puts each step on workers; Concourse's own settings for builds of one
suite job, inside your repository's pipeline; and a lock between
repositories.

## Which workers

Where a step runs is the runner's to say, in its own config block. For
concourse-runner it is `placement`: a default and rules tried in order, the
first that matches a step placing it. A rule is `for` one kind of step
(`pipeline`, `planning`, `checks`, `suite` or `bookkeeping`), optionally of
one `suite` and one `run`, and gives its `tags`; `tags: []` leaves the step
to the workers Concourse picks for an untagged step. Three workers, one for
planning and the small steps, one for the browser suite and one for the
nightly's suites:

```yaml
plugins:
  - name: concourse
    kind: runner
    repository: sideways-works/pipelinegen-runner-concourse
    release: {version: ..., sha256: ...}
    config:
      placement:
        default: [small]
        rules:
          - {for: suite, run: nightly, tags: [nightly]}   # every nightly suite, e2e too
          - {for: suite, suite: e2e, tags: [browsers]}    # e2e in every other run
```

A suite's bookkeeping (each batch's end, the suite's end) that no rule
places runs with its suite. The runner's README lists every kind of step
and what it covers. A pull request can't move its own jobs: the pipeline
is made from the default branch's config, and a change to the runner's
block takes effect once it is merged; the change's plan says so.

## Inside one repository

Each change is one build of each suite job, and builds of different
changes run side by side: nothing queues them. Inside a build every batch
runs at once, so `parallel` (or `batches`) says how many run together.
Concourse's per-worker task limit is what keeps a machine from taking more
than it can.

## Between repositories: the lock

When a machine is shared with another repository's pipeline, Concourse
can't see across, so a suite takes a lock instead:

```yaml
suites:
  e2e:
    minutes: 120
    lock: {name: browsers, repository: owner/shared-locks, slots: 1}
```

- `name` is shared by every suite that should take turns, in any repository
  that names the same `repository` (default: the code repository).
- `slots` is how many holders it admits at once: a worker that carries three
  heavy jobs at a time has 3.
- The lease lasts the suite's time limit (`suites.<name>.minutes`, 120
  by default) and 15 minutes more, so a holder lost with its worker frees
  the slot by itself. With the lock taken per build, the limit bounds all
  of the suite's work while it holds the lock; per batch, each batch. A
  run that takes longer sets `minutes` for the suite in the run.
  `lock.minutes` and a run's `lock_minutes` are the old spellings of these
  and still read.

A pull request's run takes the lock as the config the pipeline was made
from has it: a change that raises its own `slots`, `minutes`, `weight` or
`per` takes effect once it is merged, and its plan says that it edits them.

Each slot is a hidden ref, `refs/pipelinegen/locks/<name>/<n>`, holding a
lease with its holder and expiry. Taking a free slot is a push that only
creates the ref; taking an expired one is a push that names its old value;
so two holders never share a slot. The suite job takes it first
(`pipelinegen lock acquire`, waiting up to 4 hours) and frees it in an
`ensure` (`pipelinegen lock release`), whatever happened. A release frees
one slot, the one this holder took first, and only while it still holds
it; it logs when it frees none, such as after its lease expired and the
next waiter took it.

## A suite stuck waiting

`pipelinegen lock acquire` prints who holds the lock while it waits. When a
lease's holder is gone and you can't wait for its expiry:

```
$ pipelinegen unlock browsers
lock browsers: slot 1 freed, held by owner/example pr pr-12 0723eb0a27d9 e2e until 2026-11-13T10:25:00Z
```

`unlock <name> <n>` frees one slot. It finds a lease by the lock's name,
whatever the config says now: after you removed a lock from the config,
`unlock` still frees its leases, and `--repository owner/name` names the
repository of a lock that was kept elsewhere. Freeing a lease that is
still in use lets a second holder onto the machine, so check the holder's
build first: the line `unlock` prints names it.

## Bazel servers

A Bazel server holds hundreds of megabytes while it waits for its next
command. In a pipeline's step pipelinegen starts its servers with a
ten-minute idle time (`--max_idle_secs=600`): a step that runs several
commands keeps its server warm from one to the next, and a server left
in a container the CI system keeps after the step stops within ten
minutes. A value of your own in `bazel.startup_flags` wins.
Your own tasks that run Bazel should do the same: a trap that runs `bazel
shutdown`, and a short idle time for the kill a trap can't catch.

In a pipeline's step, Bazel's output root, its output base and bazelisk's
downloads go into the step's cache (`plan-cache/bazel`), not into the
container's home, which dies with the step. The next plan on that worker
starts with Bazel installed, the external repositories fetched and the
repository cache full. An `--output_user_root` or `--output_base` of your
own in `bazel.startup_flags`, and a `BAZELISK_HOME` in the step's
environment, win.

## Disk on the planning workers

Each planning job keeps a cache on the worker it runs on, and each pull
request run's sweep keeps one more for its own plans. A repository with a
pull request run, a branch run and a nightly has about five. When a plan
ends it trims its cache: the work tree (checked out afresh from the
trigger every build), the snapshots but the ones it used and the four
newest, the Go modules' sources extracted beside their zips in the module
cache, the repositories Bazel extracted from the archives of its
repository cache, and then, while the cache is larger than `keep.cache` (8GB by
default), the plugins' local answers oldest first, the plugins' own
caches, the Go build cache, npm's cache, the Go module cache, the store's
scratch repository and last Bazel's output. So a worker needs about
`keep.cache` per planning job.

The plan's log says what stayed and what went:

```
pipelinegen: cache: kept 6.1 GB (bazel 3.0 GB, caches 2.4 GB, store 412 MB, snapshots 180 MB, answers 96 MB); dropped tree 1.1 GB, snapshots 820 MB, gomodcache sources 640 MB, bazel repository contents 410 MB, 7 snapshots among them
```

The extracted copies go whatever `keep.cache` says. A CI system such as
Concourse copies a step's cache file by file into a new volume when the
step ends, so the time that takes grows with the number of files, and a
module cache or a repository cache holds most of them extracted. Go
extracts a module from its zip again when a plugin first reads it, and
Bazel a repository from its archive when it fetches it, both without the
network and both to the same files. A repository that Bazel's repository
cache holds no archive of (a `git_repository`, a download without a
checksum) is fetched again over the network.

Lower `keep.cache` to what the parts you want warm add up to. Bazel's
output and the module caches save the most time, and a value below them
makes every plan fetch again. `keep.cache: 0` keeps everything but the
work tree and the old snapshots.
