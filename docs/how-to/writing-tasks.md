# Writing a suite's tasks

pipelinegen's core never learns how your tests run: a suite's drivers know
it ([drivers](../concepts/drivers.md)), or its own task files do. Task
files are the older form: a suite that has them keeps working, and this
page is their reference. A new suite names steps instead, as the [guide's
step 7](../tutorial/tasks.md) does. A suite with task files has Concourse
task files of your own, and the generated pipeline runs them:

- `run_slice`, the **batch task**: one setup, then a batch's slices in
  turn, writing a result per slice. Required without `steps`.
- `prepare`: runs once per build of the suite, before the batches. Builds,
  installed packages, compiled binaries. Optional.
- a task per whole-suite check, run after the batches when the run selects
  it.

The exact contract is section (a) of [contracts.md](../contracts.md).

A suite whose steps are drivers has none of these files: pipelinegen's
own batch task runs the drivers ([drivers](../concepts/drivers.md)).
[Moving a suite to drivers](moving-to-drivers.md) says how to replace the
task files of a suite that has them.

## The prepare task

It gets the tree under test as `source` and leaves what the batches reuse
in `prepared`. It runs once per build, on the same worker as the batches,
so nothing is copied between workers.

```yaml
# ci/e2e-prepare.yml
platform: linux
image_resource:
  type: registry-image
  source: {repository: golang, tag: 1.27.1}
inputs:
  - name: source
  - name: tools
outputs:
  - name: prepared
run:
  path: sh
  args:
    - -euc
    - |
      tools/pipelinegen wait prepare || true   # while the worker is busy
      cd source
      go build -trimpath -cover -covermode=atomic -coverpkg=./... -tags pipelinegen_cover \
        -o ../prepared/bin/server ./cmd/server
```

