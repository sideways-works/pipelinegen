# Design

pipelinegen makes a CI pipeline run only the tests a change can break. Every
test file is a slice of its suite. For a change, pipelinegen walks the
stored graph of units and links (from its sbom and correlator plugins)
outward from what changed to the slices whose recordings or own files
reach it, joins that with what bazel-diff says changed in the Bazel graph,
and a runner plugin writes a pipeline (for Concourse) that runs those and
nothing else. The nightly run runs every slice.

It knows nothing about the repository it serves beyond a small config file and
the Bazel graph.

## What must hold

1. A change runs exactly the slices it reaches. A change to one slice's own
   tests runs that slice. A change to a frontend app runs the slices that
   open that app. A change to backend code runs the slices that depend on it.
2. Every suite works this way: browser tests and backend tests alike.
3. Once a night every slice of every suite runs, whatever changed.
4. The first run, and any run without a usable earlier state to compare with,
   runs every slice.
5. Nothing escapes a slice. Every tracked file is bound to a slice or
   declared untested with a reason, and every test file is in at least one
   slice: `pipelinegen check` refuses anything else in the commit hook, and
   planning catches it if it got through, failing the run or running every
   slice with a warning (`uncovered: fail` or `everything`, a setting). What
   cannot be known before a run
   (no earlier passing run, a changed build setup, a failed query) selects
   every slice.
6. bazel-diff is the only source of "what changed". There are no path lists
   kept by hand.
7. The pipeline is generated. Nobody edits slice jobs by hand, and a new
   slice needs no pipeline change.
8. One result per suite reaches the pull request or commit: which slices ran,
   which were left out as untouched, what failed.

## Terms

- **Suite**: one kind of check with one way to run it (e2e, backend, a
  linter). A check that is not a test, such as lint for one language, a type
  check or a migration lint, is a suite of one slice, so nothing needs a
  second mechanism. Only checks of the change itself (a commit message, a
  pull request title) stay outside, in a file the generated pipeline
  includes.
- **Slice**: the unit of selecting and reporting. In a suite of tests every
  test file matching the suite's `tests` patterns is a slice, named after the
  file; nobody declares, tags or checks one in. A suite whose units are not
  test files (a lint, a type check, a build) has explicit slices instead:
  Bazel targets carrying the suite's `tag`. A suite has one or the other.
  A slice's dependencies are everything that can change its result.
- **Whole-suite check**: a job that judges a suite as a whole, such as an API
  coverage guard or a money reconciliation after all browser tests. It runs
  only in runs that selected every slice of its suite (the nightly, a first
  run), and also whenever a change reaches one of the Bazel targets it names.
- **Run**: one pass over the selected slices for one change. pipelinegen has
  no fixed kinds of run: the config declares runs by name (a typical repository's are
  `pr`, `main` and `nightly`), and each says in data what starts it, what it
  compares with, what it selects, which suites it covers with which
  parameters, whether it records, and where it reports.

## The repository's side

A file `pipelinegen.yaml` at the root:

```yaml
pipeline: shop                    # the pipeline's name
bazel: {workspace: ., bazelisk: tools/bin/bazelisk}
bazel_diff: {version: v67.0.3, sha256: 98d7...8e4e}   # the default; optional
github: {repository: example/shop, token: github_token}
concourse:
  url: https://concourse.example
  repository:                     # the git resource's source, branch set per run
    uri: https://github.com/example/shop.git
    username: x-access-token
    password: ((github_token))
  planner:                        # pipelinegen's own steps
    image: {repository: golang, tag: "1.27.1"}  # a stock image
    digest: sha256:...
    bootstrap:
      release: {version: v0.2.0, sha256: ...}   # or commit: <sha>, before the first release
      bazelisk: {version: v1.30.0, sha256: ...}
  include: ci/root.yml            # the repository's own root jobs
plugins:
  - name: concourse               # the runner, which writes the Concourse pipeline
    kind: runner
    repository: sideways-works/pipelinegen-runner-concourse
    release: {version: v0.2.0, sha256: ...}
    config:
      placement:                  # which workers each step runs on, in the runner's words
        default: [cloud-1]
        rules:
          - {for: suite, suite: e2e, tags: [cloud-2]}
keep: {runs: {for: 5d}, lists: {for: 90d}}   # which runs and lists the sweep keeps
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts"]   # every matching file is a slice
    owner: "//{dir}:{stem}"       # the target that holds a test file's graph edges
    bind: recorded                # also bound to what each file executed
    prepare: ci/tasks/e2e-prepare.yml
    run_slice: ci/tasks/e2e-batch.yml
    lock: {name: browsers, minutes: 120}
    parallel: 3                   # batches dealt, all run at once
    durations: ci/durations/e2e.json
    whole_suite:
      - name: money-reconciliation
        task: ci/tasks/reconcile.yml
        also_on: [//backend/internal/ledger:ledger]
  backend:
    tests: ["backend/**/*_test.go"]
    bind: recorded
    prepare: ci/tasks/backend-prepare.yml
    run_slice: ci/tasks/backend-batch.yml
  lint-go:
    tag: slice-lint-go            # explicit slices: targets with this tag
    run_slice: ci/tasks/lint-go.yml
runs:
  pr:
    trigger:
      pull_requests: {base: main, draft: false, labels_without: [train]}
    compare: merged_target        # merged_target | last_passed | none
    select: affected              # affected | all
    suites:
      e2e: {params: {PROJECTS: "phone-dark,desktop-light"}}
      backend: {}
      lint-go: {}
    record: true
    report:
      status: true                # one commit status per suite
      comment: {format: mermaid}  # optional: list | mermaid
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {e2e: {params: {PROJECTS: "phone-dark,desktop-light"}}, backend: {}, lint-go: {}}
    record: true
    report: {status: true}
  nightly:
    trigger: {timer: {start: "3:00", stop: "4:00", location: Europe/Berlin}, branch: main}
    compare: none
    select: all
    suites: {e2e: {params: {PROJECTS: all}}, backend: {}, lint-go: {}}
    record: true
    report: {status: true}
everything_on:                    # belt and braces for rule 5
  - pipelinegen.yaml
  - MODULE.bazel
  - .bazelversion
  - ci/tasks/**
```

