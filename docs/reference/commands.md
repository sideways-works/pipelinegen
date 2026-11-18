# Command reference

This page is for anyone running pipelinegen by hand or writing the steps
that call it: every command and flag, with its exit codes.

```
pipelinegen <command> [subcommand] [arguments] [flags]
```

Flags take one dash or two (`-root` and `--root` are the same). Every
command exits with:

| Code | Meaning |
|---|---|
| 0 | It did what it was asked. For `affected`, also when the answer is "every slice". |
| 1 | It failed, or what it reports did not pass: an unreadable config, a failed Bazel query, a head commit that doesn't exist, a failed push, a batch with a failed slice. The reason is on stderr. |
| 2 | The command line can't be read: an unknown command, subcommand or flag, a missing argument, a suite that isn't in the config, a required environment variable that isn't set. |
| 4 | `report batch-end` or `report suite` could not write the run's records after every retry: what ran is not lost, but the run doesn't know it, and a rerun of the build runs it again. A pipeline must let no step that exits 4 pass, whatever else of its failures it tolerates. |

Run without a command, pipelinegen lists its commands and exits with 2.

A command that runs Bazel leaves its server running when it ends, warm
for the next command of the same step. In a pipeline's step
(`PIPELINEGEN_PIPELINE_STEP` set, or `PIPELINEGEN_JOB_URL` from a runner
that sets no mark) Bazel starts with `--max_idle_secs=600`
unless `bazel.startup_flags` sets its own, so a server that a container
the CI system keeps after the step still holds stops within ten minutes.
On your machine the server keeps Bazel's own idle time.

Every command reads the config and takes two flags for it:

- `--root` (default `.`, for `slice` `source`): the repository's root.
- `--config` (default `<root>/pipelinegen.yaml`): the config file.

Without `--config`, a command reads the file `PIPELINEGEN_CONFIG` names,
when it is set. That is how `pipelinegen record` hands its suite's tasks
the config of a local recording; you rarely set it yourself. A step of a
generated pipeline never has it: a command refuses it in a pipeline's
step (`PIPELINEGEN_PIPELINE_STEP` or `PIPELINEGEN_JOB_URL` set) or when
`PIPELINEGEN_RUN` names any run but
`local`, so no task can swap the config a declared run's steps read.

With `PIPELINEGEN_CPU_PROFILE` set to a file's path, any command writes a
CPU profile of itself there, for `go tool pprof`: how a slow plan spends
its time, measured on the tree that is slow.

The commands for people are `slices`, `affected`, `run`, `record`, `seed`,
`baseline`, `lists`, `why` and `also-to`.
The others are what the generated pipeline's steps call; you run them by
hand only to try a task file or to repair a run.

## `pipelinegen slices`

Lists the slices of every suite, or of one, with their own source files.
For a suite of tests it lists the tracked files that match its patterns and
hold a test; for a tagged suite it asks Bazel for every target carrying the
tag.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--suite` | every suite | Only this suite. |
| `--json` | `false` | Prints JSON: a list of `{"suite", "slices": [{"name", "label", "tags", "sources", "tests"}]}`. |

On the example:

```
$ pipelinegen slices --suite unit
unit calc/add_test.sh
  calc/add_test.sh
unit calc/lib_test.sh
  calc/lib_test.sh
unit calc/mul_test.sh
  calc/mul_test.sh
unit text/upper_test.sh
  text/upper_test.sh
```

A test file shows by its path, with itself as its one source; its `label`
in the JSON is its id, `@pipelinegen_deps//slices:calc/add_test.sh`,
and a Go test file also has its `tests`. A tagged slice shows by its label,
and its sources are the target's own `srcs` that are files, as paths from
the workspace's root; a target it depends on (`:lib`) isn't listed.

## `pipelinegen affected`