`wait prepare` holds the build back while the worker's CPU or memory is
under pressure, under the suite's `cpu.wait` or `memory.wait`, and lets
it go once the pressure eases or its wait is over
([`pipelinegen wait`](../reference/commands.md#pipelinegen-wait)).

Prepare builds; it never starts a server. Each batch starts its own. The
task gets `PIPELINEGEN_RECORD`, so it can build with coverage only when
the run records the suite; the build flags and the `pipelinegen_cover` tag
above are what go-correlator asks for, and its README has them.

## The batch task

Inputs: `source`, `prepared` (when the suite has a prepare task),
`batches` and `tools`. Outputs: `result` and, in a recording run,
`coverage`.

`batches/$PIPELINEGEN_BATCH/slices.json` lists the batch's slices in the
order to run them:

```json
[
  {"name": "e2e/editor.spec.ts", "sources": ["e2e/editor.spec.ts"]},
  {"name": "api/notes_test.go", "sources": ["api/notes_test.go"],
   "tests": ["TestCreateNote", "TestDeleteNote"]}
]
```

`tests` are a Go file's top-level test names: compile the package's test
binary once per batch and run it with `-run
'^(TestCreateNote|TestDeleteNote)$'` for that slice. A tagged suite's
entries carry `label` and `target` (the target to build or run) instead.
`tools/pipelinegen slice list` prints the same, one slice per line,
tab-separated, for a task in plain shell.

First, before its setup, the task calls `tools/pipelinegen slice
memory`: it waits, for a few minutes at most, until the worker has the
memory the batch should use by its slices' recent peaks, and when it
still hasn't, it says why and exits 1, so the task stops and the batch's
end marks its slices not run, to run again later ([sharing
machines](sharing-machines.md#memory-a-batch-waits-for-room)). A suite with
no peaks yet starts at once.

Once the batch's setup is done (the stack started, the data seeded), and
before its first slice, the task calls `tools/pipelinegen slice
setup-end`. In a recording run that takes what the setup executed into
`coverage/_setup`, so each slice's recording holds only what its own tests
ran; there `slice start` and `slice end` refuse to run before it, so when
the setup can't be closed no slice is called passed, and the batch's end
closes them as errored.

Around each slice, the task calls:

```sh
tools/pipelinegen slice start "$name"   # noted in the batch's journal
# run its tests against the batch's stack
# write result/$name.json
tools/pipelinegen slice end "$name"     # its result, recording and final state
```

Starts and ends go into a journal in `result/.pipelinegen/`; they never
reach the network themselves. By default the batch holds no token, and the
batch's end writes the journal to the repository and GitHub in one flush,
so progress shows per finished batch. For progress per slice, see [live
progress](#live-progress) below. `slice end` exits 0
whatever the slice's result, so the task goes on to the next slice. After
the task, the `ensure` step writes what the journal still holds and closes
every slice of the batch still without a final state as errored, so a
batch killed half way loses no slice that ended. The task itself exits
non-zero when any slice failed.

A suite whose batches run a long-lived server or worker says so with
`stack: true`. Its slices' coverage then comes only through a correlator's
`collect`, so in a recording run `slice setup-end` and `slice start` fail
when none of the correlators that observe the suite collects, naming the
suite, rather than record nothing of the server; and a collector that
finds no process to ask fails the slice. A suite of plain test binaries
(each `go test` run writes its own coverprofile) leaves `stack` out.

A runner that goes through the batch's test files more than once (a
browser batch with several projects) calls `slice start` from its own hook
each time the test file changes, so a slice can come round in several
parts, and calls `slice end` for every slice once the whole run is over.
The coverage of each part lands in the slice's own directory. A slice that
ran no test at all writes `{"passed": true, "tests": 0}`: it passes, keeps
its list, and the comment names it.

A batch task in outline:

```sh
set -u
tools/pipelinegen slice memory || exit 1   # wait for the room the batch should need
start_stack            # database, server: once per batch
trap 'stop_stack; tools/pipelinegen slice release' EXIT   # stop cleanly, so coverage is written; hand back the cores
tools/pipelinegen slice setup-end

tools/pipelinegen slice list | while IFS="$(printf '\t')" read -r name label target sources tests; do
  tools/pipelinegen slice start "$name"
  mkdir -p "result/$(dirname "$name")"
  run_tests "$name" "$sources" "$tests" > "result/$name.json"   # your runner writes the JSON
  tools/pipelinegen slice end "$name"
done
```

Bound every wait and stop every server on exit: a task that hangs holds
its suite until its time limit.

### Params and secrets

Every task of the suite gets pipelinegen's own variables (the run, the
suite, whether the run records it; a batch also its number, the run file
with the change and its commit, and the job's page), the suite's `params`
merged with the run's for the suite (the run's win), and the secrets
declared for its kind of task. It never gets the pipeline's token unless a
secret declares it. The exact list per kind of task is in
[contracts.md](../contracts.md#params-and-secrets), and
[secrets](secrets.md) says how to declare one.

Use the run's params for what differs between runs, such as more browsers
at night:

```yaml
runs:
  nightly:
    suites:
      e2e: {params: {PROJECTS: all}}
```

### Live progress

A batch holds no token by default: its `slice` steps only journal and
collect, and the batch's end writes everything, so the comment shows each
batch once it has finished. To see slices as they end, a repository can
give its batch a report token as a declared secret and run the flush in
the background from its own task script:

```sh
tools/pipelinegen slice setup-end
tools/pipelinegen slice flush --every 30s & flusher=$!
trap 'kill "$flusher"' EXIT
```

The cost is a token that writes sitting beside the tests, which run the
change's code. Do it only on a run of branches, never on one that takes
pull requests or changes: pipelinegen refuses a declared secret there.

## The result file

One per slice, `result/<name>.json` (a slice named `e2e/editor.spec.ts`
writes `result/e2e/editor.spec.ts.json`):

```json
{
  "passed": false,
  "tests": 24,
  "failures": [
    {"test": "the editor saves a note", "tail": "the last lines of its output"}
  ]
}
```

`passed` is true only when every test passed; any entry in `failures`
makes the slice failed whatever `passed` says. `tests` is optional.
pipelinegen keeps the first 10 failures and the last 400 lines (at most
32 KiB) of each tail, enough for a timed-out test's goroutine dump; the
pull request comment shows each tail's last 15 lines, so it stays readable. A missing or
unreadable file makes the slice errored: the task never said how it went.

## Extras for whole-suite checks

A batch task may leave more files for a slice in `result/<name>/`: a
report a whole-suite check reads, such as the API calls a spec made. They
are kept with the slice's state, at most 1 MB per slice; a slice that
leaves more keeps none, and if it passed it ends as errored. A whole-suite
check gets them back with `pipelinegen report collect --suite <suite>
--out slices`, which writes `slices/<name>/state.json`, `recording.txt`
and `extra/` for every slice of the suite in the run.

## Coverage, for recorded suites

When `PIPELINEGEN_RECORD` is `true`, the batch leaves what ran in
`coverage/`, where the correlators read it. No variable names that path:
`coverage/` is the batch task's output in its working directory, and each
slice writes into `coverage/<slice>`, every part of it there too (a Go test
binary with `GOCOVERDIR=coverage/<slice>`, a browser fixture with its output
root there):

- `coverage/_setup/`: what the stack's start and seed ran before the first
  slice, taken by the correlators' `collect` at `slice setup-end`. It
  binds every slice of the suite, or, with the `setup-split` narrowing on,
  a setup target of the suite's own (see [narrowing](narrowing.md)).
- `coverage/<name>/`: what each slice ran.

What goes in each directory is the correlator's to say; each correlator's
README lists what it reads. For Go, go-correlator takes `*.coverprofile`
files and Go's `GOCOVERDIR` output from binaries built with `-cover
-covermode=atomic -coverpkg=<module>/...`. There are three cases, and the
[notes service](../../example-notes/), whose drivers do it for it, shows
the first and the third:

- Go tests: run the test binary once per slice, with its own
  `-coverprofile` in the slice's directory. Nothing more.
- A server the tests talk to, restarted for each slice: start it per slice
  with `GOCOVERDIR` in the slice's directory, and stop it cleanly after
  the slice, so it writes its counters. Simple, and enough when the
  server starts in a second or two.
- A stack that keeps running across the batch's slices (a server that is
  slow to start, a database with seeded data): its counters have to be
  taken and cleared between slices while it runs. A correlator's `collect`
  does that at the end of the setup and of each slice; for Go,
  go-correlator, which needs its `cover` package linked into each
  long-running binary's coverage build (through a file with the build tag
  `pipelinegen_cover`, so only coverage builds link it), and `cover_dir`
  in its config. Its README has the steps. Mark such a suite `stack:
  true`, as the batch task's section says; without a correlator that
  collects, nothing takes a running server's counters between slices.

Processes must stop cleanly (exit, or handle SIGTERM), or their last
counters are lost.

The correlators read the coverage after the batch, in a step that sees
only the tree (`source/`) and the coverage output (`coverage/`). If a
correlator needs files the prepare task built, such as a browser suite's
served scripts and their source maps, have the batch copy them into
`coverage/` and point the correlator's config there
(`build_root: coverage/build`), or build them inside the tree and point at
`source/...`. A path to another task's output (`prepared/tree`) exists in
the batch but not where the correlators read, and `pipelinegen check`
refuses it before a first recording is lost to it.

A slice whose tests ran but whose correlators saw nothing is taken as one
they couldn't tell, not as a slice that touches nothing: it stays bound to
everything until a recording tells again. A test file can touch nothing of
the tree only when its tests check something outside it, such as a
deployed service or a file the task downloads; for a suite made of such
files, set `empty_recordings: allow`. A slice that left no coverage at
all, or only empty coverage, is the same case when its correlator says it
can't tell from that: go-correlator does. Either way the slice keeps its
result and gets a mark, and its list stays as it was.

A slice ends as errored, if its tests passed, when its recording itself
fails: a collect went wrong, a correlator failed, or the slice never
reached `slice end` ("the slice never ended in its batch, so it left no
coverage to record"). A slice whose tests failed stays failed, and its
list stays as it was either way.

## Whole-suite checks

A check's task takes `source` and `prepared` and writes
`result/result.json` in the same format. Under the suite's `cpu.wait` or
`memory.wait` it starts with `tools/pipelinegen wait whole || true`, with
`tools` among its inputs, as a prepare does. It runs after the batches, and
only when every batch passed: a failed slice skips it, and the report says
so.
