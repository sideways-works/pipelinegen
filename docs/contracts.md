# Contracts

This page is for people changing pipelinegen or building on it: the
interfaces between pipelinegen, the repositories that use it, its plugins,
and its own packages. A change to a contract changes its text here, the
code and the tests that guard it in the same commit ("Which package owns
what" names them). A change that breaks repositories already using the
tool follows the rule in [releases](releases.md#changing-what-the-pipelines-steps-call):
a key the previous release requires stays readable, with a warning, for
one more release.

## Names

- A slice of a test suite is a test file; its name is the file's path
  from the repository's root (`e2e/console/search.spec.ts`). An explicit
  slice (a tagged target) is named by its label without the leading `//`,
  every `/` and `:` turned into `.` (`//lint:go` is `lint.go`).
- A change is `pr-<number>` for a pull request, or the branch's name.
- A commit in a path is its first 12 hex characters.

## Slices and how a change selects them

A suite of tests (`tests:` patterns, minus `exclude:`) has one slice per
matching file that holds a test of its own. A file that holds none is a
helper: an input of the slices that use it, never a slice. Holding a test
is decided per language: a Go file by its top-level `Test...` functions
(read from the syntax tree); any other file unless it matches the suite's
`helpers:` patterns.

A test file's slice is selected by joining two answers: the walk over the
plugins' graph, from the units that changed back to the slices that bind
them, and what bazel-diff says about the repository as it is. bazel-diff
hashes the main repository only; nothing is written into it.

A slice runs when one of these holds:

- its test file came, went or changed (from the commits' diff, never
  narrowed);
- the walk reaches it. For a recorded slice that means a unit it ran
  changed, or a unit that one of those reaches over the links at the run's
  width; a recorded slice without a list yet runs when any unit changed.
  A test file that no plugin lists units for is a unit of its own, so a
  change to it also runs every recorded slice without a list. A slice of
  a suite bound by the graph runs when a unit of its own files changed,
  never through their links: what its file uses is its owner's graph,
  which bazel-diff hashes;
- one of its Bazel labels is impacted. In a recorded suite these are the
  `data` of the target that owns its file (the suite's `owner` pattern,
  `{dir}`, `{file}`, `{base}` and `{stem}` filled from its path, else the
  test rules that take the file as a source), never the owner itself,
  whose code the recording binds instead. In a suite bound by the graph,
  the owner counts whole. In both, the suite's `extra_deps` labels for the
  file and the `pipelinegen:data` labels in it count too. `//pkg` is read
  as `//pkg:pkg`;
- its set of labels differs between the two commits: a label added to an
  owner's `data` selects the slice even when that label's target is
  unchanged;
- one of its labels names a target bazel-diff doesn't hash, such as one in
  another repository, and some file changed. Whether the label changed
  can't be told, so the slice runs and the plan warns.

`extra_deps` (`[{tests: [patterns], deps: [labels or unit ids]}]`) and the
line comments near a file's top (in the first 30 lines, any comment
syntax: `pipelinegen:data //label` or `pipelinegen:data api:notes-api`)
name Bazel labels or unit ids. A unit id is a root of the walk: the slice
is bound to that unit and to what the unit reaches over the links,
whatever its list says. When an sbom lists units for a file, a recorded
slice's label for that raw file is dropped: the units of it the slice
touched are in its list already. So a test that reads a whole API
document depends on its whole-document unit, which prose edits don't
change.

A test file's slice has the id `@pipelinegen_deps//slices:<file>` in
the plan, `why`, the reports and the runner's input. The id names the
deps repository, which only a build with the `pipelinegen_forward` tag
writes.

`everything_on` is unchanged: a changed file that matches it runs every
slice before anything is hashed.

An explicit slice (a target carrying the suite's `tag`) is selected by
bazel-diff alone: it runs when its target is impacted. The target that runs it may be another one, named by
the tag `pipelinegen-run:<label>`.

### pipelinegen's own steps

Helper steps run on a stock image pinned by digest (git, certificates; a Go
image):

```yaml
concourse:
  planner:
    image: {repository: golang, tag: "1.27.1"}
    digest: sha256:...
    bootstrap:
      repository: sideways-works/pipelinegen   # the default
      release: {version: v0.2.0, sha256: ...}  # or commit: <40 hex>, before the first release
      bazelisk: {version: v1.30.0, sha256: ...}  # the default; optional
```

Each generated job's first step is one `bootstrap` task on that image, with
a task cache keyed by the release or commit and an output `tools` holding
`pipelinegen` and `bazelisk`: it fetches the release with the token in a
header (or builds the commit) and fetches bazelisk, and a missing checksum
or a mismatch fails the step. Every later helper step of the job takes
`tools` and runs `tools/pipelinegen ...`. The token must be able to read the
pipelinegen repository.

### Setup in the repository

None. Planning needs no `local_repository` named `pipelinegen_deps` in
MODULE.bazel, no `.pipelinegen/` in `.bazelignore` and `.gitignore`, and
no `pipelinegen learn --stub` in the checkout's bootstrap. A repository
that has them is still accepted: `check` warns of each line, saying it
will be refused, without failing, the plan lists the same warnings, and
`learn --stub` still writes its stub and says that the lines go. Remove
the lines and the step.

## (a) The batch task

A slice is the unit of selecting and reporting, never of running. For each
suite, the run deals its selected slices into batches: at most the
suite's `parallel`, balanced by recorded duration (a few slices make one
batch). A batch is one task: one setup (one database, one seed, one
stack), then all its slices in the order given. A suite's `run_slice` file
is that task (the key keeps its name, though the task runs a batch), run
once per batch.

A suite with `steps` instead of `run_slice` has no task file of its own:
the generated batch task runs `tools/pipelinegen slice run`, which runs
the steps' drivers in the suite's `image` and keeps the same contract for
results and coverage ([drivers](concepts/drivers.md)). That task gets
`PIPELINEGEN_TRUSTED` and no token, and pipelinegen strips both from the
drivers' environment.

Its inputs are `source`, `batches`, `tools` (with the drivers as
`tools/plugins/<name>`) and, when the suite has a prepare step,
`prepared`; its outputs are `result` and `coverage`, and `cache` is kept
with the job. A prepare step runs in a task of its own,
`tools/pipelinegen slice prepare`, which fills `prepared`. Both read
their batch and suite from the variables every batch task gets.

### Inputs

- `source`: the tree under test (for a pull request, its head merged into
  the target's tip);
- `prepared`: the output of the suite's `prepare` task, when it has one;
- `batches`: `batches/<n>/slices.json` for every batch of the build;
- `tools`: `tools/pipelinegen` and `tools/bazelisk`.

`PIPELINEGEN_BATCH` is the batch's index in the whole run, among every
batch the suite's build dealt, from 0, not its place among the batches
running at once. `batches/$PIPELINEGEN_BATCH/slices.json` lists the
batch's slices in order:

```json
[
  {"name": "e2e/checkout.spec.ts", "sources": ["e2e/checkout.spec.ts"]},
  {"name": "backend/api/orders_test.go", "sources": ["backend/api/orders_test.go"],
   "tests": ["TestCreateOrder", "TestRefundOrder"]},
  {"name": "lint.go", "label": "//lint:go", "target": "//lint:go_run"}
]
```

`tests` are a Go file's top-level test names, so the task runs the
package's test binary (compiled once per batch) with `-run` per slice.
`label` and `target` belong to explicit slices (the runnable target is the
`pipelinegen-run:` tag, else the label). `tools/pipelinegen slice list`
prints the same, one slice per line, tab-separated: name, label, target,
sources (comma-separated), tests (comma-separated), with `-` for an empty
field (so `IFS=$'\t' read` never merges two), for a task in shell without a
JSON reader.

### Params and secrets

A generated pipeline runs four kinds of task of the repository's own, and pipelinegen's own steps around them. A Concourse task has one environment: whatever a task gets, every command in it sees. So the pipeline's token goes only to pipelinegen's own steps, and a task of the repository's gets a token only when the config declares it for that kind of task.

| Kind | What it has without a declaration | What a declaration adds |
|---|---|---|
| `planner`: the planning job's prepare command (`concourse.planner.prepare`) | `GOMODCACHE`, `GOCACHE`, `npm_config_cache` (the planning job's caches) | `concourse.planner.params` and `concourse.planner.secrets`, exported inside the prepare command's subshell only, never to `pipelinegen plan` |
| `prepare`: a suite's prepare task | the suite's `params`; `PIPELINEGEN_RUN`, `PIPELINEGEN_SUITE`, `PIPELINEGEN_RECORD` (so it builds with coverage when the run records the suite) | `secrets.prepare` |
| `batch`: a suite's batch task | the suite's `params`; `PIPELINEGEN_RUN`, `PIPELINEGEN_SUITE`, `PIPELINEGEN_BATCH`, `PIPELINEGEN_RECORD`, `PIPELINEGEN_RUN_FILE`, `PIPELINEGEN_JOB_URL` | `secrets.batch` |
| `whole`: a whole-suite check | the suite's `params`; `PIPELINEGEN_RUN`, `PIPELINEGEN_SUITE`, `PIPELINEGEN_RECORD`, `PIPELINEGEN_JOB_URL` | `secrets.whole` |

In a suite with steps, pipelinegen calls the drivers, and their tests
inherit, the task's environment less every token's variable (`GITHUB_TOKEN`, `GH_TOKEN`, `PIPELINEGEN_*_TOKEN`)
and every `PIPELINEGEN_*` variable except these:

| Variable | What it holds |
|---|---|
| `PIPELINEGEN_RUN`, `PIPELINEGEN_SUITE`, `PIPELINEGEN_BATCH`, `PIPELINEGEN_RECORD`, `PIPELINEGEN_RUN_FILE` | What the batch is, as in the table above. |
| `PIPELINEGEN_COMMIT` | The commit the run runs, in a batch's steps. A test that depends on the commit reads it; a test cache that keys on the variables a test read (`go test`'s) then runs it again for every commit, and a test that doesn't read it is unaffected. It is no var of a request's `env`, so a driver that passes those to its tests by name (the bazel driver's `--test_env`) doesn't make it part of every test's key. |

`PIPELINEGEN_CHANGE` never reaches a driver, nor does a declared secret
unless the suite declares it for its batches.

Each of these tasks' environment is layered: the suite's `params` (merged with the run's for that suite, the run's win), then the declared secrets, then pipelinegen's own `PIPELINEGEN_*` variables last, so nothing overrides them.

pipelinegen's own steps keep the pipeline's token (`github.token`) as `GITHUB_TOKEN`, because they write refs and statuses: the bootstrap, `plan`, `tree`, `batches`, `lock acquire` and `lock release`, `report batch-end`, `report collect`, `report whole`, `report suite` and the error report. The pipeline job's `pipeline` and its staleness check write nothing remote and get no token. They get `PIPELINEGEN_RUN`, `PIPELINEGEN_SUITE` (in a suite job), `PIPELINEGEN_PIPELINE_STEP=1` (the mark of a pipeline's step: Bazel's output in the step's cache, a short idle time, git without the worker's own config), `PIPELINEGEN_JOB_URL` (the job's link, which a runner may leave out of planning steps), and `PIPELINEGEN_TRIGGER` where the trigger is one of their inputs, and no `params` or secrets. The pipeline job's `pipeline` and staleness steps also get `PIPELINEGEN_CONCOURSE_URL` when the config leaves `concourse.url` out: writing the pipeline is the only thing that needs Concourse's address, so no other step, and no task of the repository's, gets it.

