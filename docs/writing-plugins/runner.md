# Writing a runner

This page is for people writing a runner for another CI system. A runner
turns the config's runs and suites into one CI system's pipeline. It owns
everything specific to that system: task files and their inputs and
outputs, steps repeated over a list known only at run time, caches, the
bootstrap step that fetches pipelinegen and the plugins,
and the repository's own parts merged in. `concourse-runner` is the one
runner so far, and its documentation is the example to follow. The fields
are in [plugins.md](../plugins.md#runners).

## Calls

- `describe` adds what the CI system can do: steps repeated over a runtime
  list, caches that stay with a job, a native rerun.
- `pipeline` takes the spec of the config and answers the pipeline's
  files: `{"files": ["pipeline.yml"]}`. `pipelinegen pipeline` calls it,
  and the same spec must give the same bytes every time. What a person
  should know of the pipeline, short of refusing it, goes in
  `"notices"`, one line each: `pipelinegen pipeline` prints them and
  `pipelinegen check` reports them as warnings.

A config may have several runners, each run naming its owner in
`runs.<name>.runner`. The spec's `runs` are then the runs your runner owns
alone, and `other_runs` names the rest, which other runners' pipelines
hold: write nothing for them, and take a placement rule that names one of
them as a rule for a run elsewhere, not as one for a run the spec lacks.
The spec's `trusted` string still covers every run. With several
runners the spec's `runner` is your entry's name, and your pipeline's own
job that runs `pipelinegen pipeline` must pass it as `--for <name>`; with
one runner `runner` is left out and the command finds it alone.

## What the pipeline must do

The core knows no CI system; the pipeline's steps call its commands:

1. on a trigger, `pipelinegen plan` for the run;
2. per suite, `pipelinegen tree`, then the suite's prepare task, then
   `pipelinegen batches`, then one batch task per batch, every batch at
   once (no cap of the runner's own: `batches` deals as many as the suite's
   `parallel`, `batches` and `batch_minutes` say should run together), each
   calling `slice setup-end` once and `slice start` and `slice end` around
   its slices (they only journal and collect; the batch's end writes
   what they noted);
3. after each batch, whatever happens, `pipelinegen report batch-end`; at
   the suite's end, `pipelinegen report suite`;
4. each whole-suite check the plan selected (`tree` writes a gate for
   each), after the batches, with `pipelinegen report collect` first so the
   check can read every slice's state, and `pipelinegen report whole`
   after it, whatever happened, to report the check's result.

For a suite with `steps` instead of `run_slice` and `prepare`, the
suite's prepare task (when it has a prepare step) runs `tools/pipelinegen
slice prepare`, in the prepare step's `image` or else the suite's, and
each batch task runs `tools/pipelinegen slice run` in the suite's
`image`; both run the suite's drivers. Give those tasks the inputs and outputs
the [table of
steps](../contracts.md#commands-the-generated-pipeline-calls) names.

A runner that writes such tasks declares the capability `steps`; the core
refuses a suite with steps for a runner without it.

A suite of the spec with steps carries `steps` instead of `prepare` and
`run_slice`: whether it has a prepare step (`prepare`) and that step's
image (`prepare_image`), and the drivers its steps run (`drivers`), which
the batch task's tools must hold. Both commands take the task's layout by
default (`source`, `batches`, `prepared`, `result`, `coverage`, `work`,
`cache`), so the task calls them without flags.

Every task that runs pipelinegen itself gets `PIPELINEGEN_PIPELINE_STEP=1`:
planning, bookkeeping and suite steps alike. It tells pipelinegen that it
runs as a pipeline's step and nothing else: Bazel's output and bazelisk's
downloads go into the step's cache, a Bazel server stops after ten idle
minutes, and git reads no system or global config. A step without it is
taken for a person's command, so a planning step without it starts Bazel
cold on every build. `PIPELINEGEN_JOB_URL` is only the link to the step's
job, for statuses, records and comments; pipelinegen still takes it for
the mark when the mark is missing, as a runner from before the mark gave
it to suite steps. The kit checks that every step of the tool's has the
mark, and that no task of the repository's has it.

Every task that runs pipelinegen itself gets the spec's `trusted` string,
unchanged, in `PIPELINEGEN_TRUSTED`. A task that runs the repository's own
scripts never gets it: in a proposed change's run those scripts are the
change's, and they could rewrite it. Without it, a proposed change's plan
warns that the pipeline must be made again.

The batch task of a suite with steps is the one task that runs both
pipelinegen and the change's tests, and it gets `PIPELINEGEN_TRUSTED`,
with no token. That is safe because pipelinegen is the task's entry point,
not a script of the repository's: it reads the steps from the trusted
string before any test runs, and it takes the string and every token out
of each driver's environment, so no driver and no test it starts finds
either. The string itself holds no secret: it names vars, pins and refs,
and stands in the pipeline as a literal. The kit checks that such a task
gets the string and no token.

Where each step runs is the runner's own business, in its own words: the
spec carries no worker tags, and the core passes the runner's config block
(its entry's `config`) in every call without reading it. Read the
placement from there, under `placement`, and report the capability
`placement`; `pipelinegen pipeline` refuses a runner without it, since
such a runner would put every step on untagged workers. Choose words that
fit the CI system: concourse-runner's are a default and rules for kinds of
step (`pipeline`, `planning`, `checks`, `suite`, `bookkeeping`), of a
suite and a run. A placement changes where a step runs and nothing else:
never which steps there are, what they run or what they hold. A step
moved to another worker may need its inputs and image fetched there: add
a copy of the job's get of each under a name of its own, never
triggering, and point the task's `image` and `input_mapping` at the
copies. Refuse what
you can't follow when asked for the pipeline (an unknown key or kind, a
run or suite the spec lacks, a rule that never places a step); `pipelinegen
check` asks the runner too, when it is at hand, so the config's author
sees it before the pipeline job does. The runner's block is among the
trusted keys: a proposed change's own block takes effect once it is
merged, which the pipeline being made from the default branch's config
already ensures.

## Packages and cached images

A runner that declares the capability `packages` installs what the spec
says the steps need. Each suite with packages carries `packages`: the
names, an `install` script, and `images`, which maps each resource the
suite's steps run in that has a cached image to that image's key. The
spec's `planner_packages` are pipelinegen's own steps' packages, and its
`images` names the registry (`registry`), the var that pushes
(`credentials`), the var that only pulls (`pull_credentials`), and the
images to build (`builds`: `key`, the base resource `base` at `digest`,
`names` and `install`). The tool works out every key. The runner never
does, and never changes the scripts.

- Put the `install` script in front of the command of every task the
  runner writes that runs pipelinegen, in the same shell: the suite's
  before `slice run` and `slice prepare`, the planner's before the rest.
  It runs in a subshell, as root, does nothing when the image has every
  package, and fails the task when one can't be installed. Give the task a
  cache and name it in `PIPELINEGEN_PACKAGE_CACHE`, so the package manager
  downloads a package once per worker. A task that reports a broken build
  may go on without them.
- A task file is the repository's script: nothing goes in front of it.
  Its suite gets its packages only from a cached image.
- For each build, a job that runs when the config changes asks the
  registry for `<registry>:<key>` and, when it is missing, builds it on the
  base at its digest with `install` run as root (with
  `PIPELINEGEN_PACKAGE_CLEAN=1`, so the image keeps no package indexes) and
  pushes it. It takes nothing from the repository but the config's
  version, and it alone holds `credentials`. It takes no lock and is in no
  serial group: two builds that push one key push the same packages on the
  same base.
- The suite's tasks run in its cached image. Fetch it so that a missing
  image never holds the job: Concourse makes every `get` an input that the
  build waits for, so a task fetches it. When the registry hasn't got it or
  can't be reached, fetch the base at its digest instead: the installs then
  do the work. The fetch may hold `pull_credentials`, never `credentials`.
- The planner image is never swapped for a cached one: those steps hold
  the tokens that write, and a tag in a registry can be moved by whoever
  can push.

A runner without the capability ignores these fields and writes the
pipeline it always did. `pipelinegen pipeline` and `check` say that the
steps run without the packages.

A branch's recording run attaches its lists by itself, in `report suite`
once its last suite has ended; the pipeline adds no step for it.

No job of the pipeline runs its builds one at a time, or behind other
jobs. Builds of one job for two commits run side by side, each
on records of its own commit, and the workers' own limits are the only
bound. A planning job never runs its builds one at a time, a continuous
run's included.

A runner that declares the capability `continuous` builds a run whose
trigger kind is `continuous`: every commit of the branch starts one build
of the planning job (Concourse: `version: every`). The builds run side
by side, and each plan step records its round's start and planning in
the store for the others to read: of two builds deciding at once, the
older commit's exits and the newer one waits for its round. A runner
without `continuous` is refused a continuous run by `pipelinegen
pipeline`.

A continuous run's planning task decides first, before the planner's
prepare command: `pipelinegen plan --decide --gate <dir>` ends a
superseded commit's build at once, waits while a round of the run is in
progress, and exits 0 either way. A build that won't plan (superseded, or
its commit had its round) has `<dir>/gate.json` written, closed, as an
exited plan step writes it; the task then ends, green, and runs neither
the prepare nor the plan. Otherwise the decision leaves `<dir>/decided.json`
for the plan step, and the task runs the prepare and then `pipelinegen plan
--gate <dir>`, which decides once more (recording no second start of the
round) and plans. Give the decision the environment the plan gets, the
planner's params and secrets left out. The task's time limit is `limits.plan` and the run's
`wait_minutes`; export `PIPELINEGEN_DEADLINE` (now plus `limits.plan`)
and `PIPELINEGEN_LIMIT` (`limits.plan=<minutes>`) again once the decision
has ended, so the prepare and the plan have `limits.plan` from there.

Both of a continuous run's plan steps run with `--gate <dir>`. The job puts a
hand-off of the run (`<dir>/version`, `<run> <commit>`) only when
`<dir>/gate.json` is open, that is when the step wrote a plan (Concourse:
a put of the run's `mock` resource in an `across` over the gate). The
suite jobs trigger on those hand-offs and take the branch's commit of
the same planning build, so a build that exited starts no suite build.

A pull request run's planning job begins with `pipelinegen plan --begin`,
in a step that runs none of the change's code and holds the credential
that writes the store, before the plan's first step: it records that the
build plans its head, and the run's sweep then leaves the head to that
build for the plan's limit instead of planning it beside a slow plan;
the plan's write step (`--from`) removes the record when it leaves the
head without a plan. A pipeline without the step works, and its sweep plans a head whose plan
outlasts `missed_after` once more.

A pull request run's planning job hands a head on the same way. It plans
in two steps, `pipelinegen plan --out <plan>`, which runs the change's
code and writes nothing, then `pipelinegen plan --from <plan> --gate
<dir>`, which writes `<dir>/gate.json` before anything else, shut, and
opens it only when it wrote the head's plan, or found the head planned
with none of its suites begun after the run's `missed_after` (as the
sweep would hand it on again, marking it as the sweep does). The job puts
the head's hand-off only when the gate is open (Concourse: the put in an
`across` over the gate), so a planning build of a head planned already,
whose suites began or are on their way, ends green and starts no suite
build. The sweep's last step passes `--gate` to each `plan --from` too and
hands on only the heads whose gate opened, beside the planned heads
`pipelinegen unplanned` listed to hand on again. A suite counts as begun
once a build of it ran `pipelinegen tree` with its gate open, which
records the build's start in the store: give that step the store's token,
as every step of the tool gets it, or the suite counts as begun only from
its first batch's end, and a head whose build takes longer than
`missed_after` over its prepare and first batch is handed on again.

The pipeline's own job, which sets or checks the pipeline, passes
`--gate <dir>` to `pipelinegen pipeline` and sets or checks only when the
gate is open: builds of it run side by side too, and one whose config the
default branch no longer has must never set its older pipeline over a
newer one.

No step takes a lock. A spec from an older tool may still give a suite a
`lock`: write no lock step for it, and say so in a notice.

Every step the pipeline runs has a time limit, so a step that hangs
ends instead of holding its run pending: the spec's `limits` and each
run suite's `minutes`, as the contracts' [time limits](../contracts.md#time-limits)
say. A step in a hook gets its own: a CI system that, like Concourse, runs
a hook after a limit ended the step runs it outside that limit. Each of
pipelinegen's own steps gets
`PIPELINEGEN_LIMIT`, and a task whose script runs something before the
tool also `PIPELINEGEN_DEADLINE`, so the tool ends two minutes early and
says which limit it reached. The end task gets the suite's `batches`, so a
slice left open says whether its limit stopped it.

The step that runs `report batch-end` is the step that observes: give it
the tree as `source` and the batch's coverage output as `coverage` in its
working directory. Correlators may rely on these two and on nothing else,
so don't hand that step another step's output to make a correlator work:
the core refuses a correlator config that reads outside the two (see
[what the step that observes can see](../plugins.md#what-the-step-that-observes-can-see)).

A suite must keep going when one batch fails, so every selected slice gets
a result. A rerun must run only the slices that didn't pass; the `batches`
command leaves out those that passed for the change. Task caches should
stay with the suite's job between changes: a cold Go or Bazel cache costs
more than the tests a selection saves.

What each step mounts and which environment it gets, the token included,
is in the contracts' [table of steps](../contracts.md#commands-the-generated-pipeline-calls),
and what the repository's own tasks get in
[params and secrets](../contracts.md#params-and-secrets).

The kit checks the pipeline your runner writes for its fixture spec and
its variants (an older tool's spec with a lock on every suite, per build
and per batch, set by itself, tokens by purpose, a connector's changes, a
continuous run, suites run by drivers, and packages with cached images, the
last four only when the runner declares them; with packages every task the
runner writes that runs pipelinegen installs its packages first, every
cached image the spec names is built, and neither registry var reaches a
task that runs the repository's code nor the pushing var a job that does):
no step may run `pipelinegen lock` or `pipelinegen unlock`; every step must have a time
limit, its own or one around it, a hook's steps their own; no job may be
serial, in a serial group or bounded by `max_in_flight`, a planning job
included: a serial planning job, and a step that still sets
`PIPELINEGEN_SERIAL_PLAN`, are refused; a continuous run's suite jobs may trigger only
on what its planning job puts inside an `across` (the hand-off it puts
when `plan --gate` opened its gate), never on the branch; a pull request
run's suite jobs only on what its planning job or its sweep puts inside an
`across`, with no put of it outside one, and its planning job's `plan
--from` passes `--gate`; and a step that runs `report batch-end` or `report suite` sits in no `try`
and, run with a stand-in for the tool that exits 4 for a report, exits 4
itself, so a step that could not write the run's records fails its job
whatever else of its failures the pipeline tolerates;
with the placement of the config the kit is given, the pipeline is the
same but for worker tags and the gets a moved step needs on its worker
(a copy of a get the job has, under a name of its own and never
triggering, named only by a task's `image` and `input_mapping` in place of
the get it copies);
every `pipelinegen` call must name a command,
subcommand and flags the tool has, every step must mount what its call
reads and keep what it writes, and the pipeline may reference no var but
the token, the declared secrets and the repository's own. A task that
runs several of the tool's commands may read what an earlier one in the
same task wrote. An output that no earlier command wrote doesn't count,
because it starts empty. The one exception is the directory the tool runs
from, with the plugins beside it: it may be an input, an output or a
cache, so a bootstrap can fetch it in the task itself. A missing binary
fails at once and loudly.
A task whose run names no call of the tool, because it runs a program of
the runner's that calls it, the kit runs: when the pipeline marks it as
the tool's step with `PIPELINEGEN_PIPELINE_STEP`, in its job's order,
offline, with every pinned tool already in `tools` at its pin and your
runner in `tools/plugins/<name>`. Such a step must keep tools it finds at
their pins rather than fetch them again; what it calls is checked as a
script's calls are. The kit runs them once more with a stand-in plugin,
and once with your runner, pinned by `sums` and no `sha256`, the list
beside it as `tools/plugins/<name>.SHA256SUMS`: each task must get as far
as calling the tool, or exit 0, with the list still matching the pin and
the tool its line in it.
[Conformance](../plugins.md#conformance) has the full list.

A tool's repository names the program it holds. A repository named
`pipelinegen-KIND-NAME`, KIND a plugin kind (`plugin.Kinds`), holds
`NAME-KIND`: `owner/pipelinegen-sbom-go` holds `go-sbom`, released as
`go-sbom-linux-amd64` or `go-sbom.mjs` and built from `./cmd/go-sbom`, as
`owner/go-sbom` does. Any other repository holds the program of its own
name, the tool's included. A bootstrap fetches, builds and caches by that
name (`plugin.Program` says it), so both spellings of a renamed repository
give the same files and the same cache entries.

A tool's `repository` may be any git URL
(`https://github.com/owner/name`, `git@github.com:owner/name.git`) as well
as `owner/name`, and a tool with a `path` is one plugin of a repository
that holds several: its release is tagged `<path>/<version>`, and the path
names its program (`driver/go` holds `go-driver`, built from
`driver/go/cmd/go-driver`). A bootstrap that knows neither fetches the
wrong release for such a tool, so it should refuse one with a `path` or a
URL it can't read.

A tool in the spec carries `sha256` (its linux-amd64 file's checksum) or
`sums` (the checksum of its release's `SHA256SUMS`). For `sums`, the
bootstrap fetches `SHA256SUMS`, checks it by `sums`, keeps it beside the
tool as `<name>.SHA256SUMS`, and checks the file for the worker's platform
by its line there.

Document how your CI system hands a batch task its inputs (the tree, the
prepared output, the batches, the tools) and where it expects the results.