What each run declares:

- **trigger**, one of:
  - `branch: B`: every new commit on B;
  - `pull_requests`: open pull requests into `base`, filtered by data:
    `draft` (true, false or absent for both), `labels` (all of these),
    `labels_without` (none of these), `head` (a pattern for the head branch).
    A repository that tests several pull requests together in one batch
    pull request on its own branch pattern declares a run for those with a
    `head` pattern; pipelinegen knows nothing of batches;
  - `timer` (a daily window, as Concourse's time resource takes it) on a
    branch's tip;
  - `manual` on a branch's tip: a planning job started by hand.
- **compare**: `merged_target` (the change merged into the target branch's
  tip, compared with that tip; pull requests only), `last_passed` (the newest
  earlier commit on the branch whose run of this name passed, read from the
  commit statuses; with none, the newest whose run is still going, which
  plans everything and makes the lists, so the commits pushed meanwhile
  don't each run everything; neither means rule 4; while `passed-since` is
  on, a slice that passed later in the same run is compared from there) or
  `none` (rule 4: everything).
- **select**: `affected` or `all`.
- **suites**: which suites run, each with parameters that merge over the
  suite's own (`PROJECTS: all` at night, two screen types otherwise).
- **record**: whether the run's recorded suites record what their slices
  executed (see "Keeping the lists current").
- **report**: `status` (one commit status per suite, the default) and,
  optionally, `comment` with `format: list` or `mermaid` (pull request runs
  only). A repository may pass extra pull request numbers to post the same
  comment to (`also_to`, given to the planning job at run time).

The task files are the repository's own, in two parts per suite:

- **prepare** runs once per run of the suite: builds, databases, servers'
  binaries, installed packages. Its output is an artifact the batches reuse.
- **run-slice** runs one batch: one setup, then the batch's slices in turn,
  writing a result per slice (passed or failed, the failing test names and
  the last lines of each failure). Databases, services, race flags and the
  choice between `bazel test` and `go test` are all the repository's. Go test
  files run by name: the tool gives each slice its top-level test names, so
  the package's test binary is compiled once per batch.

pipelinegen never learns what Playwright or `go test` is, and nothing about
slices is checked in: a new test file is a new slice by being there.

## Commands

- `pipelinegen slices [--suite S]` lists the slices: the test files matching
  a suite's patterns, or the targets carrying its tag.
- `pipelinegen affected --base REV --head REV [--suite S]` prints the slices
  the change reaches, and why. It makes bazel-diff hashes for both revisions
  (in one reused work tree, cached by commit), takes the impacted targets and
  keeps the slices among them. `--all`, a missing base, a base that is no
  ancestor, a changed `everything_on` file, or a base or a bazel-diff run that
  fails gives every slice, with the reason.
- `pipelinegen check [--staged]` fails when a tracked file is bound to no
  slice and not declared untested, or a setup or config error stands. It
  reads BUILD files and git, no Bazel server, so it runs as a commit hook;
  planning runs the same code on the whole tree first and, by the
  `uncovered` setting, fails the run or selects every slice with a warning.
- `pipelinegen pipeline` writes the repository's one pipeline from the
  config, through the configured runner plugin.
- `pipelinegen plan --run NAME ...` is what a planning job runs: it finds the
  change and its base as the run declares, runs `affected`, and writes the
  plan ref and the pending statuses.
- `pipelinegen report` keeps the run's state and turns it into the commit
  statuses and the comment of rule 8.
- `pipelinegen learn --stub` writes the stub deps repository a fresh clone
  needs. Turning a slice's coverage into what it executed happens at the
  slice's end, and writing the slices' bindings for a tree at planning (see
  "Binding slices to the code they run").
- `pipelinegen durations` turns `go test -json` output or Playwright's JSON
  report into the durations file batches are dealt by.

## How a run happens on Concourse

Each repository has **one** generated pipeline, static until its config
changes. The selected slices never appear in it: a suite job learns them at
run time, as batches, from the run's plan. `pipelinegen pipeline` writes it
from the config:

- **per run**: the resource that starts it, a planning job, and one job per
  suite of the run (`<run>-<suite>`);
- **pipeline**: a job that runs when `pipelinegen.yaml` or the file it
  includes changes on the default branch and fails when the pipeline set
  is stale, so a person sets it; with `concourse.self_set` (on a Concourse
  that resolves `((name))` references itself) it sets the pipeline, never
  passing a variable's value;
- the repository's own parts from `concourse.include`: a pipeline file whose
  `resource_types`, `resources`, `groups` and `jobs` are merged in (the
  commit message and title checks; during a switch-over, the old jobs, which
  then share serial groups with the generated suite jobs through each
  suite's `run_on.serial_group`, so nothing runs twice on a worker).

What starts a run:

- `branch: B`: a git resource on B. Concourse takes the newest commit; a
  commit skipped in a burst is covered by the next one's run, which compares
  with the last commit that passed;
- `pull_requests`: a pull request resource (the maintained fork of
  telia-oss/github-pr-resource), filtered by the trigger's data, one version
  per head commit; the planning job and the suite jobs take every version;
- `timer` on a branch's tip, and `manual` (a planning job started by hand).

The **planning job** of a run checks the change out (for a pull request, its
head merged into the target's tip), plans it, and writes the plan once,
create-only, to its ref; it sets every listed suite's status to pending, or
to success with "nothing selected". A head it must not run (`once_per_ready`
and the like) gets no plan.

A **suite job** takes the same version (`passed: [plan-<run>]`). It reads
the plan ref for its version; without one it ends in seconds and posts
nothing (the gate is an `across` over an empty list, so nothing runs and
nothing fails). Otherwise: the tree under test, the lease (between
repositories only), the prepare step, then the batches: a pipelinegen step
deals the plan's selected slices of the suite into batches (at most
`parallel`, by duration), and `across` runs one batch task per batch, all
at once (`max_in_flight: all`), without fail fast. Each batch task reports every slice as it starts and ends, so the
comment moves slice by slice; an `ensure` on it closes slices left without a
result. Whole-suite checks follow when selected. The suite's status and the
run's aggregate are set last, in the job's `ensure`. A suite with nothing
selected in the plan posts "nothing selected" and ends.

- Whole-suite checks run after the batches, so a failed slice skips them
  (Concourse runs nothing after a failed `across`); the report says "not
  run: a slice failed". An `ensure` must sit on a step inside the `across`:
  on the `across` step itself it can't see the iteration's var.
- **Reruns** are Concourse's own "rerun build" of a suite's build: it keeps
  the version, so it reads the same plan, and the dealing step leaves out
  slices that already passed for that change (from the state refs). It runs
  exactly what did not pass, errored slices included (an infrastructure
  failure reruns like a test failure), and those show as running again.
- When every suite of the run has passed, the last suite job to finish
  attaches the lists (for a branch's run) and posts the final report. A
  branch's run that failed attaches too, once its last suite has ended:
  the recordings of the slices that passed go in, and every other slice
  keeps what the lists held.
- Builds of different changes queue on a suite job, or run side by side,
  as the suite's `run_on.serial_group` and `max_in_flight` say; the lease
  lock is only for machines shared with another repository.
- Task caches (the Go, npm and Bazel caches) belong to a job in Concourse,
  so a static pipeline keeps them warm from run to run.
- When the config changes, the pipeline is set again (by a person, or by
  the `pipeline` job with `self_set`).
  Running builds keep the plan they started with and finish (a running build
  of a job that a new config removes finishes too); pending builds start
  with the new config.

The repository's task files take these inputs and outputs:

- prepare: input `source` (the change's tree), output `prepared`;
- run-slice (the batch task): inputs `source`, `prepared`, `batches` and
  `tools`, outputs `result` (one file per slice) and, for recording runs,
  `coverage` (one directory per slice), with `PIPELINEGEN_*` params and the
  run's and suite's params;
- a whole-suite check: inputs `source` and `prepared`, output `result`.

[contracts.md](contracts.md) holds the exact names and formats.

## How a run reports

Each suite has one commit status (context `pipelinegen/<run>/<suite>`): pending
with "3 of 12 slices done", then success, failure ("2 of 12 slices failed (5
tests), 28 untouched") or error, followed by each narrowing that left
slices out and how many (", plugin-entry left out 207"). A run with `report.comment` also keeps one
comment per pull request and run, made when the run starts and rebuilt as
slices start and end.

Slices run in parallel, so no update patches the comment. pipelinegen
keeps no state store: a run's live state is a set of temporary git refs in
the code repository, under `refs/pipelinegen/runs/<run>/<change>/<commit>/`,
one ref per slice (its state, and its recording on the way to the lists),
each written only by that slice, so writers never contend. The plan is a ref
too, written once by the planning job. A render lists the refs by prefix and builds
the whole comment from the plan and every slice's state; the last write is
always the newest state, since each slice writes its final state before it
renders. The comment is found again by a hidden marker line,
`<!-- pipelinegen:<run>:<pull request> -->`, never by author or position.
Nothing is deleted when a run ends; the sweep removes runs `keep` no longer
keeps.

`format: list` shows each suite with its slices and their state;
`format: mermaid` draws a flowchart GitHub renders: the plan, each suite, its
slices coloured by state, and the untouched ones as one node per suite. A
chart past 400 edges or 40,000 characters (under Mermaid's limits of 500 and
50,000) falls back to the list. Both list the failed slices under it with
links to their builds (`/builds/<id>`), the failing test names and the end of
each failure's output, capped so the comment stays under GitHub's 65,536
characters.

### What we tried first, and why we left it

The first shape was one pipeline per run, set by the planning job with
`set_pipeline` (an instance per change). It worked, and these held on
Concourse 8.2.4 (throwaway pipelines, builds named by id):

1. A planning job's next successful build archives every pipeline it set
   before (paused, config cleared); running builds finish (911092) but no
   later job starts, so the report never came. A run had to adopt itself
   first (`set_pipeline: self`, which only re-parents it: run 1794518880
   survived, its report 911228 ran).
2. Setting a pipeline again without a job lets that job's running build
   finish (911190).
3. A run's jobs start by themselves on a commit pinned on the resource
   (`version:` on the resource; a get step's `version:` never starts on an
   unchecked commit) (911315).
4. A run retires itself by setting its pipeline without the job doing it;
   GC archives it while that build finishes (911357).
5. `across` over values loaded at run time works, honours `max_in_flight`,
   runs every iteration when one fails (`fail_fast: false`), and gives each
   iteration its own outputs (912961); the job fails when an iteration
   fails, and steps after the `across` don't run.

We left it because task caches are keyed by job (atc/exec/task_step.go:
`InitializeTaskCache(ctx, JobID, step name, path)`): every run's new jobs
started with cold Go, npm and Bazel caches, slower than the jobs it was to
replace. It also needed adoption, archiving, a clean-up of archived
instances, and a pipeline whose size grew with the slices. The static
pipeline keeps the fifth fact and drops the rest.

## Slices share a setup

A slice is the unit of selecting and reporting, never of running. A run
deals each suite's selected slices into batches, at most the suite's
`parallel`, balanced by recorded duration; a batch is one task with one
setup (one database, one seed, one stack) that runs all its slices in turn.
A full run costs `parallel` setups, a small change one. Slices can therefore
be small: their size no longer pays for a setup. Recording still works per
slice inside a shared stack: the stack's binaries write and clear their
coverage counters between slices when a correlator's `collect` asks them
through files (contracts (b)); the core itself never talks to a tested
process. What the setup executed belongs to every slice of the batch.

## Binding slices to the code they run

Planning walks the stored graph outward from what changed
([internals/graph.md](internals/graph.md)). The deps repository and the
selectors this section describes are how a build with the
`pipelinegen_forward` tag binds, for the proof's comparison; what is
recorded, and the rules of what a slice depends on, hold for both.

A slice that starts a whole server would, by the build graph alone, depend on
all of it: every backend change would reach every slice. That is not
acceptable, so a slice depends on the code it actually runs, and that is
recorded, never listed by hand. This is also what ties a backend change to
the browser tests that reach it: a changed route runs the spec files of the
screens that called it, because those files executed it. No explicit slice
is needed for that correlation.

For a test file, the selector bazel-diff sees is generated at plan time in
the hidden deps tree, never in the repository. It holds the file itself, the
file's graph edges (from the target that owns it, named by the suite's
`owner` pattern where the repository has a target per file, such as one per
spec with its imports, the files it reads and the apps it opens; for Go test
files from go-correlator: their declarations as closure roots, the helper
edges between test files, declared data; otherwise the file's package
target), and its recorded dependencies in both widths.

### What is recorded

Every run that records a suite, not only the full one, runs its slices
against binaries built with coverage, and each slice leaves the coverage
data of what it executed: for a browser slice the servers and workers it
started, for a backend test slice the code under test. The repository's
task produces the data, and at the slice's end the correlators that
observe the suite turn it into the list of units the slice touched; for Go,
from a `GOCOVERDIR` or a coverprofile, the top-level functions and methods
holding a covered block.

That list is all that is kept. What it expands to depends on the tree, so the
expansion is never checked in.

### What a slice depends on, per commit

By file, the stack's start, seed and stop alone execute almost half of a
backend, so a slice is bound by declaration, not by file. When the planning
job plans a run, it writes for the base tree and the head tree alike (cached
by commit), and only then asks bazel-diff:

- a **digest tree**: one small file per top-level declaration (function or
  method, type spec, const or var spec, init) holding the hash of its printed
  syntax tree without comments, but with `//go:` directives and struct tags.
  A comment-only edit changes no digest. Closures and anonymous functions
  belong to their declaration, generic instantiations to the generic one.
  The tree lives outside the repository's own Bazel packages, in a
  directory git and gazelle ignore, so no wildcard build or gazelle run ever
  picks it up;
- per slice, a target depending on its own sources and declared data, and
  on the digests of:
  - the declarations it executed (recorded);
  - their **static closure in that tree**, from the Go type checker: the
    types and constants they reference, closed over type definitions (a
    type's digest covers every method's name, receiver and signature, so an
    added `MarshalJSON` changes it); the methods of those types that have no
    statements, and every function they reference that has none (no
    coverage block, so they never show as executed); a named variable
    through its references only when its initialiser calls nothing or only
    side-effect-free constructors from a short configurable list (in
    go-correlator's config), otherwise it is bound by every slice linking its
    package; and every init and blank variable with calls in any package
    the binary links.

So an added `MarshalJSON`, a changed struct tag, constant or initialiser is
in the closure of the tree it was added to, and the slice is selected.
Unchanged code reaches new code through method sets and reflection as well
as calls, which is why the closure is needed.

A package that uses reflection by name, `go:linkname`, cgo or build tags is
bound whole; the generator detects these and widens. Binding a package whole
is also a switch per package, for where declarations don't pay.

Frontend code is bound per file through recorded file units (below); a
spec without a recording keeps its graph edges to the apps it opens.

A slice's declared data are explicit edges and stay whatever the lists
say: its owner's `data` (by the owner template, else every test rule that
takes the file as a source), `extra_deps`, and `pipelinegen:data` lines in
the file's first 30 lines. A test that reads Go sources as text keeps them
this way. Data on a target binds every test file the target owns, so a read
only some files make belongs in those files' comments.

A dependency on a whole file that an sbom describes in parts (the embedded
migrations, an API contract) becomes, while that sbom's narrowing is on, a
dependency on the parts the slice was observed to touch, plus the file's
whole unit where the reader takes it whole; with the narrowing off it is
the whole file ([plugins](plugins.md)). Otherwise every backend test,
whose build embeds the migrations, would bind every table.

### A change can't deselect itself

Planning reads the change's own config, so a change could drop an
`extra_deps` entry, a suite, a tests pattern or an exclude, and with it the
tests that would catch the change. So when the config differs between the
compared base and the change, the plan is the union of what each config
selects, both evaluated on the change's trees. A slice only the base's
config selects carries the reason "selected by the base's configuration";
a suite only the base's config has runs as the base defined it, and its
slices whose files are gone are reported as removed. A change can add
selection but never remove it for itself; a removal takes effect from the
next change on. How Bazel runs (`bazel`) is taken from the change.
A repository that lists its config in `everything_on` runs everything on
any change to it, and the union never comes into play; the tool doesn't
add its config there by itself.

The base's config is history, and history always loads: a key this
pipelinegen no longer reads or now refuses, a key it never knew and a
value a rule now forbids become warnings in the plan, never a failure.
Only the head's config is held to today's rules. A base config that can't
say which suites and test files it had, or under which planning fails,
makes every slice run, since what it selected can't be known; a change
can't deselect its own tests by making the base unreadable. A trusted run
(the default branch's own, whose config is the branch's) plans under its
own config alone instead and warns of it, so one unfit base config never
stops the branch. A suite job reads the config of the commit it
tests the same way, since an older pipeline may have planned it; one that
can't be read at all closes the job's gate and reports nothing.

For each tree, planning runs the slices query and the plugins (from their
answer cache where it can), then writes the deps repository from what they
said, the slices and the lists.

### Correlators

What knows a language or a domain (Go's declarations, TypeScript's
functions, SQL tables, API operations) is a correlator; what knows a
package ecosystem is an sbom plugin; what knows a CI system is a runner.
Each is an executable of its own that the core calls through a small
protocol; see [plugins.md](plugins.md). Planning and recording go through
them, and go-correlator binds Go the way this section describes; without
sbom and correlator plugins, a suite binds by its owners' graph alone. The
core keeps the deps repository, the closure over links, the ladder and the
checks.

### Files as units: the frontend

react-correlator binds the frontend.

A web app is bound per file, not per declaration: a slice's recording may
also list plain files (`coverage/<slice>/files.txt`, repository paths, one
per line), which the repository's task produces (for a browser suite: the
browser's JavaScript coverage per spec mapped through source maps, plus the
static files the page requested). The lists in the refs hold both kinds.
A file unit is bound through a digest of the file in the deps tree, like a
declaration's. The plugin that lists them normalises each before hashing,
so that a comment-only edit selects nothing:
TypeScript, JavaScript and CSS are printed by esbuild (a Go library) without
comments and with minimal whitespace; other files by content. A file unit
nobody recorded follows the declarations' rule: existing and unrecorded
selects nothing, new selects the suites that can load it.

What a recording can't see on the frontend (build config, global CSS, an
app's server-side code) stays bound by the graph through the repository's
per-file targets. For a spec with a recording, the owner's edges to targets
whose sources are all file units are dropped, so the recorded files replace
the app-wide edge, and what remains (build config and the like) stays. A
spec without a recording keeps the app-wide edge. `why` and the reasons name
the file.

### How far a change is followed

`follow` (global, per suite, per run) sets the width:

- `executed`: a slice runs when a declaration it executed, or its closure,
  changed. This is the setting that narrows.
- `dependents`: a slice also runs when anything that statically reaches a
  changed declaration is among what it executed (callers and users,
  transitively, with an optional `follow_depth`). This is the conservative
  setting: it covers branches the recorded run didn't take.

Measured on a backend of about 21,000 declarations (type-checking and
digests 4 s per tree, hashing 1.6 s more with the deps tree): under
`executed`, a change to a leaf function selects 1 of 14 API test slices, a
mid-level service function 6 to 9, a shared helper all of them; under
`dependents` every change selects all 14, because the router reaches the
whole server.

### Declarations no slice is bound to

Every tracked file belongs to some Bazel target, so this rule is
pipelinegen's, per suite and by declaration:

- a changed declaration that existed when the lists were recorded, and that
  no slice executed or reaches through its closure, selects nothing in that
  suite;
- a new declaration that no slice's closure reaches selects every slice of
  the suite that runs the binary it is compiled into (calls into it come
  from changed declarations, which select their slices already);
- every full run lists, per package, the declarations no slice executed, in
  its summary;
- files outside every suite's inputs (docs) select nothing.

### Keeping the lists current, with no window

Nothing of the lists is in the tree, so nothing generated ever shows in a
diff and the pipeline never commits. The lists of what slices executed are
attached to the branch's commits as hidden refs,
`refs/pipelinegen/lists/<full commit id>`, each pointing at a parentless
commit whose tree is `<suite>/<slice>.txt` (git shares unchanged files
between those trees, so a ref costs only what changed).

- **Lookup.** Planning against commit B uses lists(B): B's ref, else the
  nearest first-parent ancestor's within a bounded walk; none means every
  recorded slice is bound to everything, as on a first run. Base
  and head trees are generated with the same lists, so selection stays a
  function of the commits. To close the window between a merge and its
  lists, planning also unions in the recordings of every run whose refs
  still exist, passed or still going: that can only select more.
- **Attaching.** `pipelinegen lists attach --run R --change C --commit
  <tested> --to <branch commit>` creates, create-only, the target commit's
  ref: lists(nearest ancestor of the target) plus the run's recordings. A
  run that selected every slice and has data for a slice may shrink that
  slice's list; otherwise lists only grow. Each slice is judged by its own
  state: one that didn't pass keeps its list. A branch's run calls it when its last
  suite passes, for the commit it tested, and when its last suite ends in a
  run that failed, for the slices that passed. For pull requests the repository's
  merge automation calls it right after merging, with the new commit (a
  batch of pull requests: once, onto the last merge). Something that reached
  the branch without the pipeline gets its lists from the branch's own run.
- **Reading.** `pipelinegen lists show [--commit X] [--slice S]` prints a
  commit's lists, and `pipelinegen why --run R --change C --commit X --suite S --slice N` says why a slice did or
  didn't run for a change; nothing of this is visible on GitHub.
- **Housekeeping.** The sweep that removes runs `keep.runs` no longer keeps
  also prunes lists by `keep.lists` (never a branch's newest); an older
  commit then falls back as above.
  Backups and mirrors must include `refs/pipelinegen/*`; losing them is safe,
  since runs then select everything until they record again.
- **Local use.** `affected` fetches the refs it needs by name; a fresh clone
  needs nothing but the deps stub.

Not in a release yet: naming, in the run's summary, a slice that executed
a declaration outside its inputs.

### Requirements on the repository's tasks

- **No background work during the slices.** Code run by timers in a
  long-running stack (scheduled jobs at start, work every few seconds) lands
  in whichever slice is current and pollutes its recording. Periodic work
  is off, runs on request, or finishes before the setup snapshot. Not in a
  release yet: a full run's summary flagging declarations that appear in
  many slices' recordings with identical counts as likely background work.

- **One setup per batch.** Prepare only builds. Each batch starts its own
  database and stack, has the stack's processes write and clear their
  coverage counters between slices, and stops them cleanly at the end.
- **Missing data is not an empty list.** A slice whose coverage files are
  missing can't tell what it ran: it keeps its list as it was and runs on
  every change until it is recorded again. Tests that ran and touched
  nothing at all are taken the same way, unless the suite sets
  `empty_recordings: allow`; only then does an empty recording become an
  empty list.
- **Pipelinegen's own steps** run on a stock image (git, certificates; a Go
  image by digest) and fetch the pinned pipelinegen release themselves, its
  checksum checked, kept in a task cache; before the first release they
  build it from a pinned commit.
- **Files tests read at run time** are declared as data, and backend slices run
  where an undeclared read fails (`bazel test`'s sandbox).
- **Before the first recording** a slice depends on everything it starts,
  which is rule 4: the first run runs every slice and produces the lists.

One package with thousands of tests needs no cutting: each of its test files
is a slice, bound to the code its tests executed. A change to one handler
runs the test files and spec files that executed it, not the suite.

## What used to run everything, and the narrowing ladder

The goal is that nothing but the nightly runs everything. Replaying
the last 60 commits of a repository with 240 browser test files, 21 still
selected a whole suite. Each cause gets
a narrower answer, a **narrowing**, and each narrowing weakens the safety
argument a little; the nightly (any run that selects everything) is the
net. Rule 5 now reads: a change run falls back to a whole suite only when it
cannot be planned at all (and says so loudly), or on the first run ever.

Each cause led to one of the narrowings below. This is why each exists;
what a level switches off is in the ladder after it. Each bullet names
who declares the narrowing today: the core (the sample) or a plugin, in
its `describe` answer.

- **setup-split** (go-correlator): what a batch's setup
  executed (start-up, seeding, before its first slice: the `_setup`
  snapshot) binds the suite through `sample`, not every slice; what a
  slice's own tests executed binds that slice through its list. Lazily
  initialised code (`sync.Once`, warmed caches) lands in whichever slice
  runs first in a batch, so a recording keeps both sets for the first slice
  of a batch. Seed and fixture code that writes data was meant to select the
  slices that touch those tables as well (as for migrations), since the data
  it writes matters differently to each test; no plugin declares that yet.
- **sample** (the core): a change to what every slice exercises the same
  (the suite's task files, the build setup, setup-only code) selects a
  sample of the suite (`sample: N`), not all of it: spread over the report's
  groups, stable for a commit, rotating across commits so every slice is
  sampled over time, and always including slices whose own tests executed
  the changed code. `everything_on` keeps selecting everything for a
  repository that wants it (default empty); `sample_on` lists the paths that
  sample. The report says "sampled: N of M; the nightly runs the rest". In
  the deps repository the units every recorded slice of a suite ran leave
  each slice's selector for the suite's own `_sample.<suite>` target; a
  change it selects runs the sample.
- **sample-bound** (the core): coverage sees functions, so a type, a
  constant or a variable that every slice binds through code they all ran is
  still each slice's, and a change to it selects the whole suite. With this
  narrowing a unit that every recorded slice binds only through units
  already in the sample (every unit that brings it along, by the links and
  by the vias a bind correlator lists), or because it runs at start-up (an
  initialiser with effects, an init), goes to the sample too, through
  `_sample-bound.<suite>`. A unit one slice also reaches through its own
  code stays that slice's.
- **plugin-entry** (the core): a plugin's entry in the config (its pin,
  its checksum, its config block) changes how units are listed and
  digested, or what is recorded and bound, not the code. While it is on,
  a changed entry runs a sample of each suite it reaches (an sbom's, the
  suites that bind a unit it lists; a correlator's, the suites it
  observes and those whose bindings hold an answer of its), through each
  suite's `_sample-entry.<suite>` target, and the plan names the slices
  it reached that the sample left out. A unit that changed in its own
  right still selects its slices.
- **unobservable-edits** (the core, on the sboms' word): a changed file
  whose sbom says in both trees that its units cover it whole (`complete`
  in its units answer), with every unit as it was under the same pin of
  that sbom, changed nothing a test running it can observe: the same
  tokens in another layout, a comment no tool reads. It then selects no
  slice of a suite bound by recording through a Bazel edge that nothing
  else the change touched reaches (a direct label, a filegroup holding
  it, `extra_deps`), as bazel-diff says when it hashes the head again with
  the base's copy of the file in place.
  Suites bound by the graph and tagged checks keep the edge, since a
  format check or a lint reads the text. A recorded test that reads such a
  file's text is declared with `text_inputs`, and keeps the edge.
- **passed-since** (the core, on a `last_passed` run): the run's base is
  the newest earlier commit whose whole run passed, so one slice that keeps
  failing keeps every change since in the comparison. A slice that passed
  at a newer commit in the same run, with its tests run, is compared from
  there: for the newest two such commits the plan finds what changed from
  each to the head before the sample is drawn, and leaves out a slice that
  passed at one of them and that the change from there doesn't reach. A
  slice without such a pass, the sample's targets and a suite whose checks
  of the whole suite run keep the run's base.
- **build-files-by-targets** (the core): a recorded slice's filegroups are
  opened down to their files, and a `BUILD`, `BUILD.bazel` or `.bzl` file
  among them is no data file of the slice. A change to a rule still
  reaches it through that rule, whose hash bazel-diff takes from its
  attributes and the Starlark it loads, and a change to a filegroup's
  members as files added or removed. A test that reads such files as text
  is declared with `text_inputs` and keeps them.
- **unreached-new** (go-correlator): a new declaration or file
  unit nothing reaches selects nothing by itself: whatever starts using it
  changed too and selects its own slices. A new method is not new in this
  sense (it changes its type's method set, below); a new initialiser that
  runs at start-up falls under `sample`.
- **fields** (no plugin declares it yet): a slice depends on the struct
  fields and interface methods its executed code selects, and on a type's
  whole shape only where it converts it to an interface (`any`, encoding,
  `reflect`, `fmt`), compares or embeds it. Types that escape to reflection
  stay whole; the generator detects them.
- **method-sets** (go-correlator): a type binds its methods'
  names and signatures, not every method's body; dynamic method lookups by
  name keep a configured list of names whole.
- **pure-initialisers** (go-correlator): a named variable is
  bound by reference unless its initialiser calls outside a configured list
  of side-effect-free functions.
- **referenced-new** (go-correlator): a new declaration that a
  bound one references is not "unreached".
- **module-bumps** (go-correlator, with go-sbom's `module-graph`): a change
  to `go.sum` or a lockfile selects the slices whose linked packages import
  the changed module, transitively over the module graph (third-party code
  runs through callbacks and its own inits, which coverage doesn't see, so
  the binding is by linked packages, not by executed code).
- **document-units** (openapi-sbom, and oapi-codegen-correlator's links): an
  API contract is bound per operation, as units inside the document by a
  configured path pattern (`paths.*.*`), with `$ref`'d components binding
  every operation that refers to them. It narrows only where the generated
  code is bound per operation too (Go handlers are; a generated TypeScript
  client needs per-function units or one file per operation).
- **ts-functions** (react-correlator): frontend and test-helper code is
  bound per function, from V8 coverage (browser and Node, which can snapshot
  and reset with `v8.takeCoverage` like the Go cover package), digested per
  function by a provider: an external command that reads file paths and
  prints `<file>\t<unit>\t<digest>`, with a reference provider on the
  repository's own `typescript` package. Module top-level code counts as
  setup.
- **migrations-by-table** (pg-sbom): a migration selects the slices whose
  executed code mentions the tables it touches (string literals in executed
  declarations, the usual case with a SQL driver; struct-to-table for an ORM
  as a second resolver), and samples when a statement's tables can't be
  resolved. Dynamic SQL, views, triggers and cascades make it unsound alone:
  it stands only with the sample and the nightly.

TypeScript is normalised by esbuild before hashing, which erases types: a
type-only edit selects no browser test. That is intended; the type-check
suite still runs on such an edit, by the graph.

### The ladder

`narrowing: <level>` (global, with a per-suite override) picks how much is
narrowed. Each higher level switches off the narrowings that can miss most;
a narrowing that is off falls back to the wide rule (its whole suite, or the
full closure). The core declares these narrowings itself, with the level
that switches each off:

| Narrowing | Off from level | Why there |
|---|---|---|
| sample | 3 | task files, build setup and setup-only code that writes nothing run the same for every slice, but a sample can still miss a slice they break |
| sample-bound | 3 | what every slice reaches only through code they all ran, or runs at start-up, is exercised as that code is: the sample's own argument, with the same gap |
| plugin-entry | 4 | a correlator's entry changes what is recorded and bound, not what runs, and an sbom's entry how units are listed and digested, not the code: a sample checks the new entry on each suite it reaches, a unit that really changed still selects on its own, and the nightly runs and records the rest; off, what the entry reaches runs, beside any `sample_on` sample |
| unobservable-edits | 4 | it rests on an sbom's parse being complete and on the tests that read a file's text being declared; level 4 is where a repository takes neither from its plugins |
| passed-since | 4 | a slice that passed in the same run at a commit newer than the run's base was tested on every change up to it, so only what came since needs it; it rests on that one pass, a flaky one included, and the nightly runs the rest |
| build-files-by-targets | 5 | a BUILD file's rules reach a slice through those rules, which bazel-diff hashes, and the members it lists show as files added to or removed from a filegroup; it rests only on the tests that read such a file's text being declared, so it stays on up to the level that binds everything by declaration |

Each plugin declares its own narrowings in its `describe` answer, each with
the level that switches it off, and lists them with the reason for each
level in its README's Narrowings section. For example, go-correlator's
`setup-split` and `unreached-new` are off from 3: lazy initialisation lands
in whichever slice runs first in a batch, and a new declaration nothing
reaches selects nothing until something uses it. pg-sbom's
`rows-to-readers` is off from 2, because units below a table (one row of
seed data) rest on data, not on code; its `migrations-by-table` is off from
5, sound because the tables a test touched are recorded; openapi-sbom's
`document-units` is off from 2, because code that reads the whole contract
depends on every operation, not the ones it names.

- **Level 1** narrows the most: the least runs.
- **Level 2** is the everyday setting. Against level 1 it gives up the
  narrowings that rest on data or reflection, and keeps every structural
  narrowing, the setup split and the sample of plumbing.
- **Level 3** also runs whole suites for task files, build setup and
  setup-only code, keeps setup in every slice's list, and lets a new
  unreached declaration select the suites that link it.
- **Level 4** switches off the plugins' narrowings declared off from 4.
- **Level 5** binds by declaration and by file with the full closure, and
  follows dependents (`follow: dependents` everywhere).

The tool's default is level 3; a repository opts into more. Plugins know
their narrowings by name, never by level: the planner maps the level to
the list of names in force and passes it in every request.

### The nightly as the net

A plan records the level it was made under (`"narrowing"`) and, per suite,
each narrowing that applied: `"narrowed": [{"kind": "sampled" | "narrowed",
"narrowing", "off_at", "reason", "reached", "dropped"}]`, where `dropped`
names the slices it left out: a slice under each narrowing whose wide rule
would have selected it. The nightly counts its failing slices that
appear in `dropped` of the branch's own runs since the last such run that
passed (a pull request's plan is tied to its merge commit through `lists
attach`). Above `narrowing_alarm` (default 2) its report opens with those
changes, the narrowing each had, and "raise narrowing to N": the level
that runs every one of them, the highest among the failing slices of the
lowest `off_at` each run named it under; the status says so too. It may also say, as
information, "no failures traced to narrowing in N nightlies"; it never
recommends a lower level, and nothing changes until someone edits the
config.

## Ideas not built

- `fields`: a slice depending on the struct fields and interface methods
  its code selects rather than on a type's whole shape (off from 2).
- Seed code that writes data selecting the slices that touch its tables
  (off from 2).

- Per-operation functions in a generated TypeScript client, bound by V8's
  function ranges and the bundler's inlining (off from 4), so that
  `document-units` narrows on the frontend as it does for Go handlers.
- Method and field units of types that never reach reflection (off from 5).