When the config splits the token by purpose (`github.tokens`), pipelinegen's steps read each purpose's token from its own variable, `PIPELINEGEN_READ_TOKEN`, `PIPELINEGEN_CHANGES_TOKEN`, `PIPELINEGEN_REPORT_TOKEN` and `PIPELINEGEN_STORE_TOKEN`, and never from `GITHUB_TOKEN`; the spec names each purpose's var (`github.tokens`), and a runner gives each step only the purposes it uses. A task that runs the change's code (a prepare, a batch, a whole-suite check) gets at most the read token. With one token, nothing changes: every step reads `GITHUB_TOKEN`.

The generated pipeline never sets `PIPELINEGEN_CONFIG`, the variable `pipelinegen record` hands a local recording's config by. A step refuses it whenever it is a pipeline's step (`PIPELINEGEN_PIPELINE_STEP`, or `PIPELINEGEN_JOB_URL` from an older runner) or runs for a run other than the local one, so a task can't make a declared run's steps read another config. The config `record` derives makes its own run the default branch's, so its lists count as a trusted run's on that machine alone: the derived config reaches commands only through `PIPELINEGEN_CONFIG`, and the local store's path is the only way its lists are read.

A batch task needs no token. Its `tools/pipelinegen slice ...` calls note each slice's start and end in the batch's journal and never write to the refs, token or not: the batch's `report batch-end`, a step of pipelinegen's own, writes what the journal holds, so the slices show when the batch ends instead of live. A repository that wants live progress declares the token for the batch kind (`secrets: {batch: {GITHUB_TOKEN: github_token}}`) and accepts that its tests see it.

`params` are plain values. A config is refused (by every command that reads it) when a value contains `((`, a key starts with `PIPELINEGEN_`, or a key is one a declared secret of the same suite also names.

`secrets` name Concourse vars, never values: `{ENV_NAME: var_name}` per kind. A suite declares them for `prepare`, `batch` and `whole`; a run's entry for the suite may add to them or replace an `ENV_NAME` (the run's win); `concourse.planner.secrets` declares the planner's. The task gets `ENV_NAME` from `((var_name))`, and no other task does. An `ENV_NAME` may not start with `PIPELINEGEN_`; a `var_name` is a plain var name (letters, digits, `_`, `-`, `.`).

Git over HTTPS with a declared token: a token goes into no URL and no file. For the planner, one declared secret may be named as the git token (`concourse.planner.git_token: ENV_NAME`); the generated prepare script then gives git a credential helper for that subshell, through `GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_n` and `GIT_CONFIG_VALUE_n`, that reads the variable when git asks. A suite's prepare task is the repository's own file, so it does the same with `eval "$(tools/pipelinegen git-auth ENV_NAME)"`. What it prints names the variable and never holds the token, so a trace of the task shows nothing secret.

The vars a pipeline needs: `pipelinegen pipeline --vars` prints every `((var))` the generated pipeline references, one per line: the pipeline's token, the declared secrets, the repository's credentials (`concourse.repository`), and those in `concourse.include`. Whoever sets the pipeline passes exactly those. `concourse.vars` is gone. A generated pipeline references nothing else besides loaded vars (`((.:name))`) and `across` vars: the kit checks a runner's pipeline for its fixture spec, and the runner refuses to generate a pipeline from a param value containing `((`, a `PIPELINEGEN_` key, or a secret whose `ENV_NAME` a param also names, although the config is refused for them first.

The pull request's number is in the run file (`PIPELINEGEN_RUN_FILE`: `{"run", "change", "commit", "pr", ...}`); there is no `PIPELINEGEN_PR`. The prepare task and whole-suite checks have no trigger input, so they get no `PIPELINEGEN_TRIGGER` and no run file. They do get `PIPELINEGEN_RECORD`: whether a run records a suite is fixed by the config, so a prepare knows from it whether to build with coverage. `report suite` runs with `--root trigger`: the suite job's ensure runs even when the tree step never wrote `source`, and the trigger is always there.

The task runs in the build's directory: the config is
`source/pipelinegen.yaml`.

`source` holds the tested commit's full history (the tree step fetches the
commit without a depth limit; a merged pull request's commit has both
parents), so a test may read it.

### Steps

Once, after its setup and before its first slice, the task runs
`tools/pipelinegen slice setup-end`. In a recording run it calls every
correlator's `collect` with `setup-end`, which closes what the setup ran
into `coverage/_setup/<plugin>`; it notes the setup's end in the batch's journal,
and `slice start` and `slice end` refuse to run in a recording run until it
has, so a slice of a batch whose setup failed never ends passed:
`report batch-end` closes it as errored.

For each slice, in order, the task runs:

```sh
tools/pipelinegen slice start <name>     # noted in the batch's journal
# ... run the slice's tests against the batch's one stack ...
# ... write result/<name>.json ...
tools/pipelinegen slice end <name>       # collect coverage, note the end, flush when due
```

and the batch's `ensure` runs `report batch-end`, which flushes the journal
first, so a batch its timeout killed loses no slice that ended.

A slice's start and end go into the batch's journal,
`result/.pipelinegen/journal.jsonl`, one JSON line each, appended and
synced before the command returns; nothing is written to the repository or
GitHub per slice.

