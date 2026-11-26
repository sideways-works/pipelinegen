# Sharing machines

Three things decide where and what runs at once: the runner's placement,
which puts each step on workers, Concourse's own limits on what a worker
takes, and the worker's free memory, which a batch waits for before it
starts. pipelinegen takes no lock: nothing queues one build behind another.

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
  - repository: https://github.com/sideways-works/pipelinegen-plugins
    with:
      - name: concourse
        kind: runner
        path: runner/concourse
        release: {version: ..., sums: ...}
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
peaks and 10% more. A slice without peaks takes the 90th percentile of the
suite's other slices, so a suite measured only in part errs high. A suite
with no peaks at all gets no prediction, and its batches start as before;
its first trusted run measures them, or `pipelinegen memory sample` does
(below).

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

**Waiting until it fits.** With `memory: {wait: true}` (top level, or a
suite's own `memory.wait`), a batch waits for as long as it could still
finish within its time limit instead of 5 minutes: the suite's `minutes`
less the batch's expected time (its slices' durations) half again and a
2-minute margin, or half the limit for a batch of no known time. That is
how pipelinegen, not Concourse's task limit, decides how many batches a
worker runs at once: with every suite waiting for memory, Concourse's
`limit-active-tasks` placement can allow more tasks per worker, and a
batch the worker hasn't the room for waits on it until earlier batches
end. The plan's log says, per suite, how many of the selected slices have
a recorded peak and whether its batches wait:

```
memory: e2e: 256 of 256 selected slices have a recorded peak; its batches wait for their predicted memory (memory.wait)
```

A slice without a peak is predicted high, at the 90th percentile of the
suite's others, so raise the task limit once the suites that run heavy
batches have peaks, measured on CI or sampled.

**One after the other: the broker.** Batches that read the worker's free
memory at the same moment all see the same room, and all start. A worker
that runs `pipelinegen memory serve` (port 7480 by default) hands its
memory out instead: with `memory.broker` set (`gateway:7480` for the
task's default gateway, where a Concourse worker's tasks reach their
worker), a waiting batch asks the broker for its prediction, less what it
holds. The broker grants the asks in the order they came, each once the
worker's free memory, less what it granted in the last `--ramp` (5
minutes by default), covers it: by then a batch's own use shows in the
free memory itself. A batch whose wait ends before its grant leaves its
slices not run, as above, and one that can't reach the broker waits by its
own reading. The broker runs beside the Concourse worker, in its
container (`docker exec <worker> pipelinegen memory serve`), so the memory
it reads is the worker's, cgroup limit and all; it holds nothing a batch
must give back, so a batch that dies frees its grant when the ramp ends.

**The pipeline's own steps.** With the top-level `memory.wait`, the
steps that plan (a branch's plan, a pull request's first step) and the
bookkeeping steps that end a batch or a suite wait for their own memory
the same way, before their heavy part, asking the broker when
`memory.broker` names one:

```
memory: this planning step should use 2640 MB, by its recent peaks
```

