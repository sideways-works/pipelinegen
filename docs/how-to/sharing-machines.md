# Sharing machines

Two things decide where and what runs at once: the runner's placement,
which puts each step on workers, and Concourse's own limits on what a
worker takes. pipelinegen takes no lock: nothing queues one build behind
another.

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

## Memory: a batch waits for room

Batches of several runs can land on one worker together: three runs of
everything each start their browser batches, and the worker's memory
fills until the kernel kills test processes. pipelinegen keeps no turn
for this. It measures what each slice uses, predicts what a batch will
need, and a batch starts only when the worker has that much free.

**Measured.** Every slice's state holds `cpu_seconds` and `peak_mb` beside
its `seconds`: the CPU the batch's container used while the slice ran, and
the most memory it held then. A suite of steps (`slice run`) samples the
container's working set every quarter second, so each slice gets its own
peak. A task file's `slice start` and `slice end` are separate processes,
so there a slice's peak is the batch's up to the slice's end, read from
the container's cgroup (`memory.peak`, less the file cache it holds). The
working set leaves out the file cache the kernel reclaims before it kills
anything, so `node_modules` and browser binaries read from disk count
only while they're in use.

**Kept.** A trusted run's suite end (a branch's run, never a pull
request's) adds each slice's peak to `refs/pipelinegen/peaks/<suite>`:
the last 10 per slice, one per commit, so a suite's end told again adds
nothing.

**Predicted.** `pipelinegen batches` reads those peaks and writes each
batch's prediction beside its slices, `batches/<n>/memory.json`: the most
any of its slices should use, each the 90th percentile of its recent
peaks and 10% more. A slice without peaks takes the median of the suite's
slices. A suite with no peaks at all gets no prediction, and its batches
start as before; its first trusted run measures them.

**Admitted.** At its start, before its setup, a batch logs what the
worker has free, its cgroup's limit and the memory pressure, and what it
should use:

```
memory: 3120 MB free: 3120 MB available of 15890 MB, no cgroup limit, pressure 14.2%; this batch should use 6600 MB: the most its slices should use, each the 90th percentile of up to 10 recent peaks and 10% more
```

It starts once the worker has the prediction free (`MemAvailable`, which
counts the caches the kernel would reclaim, and what a cgroup limit above
the task leaves) and the machine's memory pressure (`/proc/pressure/memory`,
`some avg10`) is below 10%. Until then it reads again every 5 seconds, for
at most 5 minutes or a tenth of the suite's time limit, whichever is
shorter: the wait counts against the batch's own limit. A prediction above
all the worker could ever have starts at once, and says so, rather than
waiting for room that never comes.

**Not run.** A batch still short when its wait ends starts none of its
slices. Its end marks them `not run` with the reading ("not run: the
worker had 3120 MB free of 6600 MB predicted, after waiting 5m0s"), the
suite's status says `2 not run (worker short of memory)`, and the comment
lists them. They run again without anyone:

- in a pull request's run, once every suite build of the head has ended
  and the run's only gaps are such slices, the run's sweep hands the head
  on again after `missed_after` (doubled each time, three times at most),
  and the suite builds run only the slices that didn't pass;
- in a branch's run, the next round does not pass the commit, so the
  round of the next commit compares from the last passed one (or runs
  everything) and selects them again. A commit is never run twice, so on
  a quiet branch they wait for the next commit, or rerun the suite build.

A run with a failed slice isn't handed on: the hand-on would run the
failed tests again unasked. Rerun its suite build, or push.

**Crashed.** When the OOM killer kills a process of the batch while a
slice runs (`oom_kill` in the cgroup's `memory.events`), or a slice fails
naming `SIGKILL` while the memory pressure is 10% or more, the slice is
`errored` with "(out of memory)", and its peak counts as at least the
room the batch had at the slice's start, so the next prediction rises.
Such a slice counts as one not run when the sweep decides to hand a head
on.

A task file calls `tools/pipelinegen slice memory` first, before its
setup ([writing tasks](writing-tasks.md#the-batch-task)); `slice run`
does it itself. On your machine, `pipelinegen run` and `record` read the
peaks from the store the config names and refuse a suite whose batch this
machine hasn't the room for, naming the figures; nothing waits there. As
on CI, the suite's packages come first: the images with them installed
([packages and cached images](packages-and-images.md#on-your-machine))
are made before the check.

To measure a batch by hand, read its container's cgroup at its end:
`cat /sys/fs/cgroup/memory.peak` inside the task (bytes since the task
began; less `inactive_file` in `memory.stat` for the working set), or
`hijack` into the running task and watch `memory.current`.

## Between repositories

Concourse can't see across pipelines, and pipelinegen no longer takes a
lock between them. Admission by each node's observed free memory and
pressure is coming next: a suite's batches will start when their node has
room. Until it lands, a machine shared with another repository's pipeline
is kept from overfilling by the same means as inside one repository:
Concourse's per-worker task limit, and placement that keeps the heavy
suites of the two pipelines on different workers.

A config that still has `suites.<name>.lock` loads: `check` says the key
is no longer read, and you remove it. Its `minutes` is still read, as the
old spelling of `suites.<name>.minutes`, until you move it there. A
pipeline generated before still runs its lock steps; `pipelinegen lock` and
`pipelinegen unlock` do nothing and pass, so it keeps working until the
next `pipelinegen pipeline` writes it without them. They go in the next
release. The leases older pipelines left in the store are swept by the
next plan step of a trusted run.

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
