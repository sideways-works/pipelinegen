# The generated pipeline

This page is for people changing the pipeline's shape or the commands its
steps call. `pipelinegen pipeline` asks the configured runner plugin for
the pipeline
([concourse-runner](https://github.com/sideways-works/pipelinegen-runner-concourse) for
Concourse); the core writes no pipeline itself. Its jobs call `plan`,
`tree`, `batches`, `report` and `slice`. The earlier shape, one pipeline
per run, and why it was left are in
[design.md](../design.md#what-we-tried-first-and-why-we-left-it).

## One pipeline, static until the config changes

The selected slices never appear in the pipeline: a suite job learns them at
run time, from the plan. The pipeline holds:

- for each run, the resource that starts it, a planning job `plan-<run>`,
  and one job per suite of the run, `<run>-<suite>`;
- a job named `pipeline` that runs when `pipelinegen.yaml` or the included
  file changes on the default branch. By default it regenerates the
  pipeline and fails when it differs from the one set ("the pipeline is
  stale: set it"), printing no values; a person sets it. With
  `concourse.self_set: true` it sets the pipeline itself, passing no
  variables, which needs a Concourse that resolves `((name))` references
  itself (a credential manager or `var_sources`);
- the repository's own parts from `concourse.include`, merged in whole:
  `resource_types`, `resources`, `groups` and `jobs`.

The shape exists for the caches: task caches belong to a job in Concourse,
so a static pipeline keeps the Go, npm and Bazel caches warm from one
change to the next.

## What starts a run

- `branch`: a git resource on the branch.
- `pull_requests`: a pull request resource filtered by the trigger's data,
  one version per head commit.
- `timer`: Concourse's time resource, on the branch's tip.
- `manual`: the planning job, started by hand.

## The planning job

`pipelinegen plan` checks the change out (a pull request's head merged into
its target's tip, the same merge the suite jobs will test), plans it, writes
the plan once, create-only, to `refs/pipelinegen/plans/<run>/<change>/<c12>`,
and sets every listed suite's status to pending, or to success with
"nothing selected". A pull request head that the run's filters or
`once_per_ready` turn away gets no plan, and the log says why. When the
config sets `concourse.planner.prepare`, the step runs that command in the
checkout first, to fill the caches the plugins share.

## A suite job

It takes the same version (`passed: [plan-<run>]`), then:

1. the bootstrap step (below);
2. `pipelinegen tree`: the same tree under test, from the plan, and a gate
   file, `["1"]` when the plan selects something in this suite and `[]`
   otherwise. The rest of the job sits in an `across` over the gate, so a
   version without a plan, or with nothing for this suite, ends in seconds
   and fails nothing;
3. the lease, for a suite with a `lock`;
4. the suite's prepare task;
5. `pipelinegen batches`: deals the plan's selected slices into at most
   `parallel` batches by duration, leaving out those that already passed
   for this change;
6. `across` the batches, without fail fast, one batch task each. An
   `ensure` inside each iteration runs `report batch-end`, which writes
   what the batch's journal still holds and closes the batch's slices left
   without a result (on the `across` step itself it couldn't see the
   iteration's var). The batch task gets no token unless the suite
   declares one for it, so without one its slices reach GitHub at this
   step;
7. whole-suite checks, when selected, each after `report collect` has
   written every slice's state for it to read. They run only when every
   batch passed: Concourse runs nothing after a failed `across`. Each
   check's `ensure` runs `report whole`, which reports its result as the
   slice `whole.<name>`;
8. in the job's `ensure`: `report suite` (the suite's status and the run's)
   and the lease's release. When the whole run has passed, the last suite
   to finish posts the final report and, for a branch's recording run,
   attaches the lists; a branch's recording run that failed attaches the
   recordings of its slices that passed once its last suite has ended. The task sits in no `try`: a suite's end that could
   not be recorded (exit 4) fails the job, any other failure of it passes.
   The job's `on_error` and `on_abort` hooks run `report suite` too when
   the build had got as far as its tools and its trigger, so a broken or
   aborted build still ends its suite and the run's round can end;
   without them they post the suite's status and the run's as errored. A
   build aborted before any of its tasks got a worker runs neither: its
   suite has no end until `pipelinegen end` or the run's idle bound.

## Reruns

A rerun is Concourse's own "rerun build" of a suite's build. It keeps the
version, so it reads the same plan, and `batches` leaves out slices that
already passed for the change: it runs exactly what didn't pass, errored
slices included. No rerun jobs are generated.

## The token, secrets and Concourse's address

Each step's mounts and environment, the token and Concourse's address
included, are in the contracts'
[table of steps](../contracts.md#commands-the-generated-pipeline-calls),
which the kit checks every runner against. The repository's own tasks get
the suite's `params`, the secrets declared for their kind of task and
pipelinegen's own variables, never the token unless a secret declares it
([params and secrets](../contracts.md#params-and-secrets)).

## Builds of different changes

They queue on a suite job, or run side by side, as `run_on.serial_group`
and `max_in_flight` say. When the config changes, running builds keep the
plan they started with and finish; pending builds start with the new
config.

## The bootstrap step

In the runner (`bootstrap.sh` of concourse-runner). Every job's first step runs on
the planner image (pinned by digest) and leaves `tools/pipelinegen`,
`tools/bazelisk` and each configured plugin in `tools/plugins/<name>`:

- a release is fetched from the GitHub API with the token in a header, its
  `pipelinegen-linux-amd64` asset checked against
  `concourse.planner.bootstrap.release.sha256`;
- before the first release, the pinned commit is fetched and built
  (`CGO_ENABLED=0`, `-trimpath`);
- bazelisk is fetched and checked against its pinned checksum;
- each plugin's release is fetched and checked against the checksum the
  config pins for it, or a plugin pinned by commit is built.

Both are kept in the step's task cache, keyed by release or commit, so a
job fetches each once per worker. A missing checksum or a mismatch fails the
step.