Prints the slices a change reaches: the plan. A test file shows by its
path, a tagged slice by its label. It checks both commits out
into a work tree in its cache, has bazel-diff hash each (cached by commit),
and keeps the slices among the targets that differ. It reads git objects
only, never your working tree, so uncommitted changes don't count: the
config too is `--head`'s own `pipelinegen.yaml`, and it says so when your
working tree's differs. `--config` names another file to plan under. What
planning warns of (a slice a correlator couldn't tell, a cached snapshot
it had to hash again) goes to stderr as well as into the JSON. A plan that
runs longer than a minute says on stderr, once a minute, where it is, as
`plan` does (`pipelinegen affected: ...`).

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `--head`'s own `pipelinegen.yaml` | The config file to plan the head under, instead of the one the head commit holds. |
| `--base` | none | The commit to compare with. Without it every slice runs ("nothing to compare with"). |
| `--head` | `HEAD` | The commit to plan. |
| `--suite` | every suite | Prints only this suite's part. |
| `--all` | `false` | Selects every slice, as a nightly does. |
| `--json` | `false` | Prints the whole plan as JSON, with the reasons. |
| `--no-lists` | `false` | Binds recorded slices to everything instead of reading the base's lists from the repository's refs. Without read access to the refs, affected says so and does the same. |
| `--same-config` | `false` | Plans both trees under the config `--config` names (the work tree's by default) and reads neither commit's own: no union with the base's config, and no check of the head. It plans commits from before the config existed, or whose config this release can't read, as if the current config had been there all along. The plan says so in one warning. Only `affected` and `try` take it; a run's planning always reads each commit's own config. |
| `--local` | `false` | Reads the lists from the local store `pipelinegen record` writes, not from the remote. A config that names no remote (no `git.remote`, no `github` section) reads the local store without it whenever there is one, before the checkout's `origin`; with neither, it reads `origin`. Either way the first line on stderr says "lists from the local store (recorded on this machine)" and where that is. It can't go with `--no-lists`. |
| `--cache` | your user cache directory, then `pipelinegen/repos/<repository>` | Where the work tree, hashes and bazel-diff are kept: a directory per repository (worktrees of one share it), so two repositories never share a work tree. Two plans of one repository take turns: the cache's lock holds one plan at a time. |
| `--bazel-diff` | the pinned release | A bazel-diff binary to run instead of downloading one. |
| `--plugins` | `<root>/tools/plugins` | Where the configured sbom and correlator plugins are, one executable per plugin name. With such plugins configured, affected binds through them as the planning job does, keeping their answers in the cache only. A plugin whose binary is not the build its pin names (one you built yourself) is used all the same, and its answers are cached by its own SHA-256, so it is never served the pinned build's answers, nor they its. It is said once on stderr: `pipelinegen: plugin <name> is not the build its pin names (<version>, sha256 <pin>): its binary's sha256 is <digest>: it answers, and its answers are cached apart`. For a release pinned by `sums` the line also gives the checksum the list gives the file, or, with no `<name>.SHA256SUMS` beside the plugin, says it can't be checked against its pin. A plugin that isn't there is [fetched](#plugins-a-command-fetches) from its pinned release. |
| `--no-fill` | `false` | Doesn't fill the caches the plugins read before planning. By default, affected first runs what each plugin's `describe` says fills its cache (for the Go plugins, `go mod download` in every directory holding a `go.mod` that the plugins don't exclude), in both trees, once per content of those files, and says so on stderr the first time. This happens because no planner prepare ran on your machine. Without the fill, a plugin that lacks a module can't tell, and the plan binds those slices to everything. `plugins[].fill: false` turns it off for one plugin. See [filling caches on your machine](../plugins.md#filling-caches-on-your-machine). |

On the example, after a test was added to `calc/add_test.sh`:

```
$ pipelinegen affected --base HEAD~1 --no-lists
smoke: 0 selected, 2 untouched
unit: 1 selected, 3 untouched
  run calc/add_test.sh
```

When every slice runs, the first line says why:

```
$ pipelinegen affected --base HEAD~1 --no-lists --suite unit
every slice: ci/root.yml changed (everything_on ci/**)
unit: 4 selected, 0 untouched
  run calc/add_test.sh
  run calc/lib_test.sh
  run calc/mul_test.sh
  run text/upper_test.sh
```

The reasons for a full run are: no `--base`; `--all`; a base that doesn't
exist or isn't an ancestor of the head; a base whose config can't be read;
a changed `everything_on` file; a changed file no Bazel target owns; a base
that can't be hashed; a failed bazel-diff run. Unlike `plan`, `affected`
doesn't hold the plan to the check, so files no slice covers don't make it
select everything; it does print the check's warnings, such as a config
entry that matches no file. A head that can't be found or hashed is an error (exit 1):
without it there is nothing to run.

With `--json`, the plan is `{"base", "head", "all", "reason", "suites":
[...]}` and each suite's part is `{"suite", "all", "reason", "selected",
"reasons", "untouched", "removed", "whole_suite"}`; see
[concepts](../glossary.md).

## `pipelinegen try`

Plans each of the last first-parent commits against its parent under one
config for both trees (as `affected --same-config` does, without lists),
and prints one line per commit: the commit, per suite the slices selected
of how many, and the first reason, or `everything:` with the reason. A
last line sums them up: the commits planned, per suite the median and the
worst share of slices selected, and how many commits ran everything, with
the commonest reasons. It shows what pipelinegen would have run on a
repository's recent history before the repository adopts it. Snapshots are
kept by commit in the cache, so each commit's tree is hashed once.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config both trees of every commit are planned under. |
| `--last` | `10` | How many commits to plan, newest first. The oldest of them needs a parent. |
| `--head` | `HEAD` | The newest commit. |
| `--cache` | your user cache directory, then `pipelinegen/repos/<repository>` | Where the work tree, hashes and bazel-diff are kept: a directory per repository (worktrees of one share it), so two repositories never share a work tree. Two plans of one repository take turns: the cache's lock holds one plan at a time. |
| `--bazel-diff` | the pinned release | A bazel-diff binary to run instead of downloading one. |
| `--plugins` | `<root>/tools/plugins` | Where the configured sbom and correlator plugins are; a plugin that is not the build its pin names is used and said once, and one that isn't there is fetched, as for `affected`. |
| `--no-fill` | `false` | Doesn't fill the plugins' caches first, as for `affected`. |

```sh
$ pipelinegen try --last 2
7a2d41c09e5f  backend 1/1  e2e 2/2  specs 2/2  everything: stray.txt changed and no Bazel target owns it
1f0c9e2a7b3d  backend 1/1  e2e 1/2  specs 1/2  //backendtests:money_test: the target //api/money:money changed
total: 2 commits, backend median 100% worst 100%, e2e median 50% worst 100%, specs median 50% worst 100%, 1 ran everything: stray.txt changed and no Bazel target owns it (1)
```

A config written for today's tree often names targets that the
conversion's own BUILD changes add: a suite's owner, the tagged
filegroups of a suite. Older trees lack them, so their slices run on
every commit before, each saying it "depends on //x:y, which this
commit's tree lacks or bazel-diff doesn't hash". After the summary, one
line names those targets and how many commits lacked them. To see what
pipelinegen would have run on that history, replay it onto a branch that
has the BUILD changes and try that branch:

```sh
git switch -c try-replay <the commit that adds the BUILD changes>
git cherry-pick <the oldest commit to try>^..<the commit before the BUILD changes>
pipelinegen try --last <how many were picked>
```

## `pipelinegen report`

Keeps a run's state in refs of the repository and shows it on GitHub as
commit statuses and, for pull request runs that ask for one, a comment. The
generated pipeline's steps call it; its subcommands:

| Subcommand | What it does | Exits 1 when |
|---|---|---|
| `report start` | Sets every suite's status and the run's, and makes the comment. Selected slices show as pending. | a write fails |
| `report slice` | Reports one slice or whole-suite check from its own step: `--state running` at its start, its end from `--result` (a directory holding `result.json`) and, in a recording run, `--coverage`. | the slice did not pass |
| `report whole` | Reports a whole-suite check once its task has ended, from `--result` (a directory holding `result.json`; none makes the check errored). The check is shown as `whole.<name>`, beside the suite's slices. The pipeline runs it in the ensure of each check's task. | the check did not pass |
| `report batch-end` | Closes the slices of batch `--batch` still without a final state once its task ended, from `--result` (one `<name>.json` per slice) and `--coverage`, reading the batch from `--batches`. A batch none of whose slices started is `not run`, said once for the batch. When the slices' records can't be written it says how many of them passed by their results and exits 4. A batch with a slice that did not pass says why below its one line: each failure's name and tail as the statuses show it, slices that failed alike once with their count and names, capped, and where the full text is (each slice's `state.json` in the store). With `--confirmed` or `--requeued` it is a piperun session's end instead, needing no `--batches`: it writes the end the journal holds of each confirmed slice, closes a confirmed slice the journal holds no end of as errored (piperun ended it), leaves a requeued slice that isn't confirmed open for the session that takes it next, and touches no other slice, which is another session's now. A slice confirmed `stopped` (a person stopped the build) is errored, saying so, whatever the session wrote, and is never `not run`, so nothing hands it on or runs it again. A session with nothing confirmed exits 0. | a slice of the batch did not pass |
| `report flush` | A piperun session's sidecar: writes the ends of the slices listed in `--confirmed` (`$PIPERUN_CONFIRMED`, the items whose end piperun recorded) from the session's journal in `--result`, which it only reads, and closes nothing. With `--every <period>` it writes what was confirmed since, every period until it is stopped (SIGTERM), at the lowest priority; a slice whose end is unchanged is not written again, and a flush written twice writes the same states. A last line of the list without its newline is still being written and waits for the next round. A slice confirmed `stopped` is left to the session's end. In a recording run a slice waits for the session's end unless the session kept what its correlators observed. | a write fails (once; with `--every` a round that fails only warns) |
| `report suite` | Marks the suite's end (its slices that never reported become errored, each saying why when `--batches` tells it) and sets its status and the run's. A suite that did not pass says why, as `report batch-end` does, for all its slices. When that makes a branch's recording run pass as a whole, it attaches the run's recordings to the run's commit, as `lists attach` does; when it ends the last suite of such a run that failed, it attaches the recordings of the slices that passed, and an attach that fails there is a warning. When the end mark can't be written it sets the suite's status and the run's to error ("the suite's end could not be recorded: rerun the build"), says what it could not do (the run's lists among it when this was the run's last suite to end) and exits 4. | a slice of the suite did not pass, or the attach failed |
| `report finish` | Shows the run's final state. | a selected slice did not pass |
| `report collect` | Writes what every slice of the suite recorded in this run into a directory, for a whole-suite check to read: `<slice>/state.json`, `<slice>/recording.txt` and `<slice>/extra/`, the files the batch task left for the slice. A slice's failure files stay out. | a read or write fails |
| `report files` | Writes the failure files a kept run's failed slices left (a screenshot, Playwright's `error-context.md`, a trace) into `--out`, at `<suite>/<slice>/<file>`, and lists them, with the files a cap or a check dropped and why. A person runs it, not the pipeline: it reads the run named by `--run`, `--change` and `--commit` from the repository's remote (or `--local`), never from the environment, and writes nothing to the store. Without `--out` it only lists. | the run has no records left, or a read or write fails |

| Flag | Default | Subcommands | What it does |
|---|---|---|---|
| `--root` | `.` | all | The repository's root, the tree under test. |
| `--config` | `<root>/pipelinegen.yaml` | all | The config file. |
| `--state` | none | `slice` | `running`, to report the slice's start. |
| `--result` | `result` (`slice`, `whole`: none) | `slice`, `whole`, `batch-end`, `flush` | The result directory. |
| `--name` | none | `whole` | The whole-suite check, as the suite's `whole_suite` names it. Required. |
| `--coverage` | none | `slice`, `batch-end` | The coverage directory, in recording runs. |
| `--batch` | `$PIPELINEGEN_BATCH`, else `$PIPERUN_SESSION` | `batch-end` | The batch's index in the whole run, from 0; on piperun the session's number. |
| `--confirmed` | none | `batch-end`, `flush` | The items whose end piperun recorded (`$PIPERUN_CONFIRMED`): JSON lines `{"key", "outcome"}`, the outcome `passed`, `failed`, `errored` or `stopped`. A line counts only with its newline, so an unfinished last line is read on the next pass; fields it doesn't know are ignored. Required for `flush`. |
| `--requeued` | none | `batch-end` | The items the session handed back to the pool (`$PIPERUN_REQUEUED`): JSON lines `{"key", "reason"}`, a line each time, read alike. |
| `--pools` | `pools` | `batch-end` | With `--confirmed`, the pools' slices `plan --emit` wrote, for the label of a slice the journal never saw. |
| `--every` | `0s` | `flush` | Flush again every period until stopped; 0 flushes once. |
| `--batches` | `batches` (`suite`: none) | `batch-end`, `suite` | The directory of the run's batches (`<n>/slices.json`), with `started.json`, when the suite's work began: a slice left without a result then says whether the suite's time limit can have stopped it. |
| `--started` | none | `batch-end` | The directory an older pipeline's lock step stamped (`started.json`) as the batch began: its end then knows whether the batch's time limit stopped it. |
| `--tools` | `tools` | `batch-end`, `flush` | The batch task's tools. In a recording run, the correlators in `tools/plugins/<name>` say what each slice still in the journal touched. |
| `--suite` | `$PIPELINEGEN_SUITE` (`files`: none) | `collect`, `files` | The suite whose slices it collects; for `files`, only this suite's slices. |
| `--out` | `slices` (`files`: none) | `collect`, `files` | Where it writes them. `files` writes nothing without it. |
| `--run` | none | `files` | The run, as the config names it. Required. |
| `--change` | none | `files` | The change it ran, as its comment and `why` name it (`pr-12`, `main`). Required. |
| `--commit` | the change's newest plan | `files` | The commit it ran; 12 characters are enough. |
| `--slice` | none | `files` | Only this slice, by its name or its label. |
| `--local` | `false` | `files` | Reads the run from the local store `pipelinegen record` writes, not the remote. |
| `--json` | `false` | `files` | Prints each slice's kept and dropped files as JSON. |

`report files` asks the store for nothing but the slices' states and the
files it writes out:

```
$ pipelinegen report files --run pr --change pr-12 --slice e2e/checkout.spec.ts --out failure-files
e2e e2e/checkout.spec.ts (failed):
  checkout-pays-with-a-card-chromium/test-failed-1.png (screenshot of [chromium] checkout pays with a card), 41.2 KB
  checkout-pays-with-a-card-chromium/error-context.md (page at the failure of [chromium] checkout pays with a card), 1.3 KB
  dropped checkout-pays-with-a-card-chromium/trace.zip (trace of [chromium] checkout pays with a card): over keep.files.slice: 2 MB
wrote 2 files of pr pr-12 at 7a2d41c09e5f into failure-files
```

The other subcommands read the run from the environment: `PIPELINEGEN_RUN`, and
`PIPELINEGEN_CHANGE` and `PIPELINEGEN_COMMIT` or else `PIPELINEGEN_RUN_FILE`
(a run file such as `batches/run.json`, which holds both),
`PIPELINEGEN_PR` (0 for none), `PIPELINEGEN_ALSO_TO`, `PIPELINEGEN_SUITE`
(for `slice`, `whole`, `batch-end` and `suite`), `PIPELINEGEN_SLICE` and
`PIPELINEGEN_SLICE_LABEL` (for `slice`), `PIPELINEGEN_JOB_URL` and
`GITHUB_TOKEN`. `PIPELINEGEN_GIT_URL` and `PIPELINEGEN_GITHUB_API` point at
another GitHub. A step for a commit that was never planned passes without
posting anything.

```
PIPELINEGEN_RUN=pr PIPELINEGEN_CHANGE=pr-12 PIPELINEGEN_COMMIT=$(git rev-parse HEAD) \
PIPELINEGEN_PR=12 PIPELINEGEN_SUITE=unit PIPELINEGEN_BATCH=1 \
  pipelinegen report batch-end --root source
```

## `pipelinegen slice`

What a suite's batch task calls around each of its slices, in order:

```sh
tools/pipelinegen slice memory                    # first, before the setup: wait for room
tools/pipelinegen slice start calc/add_test.sh    # noted in the batch's journal
# ... run the slice's tests, write result/calc/add_test.sh.json ...
tools/pipelinegen slice end calc/add_test.sh      # its result, recording and final state
```

The steps report under the run, change and commit in `PIPELINEGEN_RUN`,
`PIPELINEGEN_CHANGE` and `PIPELINEGEN_COMMIT`, which the generated
pipeline sets from the plan; `plan` names the change from the run's
trigger. The first step of a job (`setup-end`, or the first `start`)
checks that the plan under those names exists and fails at once when it
doesn't, naming each and where it came from: a slice reported under a
change no plan was made for would lose its recording. It reads once a
job, and a store it can't read is reported as that.

`slice memory` waits, before the batch's setup, until the worker has the
memory the batch should use (`<batches>/<n>/memory.json`, which `batches`
predicts from the suite's measured peaks) free and the memory pressure is
below 10%, for at most 5 minutes or a tenth of the suite's minutes. It
logs what it read either way. A batch still short then exits 1, leaving
`<result>/.pipelinegen/memory.json`, and the batch's end marks its slices
`not run` with the figures; a suite without measured peaks starts at once.
`slice run` does the same before its set-up steps. On this machine, where
`run` and `record` checked before the batch, it does nothing. See [sharing
machines](../how-to/sharing-machines.md#memory-a-batch-waits-for-room).

Starts and ends go into the batch's journal,
`<result>/.pipelinegen/journal.jsonl`, each with a reading of the task's
cgroup; a slice's state gets the CPU it used and the batch's memory peak
while it ran (`cpu_seconds`, `peak_mb`). `slice end` takes the slice's
coverage counters at once and never reaches the network: the batch's end,
or a background `slice flush --every`, writes the journal out. `slice end` prints `slice <name>: <state>` and exits 0 whatever
the state, so the batch goes on to its next slice; the batch's end reports
the failures. A name that isn't in the batch exits 2.

A slice that comes round again (a browser batch runs its spec files once
per project) calls `slice start` again for each part. Each `slice start`
first closes the slice that ran until then: every correlator's collect
takes that part into the slice's coverage directory. The first part goes
to `<coverage>/<slice>` and part k to
`<coverage>/.pipelinegen/part-<k>/<slice>`, so no collector overwrites an
earlier part. `slice end` closes the part still running, whichever slice's, so
a test runner's hook can call `slice start` as the spec file
changes and `slice end` for every slice at the end. A slice's time, the sum
of its parts, goes into its state as `seconds`, which `durations --run`
reads.

`slice setup-end`, once after the batch's setup and before its first
slice, has every correlator that observes the suite collect what the setup
ran into `<coverage>/_setup` in a recording run. There `slice
start` and `slice end` exit 2 until it has run, so no slice of a batch
whose setup failed is called passed: the batch's end closes it as
errored. It prints `setup ended`.

In a recording run, a slice's recording is what every correlator's
`observe` says it touched in the setup's directory and in each of the
slice's parts. If a collect or an observe fails, the slice errors and the
message names the correlator. If a correlator can't tell, the slice keeps
the state its tests gave it and keeps its list, and the job's output, the
status and the comment say so. Planning then binds it to everything, so it
runs on every change until a recording tells again; a run of everything
lists such slices with the date each was first marked. When no slice of a suite could be told, the
suite's status is error and names the evidence that was missing.

`slice flush` writes what the journal still holds and prints how many
slices it wrote. With `--every <period>` it does so every period until it
is stopped (SIGTERM), at the lowest priority the machine gives, for a
batch's live progress: `slice start` and `slice end` write only the
journal. The batch's ensure, `report batch-end`, does the same
before it closes the batch, so a batch killed between writes loses no
slice that ended.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `source` | The tree under test, as a batch task sees it. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--batches` | `batches` | The directory of the run's batches. |
| `--result` | `result` | The batch's result directory. In a batch, `slice run` empties it first, as it does `--coverage` and `--work`: what an earlier batch left in a directory used again never counts as this one's. |
| `--coverage` | `coverage` | The batch's coverage directory. |
| `--tools` | `tools` | The batch task's tools; the correlators are `tools/plugins/<name>`. |
| `--prepared` | none for `slice run`, `prepared` for `slice prepare` | What the suite's prepare steps made (`slice run`), or the directory they fill (`slice prepare`). |
| `--work` | `work`; on this machine a temporary directory, removed after | The drivers' scratch directory. |
| `--cache` | `cache`; on this machine `pipelinegen/slice-run` in your user cache directory | The directory kept between builds, for the drivers and `{cache}` in params. |
| `--suite` | | On this machine, the suite `slice run` runs. |
| `--slice` | every slice | On this machine, a slice `slice run` runs; repeatable. |
| `--plugins` | `tools/plugins` under the root | On this machine, where the configured drivers are; a driver that isn't there, or isn't the build its pin names, is [fetched](#plugins-a-command-fetches) from its pinned release. |
| `--feed` | none | `slice run` as a piperun session: its feed directory (`$PIPERUN_FEED`). |
| `--pools` | `pools` | With `--feed`, the pools' slices `plan --emit` wrote (`<suite>-slices.json`). |
| `--dir` | `PIPELINEGEN_MARKS` | The bundle's marks directory (`slice mark`). |
| `--timeout` | `2m0s` | How long `slice mark` waits for the batch to take the mark. |

On this machine `slice run` also puts its results in a temporary
directory unless `--result` names one, and `--tools` names a directory
put first on the drivers' `PATH` (none by default).

`slice run` is the whole batch task of a suite run by drivers: it runs
the suite's set-up steps, `setup-end`, each run step with the slices
routed to it, and writes each slice's result and journal entries as
`start` and `end` do; then it stops the set-ups, the last started first,
and exits 1 when a slice did not pass. Without `PIPELINEGEN_BATCH` it
runs a suite's slices (`--suite`, `--slice`) on this machine through the
same steps, prepare steps first, on the working tree as it is, prints
each slice's result and reports nowhere; see [drivers](../concepts/drivers.md).
`slice run --feed <dir>` is a piperun session of a suite's pool. After
the set-up it reads the feed's chunks in order, `<dir>/1.json`,
`<dir>/2.json` and on, until `<dir>/end`, and runs each item, a slice of
the pool's file, through the suite's steps as a batch does, journaling it
and, in a recording run, observing it before its end is written. For each
item it appends one line under 4 KB to `<dir>/results.jsonl`: `{"key",
"start": true}`, then its end `{"key", "outcome": "passed|failed|errored",
"summary", "files", "duration_ms", "cpu_seconds", "peak_mb"}`, the summary
cut and the files left out, the last first, until the line fits. It reads
chunk c+1 only after every item of chunk c has its line. A failed slice's
failure files are copied to `<dir>/files/<slice>/`, at most 2 MB a slice
and 50 MB a session, the largest left out first. Before a slice's start
it checks the worker's memory against the slice's predicted peak beyond
what the session holds, and the memory pressure, waiting as a batch does;
a slice still short is handed back with `{"key", "requeue": true,
"reason"}` instead of starting. `PIPELINEGEN_LIMIT`, else the suite's minutes, is each
slice's limit: a slice still running a little before it is stopped and
errors naming the limit, and the session goes on. The batch is `PIPELINEGEN_BATCH`, else
`PIPERUN_SESSION`. The result and coverage directories are not emptied,
so a session started again keeps its journal; what it held unreported
went back to the pool, so its journal forgets it, an item it ended is not
run again, and an item it is fed again starts anew. It exits 0 once the
end is there and every item it took has its line, and 1 when it stops
before that, leaving piperun to end what it hadn't reported. Only a suite
run by drivers takes a feed.

`slice mark start|stop <name>` is what a driver calls at a bundle's slice
boundaries in a recording run of a stack suite; it returns once the
running `slice run` has taken the mark, and fails when the batch failed
the mark or didn't take it in time. `slice prepare` runs the suite's
prepare steps, in the suite's prepare task.

It reads the environment `report` does, with `PIPELINEGEN_SUITE` and
`PIPELINEGEN_BATCH` required. `slice list` prints the batch's slices one per line,
tab-separated, for a task in plain shell. See [writing tasks](../how-to/writing-tasks.md).

## `pipelinegen lock`

Gone: locks between runs are removed, and `lock` does nothing. A pipeline
generated before still has steps that run `lock acquire <name>` and `lock
release <name>`; they print that locks are gone and exit 0, whatever their
arguments, environment or config, so the pipeline keeps working until
`pipelinegen pipeline` writes it again without them. `lock acquire
--started <dir>` still stamps the directory (`started.json`), which that
pipeline's batch end reads. The next release removes the command. See
[sharing machines](../how-to/sharing-machines.md#between-repositories).

## `pipelinegen end`

Ends a run of a change by hand: a continuous run's round whose build is
gone, so the next build stops waiting for it, or a change's run nobody
wants finished. It records that the run is over and ends each of its
suites that hadn't ended on its own, whose slices without a result are
errored "the run was ended by hand"; it posts no success for what didn't
run. A suite that had ended keeps its end mark, and `end` says when it
ended. A suite job of the run that starts later runs nothing; one that
runs now finishes its batch. For a branch's recording run, what the slices
that passed before the end recorded goes into the commit's lists, as at
the end of any run that failed; that needs the commit and its history in
the checkout at `--root`, and a warning on stderr says so when they are
missing.

```
$ pipelinegen end --run full --commit 9a0e51c2d4f8
full main 9a0e51c2d4f8: suite e2e ended by hand
full main 9a0e51c2d4f8: suite unit had ended at 2026-11-15T15:00:00Z: left as it is
```

A continuous round whose build recorded its start and died before its
plan has no suites yet: `end` records that it is over, and the next build stops
waiting for it at once instead of after `limits.plan` and 10 minutes.

```
$ pipelinegen end --run full --commit 4f1c2a9e07b3
full main 4f1c2a9e07b3: the round ended by hand before its plan
```

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | none | The run, by its name in the config. Required. |
| `--commit` | none | The run's commit, in full or its first 12 characters. Required. |
| `--change` | the run's branch | The change, for a run of changes (`pr-12`). |

## `pipelinegen unlock`

Gone, as [`lock`](#pipelinegen-lock) is: it prints that locks are gone and
exits 0. The leases older pipelines left in the store are swept by the next
plan step of a trusted run, so nothing needs freeing by hand. The next
release removes the command.

## `pipelinegen also-to`

Posts a pull request run's comment to more pull requests, for a repository
that tests several pull requests together in one batch pull request:

```
pipelinegen also-to --run pr --change pr-41 38 39 40
```

It writes the ref `refs/pipelinegen/also-to/<run>/<change>`; the reporter
reads it at every render, so it may run before or during the run.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | none | The run's name. |
| `--change` | none | The batch pull request, `pr-<number>`. |

## `pipelinegen check`

Fails when a tracked file is something no slice depends on and isn't listed
in `untested`, when a tagged suite's slice is bound to nothing, when a
target's inputs can't be read, or when the setup or the config is wrong. It
reads BUILD files and git and starts no Bazel server, so it fits a commit
hook:

```
$ pipelinegen check --staged
web/unused.ts: no slice depends on this file, so a change to it would run nothing
  fix: add it to the srcs or data of a target a slice depends on, or to untested in pipelinegen.yaml with a reason
```

Each problem takes two lines: the file and what's wrong, then the fix. It
exits 1 when there is any problem and 0 when there is none. See
[the check](check.md).

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. With `--staged`, the config in git's index is used when it has one. |
| `--staged` | `false` | Checks what the next commit holds (git's index), and reports only what the commit touches: the files it adds or changes, and, when it changes a BUILD or `.bzl` file, the setup or the config, every problem it brings that `HEAD` doesn't have. |
| `--summary` | `false` | Prints, instead of each finding, how many findings of each kind each top-level directory holds (`<count>\t<kind>\t<directory>/`, `.` for files at the root), most first, then the total. The exit code is the same. Use it to see where an existing repository's work is before writing its `untested` list. |
| `--plugins` | `<root>/tools/plugins` | Where the configured plugins are. The check asks the sbom plugins, the correlators and the drivers only for their `describe` answers (and a validating driver to validate its steps), and runs only a binary whose SHA-256 is its pin; one that isn't there is [fetched](#plugins-a-command-fetches) from its pinned release. With an sbom's answer it tells which uncovered files planning will judge by their claims, and with a driver's it checks the steps that name it. |

## `pipelinegen pipeline`

Writes the repository's pipeline from the config, through the configured
runner plugin (see [plugins](../plugins.md)); without a runner it fails
and says so. With several runners, `--for` names the one whose pipeline it
writes, and that pipeline holds the runs whose `runs.<name>.runner` names
it: the runner gets only those as the spec's `runs`, and the names of the
others as `other_runs`. The Concourse runner writes, for each run, the
resource that starts it, a planning job `plan-<run>` and a job per suite
`<run>-<suite>`; a job `pipeline` that runs when the config changes on
`concourse.branch` and fails when the pipeline set is stale (or sets it,
with `concourse.self_set`); and the parts of `concourse.include`. It reads
no Bazel, and no git but with `--gate`: the same config always gives the
same pipeline. What the runner says of the pipeline short of refusing it
it prints on stderr, each notice once on a line of its own:
`pipelinegen pipeline: runner <name>: <notice>`. The check reports the
same notices as `runner-notice` warnings when the runner is at hand.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--out` | stdout | Writes the pipeline to this file instead. |
| `--runner` | `<plugins>/<name>` | The runner plugin's executable, for running it outside a pipeline. |
| `--for` | the only runner | The name of the runner plugin whose pipeline to write, with the runs it owns. Needed when the config has several runners. |
| `--plugins` | `tools/plugins` | The directory the bootstrap fetched the plugins into. |
| `--vars` | `false` | Prints the vars the pipeline references, one per line, instead of the pipeline: the token, the declared secrets, the repository's credentials and those in `concourse.include`. Whoever sets the pipeline passes exactly those. It needs no `concourse.url`. |
| `--gate` | none | In the pipeline's own job: writes `gate.json` into this directory, `["1"]` when the checkout's config files are the default branch's now and `[]` otherwise, and writes no pipeline when it is shut. That job's builds run side by side, so one of an older config never sets or checks its pipeline over a newer one's. It reads the branch's tip from the remote. |

On the example:

```
$ pipelinegen pipeline --out pipeline.yml
$ pipelinegen pipeline --vars
github_token
$ fly -t ci set-pipeline -p example -c pipeline.yml -v github_token=...
```

For the example, the jobs are `pipeline`, then `plan-main`, `main-smoke`,
`main-unit`, `plan-nightly`, `nightly-smoke`, `nightly-unit`, `plan-pr`,
`pr-smoke` and `pr-unit`. See [the pipeline](../internals/pipeline.md).

## `pipelinegen plan`

The planning job's step. Once it has run for a minute it says on stderr,
once a minute, where it is: `pipelinegen plan: <stage>, <n> of <m>
plugins done, <time> so far`, the stage being the commit under test, the
plugins' claims, the check, reading the lists, a plugin's call (`plugin
go-sbom: units of 812 files`), a Bazel command or a bazel-diff run, or
selecting the slices. A step that ends within a minute says none of it,
and stdout is never touched. `pipelinegen affected` says the same,
starting `pipelinegen affected:`. It reads the change from the trigger, and does
nothing when the change has a plan already. Otherwise it makes the commit
under test (a pull request's head merged into its target's tip, the same
commit every time), finds the base (`compare`: the tip; the newest earlier
commit whose run passed, else the newest whose run is still going; or
none), runs the check (`uncovered: fail` fails
the run here), plans like `affected`, writes the plan ref, sets the pending
statuses and the comment, and sweeps old refs. A pull request run plans in
two steps, `--out` then `--from`, so the change's own code never runs where
a credential that writes is, after a step `--begin` that records that the
build's planning began. A continuous run's build decides first, with
`--decide`, before the planner's prepare command, and plans after it.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.`, set to the trigger | The checkout whose config is read. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | | The run, by its name in the config. Required. |
| `--suite` | | The suite. |
| `--trigger` | `trigger` | The trigger's checkout: a pull request resource's (its `.git/resource` files say which pull request and head) or a branch's. |
| `--cache` | the user's cache | The work tree, Bazel's output and the hashes, kept between runs. |
| `--bazel-diff` | the pinned release | A bazel-diff binary to run instead. |
| `--out` | | Plan into this directory instead: `plan.json`, or `failed.txt` with why there is no plan; the store is read only and no status is set. The first of a proposed change's two steps, the one that runs the change's code. |
| `--from` | | Write the plan `--out` left in this directory: the commit under test and the base are worked out again from the trigger, a plan of anything else is refused and shown as a failed plan; then the plan ref, the statuses and the comment, as a plan in one step. The second step, which runs none of the change's code. A head planned already is judged as the sweep judges it (see `unplanned`): handed on again only when none of its suites began after `missed_after` and no sweep or build took it up lately, and then marked as the sweep marks it; a head the sweep gave up on is handed on all the same. |
| `--gate` | | Write `gate.json` here, open (`["1"]`) only when this build hands the change on to its suite jobs, else `[]`. A branch's run opens it only when this step wrote the run's plan, and also writes `version` (`<run> <commit>`), the hand-off its suite jobs follow: a build that exited (superseded, or nothing changed) hands nothing on, so no suite job starts for its commit. The plan step reads here the decision `--decide` left. With `--from` (a pull request's second step) the gate is written shut before anything else and opened only when the step wrote the plan, or found it planned and stalled as the sweep would hand it on again; a head planned with a suite begun or on its way, one the run leaves out, one the first step left no plan for, a superseded change and a failed plan all leave it shut. Not with `--out`, which hands nothing on. |
| `--begin` | | A pull request's planning build, in a step of its own before the first (`--out`), which runs none of the change's code: record that the build plans its head (the round's start record, written over an older one), so the run's sweep leaves the head to this build until `limits.plan` and ten minutes have passed, however slow the plan, or until the build's `--from` step ends without a plan, which removes the record. A head planned already gets no record. A store that can't be read or written costs a warning on stderr and nothing else: the step exits 0 and the plan goes on. Takes none of `--out`, `--from`, `--gate`, `--decide`. |
| `--decide` | | A continuous run's build, before the planner's prepare: decide whether it plans, waiting while a round of the run is in progress, and plan nothing. A build that won't plan writes the closed gate into `--gate` (required), as an exited plan step does, and the task stops before the prepare; one that will writes `decided.json` there, and the plan step after the prepare decides once more without recording a second start of the round. It exits 0 either way, and 1 when it waited `wait_minutes` for a round that didn't end. |
| `--emit` | | With `--from`, for a build on piperun: also write piperun's emission of the plan into this directory, `emit.json`, and the plan beside it as `plan.json`, the build's run-level record (`application/vnd.pipelinegen.plan+json`), which the generated step records. Per suite with something to run it emits the suite's prepare when it has one, a pool of its selected slices (items are slice names, longest recorded duration first, a slice without one at the median), each selected whole-suite check after the pool, and the suite's end after all of these with `ended: true`; a suite with nothing to run gets its end alone. Instances are named by the convention below. An emission piperun would refuse (over 500 instances, 2,000 items in a pool or 48 KB) or a name it can't take fails the plan before it is written, as a forged plan does, naming the limit. A plan written before, by an earlier attempt of this step or by another build, is emitted again without the slices that passed. Not with `--gate`. |
| `--pools` | `pools` | With `--emit`, where each pool's slices go for its sessions, `<suite>-slices.json` (name, label, target, kind, sources, tests, and the peak the slice should reach from the suite's measured peaks, `predicted_mb`), with the run's identity as `run.json` for `PIPELINEGEN_RUN_FILE`. `slice run --feed` reads them. The emitting step declares `outputs: {pools: pools}`, and a suite's slices template takes `inputs: {pools: {artifact: <emitter>/pools}}`, restored at `./pools/`. |

On piperun the instances `plan --emit` writes are named after the suite,
each the name of its template, as the piperun runner names its templates
(the functions `PiperunPrepare`, `PiperunSlices`, `PiperunWhole` and
`PiperunEnd` of the `plugin` package):

| Instance | What it is |
|---|---|
| `<suite>-prepare` | The suite's prepare, when it has one. |
| `<suite>-slices` | The pool of the suite's selected slices, after its prepare. |
| `<suite>-whole-<check>` | A selected whole-suite check, after the pool passed (after the prepare when no slice is selected). |
| `<suite>-end` | The suite's end, `report suite`, after each of the above that is emitted has ended, however it did; a skipped node counts as ended. |

## `pipelinegen unplanned`

A pull request run's sweep step. The pull request resource's check can
order a new head behind heads already planned, and the planning job then
never takes it; a suite job that is new takes only the newest hand-off. So
the sweep looks at every open head the run would test (its base, its
filters, no forks) and lists two kinds, each once it has waited the run's
`missed_after`, which leaves a head to its own builds while they come:

- a head with no plan, that the run would plan: a checkout of it in
  `<out>/plan/<number>`, with the pull request resource's files in
  `.git/resource` and `origin` pointing at the repository, so
  `pipelinegen plan --trigger <out>/plan/<number>` plans it unchanged;
- a planned head none of whose suites began (its run has no suite build's
  start record, no slice record and no suite's end): the resource's files
  alone in `<out>/handoff/<number>`, to hand to the suites again;
- a planned head whose run ended with slices not run for want of memory,
  and no other gap: every slice passed but those whose batches found the
  worker short of memory or that crashed out of memory, every suite build
  that began has ended, none began or ended since the run's mark of it,
  and nobody ended the run by hand. It is handed to the suites again the
  same way, and their builds run only the slices that didn't pass. A run
  with a failed slice is left to a person, so no failing test runs again
  unasked.

A suite build counts as begun from the moment its start step opens its
gate (`pipelinegen tree` writes the build's start record then), not from
its first batch's end: a build busy with its prepare and its first batch
for longer than `missed_after` is not handed on again. A build that waits
for a worker has written nothing yet, so a head whose suite builds wait
longer than `missed_after` is still handed on again, and both builds then
run every slice without a pass.

A head with no plan whose planning build began (`plan --begin`, or a sweep
that planned it) is left to that build until `limits.plan` and ten minutes
have passed since, however long `missed_after` is: a cold plan on new
plugin versions can take most of an hour, and planning the head again
beside it would only slow both. It prints `#<n> <commit>: its planning
began <time> ago: left to its build`. Once that time has passed the build
is taken for dead and the head counts as missed, as before. A planning
build whose write step (`--from`) leaves the head without a plan (the run
leaves it out, as a draft, or its plan failed) removes the record as it
ends, so the sweep takes the head up after `missed_after` as if no build
had begun. It removes the record whichever build of the head wrote it:
the sweep may then plan a head beside a second build of it, and the first
plan written stands. A build that never reaches its write step (its first
step failed, or it was stopped) leaves the record until the time has
passed, so start the planning job again to plan such a head sooner. A head the sweep plans gets the same record,
in the same write as its mark.

A head it lists gets a mark in the store
(`refs/pipelinegen/swept/<run>/...`), and the next sweep leaves it alone
for `missed_after` again, twice as long after every time: its own builds
may still be on their way (a suite build waiting in a serial group's queue
writes nothing yet). After three times, a head that still has no plan, or
no suite begun, is left for good, and its run status says so once. A
head that is no longer open loses its mark.

It prints one line per open head, saying which kind it is or why it is
left alone. A planned head's line is the one the planning job's second
step (`plan --from`) prints for a head it finds planned: both judge it by
one rule, `planned, a suite began`, `planned, its suites on their way` or
`planned, no suite began: hand it off again`, and a build of the planning
job that hands a head on again marks it too. That build leaves a head
taken up lately, but not one the sweep gave up on: a planning build
started by hand is how a person starts the suites of such a head.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The checkout whose config is read. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | | The pull request run, by its name in the config. Required. |
| `--out` | `sweep` | Where each head's directory goes. |
| `--cache` | the user's cache | Where the store's scratch repository waits for the next sweep. |

## `pipelinegen tree`

A suite job's first own step. It writes `gate/run.json` (the change, for
the steps after it) and the gates: `gate/gate.json` is `["1"]` when the
change's plan selected something of the suite and `[]` otherwise (no plan,
or nothing selected), `gate/whole-<name>.json` the same for each whole-suite
check. With the gate open it writes the build's start record in the store
(`<run's records>/_began/<suite>`, over the one before, with when the
build started and the job's link) and prints `<run> <change>: <suite>
began`; from then on the run counts the suite as begun, so its sweep and
its planning builds hand the head on no more. A store that can't be
written costs a warning on stderr and nothing else. Then it writes the
tree under test into `--out`: a git work tree whose HEAD is the tested
commit.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.`, set to the trigger | The checkout whose config is read. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | | The run, by its name in the config. Required. |
| `--suite` | | The suite. |
| `--trigger` | `trigger` | The trigger's checkout: a pull request resource's (its `.git/resource` files say which pull request and head) or a branch's. |
| `--out` | `source` | Where the tree under test goes. |
| `--gate` | `gate` | Where the gates and the run file go. |

## `pipelinegen batches`

Deals the plan's selected slices of a suite into at most `parallel`
batches of near-equal time (by the suite's `durations` file), leaving out
the slices that already passed for the change, so a rerun of the build runs
exactly what didn't pass. Writes `batches/batches.json` (the batch numbers
the `across` step runs over, as strings: Concourse puts each into a task's
`args` and `params`, which take strings only), `batches/<n>/slices.json`
and `batches/run.json`. From the suite's measured memory peaks
(`refs/pipelinegen/peaks/<suite>`) it also writes `batches/<n>/memory.json`,
the peak the batch should reach, which the batch waits for before it starts;
a suite with no peaks gets none.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The checkout whose config is read. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | | The run, by its name in the config. Required. |
| `--suite` | | The suite. |
| `--trigger` | `trigger` | The trigger's checkout: a pull request resource's (its `.git/resource` files say which pull request and head) or a branch's. |
| `--out` | `batches` | Where the batches go. |

`tools/pipelinegen slice list` prints the running batch
(`PIPELINEGEN_BATCH`) one slice per line, tab-separated: name, label,
target, sources, tests, with `-` for an empty field; `--batches` names the
directory (default `batches`).

## `pipelinegen learn`

`learn --stub` writes the stub deps repository (`.pipelinegen/deps`).
Planning doesn't read it: the command still writes the stub and warns
that the step and the repository's lines go and will be refused. See
[fresh clones](../how-to/fresh-clones.md).

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--stub` | false | Write the stub. Required. |

## `pipelinegen durations`

Records a run's test reports into a suite's durations file, the one its
`durations` key names, which batches are dealt by. It reads `go test -json`
output or reports of Playwright's JSON reporter. Each test file the
reports measured gets its new seconds per parameter set, the others keep
theirs, and files that are no longer the suite's test files are dropped.
Go tests are matched to the files that declare them, packages to
directories by `go.mod`.

```
$ pipelinegen durations --suite unit --go-json unit.json
ci/durations/unit.json: 1 added, 1 changed, 1 removed, 3 test files
  + calc/mul_test.go 0.5s
  ~ calc/add_test.go 5.0s, was 4.0s
  - calc/div_test.go 4.0s
no test file of unit declares these tests the reports timed:
  example.com/calc.TestVanished
```

It writes the file and prints what changed. Then it lists the tests the
reports timed that no test file of the suite declares (removed since, or
run from elsewhere). Commit the file yourself.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root; it reads the tracked files from git. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--suite` | none | The suite, a suite of test files with a `durations` file. Required. |
| `--go-json` | none | A file of `go test -json` output. Repeat it for several; their times add up. |
| `--playwright` | none | A report of Playwright's JSON reporter. Repeat it for several. |
| `--param` | none | The parameter set the measured run ran with, such as a screen type. |
| `--playwright-base` | found from the report | The directory, from the root, that the Playwright reports' file names are relative to. |
| `--run` | none | Takes each slice's time from this run's refs, as its batches' journals timed it (wall time, the sum of its parts). Needs `--change`. |
| `--change` | none | The change of `--run`: `pr-<number>` or the branch. |
| `--commit` | the change's newest run | The commit of `--run`. |

At least one source is needed: `--go-json`, `--playwright` or `--run`.
`--param` names the parameter set any of them ran with. See
[durations](../how-to/durations.md).

## `pipelinegen plugins`

`plugins claims` prints what each configured sbom and correlator plugin
claims in the tree, one file per line, `<plugin>\t<path>\t<role>`, with a
fourth column `recorded` where a correlator says its recordings hold the
file, or `erased` where it says the build erases the file, and `<plugin>\t-\tclaims nothing` for a plugin that claims nothing.
With `--json` it prints the claims as a list of `{"plugin", "path",
"role"}`, with `depends`, `by_units`, `recorded` and `erased` where set. Plugins
find their files by detection, so their config stays small; this is how
to see what they found. It asks the plugins the way a plan does, with the
answers kept in the cache.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--plugins` | `<root>/tools/plugins` | Where the configured plugins are, one executable per plugin name. |
| `--cache` | the user's cache, a directory per repository | Where the plugins' answers are kept. |
| `--json` | `false` | Prints the claims as JSON. |

## `pipelinegen git-auth`

Prints shell lines that have git authenticate to a host with the token in
a variable, for a task of the repository's that fetches private code (a
prepare task running `go mod download`, say). The lines set a credential
helper for the host through git's environment
(`GIT_CONFIG_COUNT`, `GIT_CONFIG_KEY_n`, `GIT_CONFIG_VALUE_n`) that reads
the variable when git asks, and `GIT_TERMINAL_PROMPT=0`. They name the
variable and never hold the token, so a trace of the task (`set -x`) shows
nothing secret, and the token goes into no URL and no file. Entries
already in `GIT_CONFIG_COUNT` stay.

```
$ eval "$(tools/pipelinegen git-auth GITHUB_TOKEN)"
$ go mod download
```

| Flag | Default | What it does |
|---|---|---|
| `--host` | `github.com` | The host git authenticates to. |

## `pipelinegen help`

Lists the commands with a line on each, as `pipelinegen --help`, `-h` and
`pipelinegen` alone do (alone, it exits 2). `pipelinegen help <command>`
describes one command: its usage line, what it does and its flags, and
for a command with subcommands each subcommand with its own flags. It
prints the same as `pipelinegen <command> --help` or `-h`, which every
command takes, wherever the flag stands before a `--`. Asking for help
exits 0; `help` with a command that doesn't exist exits 2. It takes no
flags.

## `pipelinegen version`

Prints pipelinegen's version: a release's tag, or `dev` for a build from a
commit. It takes no flags.

## `pipelinegen plugin`

Checks a plugin against the protocol, pins plugins' releases in the
config, and starts a plugin of your own.

| Subcommand | What it does | Exits 1 when |
|---|---|---|
| `plugin test <executable>` | Checks a plugin against the protocol, the way the core will call it, and prints one line per check (`ok` or `FAIL` with what went wrong). Run it in the plugin's own CI. See [plugins](../plugins.md#conformance). | any check fails |
| `plugin add <git-url> [<path>...]` | For each path, the plugin a repository of several holds in that directory, or with no path the repository's own plugin: finds its newest release that is no prerelease (tagged `<path>/vX.Y.Z`, or `vX.Y.Z`), downloads the release's `SHA256SUMS` and every file of the plugin's program it lists, checks each, reads the plugin's name and kind from its `plugin.json`, and writes its pin by `sums` into the config. A plugin at a path goes into the repository's group (`with`), made at the end of `plugins` when there is none; a repository's own plugin gets an entry of its own. The config is edited in place, keeping its comments and layout, and written only once every plugin is pinned. Prints a line per plugin. See [adding a plugin](../how-to/adding-a-plugin.md). | the config doesn't read, a release can't be listed, has no `SHA256SUMS` or a file that doesn't match it, or the name is taken |
| `plugin upgrade [<name>...]` | Moves the named plugins' pins, or every plugin's pinned by a release, to their newest releases, each checked as `add` checks one, keeping each pin's form (`sums`, or `sha256` of the `linux-amd64` file or Node script). Prints `<name>: <old> -> <new>`, or `<name>: <version>, the newest`. A plugin it can't move (its releases can't be listed, or the newest fails its check) gets `<name>: not moved: <why>` and keeps its pin, and the others still move. | a plugin wasn't moved, or a named plugin is missing or pinned by commit |
| `plugin new <kind> <name>` | Writes a new plugin, `<name>-<kind>`, from the template the plugins repository releases for its kind: `templates/<kind>/` at the newest tag `templates/vX.Y.Z` that is no prerelease, fetched with git. The template's module becomes the new module path, its one program under `cmd/` becomes `<name>-<kind>`, and its own directory, `templates/<kind>` (in a `BUILD.bazel` or a README, say), becomes the new one's: its path in the git work tree that holds `--into`, or `<kind>/<name>` outside one. Each is rewritten in every file's text and path. | the directory holds files, the tag or the kind's template is missing, or the template holds a link |

```sh
pipelinegen plugin test ./bin/go-sbom --tree testdata/tree
pipelinegen plugin add https://github.com/sideways-works/pipelinegen-plugins driver/go sbom/go
pipelinegen plugin upgrade --pre go-sbom
pipelinegen plugin new correlator rust --into ../rust-correlator
```

| Flag | Default | Subcommands | Meaning |
|---|---|---|---|
| `--tree` | none | `test` | A fixture tree of the plugin's own. Sboms and correlators need one for more than `describe`. |
| `--config` | none | `test` | A JSON file with the plugin's config block, passed in every request. |
| `--cache` | a fresh directory | `test` | The plugin's cache directory, filled beforehand the way the planning job's prepare fills it: one directory per cache the plugin declares (`gomodcache` holding the repository's Go modules, say). A Go plugin needs it on any tree with third-party modules. |
| `--sbom` | none | `test` | An sbom plugin run beside a correlator, listing the units its links name. The checks that compare the links' ends' digests need it, and say so when it is missing. |
| `--sbom-config` | none | `test` | A JSON file with the companion sbom's config block. |
| `--root` | `.` | `add`, `upgrade` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | `add`, `upgrade` | The config file it edits. |
| `--version` | the newest that is no prerelease | `add`, `new` | The release to pin, `vX.Y.Z`, the same for every path; for `new`, the templates' release. |
| `--name` | the name in the plugin's `plugin.json` | `add` | The entry's name, with one path. |
| `--pre` | `false` | `upgrade` | Moves to a prerelease too, when it is the newest. |
| `--into` | `<name>-<kind>` in the current directory | `new` | The directory to write the plugin into, new or empty. |
| `--from` | `https://github.com/sideways-works/pipelinegen-plugins` | `new` | The git URL of the repository whose `templates/vX.Y.Z` tags hold the templates, one directory a kind under `templates/`. |
| `--module` | the template's, with `templates/<kind>` made `<kind>/<name>` | `new` | The new plugin's Go module path. |

## `pipelinegen lists`

Reads and writes the lists of what each slice touched, which hang off a
branch's commits as `refs/pipelinegen/lists/<commit>`. Run it in a
checkout of the repository with `GITHUB_TOKEN` set; it needs the commits'
history, so not a shallow clone.

| Subcommand | What it does | Exits 1 when |
|---|---|---|
| `lists attach` | Merges the recordings of run `--run`, change `--change`, commit `--commit` into the lists of the branch commit `--to`. When `--to` has no lists yet, it starts from its nearest first-parent ancestor's (within 50 commits). A recording only adds to its slice's list, except in a run that selected every slice: there a slice that passed gets its recording as its whole list. Each slice is judged by its own last state, for every slice the run's records hold, whatever the plan names, and whether or not the run passed: a slice that didn't pass keeps its list or its mark, and only the slices that passed bring their marks, each with the newest reason. What a batch's setup ran goes into the suite's own list, `<suite>/_setup`. It also notes the run in `refs/pipelinegen/attached/<commit>`. The same run attached again writes nothing and says so, unless a trusted run brings what the lists don't hold yet, such as the slices that passed in a rerun of a suite's build; another run attached to the same commit merges in too. | the run has no plan, `--to` isn't in the checkout, the clone is shallow, or a write fails |
| `lists show` | Prints the lists planning would use for `--commit`: which ref they came from (the commit's own or an ancestor's), the live runs whose recordings it added, and the number of units per slice. With `--slice`, that slice's units, one per line. A slice whose last recording a correlator couldn't tell is marked: the line names the correlator, the reason and the time the mark was first set, since planning binds that slice to everything until a recording tells again. | a read fails |

| Flag | Default | Subcommands | What it does |
|---|---|---|---|
| `--root` | `.` | all | The repository's root, a checkout with history. |
| `--config` | `<root>/pipelinegen.yaml` | all | The config file. |
| `--run` | none | `attach` | The run whose recordings are attached. Required. |
| `--change` | none | `attach` | The run's change: `pr-<number>` or the branch. Required. |
| `--commit` | none | `attach` | The commit the run tested, in full (a pull request's head). Required. |
| `--to` | none | `attach` | The commit on the branch that gets the lists (the merge commit). Required. |
| `--commit` | `HEAD` | `show` | The commit whose lists it prints. |
| `--suite` | every suite | `show` | Only this suite. |
| `--slice` | none | `show` | Prints this slice's units instead of the counts. |
| `--local` | off | `show` | Reads the local store `pipelinegen record` writes even when the config names a remote. |

```
$ pipelinegen lists attach --run pr --change pr-12 --commit 0723eb0a27d9... --to 5c1e09d4b7a2...
lists of 5c1e09d4b7a2: pr pr-12 0723eb0a27d9 merged into the lists of 81f0aa3c9e17, 2 commits back; 14 slices, 3 changed
```

On the proof's fixture ([proof](../proof.md#planning-through-the-released-go-plugins)),
after its recording run and a later one whose greet slice left no
coverage:

```
$ pipelinegen lists show --commit 50b6ade61c0b
lists of 50b6ade61c0b: its own lists
with the recordings of the live runs:
  full main 50b6ade61c0b (3 slices)
  full main 51a71de0a8ee (3 slices)
go: 3 slices
  app/greet_test.go 3, bound to everything until a recording tells again: go-correlator couldn't tell since 2026-11-13T11:04:42Z (go-correlator observe: can't tell: ... no coverage: ...)
  app/price_test.go 4
  report/report_test.go 4
```

`lists show` reads the store `affected` and `why` read: with a config that
names no remote, the local store a recording made on this machine, and its
first line on stderr says so (`pipelinegen lists show: lists from the local
store (recorded on this machine): <path>`).

A commit with no lists within 50 commits shows "no earlier lists": its
recorded slices are bound to everything, as on a first run. See
[attaching lists](../how-to/attaching-lists.md) and
[looking inside](../how-to/looking-inside.md).

## `pipelinegen run`

Plans a change as `affected` does and runs the slices it selects on this
machine, the way a pull request's run would on CI: per suite with a
selected slice, the suite's prepare once and then one batch of its
selected slices, through its drivers or its task files, in a build
directory laid out as `record`'s is. No CI system, host, token or `runs`
section is needed: a config with `suites` and `plugins` is enough, and a
`github` section is ignored for the run itself.

It plans and runs commits. `--head` (default `HEAD`) is planned against
`--base` with `--head`'s own `pipelinegen.yaml`, and the build is a fresh
checkout of `--head`, so uncommitted changes are neither planned nor run.
With `--head` at `HEAD` and changes in the work tree, it names them on
stderr and says so again on its last line, but runs: testing the last
commit while you edit is what it is for. Under `--record` it refuses
them, as `record` does.

`--base` has no default. Without one, `affected` plans every slice
("nothing to compare with"); `run` asks for `--base`, or for `--all` to
run every slice, so a forgotten flag doesn't run the whole suite.

Before a suite's batch, it reads the suite's memory peaks from the store
the config names, as CI's batches predict them, and when this machine has
less free than the batch should use it runs none of that suite's slices,
says so with the figures (`e2e: 12 not run: not enough free memory: ...`)
and exits 1 at the end. Nothing waits: close what you can spare and run it
again. Without a store or peaks, it doesn't check. `record` does the same
and refuses.

Its output, on stdout:

1. the plan, as `affected` prints it: a line per suite with how many
   slices it selected, and the slices;
2. what the batches print as they go: `prepared <suite>`, `setup ended`
   and `slice <name>: <state>` as each slice ends (the tests' own output,
   and why a slice failed, go to stderr);
3. a summary: per suite, how many slices passed, failed and errored (and
   any that never ran), each slice that did not pass, and, under
   `--record`, whether the suite's lists were attached.

It exits 0 when every slice passed, and when the plan selects nothing
(`nothing selected: no slice to run`); 1 when a slice failed, errored or
never ran, or the run itself failed; 2 for a command line it can't read,
`--base` and `--all` both missing among them, and a `--slice` no suite has.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `--head`'s own `pipelinegen.yaml` | The config file to plan and run under. |
| `--base` | none | The commit to compare with. Required unless `--all`. |
| `--head` | `HEAD` | The commit to plan and run. |
| `--suite` | every suite | Only this suite; repeat it for more. |
| `--slice` | every selected slice | Only this slice, by its name (a test file's path, or a tagged target's name) or its label, in every suite that has it; repeat it for more. With `--all`, only the slices named. A slice the plan didn't select runs too, and a line on stderr says it wasn't selected: you named it. A name no suite has (of those `--suite` names) exits 2 and names it, before anything runs. |
| `--all` | `false` | Runs every slice, not only those the change selects. |
| `--record` | `false` | Records the suites bound by recording, as `record` does, and attaches their lists to `--head` in the local store, each suite's only when every slice of it that ran passed. A suite not bound by recording runs without, and a line on stderr says so. |
| `--local` | `false` | Plans with the lists of the local store, as `affected --local`. |
| `--no-lists` | `false` | Binds recorded slices to everything instead of reading the lists, as `affected --no-lists`. |
| `--cache` | your user cache directory, then `pipelinegen/repos/<repository>` | The planning cache, as `affected`'s. |
| `--bazel-diff` | the pinned release | A bazel-diff binary to run instead of downloading one. |
| `--plugins` | `<root>/tools/plugins` | Where the configured plugins are, one executable per name: the sbom and correlator plugins planning asks, and the drivers the batches run. A plugin it lacks, and a driver there that isn't the build its pin names, is [fetched](#plugins-a-command-fetches) from its pinned release. A driver must be the build its pin names, as on CI. |
| `--no-fill` | `false` | Doesn't fill the caches the plugins read before planning, as `affected --no-fill`. |
| `--work` | a temporary directory, removed after | Where the builds go, one directory per suite (`<work>/<suite>/build`, with the run's records in `<work>/<suite>/store.git` when it doesn't record). A directory used before is reused as `record --work` reuses it, which keeps Bazel's output base warm. Without it the records go with the directory, so why a slice failed is said in full on stderr, with no ref to read the rest at. |
| `--no-image` | `false` | Runs the suites' steps on this machine instead of in the images the config names. |
| `--container-tool` | `docker`, else `podman`, whichever is on `PATH` | The tool that runs the steps in their images. |
| `--as-me` | `false` | Runs the steps in their images as you, not as root as CI does; a privileged suite runs as root either way. |

```
$ git commit -am "a test of adding zero"
$ pipelinegen run --base HEAD~1 --suite unit
unit: 1 selected, 3 untouched
  run calc/add_test.sh
prepared unit
setup ended
slice calc/add_test.sh: passed
ran at 1f6e332512d3:
  unit: 1 passed, 0 failed, 0 errored
```

A slice that fails is named in the summary, and why it failed is on
stderr:

```
$ pipelinegen run --base HEAD~1 --suite unit
unit: 1 selected, 3 untouched
  run text/upper_test.sh
prepared unit
setup ended
slice text/upper_test.sh: failed
ran at 9619a1b7ebfa:
  unit: 0 passed, 1 failed, 0 errored
    failed text/upper_test.sh
```

A suite's steps run in the image CI runs them in, as
[in their images](#in-their-images) says; `--no-image` runs them on this
machine.

What a local run can't do:

- With `--no-image`, the steps run on this machine, so it needs what the
  images have (a browser, Node, Go, a database); a missing tool fails the
  batch, and the line on stderr names the image. A setup step that starts
  a server binds its port here.
- Each suite runs as one batch: no `parallel` split, no `run_on`.
- Whole-suite checks don't run; a line on stderr names the selected ones.
- Without `--record`, nothing is observed and the local store is left
  as it was. With it, attaching only some slices is as valid as
  `record --slice`: a local recording adds to the lists, and a slice that
  didn't run keeps its own.

## `pipelinegen record`

Records a suite's slices at HEAD on this machine, so `affected` and `why`
show the selection the recordings narrow, with no CI system, host or
token. It runs the suite's own `prepare` task, when it has one, and then
its `run_slice` task, as one batch of every slice (or those `--slice`
names). Each task's `run` runs in its image, as
[in their images](#in-their-images) says, or with `--no-image` here, in a
build directory laid out as a suite job's: `source` is a worktree of HEAD made inside the build directory
(so a task that runs `cd source` and writes `../coverage` writes into the
build, as on CI) and removed after, `tools/pipelinegen` this pipelinegen, `tools/plugins` the plugins, and `batches`, `prepared`,
`result` and `coverage` as the [batch contract](../contracts.md#a-the-batch-task)
says. When every slice passed, the lists of what the correlators observed
are attached to HEAD in a local store, a bare repository in your git
directory (`.git/pipelinegen/local.git`) that nothing pushes and no clone
carries.

A suite run by drivers has no task files: `record` runs `slice prepare`
and then `slice run` for its one batch, in the same build directory, as
the generated tasks do, and the batch reads what the prepare steps made.

The config needs only `suites` and `plugins`: the suite bound by
recording, its steps or task files, and its correlators. Runs, a CI system and a
host are not needed, and a `github` section is ignored here.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--suite` | none | The suite to record. Required. |
| `--slice` | every slice of the suite | Records only this slice, by its name or label; repeat it for more. |
| `--store` | `<git directory>/pipelinegen/local.git` | The local store. `affected` and `why` read only the default one. |
| `--plugins` | `<root>/tools/plugins` | Where the configured plugins are, one executable per name. A plugin it lacks is [fetched](#plugins-a-command-fetches) from its pinned release. |
| `--no-image` | `false` | Runs the suite's tasks on this machine instead of in their images. |
| `--container-tool` | `docker`, else `podman`, whichever is on `PATH` | The tool that runs the tasks in their images. |
| `--as-me` | `false` | Runs the tasks in their images as you, not as root as CI does; a privileged suite runs as root either way. |
| `--work` | a temporary directory, removed after | Where the build's directories go, to look at what a task left. A directory used before is reused: everything an earlier recording left there that belongs to one run (coverage, results, batches, prepared, work, tools) is removed first, and a line says what; only the caches stay (`cache`, the caches the suite's task files declare, and the checkout's path, by which Bazel keys its output base). A file it can't remove stops the recording. |

It refuses, with exit 1:

- a tree with changes, tracked or new: the lists describe HEAD, so it
  names the files and asks you to commit them first;
- a suite not bound by recording, or one no correlator observes;
- a task file without a `run.path`, or one that takes an input a local
  recording doesn't make.

A task that fails stops the recording and attaches nothing. With
`--no-image` the tasks run on this machine, not in the image their file
names, so the last line says which image that is: what the task needs (a
browser, Node) must be here.
`source` is a fresh checkout of HEAD, as on CI, so the files git ignores in
your working tree (an installed `node_modules`, a built `dist`) are not in
it: the suite's `prepare` task has to make what the batch needs, and the
last line of a failed task says so.

```
$ git commit -am "record the browser suite"
$ pipelinegen record --suite e2e
recorded 12 slices of e2e at 3f2a9c1e07bd; the lists are in the local store .git/pipelinegen/local.git, which affected and why read unless the config names a remote (then: --local)
$ git commit -am "a change to the cart"
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): .git/pipelinegen/local.git
```

The lists belong to the recorded commit, so the change is planned against
it: `--base` names that commit.

### In their images

`run` and `record` run a suite's steps in the image CI runs them in, with
`docker` or `podman`, whichever is on `PATH` (`--container-tool` names
one):

- a suite's `image`, a `registry-image` resource of the root pipeline
  file (`concourse.include`), read at the commit that runs: `source.repository`
  and `source.tag`, or by digest when the resource pins a `version: {digest: ...}`;
  a prepare step's own `image` for `slice prepare`;
- otherwise a task file's `image_resource`;
- with neither, on this machine, as before.

An image it doesn't have is pulled first. Each step is one container,
removed when it ends, on the default bridge network, so it reaches what
this machine reaches, as a CI task does; a server a set-up step starts
listens inside the container. The suite's `privileged` runs it privileged.
Its environment is the task's alone: the build's `PIPELINEGEN_*`
variables and the suite's and the task file's `params`, never a token or
anything else of this machine's environment.

The build's directory, the repository's git directory (read only, as
`source` is a worktree of it) and the tools are mounted at the paths they
have here, so every path a step is given holds in the container:
`tools/pipelinegen` is this pipelinegen when it is a static Linux build for
the image's platform, and otherwise the release
`concourse.planner.bootstrap.release` pins, fetched for that platform (on
macOS, always); `tools/plugins` holds the drivers and correlators fetched
for that platform. Each step runs as root, as a CI task does, with the
image's own `HOME` and entrypoint set aside for the step's command. On
Linux with a rootful tool, what it wrote is then root's: once the step
ends, failed or stopped, a container of the same image gives it back to
you (`chown -R`), and if that can't run a line says which `sudo chown`
does; with rootless docker or podman root is you already. `--as-me` runs
the steps as you instead (`--user`, or `--userns keep-id` with rootless
podman, and `HOME=/tmp`), except a privileged suite's, which needs root to
start its own container engine. With neither tool on `PATH`, a suite that
names an image fails, saying to pass `--no-image`.

### Plugins a command fetches

`affected`, `why`, `try`, `run`, `record`, `slice run` and `check` find each
plugin they run in `--plugins` (default `<root>/tools/plugins`). One that isn't
there is fetched from its pinned release on GitHub, for this machine's
platform (or the image's), into your user cache
(`pipelinegen/releases/<sha256>/`), and checked against its pin: its
`sha256`, the linux-amd64 file's checksum or a Node plugin's one `.mjs`, or,
for `sums`, its line of the release's `SHA256SUMS`, itself checked by the
pin. A file that doesn't match is refused and never runs; only `sums`
checks a binary for another platform than linux-amd64. Plugins fetched for
one command are linked together in `pipelinegen/plugin-sets/`. No token is
needed or sent: the plugins' repositories are public. A command's first
fetch removes from `pipelinegen/releases/` what no command used in 30 days,
so the cache doesn't grow without bound; a release used again is fetched
again.

A driver in `--plugins` that isn't the build its pin names is passed over
for the pinned release, with a line on stderr. An sbom or correlator there
that isn't its pin still runs, its answers cached apart, which is how a
change to a plugin is tried. A plugin pinned by `commit` has no release:
the error names it and the command that builds it into `--plugins`.
`check` never runs a binary that isn't its pin: one there that isn't is a
warning, as is a plugin it can't fetch, and for one pinned by `commit` the
warning gives the commands that build it.

## `pipelinegen seed`

Puts the local recordings of HEAD into the repository's store as HEAD's
lists, so the branch's first run on the CI system is already narrow
instead of running every slice to learn what each one executes. It reads
every recording `pipelinegen record` made of HEAD in the local store and
attaches each one to HEAD in the store the config names (`git.remote`,
the host's repository, or the checkout's `origin`; on refs or on the
store's branch, as `store.backend` says), exactly as `lists attach`
attaches a trusted run's recordings. Several recordings of HEAD (one per suite, or a suite recorded again)
are seeded as one: each slice takes its newest passed recording, read from
every slice the recording's records hold. Only a slice that passed in a
local recording gets a list; a slice that failed or didn't finish gets
none, and so does every slice of a suite you didn't record: those run
whole on CI until a run records them.

Each attached recording leaves a note in the lists'
`_attached/seed/<recording>/<commit>.json` that says the lists were
seeded, from which commit, with which pipelinegen and with which plugin
builds, and `lists show` prints it. The note also makes HEAD the base of
the default branch's runs that compare with the last passed commit, until
one of them passes: a run on HEAD itself selects nothing, and a run on a
later commit selects only what the change reaches (plus every slice
without a list, and everything when an `everything_on` file changed, as
always).

Without `--replace` a seed only adds: a slice's list that a run already
made keeps every unit and gains the local recording's, and no slice
marked "can't tell" loses its mark. With `--replace`, each slice that
passed locally gets its local recording as its whole list, as a run of
everything on the branch does. The same recording seeded again writes
nothing; seeding it with `--replace` after a plain seed does replace.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root, a checkout with history. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--commit` | `HEAD` | The commit to seed. It must be HEAD. |
| `--store` | `<git directory>/pipelinegen/local.git` | The local store `record` wrote. |
| `--dry-run` | off | Prints what it would write, per local recording: how many slices get a new list, gain units, or have theirs replaced, and which get none and why. Writes nothing and needs no credential. |
| `--replace` | off | Lets each slice that passed locally replace its list instead of adding to it. |

It refuses, with exit 1 and before writing anything:

- any pipeline step (`PIPELINEGEN_PIPELINE_STEP` or `PIPELINEGEN_JOB_URL`
  set) and a pull request's
  (`PIPELINEGEN_CHANGE` starting with `pr-`): seeding is a person's
  command. The writing credential is the real gate: a step that plans a
  pull request holds the read token only;
- a tree with changes, tracked or new;
- a `--commit` other than HEAD, and a commit the default branch
  (`concourse.branch`) doesn't hold, as the checkout knows the branch;
- a commit the local store has no recording of, naming the commits it has
  recordings of;
- a missing writing credential for the store: the store's token
  (`GITHUB_TOKEN`, or `PIPELINEGEN_STORE_TOKEN` with tokens by purpose) or,
  over ssh, `PIPELINEGEN_SSH_KEY_STORE`. It is read from the environment
  only, never from a flag;
- a config that names no store.

```
$ pipelinegen record --suite e2e
$ pipelinegen seed --dry-run
dry run, nothing written: seeding 3f2a9c1e07bd would merge into no earlier lists (none within 200 commits)
the local recording local-20261114-101500-000000000: a new list for 11 slices, more units for 0 slices, a replaced list for 0 slices
  no list for 1 slice that failed locally: e2e/e2e/pay.spec.ts
$ GITHUB_TOKEN=... pipelinegen seed
lists of 3f2a9c1e07bd: the local recording local-20261114-101500-000000000 merged into no earlier lists (none within 200 commits); 11 slices, 11 changed
  no list for 1 slice that failed locally: e2e/e2e/pay.spec.ts
seeded 3f2a9c1e07bd: runs on the default branch compare with it until one of them passes
```

A local machine can differ from the CI system: a code path only one
operating system takes, a test skipped here for want of a browser or a
service. A seeded list can then lack what the test executes on CI, and a
change there would leave the test out. The nightly run of everything
replaces each list with what CI saw, so schedule one before relying on a
seed. The plugins' answers are not seeded: a plugin runs with the
machine's own toolchain, which the answers' keys don't name, so the first
plan on CI computes them itself.

## `pipelinegen baseline`

Declares HEAD good by hand as the base of a run that compares with the
last passed commit, so the run's first runs don't select everything for
want of an earlier passed run. With a host it posts a status of its own,
`pipelinegen/<run> baseline` (the run's context and " baseline"), on HEAD
as `success` with the description "baseline set by hand with pipelinegen
baseline, not a run", using the report token. Being apart from the run's
own status, no status of the run (a failed plan's error included) erases
it, and a branch rule that requires the run's status is never met by it. Without a host it writes the same declaration into HEAD's
lists in the store, as a note that names the run, carrying the nearest
ancestor's lists over unchanged.

A run on the baseline's commit compares it with itself and selects
nothing; a later commit's run compares with it and selects what the
change reaches, until a later commit's run passes. A commit's own run
outranks its baseline once its tests decided it: after a run on it that
failed, later commits no longer compare with it. Only a baseline does
that for its own commit: a run's ordinary passed status never keeps the
same commit's next run from running. A baseline the first release posted
(in the run's own context, described as a baseline) still counts.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root, a checkout with history. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | none | The run whose base it sets: a branch run with `compare: last_passed`. Required. |
| `--commit` | `HEAD` | The commit declared good. It must be HEAD. |
| `--dry-run` | off | Prints the status it would post, or the note it would write, and does neither. |

It refuses what `seed` refuses (a pipeline step, a pull request's step, a
tree with changes, a commit other than HEAD, no credential: the report
token with a host, the store's without), and also a run of pull requests,
a run that doesn't compare with the last passed commit, a run on another
branch than the default one (`concourse.branch`), and a commit the
branch doesn't hold.

```
$ GITHUB_TOKEN=... pipelinegen baseline --run main
baseline: 5c1e09d4b7a2 has the status pipelinegen/main: success, "baseline set by hand with pipelinegen baseline, not a run"; runs of main on it select nothing, and later ones compare with it until one passes
```

A baseline says nothing about what the tests execute: slices without
lists still run on every change until a run records them. Seed first when
you have local recordings.

## `pipelinegen status`

Says what the records hold about a commit's runs, one line per run that
planned it, and answers with its exit code. It is how a repository on a
plain git server, with no host to show commit statuses, learns how its
runs went. A server's `post-receive` hook, or a shell, can print the lines
as they are:

```
$ pipelinegen status review/cart
pipelinegen: review branch-review%2Fcart 3f2a9c1e07bd: failed (1 of 3 suites failed)
$ echo $?
1
```

The commit comes first: a commit id, or anything the checkout resolves to
one (a branch, `HEAD`). A bare repository will do, so it also runs on the
server itself. The records come from the store `affected` and `why` read:
`git.remote`, or the local store a recording made when the config names
no remote.

| Exit | Meaning |
|---|---|
| 0 | every run that planned the commit passed |
| 1 | a run failed or errored |
| 2 | a run is still pending, or `--run` names a run with no records for the commit |
| 3 | no run planned the commit |

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The checkout, or bare repository, that resolves the commit. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--run` | every run | Only this run. |
| `--json` | `false` | Prints the runs as JSON: `run`, `change`, `commit`, `state` (`passed`, `failed`, `errored` or `pending`) and `description`. |
| `--local` | `false` | Reads the local store `pipelinegen record` writes, even when the config names a remote. |

## `pipelinegen why`

Says why a slice was selected for a change, or why it was left out, one
reason per line.
It works in one of two forms:

```sh
pipelinegen why --run <run> --change <change> --commit <commit> --suite <suite> --slice <slice>
pipelinegen why --base <base> --head <head> --slice <slice>
```

The first reads the plan the planning job kept for a run, which is what
the run actually did: use it for a slice of a CI run. `<run>` is the run's
name in the config, `<change>` the change as the run names it (`pr-12`
for pull request 12, the branch for a branch's run), `<commit>` the
commit the run planned (its first 12 characters do; left out, the
change's newest kept plan) and
`<suite>` the slice's suite (left out, every suite that has the slice).
The second plans `--head` against `--base` again in your checkout the way
the planning job does, with the base's lists and the live runs'
recordings read from the repository's refs: use it for commits no run
planned yet, or to try a change on your machine. It needs Bazel and the
plugins, and takes as long as a plan.

Read from a kept run, a slice that ran there and failed or errored with
failure files says how many it kept and dropped, and the command that
writes them out:

```
  failure files: 2 kept, 1 dropped; pipelinegen report files --run pr --change pr-12 --commit 7a2d41c09e5f --slice e2e/checkout.spec.ts --out failure-files writes them out and says why any were dropped
```

On the notes service of the book, for a change to the backend's
validation, which only one browser spec reached:

<!-- output: why-validate-create -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 26118462acef to 8933844527a0:
  it executed api.validate during its recording (api/api.go), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

<!-- output: why-validate-search -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/search.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/search.spec.ts was left out from 26118462acef to 8933844527a0:
  nothing it is bound to changed
  observed by: go-correlator, oapi-codegen-correlator
```

Each reason is a chain, one hop per line: what the slice executed during
its recording, then each link the walk followed, with the plugin that gave
it, up to what changed. For a change to the API contract:

<!-- output: why-contract-spec -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 26118462acef to d4d136210ea4:
  it requested POST /notes (createNote) during its recording, which changed in this commit
  it executed notesapi.ServerInterfaceWrapper.CreateNote during its recording (api/notesapi/notesapi.gen.go)
    → which leads to POST /notes (createNote) (a link from oapi-codegen-correlator), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

`--ids` adds each unit's id beside its name:

<!-- output: why-validate-ids -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts --ids
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 26118462acef to 8933844527a0:
  it executed api.validate [go:api#func:validate] during its recording (api/api.go), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

With `--run` and `--change` it reads the kept plan instead and says "was
selected in <run> <change> at <commit>" (or "was left out in").

For a slice that was selected, the reasons are the plan's: everything ran (and
why), a new slice, a test added to it, removed from it or changed, a unit
it touched that changed, code it depends on through a path of declarations,
a file of an app it opens, a Bazel target, a changed plugin entry, the
sample that kept it, the
base's config (a change to the config never selects less for itself), or a
mark that binds it to everything because a correlator couldn't tell what
it touched. For
a slice that was left out: nothing it is bound to changed; a narrowing left
it out, with the narrowing level from which it would have run; the sample
left it out; or the head no longer has it. A name in several suites gets
an answer per suite, and a slice of a recorded suite names the correlators
that observe it. A slice that is in no suite of the plan exits 1.

| Flag | Default | What it does |
|---|---|---|
| `--root` | `.` | The repository's root, a checkout with history. |
| `--config` | `<root>/pipelinegen.yaml` | The config file. |
| `--slice` | none | The slice, by its name (a test file's path) or its label. Required. |
| `--suite` | every suite | Only this suite. |
| `--run` | none | Reads the kept plan of this run; needs `--change`. |
| `--change` | none | The change: `pr-<number>` or the branch. |
| `--commit` | the change's newest plan | The commit of the kept plan to read. |
| `--base` | none | Plans again: the commit to compare with. |
| `--head` | `HEAD` | Plans again: the commit to plan. |
| `--cache` | the user's cache, a directory per repository | Where work trees and hashes are kept when it plans again. |
| `--bazel-diff` | the pinned release | A bazel-diff binary to run instead, when it plans again. |
| `--plugins` | `<root>/tools/plugins` | Where the configured sbom and correlator plugins are, when it plans again, as for `affected`, which fetches one that isn't there. |
| `--no-fill` | `false` | When it plans again, doesn't fill the plugins' caches first, as for `affected`. |
| `--no-lists` | `false` | When it plans again, binds recorded slices to everything instead of reading the base's lists. With no remote to read them from (no `git.remote`, no `github` section, no `origin` in the checkout), or a remote whose lists can't be read, it says so and does the same: a warning below the reasons names why; a kept plan needs the remote. |
| `--local` | `false` | Reads the lists, or the kept plan of a local recording (`--run local`), from the local store `pipelinegen record` writes. |
| `--json` | `false` | Prints JSON: a list of `{"suite", "slice", "label", "ran", "reasons": [{"kind", "what", "through", "hops", "narrowing", "off_at", "text"}], "observed_by"}`. `hops` is the chain from the unit the slice's recording holds to what changed: `{"from", "to", "kind", "plugin", "back", "label", "file"}`, the first with kind `listed`, each other with the link's kind and the plugin that gave it. |
| `--ids` | `false` | Shows each unit's id beside its name in the chains. |
| `--verbose` | `false` | Also says how many files each plugin answered from the plan's cache. |

Give either `--run` and `--change` or `--base`; both, or neither, exit 2.
A kept plan stays readable while `keep` keeps its run. See
[looking inside](../how-to/looking-inside.md).