A slice may run in parts: a browser batch runs its spec files once per
project, so each comes round several times. `slice start` on a slice that
already started begins its next part. Whenever another slice starts, the
one that ran until then is closed first: every correlator's `collect` gets
`slice-end` with its `coverage/<slice>/`, and takes what ran from the stack
(see (b)) there, so the parts of one slice add up in one directory. `slice end`
closes the part still running too, the slice's own or, at the first end of
another slice, that one's, so no slice's time takes in the others' ends;
it notes the end with the slice's time, the sum of its
parts in wall time (each part from its start to the next switch or its
end), which the flush writes as `seconds` into the slice's state. The plain
loop (start, run, end, then the next slice) is the case of one part each.
A test runner's hook may call `slice start` as the spec file changes and
`slice end` for every slice once the run is over; each call writes only
the journal, plus the collectors at a switch (each correlator's collect
bounded by its entry's `collect_seconds`, 10 by default: one that takes
longer fails that slice's recording, never the test). Neither ever reaches the network: they run on the clock
of the test that calls them.

The journal goes out in a flush: one atomic push of every slice's ref
noted since the last flush (its state and, for an end, its recording,
made from the coverage on disk by every correlator's observe), one render
of the statuses and the comment, and a `flushed` line. By default a batch
holds no token, so the batch's end (`report batch-end`) does the one flush,
and progress shows per finished batch. A repository that knowingly gives
its batch a report token as a declared secret, on a run of branches only,
may start `tools/pipelinegen slice flush --every <period>` in its own task
script after `slice setup-end` and stop it by its PID when its tests end
([writing tasks](how-to/writing-tasks.md)): it flushes every period at the
machine's lowest CPU and I/O priority, one slice's observe at a time, and
gives a round up rather than waiting out a busy store's back-offs. If it
dies the batch goes on.

A recording batch run by drivers (`slice run`) observes its slices in its
own task, where its tests ran and its correlators collected, once its
last slice has ended: each correlator's answer for each slice is kept in
`result/.pipelinegen/observed/<slice>/<correlator>.json`, and the batch's
end takes the kept answers instead of observing again. The batch's end
observes, as before, only what the batch left: a correlator whose observe
failed in the batch's task, or one the batch had no time for. An
observation in the batch never changes a slice's result: it comes after
the last slice ended and its result was written, an error only leaves the
call to the batch's end, and it stops two minutes before the task's
limit (the suite's minutes, counted from the step's start), so the task
ends, its journal already written, within its limit; its time counts
against the batch task's limit, not against any slice's. In a recording
run the batch's kept answers come from the task that ran the tests, as
the recordings they are made from already do, and its correlators run
there with no token that writes; nothing of a proposed change's run is
attached to a branch, so neither can reach a branch's lists. A batch of
task files (`slice start` and `slice end`) is observed by its end, as
before.

Before its first slice, a recording batch reads the first line of each
correlator's file in `tools/plugins/<name>`: the interpreter it names
(`#!/usr/bin/env node`, or a path) must be in the task, or the batch stops
at once, naming the suite, the correlator, the interpreter and the task
("suite e2e records with react, which needs node; this task (slice run)
has none"): without it the correlator can't describe itself or collect
there, and the batch would end every slice errored after its tests ran.
The batch's end checks the same for each correlator it still has to
observe: when its task lacks the interpreter too, it says so once, and
those slices fail with that line, never with an exit status. A
correlator's `needs` from its describe are a matter of planning, where
they are checked; they aren't asked of the batch's tasks.

The batch's ensure is `report batch-end`, which flushes whatever the
journal holds before it closes the batch's slices, so a batch killed
between flushes loses no slice that ended: the ensure finds the journal in
the `result` output, whether or not a background flush ran. `slice flush`
without `--every` does one flush on its own, for a task that wants to. A flush that dies after its push and before its `flushed`
line writes the same states again the next time, which is harmless. No
slice may be named `.pipelinegen` or start with that path part.

`slice start` and `slice end` may be called from a shell loop or from the
test runner's own hooks inside one invocation (one Playwright run with one
worker for the whole batch, a fixture calling them as the spec file
changes). Each call is synchronous and bounded (collecting across a stack's
processes takes well under a second), and the order is strict: `slice end`
of one file before `slice start` of the next.

### Outputs

- `result`, holding `result/<name>.json` for every slice of the batch:

  ```json
  {
    "passed": false,
    "tests": 24,
    "failures": [
      {"test": "checkout pays with a card", "tail": "last lines of its output"}
    ]
  }
  ```

  `passed` is true only when every test passed; any entry in `failures`
  makes the slice failed whatever `passed` says. `tests` (how many ran) is
  optional; `"tests": 0` with `passed` true says the slice ran no test at
  all: it ends passed, records nothing (its list stays as it was), its
  state says `"tests": 0`, the comment lists it under "Ran no tests" and
  the suite's status counts it ("1 ran no tests"),
  since a test file that runs nothing is a finding. A slice with tests and
  no coverage in a recording run still errors. pipelinegen keeps the
  first 10 failures and the last 15 lines
  (at most 1,500 bytes) of each tail. A slice whose result is missing or
  unreadable when the batch ends is errored: an `ensure` after each batch
  task closes every slice of the batch still without a final state. The task
  exits non-zero when a slice did not pass.

- `coverage`, only when `PIPELINEGEN_RECORD` is `true`: see (b).

A batch task may also leave small extra files for a slice in
`result/<name>/` (at most 1 MB per slice), which `slice end` keeps in the
slice's ref. A whole-suite check takes `source`, `prepared` and `slices`
(every slice of the suite in this run: `slices/<name>/state.json`,
`recording.txt` and its extra files, written by `pipelinegen report collect
--suite <s> --out slices` before it), and writes `result/result.json` the
same way.

## (b) Coverage, the correlators' input

A suite says how its slices are bound: `bind: graph` (the default: the Bazel
graph only, never recorded, never failed for missing coverage) or `bind:
recorded` (also bound to what its slices touched). Only a recorded suite
records, in a run with `record: true`; its tasks then get
`PIPELINEGEN_RECORD=true`, and its slices get recorded lists and both
widths. A recorded suite needs sbom and correlator plugins: a config that
has one without them is refused.

When `PIPELINEGEN_RECORD` is `true`, the batch task leaves in its
`coverage` output:

- `coverage/_setup/`: what the stack's start, seed and scenario setup ran
  before the first slice, which every correlator that observes the suite
  collects at `slice setup-end`;
- `coverage/<slice>/`: what each slice ran.

What a correlator reads there is its own contract, in its README: for Go,
`*.coverprofile` files and `GOCOVERDIR` output (`covmeta.*`,
`covcounters.*`) from binaries built with `-cover -covermode=atomic
-coverpkg=<module>/...` and the tag `pipelinegen_cover`; for a browser
suite, what react-correlator asks for. Tasks write coverage straight into
`coverage/<slice>/`; at the slice's end each correlator's `collect` takes
what belongs to it from there into its own `coverage/<slice>/<plugin>`, so
a task's layout doesn't depend on which plugins are configured.

A stack's long-running binaries can't restart per slice, so their counters
are written and cleared between slices. The core never talks to a tested
process: a correlator's `collect` does. go-correlator does it through
files (a request file the binary polls, a ready file it touches), with a
small package of its own linked into the binary's coverage build. A suite
whose batches run a stack says `stack: true`; a recording run of it fails
at `slice setup-end` and `slice start` when none of the correlators that
observe it collects. Backend test slices need none of that: compile the
test binary once, run it per slice by test name, each run with its own
coverprofile.

Processes must stop cleanly (exit or SIGTERM handled), or their last
counters are lost. A slice that ran with `PIPELINEGEN_RECORD=true` and left
no evidence of a recording for a correlator can't be told for it: its list
is kept as it was, it gets a mark, and it runs on every change until it is
recorded again (the plan names it among `plugins.warnings`). The run names,
per recorded suite, how many slices couldn't be recorded; when no slice of
a recorded suite in a run could be told, the suite's status is error, with
the evidence that was missing. Planning follows the same rule: a plugin that can't tell never narrows a
selection. What it couldn't describe is bound wide, and the plan says so
(see [plugins](plugins.md)).

From that, each correlator's `observe` says which units the slice touched,
by the IDs the sbom plugins give them. go-correlator records the Go
declarations holding at least one covered block, as go-sbom names them,
`go:<package directory>#<kind>:<name>` with the package directory relative
to the repository's root:

```
go:backend/internal/orders#func:Create
go:backend/internal/orders#method:Order.Total
go:backend/internal/orders#init:orders.go:1
```

(methods are named by the receiver's type without `*`; inits by file and
their order in it). A list is a text file, one ID per line, sorted,
without duplicates. A slice's `recording.txt` in a run's refs starts with
`# observed` when the correlators made it, then holds the slice's own IDs
and, after a `# setup` line, what only the batch's setup ran or what ran
only as loading; a recording a correlator couldn't tell holds a line
`# cannot tell: <plugin> (since <time>): <reason>` per correlator and no
IDs.

## (c) Durations and batches

Nothing about slices is checked in. Batches are dealt at run time by
`batch.Deal(slices, n)` (deterministic, balanced by duration) from the
suite's `durations` file, checked in, made by `pipelinegen durations` from
`go test -json` output or Playwright's JSON report: seconds per test file,
either one number or a map per parameter set (summed):

```json
{"version": 1, "files": {"e2e/console/search.spec.ts": {"phone-dark": 35.0, "desktop-light": 31.5}, "backend/pkg/api/orders_test.go": 7.9}}
```

Keys are relative to the repository's root; a file missing from it counts
as the median. `check` is one function, `Find(cfg, tree, staged)
[]Finding`, which the hook and planning both call, with the finding kinds
`uncovered-file`, `setup`, `bad-config`, `unreadable`, `duplicate-label`,
`declared-macro` and `pinned-by-version` (warnings) and, for tagged suites, `untagged-slice` and `unbound-slice`. `unbound-slice` is
also a test file whose `owner` names no target, and, once per suite bound
by the graph without an `owner`, the test files no target lists, no
`extra_deps` entry names and no `pipelinegen:data` line binds. A plan
that meets such a slice anyway (no target holds the file, or only one that
is no test rule) selects it on every change, with the reason. A file counts
as covered when it is a test file, reached by a test file's owners'
closure (the targets that list it, without an `owner`), its extra deps or a tagged target's closure, or matched by
`everything_on`, `sample_on` or `untested`; a call of another repository's
macro also covers what `macros` declares it reads in the calling package.
Planning holds each such call against Bazel's source-file dependencies of
the targets it made: a declared file they don't read fails the plan, and
a file of the package they read that nothing declares is a warning. Not in a release yet: counting
a test file's `pipelinegen:data` labels and its recorded dependencies as
covering.

## (d) Run state: temporary refs

pipelinegen keeps no state store and no state branch. What outlives a job
is refs: the lists, a run's live state, the locks (below) and the plugins'
answer cache ([plugins.md](plugins.md#caching-by-content)).

The lists of what slices touched hang off the branch's commits as
`refs/pipelinegen/lists/<full commit id>`, each a parentless commit whose
tree is `<suite>/<slice>.txt` in the format of (b), with
`<suite>/<slice>.cannot-tell` beside a marked slice's list. Planning reads lists(B)
for the base commit B (its ref, or the nearest of its first
`keep.lists.commits` first-parent ancestors') for both trees, plus the recordings of every run
whose refs still exist, where they add: units onto a slice's list and a
suite's setup list. A live run never makes a list for a slice that has
none, since a run may be a proposed change's and a slice without a list
is bound to everything. The lists ref's `_replaced.json` names, by commit,
the slices (and setup lists) whose list a run of everything replaced with
the whole recording it made of that commit; a live run of that commit or
of one of its ancestors adds nothing to such a list and leaves no mark on
it, and a ref without the file says nothing was replaced. When none of those commits has lists (the sweep
removed them, or none were made yet), every recorded slice runs as
without a list, the live runs' recordings narrow nothing, and the plan
says so once under `warnings`; it never selects less for lists that are
gone. A
slice with no list, or with a cannot-tell mark, is bound to everything (and
the plan warns of each mark); a list that exists, even an empty one, binds
exactly its units. Everything means every unit the plugins list, not
every file: a file no plugin claims reaches such a slice only through its
Bazel data and `extra_deps`.
The lists ref also names the runs whose recordings went into it, one file
per run, `_attached/<run>/<change>/<commit>.json` (each part
percent-encoded), holding `{"run", "change", "commit"}`; it ties a pull
request's plan to the commit it landed as. A file `pipelinegen seed` or
`pipelinegen baseline` wrote (run `seed` or `baseline`) also holds
`"seed": {"from", "tool", "plugins": [{"name", "version", "pin"}], "at"}`,
with `"baseline": true` for a baseline set by hand, whose change is the run
it is the base of. Lists written before kept these
files in `refs/pipelinegen/attached/<full commit id>`, which is read the
same until the sweep prunes it with the lists of the same commit.

A run's live state is temporary refs in the code repository. Every push
to the repository is told of every ref it has, so a run keeps few: each
batch writes its slices into a ref of its own while the run goes, and a
suite's end moves them into the run's one ref:

```
refs/pipelinegen/run/<run>/<change>/<c12>                      the plan, the suites' end marks, and the slices once their suite ended
refs/pipelinegen/batch/<run>/<change>/<c12>/<suite>/<batch>    a live batch's slices
refs/pipelinegen/also-to/<run>/<change>                        more pull requests for the comment (also_to.json)
```

Readers see each record under its own name, whichever ref holds it:

```
refs/pipelinegen/plans/<run>/<change>/<c12>                    the run's plan (plan.json)
refs/pipelinegen/runs/<run>/<change>/<c12>/<suite>/<slice>/=   the slice's state.json and, in a recording run, recording.txt
refs/pipelinegen/runs/<run>/<change>/<c12>/_end/<suite>        the suite's end marker (end.json)
```

`<c12>` is the commit's first 12 hex characters. In a run's or a batch's
ref each record is a directory (`_plan/`, `<suite>/<slice>/=/`,
`_end/<suite>/`) with beside it an empty marker `<dir>:<unix>.<nonce>`
saying when it was written; where a batch's ref and the run's both hold
a record, the newer counts, so a slice run again after its suite ended
shows its last state. A slice's final state and its recording land in one
write, by that slice's batch only, so batches never contend. A suite's
end compacts in one push guarded by what it read: a batch writing
meanwhile makes it read again, and a batch writing after it starts its
ref anew, which readers read and the next compaction or the sweep takes.
Nothing is downloaded for these writes but trees. A run planned before
this layout, whose plan is a ref of its own, keeps one ref per record
until the sweep removes it. Suite names start with a letter, so `_end`,
`_began` and `_plan` never clash. A suite build's start step writes
`_began/<suite>` (`began.json`: the suite, when the build started, the
job's link) as soon as its gate opens, over the one before, so a suite
counts as begun from its build's start, not from its first batch's end. A slice's state:

```json
{
  "name": "e2e/checkout.spec.ts",
  "state": "failed",
  "url": "https://concourse.example/teams/t/pipelines/p/jobs/pr-e2e",
  "updated": "2026-11-13T09:12:44Z",
  "failures": [{"test": "checkout pays with a card", "tail": "..."}],
  "seconds": 41.3
}
```

`state` is one of `pending`, `running`, `passed`, `failed`, `errored`; a
slice without a ref is pending, and one still without a final state when
its suite's end marker exists had no result. The plan is written once,
create-only, by the planning job before the suite jobs start (it is too big to
travel in the pipeline: tens of KB, needed by every report step). Report
renders from the plan and the refs listed by prefix, never from anything
else.

Lifetimes: nothing is deleted when a run ends. A run's records (plan,
states, recordings) stay readable while `keep` keeps the run, so its
report and `why` can be reproduced later. The sweep (`report.Sweep`, run
by every planning job) removes what falls out of `keep`, a run with its
run ref and any batch refs a suite that never ended left behind:

```yaml
keep:
  runs: {for: 5d}          # or {last: 20}; with both, kept while either holds
  lists: {for: 90d, commits: 200}   # or {last: <lists>}
runs:
  nightly: {keep: {last: 30}, ...}   # a run may override keep.runs
```

Periods take `s`, `m`, `h`, `d` or `w`. Defaults: runs 7 days, lists 90 days, 200
commits. The lists of the default branch's last `commits` first parents are
kept whatever their age, and those of its older commits are removed
whatever their age; other branches' lists keep to `for` and `last`. When
the planning job's checkout is shallow, or holds no ref of the default
branch, only `for` and `last` apply. The newest lists of each branch are never
pruned, whatever the setting: the newest commit its runs attached lists to,
and the lists a plan read. A plan whose base's lists are gone runs every
recorded slice as without a list (above).

What the repository and its token need: the token writes the hidden refs
with contents read and write (the rest of what it needs is in
[secrets](how-to/secrets.md)), and nothing may refuse pushes to
`refs/pipelinegen/*`: a pre-receive hook or a push rule that only lets
branches and tags through would refuse every record. Every write names
the value it expects (`--force-with-lease` on its own refs under
`refs/pipelinegen/`); pipelinegen never force-pushes a branch or a tag.
Clones and fetches never see these refs, since they take branches and
tags only. Every push to the repository is told of them all, which is
why a run keeps one ref. A simulated repository of about a thousand
recorded slices, with ten merges and twenty pull request runs a day, held
291 refs after seven days (217 runs, 70 lists, 4 answer refs), 26 KB told
to each push, where one ref per slice had held 19,723 refs and 2.4 MB. The
simulation was meant to run thirty days but stopped when its machine ran
out of disk; past the seventh day the count follows from the keep rules:
the runs stay at a week's worth, the lists grow to the default branch's
last 200 commits, and each plugin keeps two weeks of answers, so a few
hundred refs and some 40 KB a push. Backups and mirrors that should keep
the lists copy `refs/pipelinegen/*` too.

The plan JSON is `affected`'s plan: `{"base", "head", "all", "reason",
"suites": [{"suite", "all", "reason", "selected": [{"name", "label",
"sources", "tests"}], "reasons", "untouched", "removed", "whole_suite",
"narrowed", "observed_by", "unlisted"}], "narrowing", "plugins", "lists",
"warnings", "rebuild", "unobservable", "label_groups",
"label_hash_seconds"}`. `label_groups`, left out when 0, is how many times
bazel-diff hashed the head again, once for each group of recorded slices
taking the same changed files for no reason of a label's (files that
changed nothing observable, files whose anchor their lists hold), with the
base's copies of those files in place: a label it still found impacted
stayed. `label_hash_seconds` is how long each of those hashings took; the
plan's log line says both ("label groups 1, hashed again in 41.3 s").
`unobservable`, left out when empty, is `[{"path", "plugin", "units"}]`:
the changed files in which nothing a test can observe changed, by the word
of the sbom named (its units cover the file whole and are all as they
were), while `unobservable-edits` is on; the slices they alone would have
selected are under `narrowed`. `observed_by` names, for a recorded suite,
the correlators that observe its slices (its `correlators`, or every
correlator). `unlisted`, left out when empty, names a recorded suite's
slices that had no list yet, each bound to every unit. `narrowing` is the
level the deps repository was written at: the highest level of any suite,
since all share one repository. When code that only a suite's setup ran
changed, the suite runs whole, or a sample of it while the sample is on.
`narrowed`, left out when empty, is `[{"kind": "sampled" | "narrowed",
"narrowing", "off_at", "reason", "reached", "dropped": [slice names]}]`: a
sample (kind `sampled`, by `sample`, `sample-bound` or `setup-split`) or a
narrowing that left out slices the wide rule would have selected (a changed
plugin entry's sample is named this way: kind `narrowed` under
`plugin-entry`, with the slices the entry reached that the sample left
out); `reached` counts the slices selected plus those it left out. A slice
that the wide rules of several narrowings would select is named under each
of them, so it runs from the lowest `off_at` it is named under; it is never
named twice under one narrowing, nor under a narrowing when a sample left
it out. Planning names a slice under a narrowing when its plan with that
narrowing alone switched off selects it, so for each level N and each
higher level L, a slice the plan at L selects and the plan at N doesn't is
named under a narrowing off above N and up to L (level 5 also follows
dependents, which no narrowing records). A slice that only two or more
narrowings switched off together select is named under none of them, so a
failure of it in the nightly traces to no narrowing. A sampled slice the
change didn't select on its own has no `reasons`. `plugins` counts, per
plugin and call, the files asked about and answered from the cache, with
plan-level warnings; `warnings` holds planning's own, such as a cached
snapshot it hashed again. `rebuild` is there only in a run that rebuilds
the graph cold (one that selects everything and records, never a pull
request's run): `{"check", "differences": [{"plugin", "call", "inputs",
"input_units", "what"}], "error"}`. `check` is the config's
`rebuild_check`. Each difference is an answer a plugin gave anew that
differs from what the cache serves for the same file or set, with one line
per thing that differs; `error` says why the rebuild couldn't finish. Under
`error`, any of them makes the run's status a failure. `lists` says where
the lists both trees were bound with came from: the commit asked about, the
lists ref read and how many first parents back it lies, and the live runs
whose recordings were unioned in; `gone` is set when no lists were found.
When files no slice covers make a run select everything, `reason` names
them. When the change edits the config, the plan is the union of what the
base's and the change's configs select on the change's trees; a slice only
the base's selects has a reason of kind `base-config`. The plan's `config`
lists each part of the config the change edits, by its key path, with what
it means for the tests (`nothing`, `selection`, `slices`, `plugin-entry` or
`file`), and `config_understood` is set when no part is `file`: the config
then counted as no changed file of its own. A run's plan holds the suites
its entry names (`runs.<name>.suites`) in the tested commit's config, with,
for a proposed change, the ones the trusted keys name for the run
(`run_suites` in `PIPELINEGEN_TRUSTED`), and no others, each with its
untouched slices, even when nothing of it is selected. The steps that read
a kept plan (the reports, the run's end, `status`, `why --run`) keep the
same suites, by the config of the tree they are given, so a kept plan that
holds every suite of the config counts only the run's own suites and its
run still ends. A plan made for no run (`affected`, `why` planning again,
`try`) holds every suite. Every slice has its `name` (a test file's path,
or a tagged target's name); `label` is its id:
`@pipelinegen_deps//slices:<file>` for a test file, the target's label for
a tagged slice; `reasons`, `untouched` and `removed` are keyed by label. In
ref names, each path part of a slice's name has the bytes a ref can't hold
percent-encoded.

No slice may be named `whole.<anything>` (a Bazel package named `whole` at
the root): those names belong to whole-suite checks, and planning refuses
them.

## Keeping the lists current

`pipelinegen lists attach --run <run> --change <change> --commit <tested> --to <branch commit>`
merges the run's recordings (`report.Recordings`) into the lists of the
branch commit. When the commit has no lists ref yet, it starts from
lists(nearest ancestor) and creates the ref; when it has one, it reads it
and writes with the value it read (compare-and-swap, read again and retried
a few times when another attach moved it). Lists only grow, except that a
run that selected every slice replaces the list of each slice that passed
with its new recording. Only a trusted run (one on the default branch,
not a proposed change's) may do that, clear a slice's cannot-tell mark,
or make a list for a slice that has none; a proposed change's
recordings, attached by the merge, only add units to the lists that
exist. Each slice is judged by its own state in the run, not by the
run's: a slice that failed, errored or never ran keeps its list or its
mark, and what it recorded part way only adds to a list it has. Only the
marks of the slices that passed go in; a slice marked again keeps the time
it was first marked and takes the newest reason. The same write adds the
run's file under the lists ref's `_attached/`; the same run attached again
writes nothing and exits 0, unless a trusted run brings what the lists
don't hold yet: the slices that passed since it was first attached (a
rerun of a suite's build) then replace their lists in a run of everything
and add to them in any other, and the marks of its slices that passed go
in again. Nothing is ever
deleted. A recording's setup part (after its `# setup` line: what only the
batch's setup ran, or what ran only as loading) goes into the suite's setup list,
`<suite>/_setup`, and each slice's list keeps only its own part; the setup
list may shrink only when every slice the run selected of the suite
passed in a run of everything. Who calls it:

- a branch's recording run (any trigger but `pull_requests`), for the
  commit it tested: the reporter attaches in-process as soon as a suite's
  end (`report suite`, or `report finish` after it) finds the whole run
  passed. Every suite job that sees it may try; the second writes nothing.
  When the attach fails, the run's own status is `error` ("passed, but its
  lists weren't attached: ...") and the step exits 1, so the run never
  shows green without its lists. A run that failed attaches too, once
  every suite of its plan has ended and a slice of it passed, from the
  suite's end or from `pipelinegen end`; an attach that fails there is a
  warning, the run being red already;
- the repository's merge automation, right after merging a pull request,
  with the new commit (several pull requests merged as one batch: once,
  onto the last merge commit);
- a person, once, with `pipelinegen seed`: the local recordings of a
  default branch commit, attached as a trusted run's, noted in the lists'
  `_attached/seed/` as seeded (from which commit, with which pipelinegen
  and plugin builds). The note also makes the commit the base of the
  branch's runs that compare with the last passed commit until one
  passes; `pipelinegen baseline` writes such a note without recordings
  for a repository with no host, and posts a baseline status with one;
- nothing else: something that reached the branch without the pipeline gets
  its lists from the branch's own run.

`pipelinegen lists show [--commit X] [--suite S] [--slice S]` prints lists,
and `pipelinegen why --run R --change C --commit X --suite S --slice N`
(a kept plan; `--commit` and `--suite` may be left out), or `pipelinegen
why --base B --head H --slice N` (planning again) says why a slice did
or didn't run. The sweep
prunes by `keep` (above). Backups and mirrors must include
`refs/pipelinegen/*`; losing them is safe (runs select everything until they
record again).

## Locks between runs

Concourse's serial groups hold only inside one pipeline, so a suite that
needs a machine shared with another repository's pipeline takes a lease
instead; inside one repository, `run_on.serial_group` is enough.

```yaml
suites:
  e2e:
    minutes: 120
    lock: {name: browsers, repository: example/shop, slots: 1}
```

`name` may be shared by several suites, and by several repositories that
name the same `repository` (the lock store, one they already write to; it
defaults to the code repository). A lease expires 15 minutes after the
suite's time limit (`minutes`, see [time limits](#time-limits)). `slots` is how many holders the lock
admits at once (default 1; a worker that carries three heavy jobs at a time
has 3).

A slot is the ref `refs/pipelinegen/locks/<name>/<n>` (`n` from 1 to
`slots`) in the lock store, pointing at a parentless commit holding
`lease.json`:

```json
{"holder": "owner/repo pr pr-123 0123456789ab e2e", "job_url": "https://...", "acquired": "2026-11-13T09:00:00Z", "expires": "2026-11-13T11:15:00Z"}
```

- `acquire`: create a free slot's ref with a create-only push (it fails
  when the ref exists); when every slot is held, take over an expired one
  with a push that names its old value (force with lease), and otherwise
  wait and try again. A push that loses a race simply fails; nothing is
  ever rebased or retried blindly.
- `release` (in an `ensure` of the suite job): delete the slot's ref with
  a push that names its value, so only the holder releases it.
- The holder is `<owner/repo> <run> <change> <commit> <suite>`, plus
  the batch when one is named. Acquiring is never re-entrant, and gives up
  after 4 hours unless `--wait` says otherwise.
- An expired lease is taken by the next waiter, so a holder lost with its
  worker frees itself at its expiry.
- A waiter that takes several slots at once (`weight`) leaves a mark,
  `refs/pipelinegen/locks/<name>/wait-<id>` (lease.json with the waiter and
  an expiry two minutes on, renewed while it waits and removed in the
  write that gives it the slots); a lighter taker takes no slot while a
  heavier waiter's mark is live. `pipelinegen unlock <name> [<n>]` deletes
  a lease by hand.

## Time limits

Every step of the generated pipeline has a time limit, its own or one on
a step around it; a step in a hook (`ensure`, `on_failure` and the rest)
has its own, because Concourse runs a hook after a limit ended the step
it hangs on, outside that limit. The spec gives the runner the minutes:

- a suite's own tasks: `minutes` of the suite in the run (`suites.<s>.minutes`,
  `runs.<r>.suites.<s>.minutes`, 120 by default), on each task alone: the
  prepare, each batch, each whole-suite check. A suite whose lock is taken
  per build has it once, around all its work after the lock is taken;
- the planning job's and the sweep's tasks: `limits.plan` (120), plus a
  continuous run's `wait_minutes` for its planning task, whose decision
  waits for the round in progress to end before the prepare; the prepare
  and the plan after it have `limits.plan` from the decision's end;
- pipelinegen's own steps in a suite job, and the pipeline job:
  `limits.steps` (20), plus the lock's wait (4 hours) for a step that
  takes a lock. A lock's wait for a slot never counts against the limit
  of the batch it guards.

Each of pipelinegen's own steps gets `PIPELINEGEN_LIMIT=<key>=<minutes>`
(such as `limits.steps=20`); a task whose script runs something first
(the planner's prepare command) also exports `PIPELINEGEN_DEADLINE`, the
limit's end in Unix seconds, at the script's start. The step then ends two
minutes before the limit, says which limit it reached and the key that
raises it, and frees what it holds: a lock's
waiting mark.

A stretch the limit ends leaves its slices without a result, and the step
after it says why, as exactly as it can tell:

- `batches` stamps `batches/started.json` (`{"at": ...}`) as the suite's
  work begins; `report suite --batches batches` reads it;
- a lock taken per batch stamps the batch's start into its own output
  (`lock acquire --started begun`), and the batch's end reads it
  (`report batch-end --started begun`): with it, or with the lock taken per
  build, the end knows whether the limit stopped the batch;
- without either, the end knows only when the suite's work began: a batch
  that stopped sooner after it than its limit died on its own, one that
  stopped later may have reached its limit, and the message says so.

Each open slice is errored with that reason, and the suite's status is
an error; nothing stays pending.

## Commands the generated pipeline calls

Every step runs in the build's directory, with `tools` and the inputs its
command reads, and the environment "Params and secrets" in (a) gives
pipelinegen's own steps: `PIPELINEGEN_RUN`, `PIPELINEGEN_SUITE` (in a suite
job), `PIPELINEGEN_PIPELINE_STEP=1`, `PIPELINEGEN_JOB_URL`,
`PIPELINEGEN_TRIGGER` where the trigger is an input, and `GITHUB_TOKEN`.
The change and its commit come from the trigger's input (`--trigger <dir>`).

| Where | Command | Inputs | Outputs | Environment |
|---|---|---|---|---|
| planning job | `tools/pipelinegen plan --run <run> --trigger trigger --root trigger --cache plan-cache`: plans the change (for a pull request, its head merged into the target's tip) and writes the plan ref (create-only; none for a head the run doesn't take), the pending or "nothing selected" statuses, the comment; a continuous run's planning job that runs its builds one at a time (the spec's `serial_plan`) gives it `PIPELINEGEN_SERIAL_PLAN=1`, and without it the step records its round's start and planning; with `--gate handoff` (a continuous run's job) it writes `handoff/gate.json`, open only when it wrote the plan, and `handoff/version`, which the job puts as the run's planned hand-off for its suite jobs; a pull request run plans in two steps, `--out plan` then `--from plan --gate handoff`, and the second writes `handoff/gate.json` the same way, open only when it wrote the plan or found the head planned with none of its suites begun after `missed_after` (the sweep's rule), so the job puts the head's hand-off, and its suite jobs start, only then; a second step that leaves the head without a plan removes the record of `plan --begin`, so the sweep takes the head up after `missed_after` | `tools`, `trigger`, `plan-cache` | `plan-cache` | `GITHUB_TOKEN`, `PIPELINEGEN_BAZELISK` |
| pull request run's planning job, first | `tools/pipelinegen plan --begin --run <run> --trigger trigger --root trigger --cache write-cache`: in a step that runs none of the change's code and may write, records that the build's planning of its head began (the round's `started` record, over an older one), so the run's sweep leaves the head to the build until `limits.plan` and ten minutes have passed, or until the build's second step ends without a plan; nothing for a head planned already; a store it can't write costs a warning, and the step exits 0 | `tools`, `trigger`, `write-cache` | `write-cache` | `GITHUB_TOKEN`, `PIPELINEGEN_BAZELISK` |
| continuous run's planning job, before the prepare | `tools/pipelinegen plan --decide --run <run> --trigger trigger --root trigger --cache plan-cache --gate handoff`: decides whether the build plans, waiting while a round of the run is in progress, and plans nothing; writes the closed `handoff/gate.json` for a build that won't plan (the task then ends, without the prepare) and `handoff/decided.json` for one that will, which the plan step after the prepare reads | `tools`, `trigger`, `plan-cache` | `plan-cache`, `handoff` | `GITHUB_TOKEN`, `PIPELINEGEN_BAZELISK` |
| planning job's prepare | `tools/pipelinegen git-auth <ENV_NAME>`: the lines that have git read a declared token (see (a)) | `tools` | none | none |
| suite job, first | `tools/pipelinegen tree --run <run> --trigger trigger --out source`: the tree under test, from the plan (a git work tree whose HEAD is the tested commit); `gate/gate.json` is `["1"]` with a plan for this suite and `[]` without; with the gate open it first writes the suite build's start record (`_began/<suite>` among the run's records), for which it needs the store's token, though a refused write only warns; every list a step writes for `load_var` holds strings, because Concourse puts a loaded value into task `args` and `params`, which take strings only | `tools`, `trigger` | `source`, `gate` | `GITHUB_TOKEN` |
| suite job, before the batches | `tools/pipelinegen batches --run <run> --suite <s> --trigger trigger --out batches --root source`: deals the plan's selected slices of the suite, leaving out those already passed for this change (a rerun) | `tools`, `trigger`, `source` | `batches` | `GITHUB_TOKEN` |
| the batch task, after its setup | `tools/pipelinegen slice setup-end` | see (a) | see (a) | see (a) |
| the batch task, per slice | `tools/pipelinegen slice start <name>`, `tools/pipelinegen slice end <name>` | see (a) | see (a) | see (a) |
| a suite with steps: the prepare task | `tools/pipelinegen slice prepare`: runs the suite's prepare steps | see (a) | see (a) | see (a) |
| a suite with steps: the batch task | `tools/pipelinegen slice run`: the whole batch task, its drivers included | see (a) | see (a) | see (a) |
| each batch's ensure | `tools/pipelinegen report batch-end --batch <n> --result result --root source`: reads `batches/<n>/slices.json`, `batches/started.json` and the batch's journal in `result`; with a lock taken per batch, `--started begun` adds that input | `tools`, `trigger`, `source`, `batches`, `result`; recording: `coverage` | none | `GITHUB_TOKEN`, `PIPELINEGEN_TRIGGER` |
| before each whole-suite check | `tools/pipelinegen report collect --suite <s> --out slices --root source`: every slice of the suite in this run, for the check to read | `tools`, `trigger`, `source` | `slices` | `GITHUB_TOKEN`, `PIPELINEGEN_TRIGGER` |
| each whole-suite check's ensure | `tools/pipelinegen report whole --name <name> --result result-whole.<name> --root source` | `tools`, `trigger`, `source`, `result-whole.<name>` | none | `GITHUB_TOKEN`, `PIPELINEGEN_TRIGGER` |
| suite job's ensure | `tools/pipelinegen report suite --root trigger --batches batches`: the suite's status and the run's aggregate; when the whole run has passed, the final report and, for a branch's recording run, `lists attach`; when the run's last suite has ended in a run that failed, `lists attach` of the slices that passed. Its root is the trigger, because the ensure runs even when the tree step never wrote `source`. The job may pass whatever else this step's failures are, but not exit 4, the suite's end that could not be recorded: that fails the job | `tools`, `trigger`, `batches` (when the start step wrote it) | none | `GITHUB_TOKEN`, `PIPELINEGEN_TRIGGER` |
| suite job, around the work | `tools/pipelinegen lock acquire <name> --root source` and, in the ensure, `tools/pipelinegen lock release <name> --root source`; the lease's minutes are the suite's time limit in the run; a lock taken per batch passes `--started begun` to stamp, in that output, when its batch began | `tools`, `trigger`, `source` | none | `GITHUB_TOKEN`, `PIPELINEGEN_TRIGGER` |
| pipeline job | `tools/pipelinegen pipeline --root repo --plugins tools/plugins --out pipeline.yml`, then a check that fails when it differs from the pipeline set (with `concourse.self_set`, `set_pipeline: self` without variables) | `tools`, `repo` | none | `PIPELINEGEN_CONCOURSE_URL` |

