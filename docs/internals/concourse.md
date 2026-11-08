# Concourse

The pipeline's shape rests on facts about Concourse 8.2.4 that were proven
on throwaway pipelines, with the builds named by id. design.md keeps them
with the shape they led to and the one they led away from: see
[What we tried first, and why we left it](../design.md#what-we-tried-first-and-why-we-left-it).
The end-to-end proofs on this repository's own pipeline are tracked in
[proof.md](../proof.md).

## What the static pipeline relies on

- Task caches are keyed by job (`atc/exec/task_step.go`:
  `InitializeTaskCache(ctx, JobID, step name, path)`). A fresh pipeline per
  run started every build with cold Go, npm and Bazel caches; one static
  pipeline keeps them warm. This is why there is one pipeline per
  repository.
- `across` over values loaded at run time works, honours
  `max_in_flight`, runs every iteration when one fails (`fail_fast:
  false`), and gives each iteration its own outputs (build 912961). The job
  fails when an iteration fails, and steps after the `across` don't run.
  The suite job's batches and its gate are built on this.
- An `ensure` must sit on a step inside the `across`: on the `across`
  step itself it can't see the iteration's var. That is where each batch's
  `report batch-end` goes.
- A step's `timeout` ends it as failed, not errored (`atc/exec/timeout_step.go`
  in 8.2.4: a deadline returns failure without an error), so a job's
  `on_error` and `on_abort` hooks don't run for it, and the build log
  says `timeout exceeded`. A task the timeout ended still registers its
  outputs, so the batch's journal reaches its ensure. An `ensure` runs
  after it with a fresh context (`ensure_step.go`), so no timeout around a
  step bounds its hooks: each step in a hook carries its own. The runner
  gives every step a limit for this reason, and the kit checks it.
- "Rerun build" keeps the version, so a rerun reads the same plan; the
  dealing step leaves out what already passed for the change.
- Setting a pipeline again lets running builds finish, with the config
  they started with (build 911190, a job removed while its build ran). The
  `pipeline` job relies on it when the config changes.

## Pull request runs: why the suites take hand-offs

The pull request resource's check lists every open pull request's head,
oldest commit first, and whenever one of them is new Concourse moves all
of them, in that order, behind every version it had. Two things went wrong
on this repository's pipeline because of it, several times in one day:

- Suite jobs took the trigger with `version: every` and
  `passed: [<planning job>]`. Concourse resolves that by walking the
  planning job's builds in the order they began, after the build that fed
  the last suite build, so a plan that ended after a later one was never
  taken, and the suites of several planned heads never started.
- The planning job takes every version of the trigger itself. A new head
  whose commit is older than a head planned before lands behind it, and
  the planning job never plans it.

Both were unstuck by pinning the trigger resource by hand. The Concourse
runner now makes neither possible:

- The planning job puts the head it planned to a resource of its own,
  never checked, as a new version each time (its `committed` is the
  moment of the plan). Suite jobs take every version of it, without
  `passed`: Concourse takes them in the order the puts land. It puts only
  through the gate its write step (`plan --from --gate handoff`) opens: a
  build that finds its head planned, its suites begun or on their way,
  puts nothing. Before that gate every planning build put a version, so a
  head planned by the sweep and then by a build started by hand ran a
  second set of suite builds beside the first, each taking the slices
  without a result yet.
- A sweep job, every five minutes and after every hand-off, runs
  `pipelinegen unplanned` and plans the open heads with no plan, and hands
  off again the planned heads none of whose suites began, each once it
  waited the run's `missed_after`. A suite began when a build of it ran
  its start step, which records that in the store before the suite's
  prepare; a suite build still waiting for a worker has recorded nothing,
  so a wait longer than `missed_after` brings a second build of the head. That also covers a new pipeline, whose
  suite job takes only the newest hand-off the first time it runs. A mark
  per head keeps the sweep from taking it up again while its builds come,
  and after three times it stops and says so on the head's run status.

The proof jobs of every run shared the serial group `proof`, so each
landing on `main` queued a `main-proof` build ahead of a pull request's
`pr-proof`. `concourse.serial_groups.proof.per_run` now gives each run its
own group.

## Workers, tags and streaming

A step that reads what a step on another worker fetched or made gets it
streamed. Without peer-to-peer streaming every stream goes through the web
node; on this repository's Concourse that ran at under half a megabyte a
second, so a one-gigabyte image took the better part of an hour, and a
build that only waited on another build's stream sat in "initializing".

Two workers under one tag are a pool: Concourse places a task and the fetch
of its image apart within it, and the image then crosses. That is what
happened when this repository's suites shared a tag carried by both cloud
workers. So each suite runs on a tag that only one worker carries, and the
config says why next to it. The runner can't know which workers carry which
tag, so neither it nor `check` can warn about a pool.

Two settings of the Concourse web node change this, and both are the
operator's to make: `CONCOURSE_ENABLE_CACHE_STREAMED_VOLUMES` keeps a
streamed volume on the worker that received it (each version then crosses
to a worker once, not on every build), and peer-to-peer streaming lets the
workers stream to each other directly.

## What the per-run shape needed, and the static one doesn't

Adopting a run (`set_pipeline: self`) so the planning job's next build
didn't archive it, pinning the commit on the resource, retiring a run by
setting it without its last job, and a clean-up of archived instances. The
static pipeline needs none of these.

## GitHub's limits

Measured on this repository's pull requests ([proof.md](../proof.md)): a
comment body may hold 262,144 bytes, so 65,536 characters always fit;
GitHub renders Mermaid with its defaults, at most 50,000 characters of chart
and 500 edges. The reporter's fallback at 40,000 characters and 400 edges
stays clear of both.
