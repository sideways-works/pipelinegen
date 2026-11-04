# Glossary

Each term links to the page that introduces it.

**Base.** The commit a change is compared with: the pull request's target
branch tip, or the last commit whose run passed. See [changes and
tests](concepts/changes-and-tests.md).

**Batch.** A group of selected slices of one suite that one CI task runs
after a single setup. See [suites, slices and
batches](concepts/suites-and-slices.md#batch). Not a bundle: a run step's
setting `batch: true` bundles the slices of a batch.

**Batch task.** The CI task that runs one batch: your task file
(`run_slice` in the config), or, for a suite with steps, the generated
one, which calls `pipelinegen slice run` to run the steps' drivers. See [writing a suite's
tasks](how-to/writing-tasks.md) and [drivers](concepts/drivers.md).

**bazel-diff.** The tool that lists the Bazel targets whose hash changed
between two commits. pipelinegen downloads a pinned release of it. See
[what Bazel does](concepts/bazel.md#bazel-diff).

**Bind.** How a suite's slices are tied to code: `graph` (the Bazel graph
alone) or `recorded` (also what each slice executed). See [recordings and
lists](concepts/recordings-and-lists.md#bound-by-the-graph-or-by-recording).

**Bundle.** The slices one invocation of a test tool runs, inside a batch:
a package's Go test files in one `go test`, a batch's targets in one
`bazel test`. Each slice of a bundle keeps its own result and recording.
See [bundle a suite's slices, or run each alone](how-to/batching.md).

**Can't tell.** A plugin's answer when it can't work something out from
files. pipelinegen then binds wide. See [links and
correlators](concepts/links-and-correlators.md#when-a-plugin-cant-tell).

**Change.** The difference between a base and a head commit. See [changes
and tests](concepts/changes-and-tests.md).

**Check.** `pipelinegen check`: makes sure every file of the repository is
covered by a slice or listed as untested with a reason. See [the
check](tutorial/check.md).

**Cold rebuild.** The nightly's re-reading of every file by every plugin,
without the cache, compared with the cached graph. See [the
nightly](concepts/the-nightly.md).

**Connector.** The plugin that talks to the git host: proposed changes,
statuses, comments. See [connectors](concepts/connectors.md).

**Correlator.** A plugin that links units to each other and observes which
units a test executed. See [links and
correlators](concepts/links-and-correlators.md).

**Digest.** A hash of what a unit is. A unit changed when its digest
differs between base and head. See [units and sbom
plugins](concepts/units-and-sbom.md).

**Driver.** A plugin that runs a suite's tests: it runs the slices routed
to it, bundled or one by one, and gives a result and a recording per
slice. A driver may also build what a suite's batches share and start
what a batch's tests need. The command driver is built in. See
[drivers](concepts/drivers.md).

**Everything on.** `everything_on`: files whose change runs every slice.
See [narrowing](concepts/narrowing.md#everything-on-and-sample-on).

**Follow.** How far the walk follows uses from a changed unit: `executed`
or `dependents`. See [the graph and the
walk](concepts/the-graph-and-the-walk.md#how-far-the-walk-goes).

**Graph.** The units, their digests and the links between them, for one
commit, kept up to date from the plugins' cached answers. See [the graph
and the walk](concepts/the-graph-and-the-walk.md).

**Head.** The new commit of a change. See [changes and
tests](concepts/changes-and-tests.md).

**Helper.** A file that matches a suite's test patterns but holds no test
of its own. See [suites, slices and
batches](concepts/suites-and-slices.md#slice).

**Hidden refs.** The git refs under `refs/pipelinegen/` where pipelinegen
keeps its records. See [the store](concepts/the-store.md).

**Link.** A typed edge from one unit to another it uses, reads, writes or
calls. See [links and correlators](concepts/links-and-correlators.md).

**Lists.** Every slice's recording for one commit, kept in a hidden ref.
See [recordings and lists](concepts/recordings-and-lists.md#the-lists).

**Narrowing.** A rule that runs fewer slices for one kind of change, and
the level from 1 to 5 that turns narrowings on and off. See
[narrowing](concepts/narrowing.md).

**Nightly.** A run that compares with nothing, selects everything,
records, and rebuilds the graph cold. See [the
nightly](concepts/the-nightly.md).

**Owner.** The Bazel target that says what a test file depends on: by
default the test target that holds the file. See [what Bazel
does](concepts/bazel.md#bazel-diff).

**Plan.** What a run runs: per suite, the selected slices with their
reasons and the untouched ones. `pipelinegen affected` prints one.

**Plugin.** A separate program, pinned by version and checksum, that knows
one language, tool, CI system or git host. There are six kinds: sbom,
correlator, runner, driver, connector and store; the store is still a
design.

**Prepare task.** An optional CI task that runs once per suite job before
its batches (builds, installs). For a suite with steps, it runs the
suite's prepare steps.

**Reason.** Why a slice was selected: the path from the change to one of
its roots, or why everything ran. `pipelinegen why` prints it.

**Recording.** The units one slice executed in one run. See [recordings
and lists](concepts/recordings-and-lists.md).

**Run.** One pass over the selected slices of one change, declared by name
in the config (`pr`, `main`, `nightly`). See [runs and the
pipeline](concepts/runs-and-the-pipeline.md).

**Run of everything.** A run that compares with nothing and selects every
slice, such as a nightly or a round of a continuous run. When it passes,
each slice's recording replaces its list. See [refreshing the
lists](concepts/refreshing-the-lists.md).

**Runner.** The plugin that writes the pipeline for a CI system. See [runs
and the
pipeline](concepts/runs-and-the-pipeline.md#the-runner-and-the-generated-pipeline).

**Sample.** A few slices of a suite, spread over it, that run instead of
the whole suite for a change every slice exercises the same way. See
[narrowing](concepts/narrowing.md).

**sbom plugin.** A plugin that lists the units in the files it
understands. See [units and sbom plugins](concepts/units-and-sbom.md).

**Slice.** What pipelinegen selects, runs and reports on its own: a test
file, or a tagged Bazel target. See [suites, slices and
batches](concepts/suites-and-slices.md#slice).

**Step.** One entry of a suite's `steps`, run by a driver: a prepare step
(once per build of the suite), a set-up step (once per batch, stopped when
the batch ends) or a run step (the batch's slices routed to it). See
[drivers](concepts/drivers.md#a-suites-steps).

**Suite.** One kind of test with one way to run it. See [suites, slices
and batches](concepts/suites-and-slices.md#suite).

**Tagged suite.** A suite whose slices are the Bazel targets carrying a
tag, for checks that aren't test files (lint).

**Target.** A thing a Bazel BUILD file declares, named by a label such as
`//text:text`. See [what Bazel does](concepts/bazel.md).

**Unit.** A piece of the repository a change can touch and a test can
depend on: a function, a table, an API operation, a module. See [units and
sbom plugins](concepts/units-and-sbom.md).

**Untested.** Files the config lists as covered by no test, each group
with a reason. See [the check](tutorial/check.md).

**Untouched.** A slice the change doesn't reach; it doesn't run. See
[changes and tests](concepts/changes-and-tests.md#reaching-a-test).

**Walk.** The search from each changed unit backwards over the links to
the slices that reach it. See [the graph and the
walk](concepts/the-graph-and-the-walk.md#the-walk).

**Whole-suite check.** A check that judges a suite as a whole (an API
coverage guard) and runs only when every slice of its suite ran. See
[adding a suite](how-to/adding-a-suite.md).

## The narrowings by name

A plan's reasons and `pipelinegen why` name a narrowing when it left a
slice out ("the narrowing doc-text left it out"). Each is off from a
level ([narrowing](concepts/narrowing.md)): at that level and above, the
strict rule (the "wide rule" in `why`) applies instead. 5 is the highest
level, so "would have run at level 5" means only with every narrowing off.

| Narrowing | Declared by | Off from | What it does while on |
|---|---|---|---|
| `sample` | pipelinegen | 3 | a change to code every slice ran the same way runs a sample of the suite |
| `sample-bound` | pipelinegen | 3 | the same for what every slice reaches only through that code, or what runs at start-up |
| `plugin-entry` | pipelinegen | 4 | a changed plugin entry (a pin, a checksum, its config block) runs a sample of each suite it reaches, not every slice it reaches |
| `rows-to-readers` | pg-sbom | 2 | a slice that only wrote a table binds the table's definition alone, not the rows other code reads |
| `document-units` | openapi-sbom | 2 | a test that depends on a contract file as a whole binds only the operations it touched |
| `setup-split` | go-correlator | 3 | what a batch's setup ran binds the suite's sample, not every slice |
| `unreached-new` | go-correlator | 3 | a new declaration nothing reaches selects nothing |
| `ts-functions` | react-correlator | 4 | a spec binds the functions it ran, not every unit of their files |
| `method-sets`, `test-method-sets` | go-correlator | 5 | a type binds only the methods a slice can reach, not all of them |
| `pure-initialisers`, `pure-test-variables` | go-correlator | 5 | a variable binds through the code that uses it, when its initialiser can't act on its own |
| `referenced-new` | go-correlator | 5 | a new declaration a bound one refers to counts as reached |
| `module-bumps` | go-correlator | 5 | a slice binds the modules its binaries link, not every module required |
| `module-graph` | go-sbom | 5 | a slice binds only the modules its code links |
| `types-erased` | react-correlator | 5 | TypeScript types bind no test |
| `migrations-by-table` | pg-sbom | 5 | a slice that depends on a migration file depends on the tables of it the slice touched |
| `gorm-models`, `gorm-associations`, `gorm-model-types` | gorm-correlator | 5 | GORM statements bind the tables and hooks they use, not every table of a model |
| `doc-text` | openapi-sbom | 5 | an API contract's digests leave its documentation text out |
| `generated-from-source` | oapi-codegen-correlator | 5 | code generated from the whole contract changes only with the contract's shared parts |

Each plugin's README explains its narrowings and why each is off where it
is.