A suite with an `image` gets one more step first: a `get` of that
`registry-image` resource of the root pipeline file. The suite's prepare,
batch and whole-suite tasks then run in it (`image:` on their task
steps), so a private image needs no credentials in a task file. With
`concourse.planner.prepare_image` the planning job does the same for the
prepare command. pipelinegen's own steps never run in such an image.

A suite whose image is the planner's (the same repository, pinned to the
planner's digest or naming its tag) is fetched once: the job's `planner`
get takes it from the suite's resource.

The planning step's cache holds the work tree (`tree/`, checked out afresh
in every build, since its git link points into the trigger's checkout),
bazel-diff's snapshots (`snapshots/`), the prepare's and the plugins'
shared caches (`caches/`), each plugin's own cache (`plugins/<name>/`), the
local level of the plugins' answers (`answers/`), the store's scratch
repository (`store/`), the bazel-diff download (`bazel-diff/`) and, in a
pipeline's step, Bazel's output root, output base and bazelisk's
downloads (`bazel/`). When `plan` or `unplanned` ends it drops the Go
modules' extracted sources whose zips `caches/gomodcache/cache/download`
keeps and Bazel's extracted repositories
(`bazel/root/cache/repos/<version>/contents`, when the
`content_addressable` archives beside them stay), trims the cache to
`keep.cache` and says on stderr what it kept and dropped, by part
(`pipelinegen: cache: kept ...`); a cache another plan holds is left as
it is.