Each such step of a trusted run records the most its task used when it
ends (its cgroup's peak), by step: its command, its run and, for a
suite's step, the suite. A step expects the 90th percentile of its own
last 10 peaks and 10% more; without any, the most the same command's steps
of other runs expect (a pull request's plan, which keeps no peaks,
expects what the branches' plans used); without those,
`memory.steps.planning` (4096 MB) or `memory.steps.bookkeeping` (1024 MB).
Unlike a batch, such a step never gives up: it waits at most a third of
what its own time limit leaves (15 minutes at most), then starts anyway
and says so, since a plan or a report that failed for memory would stop
the run it is for. With these steps waiting too, the worker that plans can
take more tasks at once without running out of memory.

**CPU.** Memory is not all batches share: a worker running more browser
batches than it has cores starts none out of memory, yet each runs slower
and hits its timeouts. With `cpu: {wait: true}` (top level, or a suite's
own `cpu.wait`), a batch also waits for the worker's CPU, in the same wait
and for as long as it could still finish within its time limit:

- **The pressure hold.** It starts only while the worker's CPU pressure
  (`/proc/pressure/cpu`, the larger of `some avg10` and `avg60`: the
  share of the last 10 seconds, or of the last minute, some runnable task
  waited for a core) is below `cpu.pressure`, 40% by default; the
  minute's keeps a few quiet seconds under load from reading as room. An unreadable pressure holds nothing back.
- **Cores.** A trusted run's suite end keeps each slice's cores beside its
  peak: its `cpu_seconds` over its `seconds`, the cores it kept busy on
  average. A batch runs its slices one at a time, so its prediction,
  `cores` in `batches/<n>/memory.json`, is the most any of its slices
  should keep busy, each the 90th percentile of its recent cores and 10%
  more; a batch none of whose slices has any is predicted `cpu.cores` (1
  by default). The plan's log says, per suite that waits for CPU, how many
  selected slices have recorded cores.
- **Core grants are leases.** With `memory.broker`, the batch asks the
  broker for its cores with its memory. The broker counts `--cpu-factor`
  (1.25) cores per core of its machine, and grants the ask once its cores
  fit beside those of the grants whose leases still run, and the CPU
  pressure is below `--cpu-pressure`. Grants of cores go `--cpu-settle`
  (15s) apart, so the pressure each reads shows the batch granted before
  it, and batches that fit at once don't start together. A grant's cores
  count for the
  batch's expected time half again (for as long as it may wait, when its
  time is unknown); a suite of steps (`slice run`) releases them at its
  batch's end, so the next batch starts at once. A task file's batch,
  which asked from `slice memory` before its setup, releases them by
  `tools/pipelinegen slice release` in its exit trap. Without that, the
  broker asks every `--probe-every` (30s) whether the task's
  container is still on the worker's network, and ends the cores of one
  no route led to on two probes in a row. A probe that times out, as
  behind a firewall that drops the refusal, ends nothing, and nor does a
  finished task whose container the worker keeps a while. A batch predicted more
  cores than the broker counts starts once the pressure allows.

A batch still short of CPU when its wait ends leaves its slices not run as
one short of memory does, and says so: "not run: the worker's CPU pressure
was 63%, at or above the 40% a batch waits below (cpu.pressure), after
waiting 45m0s". Under the top-level `cpu.wait` the planning and
bookkeeping steps wait for the CPU pressure too, keeping no cores, and
start anyway when their wait ends. A suite's prepare and its whole-suite
checks run the change's code outside a batch: under the suite's
`cpu.wait` or `memory.wait` they wait while the worker is under CPU or
memory pressure, `slice prepare` by itself and a task of the
repository's by calling `tools/pipelinegen wait prepare` (or `wait
whole`) first, and start anyway when their wait ends. Once the suites that run heavy batches
wait for CPU as well as memory, Concourse's task limit can rise further:
pipelinegen then holds a batch back when the worker is busy, whichever
runs short.

**Sampled here.** `pipelinegen memory sample --suite <suite>` runs 8 of
the suite's slices on your machine (`--slices`, spread over their
durations from the longest; `--slice` names them), each in a batch of its
own in its image, measures each one's peak as CI does, set-up included,
and adds it to the store's peaks with 25% on top (`--headroom`). It needs
the store's writing credential, as `seed` does; `--dry-run` measures
without writing. A suite sampled this way waits for memory from its next
run on, and CI's own measurements join the sample's.

**Not run.** A batch still short when its wait ends starts none of its
slices. Its end marks them `not run` with the reading ("not run: the
worker had 3120 MB free of 6600 MB predicted, after waiting 5m0s"), the
suite's status says `2 not run (worker short of memory or CPU)`, and the comment
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
does it itself. With `memory.wait`, a task file that doesn't is waited
for at its first `slice start`: the batch then needs its prediction less
what its container already holds, since its own setup runs before. On your machine, `pipelinegen run` and `record` read the
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
lock between them. What replaced the lock is the wait for memory above:
the room a batch waits for is the worker's, whichever pipeline holds the
rest, so another repository's batches that fill the machine keep this
one's waiting as its own would. Nothing is reserved: two batches that read
the same free memory at once both start, and a suite not yet measured
starts without waiting. Placement that puts the two pipelines' heavy
suites on different workers keeps them apart outright.

A config that still has `suites.<name>.lock` loads: `check` says the key
is no longer read, and you remove it once the pipeline runs this release.
Its `minutes` is still read, as the old spelling of `suites.<name>.minutes`,
until you move it there. A pipeline runs the pipelinegen it was generated
with, so one generated before keeps its working lock steps until it is set
again; removing the key while it still runs an older pipelinegen fails
its lock steps, which can no longer find the lock. A lock step that runs
this release (`pipelinegen lock` or `pipelinegen unlock`) does nothing and
passes. Both commands go in the next release. The leases older pipelines left in the store are swept by the
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