Inputs and Outputs are the step's mounts; a cache counts as both.
Environment is what the command needs beyond `PIPELINEGEN_RUN` and
`PIPELINEGEN_SUITE`: the token where it writes refs or statuses,
`PIPELINEGEN_TRIGGER` where it learns its run from the trigger,
`PIPELINEGEN_BAZELISK` where it runs Bazel, and Concourse's address for
writing the pipeline. `pipelinegen plugin test` checks a runner's pipeline
against the same description, and `internal/docscheck` keeps this table
equal to it.

From the repository's own jobs or by hand, in a checkout of the repository
(the config at `./pipelinegen.yaml`, `GITHUB_TOKEN` set):

- `pipelinegen also-to --run <run> --change pr-<n> <numbers...>`
- `pipelinegen lists attach --run <run> --change <change> --commit <tested sha> --to <merge commit sha>`:
  `--commit` is the commit the run was keyed by (a pull request's head, the
  trigger's version), `--to` the commit on the branch that gets the lists
  (the merge commit)
- `pipelinegen lists show [--commit <sha>] [--suite <suite>] [--slice <name>]`
- `pipelinegen why --run <run> --change <change> --commit <sha> --suite <suite> --slice <name>`
  (`--commit` left out: the change's newest kept plan; `--suite` left out: every suite with the slice),
  or `pipelinegen why --base <sha> --head <sha> --slice <name>` to plan again

`slice end` has every configured correlator `collect` the slice's part and
`observe` it (see [plugins](plugins.md)), and writes the slice's final
state and recording in one update. A failed `collect` or `observe` (not a
"can't tell") makes the slice errored, so nothing turns green on a broken
recording. A slice runs in parts as a browser batch runs one project after
another: part 1 is recorded in `coverage/<slice>`, part k ≥ 2 in
`coverage/.pipelinegen/part-<k>/<slice>`.

## The hand-off file

Not in a release yet. A pipeline hands a change off this way only with a
runner that builds the chain of a connector's runs (its describe has the
capability `connector_changes`); `pipelinegen pipeline` refuses such a run
with any other.

A change a [connector](plugins.md#connectors) listed reaches the planning
job and the suite jobs as a hand-off: a directory with `run.json` at its
root, written by the sweep for each head it plans or hands off again, and
by the runner's hand-off resource type into its input directory. Its
fields are those of the run the build is about:

| Field | What it holds |
|---|---|
| `run` | the run's name in the config |
| `change` | the change's id, carrying the connector: `git/branch-review%2Fcart` |
| `commit` | the head commit the run is keyed by |
| `pr` | the pull request's number, `0` for a change that is none |
| `branch` | the head branch |
| `base` | the branch the change goes into |
| `connector` | the connector that listed the change, as the trusted config names it |
| `remote` | the code's remote the change is fetched from, as the trusted config names it |
| `handed` | when the sweep handed the change on, with a random part |

`run`, `change` and `commit` are required; `connector` and `remote` too
for a step that fetches the change. Nothing reads `handed`. It exists only
so that the same head handed on again is a new version of the CI
system's hand-off resource, which would fold an identical one into the
old and never start it again. Two hand-offs of one head name the same
change, so the planning step plans it once: the second finds the plan
the first left, shuts its gate, and hands nothing on to the suites; the
sweep's mark decides when a head goes again. They come from the sweep, which reads
the repository's own config, so a step fetches the change from where the
repository says, never from where the change's own config says. A step fails on a `run.json`
written for another run.

A step that reads its config and tree from the trigger (`plan`, `tree`,
the suite's ensure) makes a checkout of the change first: in a directory
of its own under `TMPDIR`, named by nothing in the hand-off and removed
when the step ends, fails or is stopped, it fetches `refs/heads/<branch>`
from `remote`, with the read token in git's environment for that fetch
alone and nothing written into the checkout's config, and requires the
branch's tip to be `commit` exactly. A branch name git wouldn't take, or
one that reads as an option, is refused before git sees it. Over ssh the
fetch uses the read key (`PIPELINEGEN_SSH_KEY_READ`) and the host keys of
the config the pipeline was made from (`git.known_hosts` in
`PIPELINEGEN_TRUSTED`), checked strictly. The step refuses a server whose
key isn't listed, or changed, and says to add the server's key to
`git.known_hosts`; no host is trusted on first use. Without the trusted
keys, a host key or the read key it refuses before ssh runs, and never
uses the machine's own ssh setup. When the
branch moved on since the sweep wrote the hand-off, the change is
superseded: `plan` says "superseded, not run" and writes nothing, `tree`
shuts the gate, and the ensure ends without an error. The newer head is
the sweep's to plan.

What a trigger is comes from what it holds, never from the config, which
may be the proposed change's own: a checkout with a pull request
resource's files (`.git/resource`) is a pull request's, whatever else it
holds; a directory with a `run.json` and no checkout is a hand-off; any
other checkout is a branch's. When the config's run takes something else
(a branch run handed a pull request, a connector's run handed a pull
request's checkout), the step fails with "a change may not move where its
run reads or writes". So a proposed change can't make its own run a
trusted one by editing its trigger, and a `run.json` a repository keeps at
its root is never read as a hand-off.

## Commit statuses

Every suite a run lists posts its status in every run, and no other suite
does, context
`<status_prefix>/<run>/<suite>` (`github.status_prefix`, default
`pipelinegen`), built by `report.Context(cfg, run, suite)`; a suite with
nothing selected is success, "nothing selected". One more status per run,
`<status_prefix>/<run>`, sums the run's suites (pending while any is,
failure or error when any is, success when all are), so a branch rule names
one check. A run with `compare: last_passed` reads the run's own status to
find the newest earlier commit whose run passed, or, with none, the newest
whose run is still pending; the plan's `warnings` say so.

## More pull requests for one comment

A repository that tests several pull requests together (a batch on its own
branch) posts the same comment to them with
`pipelinegen also-to --run <run> --change pr-<n> <numbers...>`, which writes
the ref `refs/pipelinegen/also-to/<run>/pr-<n>` (`{"also_to": [8, 9]}`).
Report reads it at every render, so it may be written before or during the
run; `PIPELINEGEN_ALSO_TO` is the value at planning time.

## Why a slice runs

`plan.json` holds, per suite, `reasons`: per selected slice a list of

```json
{"kind": "executed", "what": "go:backend/internal/orders#func:Create", "through": []}
```

with `kind` one of `everything` (`what` is the reason, such as "nothing to
compare with"), `new-slice`, `test-added`, `test-removed`, `test-changed` (`what` is
the test file, or the test name in the by-name shape), `executed`,
`depends` (`through` is the path of declarations from the changed one to
one the slice touched), `opens` (`what` is the changed file, `through` the
app's label) `target` (`what` a changed Bazel target, `through` the
path from bazel-diff; where the target is a rule whose own files changed,
`what` is each of those files' labels and `through` starts at the rule), `input-added` and `input-removed` (`what` a Bazel
label the slice's inputs gained or lost between the commits),
`input-unknown` (`what` an input label bazel-diff doesn't hash and
nothing pins, so whether it changed can't be told, `through` saying why
and what would pin it; a label in another repository is judged by its pin
as the build resolves it: a module's version, registries, overrides and,
when committed, the lock's hashes; a repository rule's sha256, integrity
or commit: unchanged, or `target` with `through` "the pin of its
repository changed"),
`text-input` (`what` a changed file the suite's `text_inputs` say the slice
reads as text, which its sbom says changed nothing observable: the label
holding it still selects the slice) and
`plugin` (`what` a plugin whose entry in the config changed). An
`executed` or `depends` reason the walk found also carries `hops`: the
chain from the unit the slice's recording holds to the changed one, one
link a hop with its kind and the plugin whose answer held it, each unit
with its sbom's label and file; `through` stays. Planning fills them from bazel-diff's root causes
(`explain`) and the plugins' reasons for each binding. `removed` lists the slices
that the base had and the head no longer has. Each suite also holds `causes`: the reasons two or more of its selected
slices share, other than their own test files, each once with `kind`,
`what`, `through` and `slices`, how many selected slices it is a reason
of, the largest first. An `executed` or `depends` reason counts by `what`
alone, since its path back to each slice differs.

```json
{"kind": "target", "what": "//e2e:reporter.ts", "through": ["//e2e:runner"], "slices": 241}
```

With `report.reasoning: true` the comment shows up to three reasons per
slice and the count of the rest, under the list or the chart. It names a
suite's shared causes once, with how many slices each selects, also with
the reasons off when every slice of the suite runs, and leaves them out of
each slice's own reasons.

## Nothing outside a slice: `pipelinegen check`

Every tracked file is bound to a slice or declared as untested. The config
says which files are a suite's tests and which files no test covers:

```yaml
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts"]        # every one of these that holds a test is a slice
untested:
  - paths: ["docs/**", "**/*.md"]
    reason: prose, read by people only
  - paths: [".github/**"]
    reason: repository settings
```

`pipelinegen check` fails, naming the file and the fix, when:

- in a tagged suite, a slice target lacks its suite's tag, or depends on
  nothing but its own tests;
- the config is wrong (the deps repository's old setup lines are
  warnings, which fail nothing);
- a tracked file is an input of no slice at all and matches no `untested`
  entry (`everything_on` files count as covered: they select every slice);
- a target names one label twice in an attribute, under two spellings
  (`//docs` and `//docs:docs`), which Bazel refuses to load.

What `untested` promises: no test needs to run when a file it matches
changes. A changed file that an entry matches and no Bazel target owns
(a docs image, a workflow file, a file under a directory Bazel ignores)
selects nothing by itself, and the plan's warnings carry
`<file>: untested by declaration: <reason>`. A changed file no target owns
and no entry matches selects every slice, as before. A file a target owns
is selected through that target whether an entry matches it or not. If a
slice of the head demonstrably reads a file an entry matches (its
recorded list, or the units its `extra_deps` and `pipelinegen:data` lines
give it, hold one of the file's units), the entry is wrong: planning
treats it as an uncovered file (`uncovered: fail` fails the run; otherwise
it warns, naming the slices) and selects through those units. The commit
hook's check reads no recordings, so only planning sees this.

Packages under a directory Bazel ignores (`.bazelignore`, or
`ignore_directories` in `REPO.bazel`) hold no targets for the check, as for
Bazel, and a tagged suite's `exclude` leaves out the targets in matching
packages. A test file there is no slice of any suite, since no target can
run it, and the files there that nothing else covers make one
`uncovered-file` finding for the directory, whose fix is its `untested`
line.

It reads BUILD and `.bzl` files with a parser and the tracked files from git,
and starts no Bazel server, so it is fast enough for a commit hook. It
runs no plugin over the tree: it asks the configured sbom plugins only for
`describe`, running only a binary whose SHA-256 is its pin and keeping the
answer by that pin, and an uncovered file their `candidates` or `include`
match is a warning, judged by the plan. Planning counts a file an sbom
claims in the head as covered (its units carry a change), and a changed
file an sbom claims needs no Bazel target.
`--staged` checks only what the commit touches; without it, the whole tree.
`plan`, and `why` when it plans again, run the same check on the whole
tree first (one implementation, so the hook and planning never disagree);
`affected` doesn't. What planning does
with its findings is a setting, global and overridable per run:

```yaml
uncovered: fail            # or: everything (the default)
runs:
  nightly: {uncovered: everything, ...}
```

- `fail`: the run fails at planning, before any slice starts; its statuses
  and comment list what is uncovered and the fix.
- `everything`: the run selects every slice and reports the same list as a
  warning, so a repository can adopt pipelinegen step by step.

The commit hook fails on the findings whatever the setting.

The hook, in a repository's own pre-commit hook (or `.git/hooks/pre-commit`
when it has none):

```sh
pipelinegen check --staged || exit 1
```

## (e) Which package owns what

Each contract lives in the package that defines the format or the
behaviour, and that package's tests guard it.

| Contract | Package | Tests |
|---|---|---|
| The config, its keys, defaults and the history rule | `internal/config` | `internal/config`; `internal/docscheck` holds the config reference to `config.Keys` |
| Slices and how a change selects them | `internal/bazel`, `internal/graph` (the walk), `internal/affected` (the join) | `internal/graph`, `internal/affected`, `internal/cli`, `internal/example` |
| The plan, its reasons and the full-run reasons | `internal/affected` | `internal/affected`, `internal/example` |
| The plugin protocol | `plugin`, `internal/plugins`, `internal/plugincache` | `plugin`, `internal/plugins`, and the kit (`internal/kit`, `pipelinegen plugin test`) for every plugin |
| The runner's spec | `plugin` (`plugin.Spec`), `internal/spec` | `internal/spec`, `internal/kit` |
| (a) The batch task's commands and journal | `internal/cli`, `internal/report` | `internal/cli`, `internal/report` |
| (b) Recordings and marks | `internal/recording`, `internal/report` | `internal/recording`, `internal/report` |
| (c) Durations and batches | `internal/durations`, `internal/batch` | `internal/durations`, `internal/batch` |
| (d) Run state in refs, statuses, the comment, the sweep | `internal/report`, `internal/gitstore` | `internal/report`, `internal/gitstore` |
| The lists and their attach | `internal/lists`, `internal/attached` | `internal/lists`, `internal/attached` |
| Locks | `internal/lock` | `internal/lock` |
| The check | `internal/check`, `internal/buildfile` | `internal/check`, `internal/buildfile` |
| Commands and flags | `internal/cli` | `internal/cli`; `internal/docscheck` holds the command reference to `cli.Commands` |
