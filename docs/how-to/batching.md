# Bundle a suite's slices, or run each alone

How to choose a run step's `batch` setting: whether its driver runs the
slices it was given together, in bundles, or each in an invocation of its
own. What a driver and a run step are is in
[concepts](../concepts/drivers.md). After this page you can set `batch` on
a suite's run step, say what changes and what stays the same when you do,
and tell when a suite or a single test file should not be bundled.

What is released of drivers is in [one place](../concepts/drivers.md#what-is-released).

## Two words: batch and bundle

`batch: true` means: bundle the slices of a batch. The two words name two
different things, and this page keeps them apart.

- A batch is what pipelinegen already deals a suite's selected slices
  into: one CI task on one machine, with one set-up
  ([batches](../concepts/suites-and-slices.md#batch)). The `batch` setting
  doesn't change how batches are dealt.
- A bundle is what one invocation of the test tool runs, inside a batch.

The setting decides how the run step's driver is called for the slices
the batch routed to it:

- `batch: false`, the default: one call per slice. Five selected Go test
  files are five `go test` runs, one after another, in the same batch,
  each between the slice's start and end, as a task file runs them.
- `batch: true`: one call for all of the step's slices, which the driver
  combines into as few invocations as its tool allows. Five Go test files
  in two packages are two `go test` runs, one per package.

```yaml
suites:
  go:
    tests: ["**/*_test.go"]
    image: golang
    steps:
      - run: go
        batch: true
```

The set-up steps run once per batch either way. `batch` changes how many
processes the tests run in, never what is selected or reported. It is off
by default, so moving a suite from task files to drivers changes nothing
but who runs the command; bundling is a choice you make afterwards.

## Every driver takes both

`batch` is the same setting with the same meaning on every driver's run
step; no driver declares it among its own settings, and none may refuse
either value. With `batch: false` pipelinegen itself calls the driver once
per slice, so the mode is the same for every driver by construction. With
`batch: true` a driver whose tool has no cheaper way to run several slices
may still run them one by one. The kit runs every driver in both modes on
the same slices and compares what comes back ([writing a
driver](../writing-plugins/driver.md#both-modes)).

## What stays the same

The slice stays what pipelinegen selects, reports and records, in both
modes:

- Selection doesn't change. A change selects the same slices whether the
  suite bundles or not, and the batches are dealt the same way.
- Every slice gets a result of its own. A bundle of three files gives
  three results, each with its own failing tests and the end of their
  output. A driver reads them from this invocation's own record of each
  slice (the per-test events of `go test -json` and Playwright's JSON
  reporter, each target's result in Bazel's build events), never from the
  bundle's exit code. A slice with no record of its own is not passed.
- In a recording run, every slice gets a recording of its own, in the
  form the suite's correlators read. A recording of a whole bundle, handed
  to each of its slices, is never made: it would bind every file of a
  package to what any of them ran, so they would always be selected
  together.
- A test file's time limit stays its own. The go driver gives a bundle
  its `timeout` once per file it holds, never past the step's own limit,
  so bundling never tightens a file's limit.
- A bundle that dies (a panic, a test that calls `os.Exit`, a time limit,
  a killed process) leaves some slices without a result. pipelinegen runs
  each of those again in a call of its own, and only then reports them; a
  slice that still has no result is errored, with the driver's last lines
  of output. A slice never loses its result because another slice of its
  bundle crashed.
- A slice whose code didn't build ends failed with the build error, in
  both modes, and isn't run again.

Outside stack suites, a recording run bundles only where the recordings
stay apart. A driver says in its description whether a bundle still
leaves one recording per slice; when it doesn't, a recording run calls it
once per slice even with `batch: true`, and the batch's log says so once.
Where it bundles, the bundle of a suite that names correlators is marked
(below), so each correlator collects at a slice's own start and end. A
stack suite's recording run bundles with marks always. Runs that don't
record bundle as set.

## What a bundle is, driver by driver

| Driver | `batch: false` | `batch: true` |
|---|---|---|
| go | each test file is `go test -run '^(TestA\|TestB)$' ./pkg` with only that file's tests | the selected files of one package are one `go test -json` of that package, with all their tests; packages one after another, never two in one process |
| bazel | each target is a `bazel test` of its own | the batch's targets are one `bazel test`, or one per test filter when slices filter their target's tests |
| playwright | each spec file is a `playwright test` of its own | the batch's spec files are one `playwright test`, with the step's `workers` setting |
| command | the step's `each` command runs once per slice | with `all`, the command runs once with every slice and writes a result file per slice; with only `each`, the driver runs `each` for every slice in turn, which gains nothing, and says so once in the batch's log |

The go driver bundles by package because that is what an ordinary `go
test ./pkg` does: a package's test files are always compiled into one test
binary and run in one process. Bundling a package therefore runs its tests
the way a developer's `go test` runs them. Two packages never share a
process. A slice whose files span packages runs each package's part in
turn, and passes only if every part does.

In a recording run the go driver builds a package's test binary once with
`go test -c` and runs it once per file, exactly as `go test` runs a
binary, each with its own coverage profile, so every file keeps its own
recording. It says so (`apart`) since an acceptance run on pipelinegen's
own go suite: 249 of its 252 slices covered the same blocks bundled as
with one `go test` per file, and the other three vary from run to run in
either mode.

The bazel driver's bundle is one Bazel invocation: Bazel still runs each
target's test in a process of its own, with its own sandbox, so the
targets see nothing of each other. What the bundle saves is the
invocation's start and its analysis; the Bazel server stays up between
calls and batches. A slice that runs only some of its target's tests
passes them as Bazel's `--test_filter`, which holds for a whole
invocation, so the driver groups a bundle by filter. The driver reads
every target's result from its own `testResult` event in the
invocation's build event file, in both modes. Bazel's test
result cache stays on, except in a stack suite: a target whose test and
inputs are as they were gets its stored result, and that counts as its
result. When the Bazel
server dies in the middle of a bundle, the targets left without a result
are run again one by one, as for any bundle that dies; so are the
targets an interrupt at the step's deadline left without a result. A
recording run is a `bazel coverage` that instruments the whole
repository, not only the tests' own packages, and leaves one LCOV file
per target, in both modes.
go-correlator doesn't read LCOV yet, so a Go suite run through Bazel stays
`bind: graph` until it does.

The playwright driver's bundle is one `playwright test` over several spec
files. Playwright spreads them over its workers as it always does, and
the driver reads each spec file's result from Playwright's JSON report; a
spec file whose worker died before its tests finished gets no result and
is run again alone. In a recording run the suite's coverage fixture
writes each spec file's recording as the file ends, and the driver checks
that every file that ran left one. Before it runs, the driver installs
the project's packages (`npm ci`) when `node_modules` doesn't match the
lockfile, unless the step sets `install: false`.

The command driver runs commands of the suite's own. With `each`, its
result is the file the command wrote for the slice, in the [result
file's](writing-tasks.md#the-result-file) form, or else its exit code. With
`all`, the command must write every slice's result file; a slice without
one is run again alone, and then errored. A run step with `batch: true`
and only `each` is still accepted: the driver takes the bundle and runs
`each` for its slices one by one, with the same results as `batch:
false` and nothing saved.

## Bundles in recording runs

A suite with `stack: true` runs its tests against servers that keep
running across the batch, and in a recording run each slice's share of
their coverage is taken by the correlators' collect at the slice's end
([coverage for recorded suites](writing-tasks.md#coverage-for-recorded-suites)).
Any suite's correlators collect at each slice's start and end the same
way (a statement log, a request log), and those calls must come at the
slice's real edges. So a recording run's bundle of a stack suite, or of a
suite that names correlators, is marked. Such a suite bundles too. In a recording run its bundle goes to the
driver in one invocation, and the driver runs the bundle's slices one at
a time: at each slice's end it holds until pipelinegen has collected the
stack's coverage for that slice, then goes on with the next. Each slice's
recording is therefore what it would be without bundling.

Such a bundle runs its slices one after another, never side by side: one
stack can't tell two slices running at once apart, and the batch's log
says once that the bundle runs serially. It still saves what a bundle
saves: one invocation, one build, one browser start instead of one per
slice. In runs that don't record, the bundle runs as the driver always
runs it: Playwright with its own workers, Bazel with its own test jobs.

The driver marks each boundary with `pipelinegen slice mark start
<slice>` before the slice's first test and `pipelinegen slice mark stop
<slice>` after its last, and each mark returns once the stack's coverage
for the slice is collected. How each driver does it:

| Driver | How it holds at a slice's boundary |
|---|---|
| go | it runs the package's built test binary once per file, and marks around each run |
| playwright | one worker for the bundle; the suite's coverage fixture marks at each spec file's start and end (react-correlator's does it by itself; a fixture of the suite's own calls `markStart` and `markStop` from `marks.mjs`) |
| bazel | one test at a time (`--local_test_jobs=1`), each test action wrapped by `--run_under` in a command that marks before and after |
| command | the suite's `all` command marks at its own boundaries, with the `{mark}` placeholder: `{mark} start <slice>`, `{mark} stop <slice>`; an `each` command that holds no `{mark}` is marked around by the driver |

A slice that ends without its start mark errors in the recording run: it
says which driver or fixture has to mark, and that `batch: false` on the
run step gives each slice a call of its own and real edges without marks.

A slice of a stack suite is never replayed from a test tool's cache, in
any run, bundled or not. The cache knows the test's own inputs, but the
servers it talks to are no input of it, so a stored pass could stand
after the server's code changed. It is the one place a driver forces a
test to run again: the bazel driver adds `--nocache_test_results` to such
invocations, and the go driver `-count=1` to such runs, and nowhere else.

A driver that can't hold at a boundary says so instead of running: under
Bazel's remote execution the tests run elsewhere and can't reach the
marks, so the bazel driver fails a recorded stack bundle's slices with
that reason.

## What it saves and what it costs

A bundle pays once for what each invocation pays: compiling and linking a
Go test binary, starting a process, a `TestMain` and the fixtures it sets
up, Bazel's start and analysis, a browser's start. That is all it saves.
The tests themselves take as long bundled as alone, so the gain depends
on how much of a suite's time is start-up: a suite of many short files
that each pay for a link or a browser gains most, a suite whose time is
its tests' own gains little.

pipelinegen's own go suite, 252 slices in 39 packages, measured on one
machine through the go driver, every run with a fresh temporary directory
so that Go replayed no result:

| Run | CPU | Peak memory | Wall time |
|---|---|---|---|
| one by one, not recording | 544 s | 733 MB | 1947 s |
| bundled, not recording | 428 s | 658 MB | 2159 s |
| one by one, recording | 619 s | 749 MB | 1658 s |
| bundled, recording | 560 s | 736 MB | 1749 s |

All 252 slices passed in every run. Other work loaded the machine, and
the load fell from 36 to 2 over the four runs, so the wall times can't be
compared with each other; CPU and peak memory can. Bundling saved 21 % of
the CPU in runs that don't record and 10 % in recording runs, and 5 to
10 % of peak memory. It saved no wall time on this suite: its time is
its tests' own. Bundling shortens a suite by the links, processes and
start-ups it no longer repeats: for Go one link and one `TestMain` per
package, for Bazel one analysis per batch, for Playwright one browser
start per bundle.

What it costs:

- A bundle shares a process, so tests of different files see each other's
  package state: a global one file sets, a `TestMain` that runs once for
  the package instead of once per file. A plain `go test ./pkg` runs them
  the same way, so a package whose tests pass there has already shown
  that its files can share a process.
- A failure reaches further. A bundle that dies runs its unfinished
  slices again one by one, so a crash costs those slices a second run.
- Go's test cache, in recording runs. A recording bundle runs a built
  test binary, which Go never replays from its cache. That costs little:
  the binary covers the whole module, so it changes with any change to
  the module, and Go could only replay a file when nothing changed at all.
  Runs that don't record go through `go test`, bundled or not, and keep
  Go's cache, except in a stack suite.

## When not to bundle

Leave a run step at `batch: false` when its test files can't share a
process: each file sets process-wide state it never resets (environment
variables, the working directory, a global registry), or each needs a
fresh server, port or database that the files of one package would fight
over.

When only some files can't share, bundle the step and let those files ask
to run alone. A test file asks with a line of its own:

```go
//ci:alone
```

A file with that line runs in an invocation of its own in both modes, and
gets its result and recording as any other slice does. The line
`//ci:race` asks for the same and for the race detector besides: the
detector is a build flag, so such a file runs alone with it. It is one
mechanism, read by the driver, and the race detector is never on for a
file that doesn't ask for it.

## Try it

`pipelinegen slice run` runs a suite's steps on your machine, on your
working tree, and prints each slice's result. Here are two test files of
one package of pipelinegen's own repository, `internal/durations`, with
the go suite's run step as [moving a suite to
drivers](moving-to-drivers.md#the-go-suite) writes it (the tests' own
lines cut to `...`). With `batch: false`, each file is a `go test` of its
own:

```
$ pipelinegen slice run --suite go --slice internal/durations/durations_test.go --slice internal/durations/record_test.go
...
go run: PASS
go run: ok  	github.com/sideways-works/pipelinegen/internal/durations	0.003s
slice internal/durations/durations_test.go: passed
...
go run: PASS
go run: ok  	github.com/sideways-works/pipelinegen/internal/durations	0.003s
slice internal/durations/record_test.go: passed
```

With `batch: true`, both files are one `go test` of the package, and each
still gets its own result:

```
$ pipelinegen slice run --suite go --slice internal/durations/durations_test.go --slice internal/durations/record_test.go
...
go run: PASS
go run: ok  	github.com/sideways-works/pipelinegen/internal/durations	0.003s
slice internal/durations/durations_test.go: passed
slice internal/durations/record_test.go: passed
```

A file whose tests and inputs are as they were since an earlier run gets
Go's stored result, which `go test` prints as `(cached)`: a replayed
result is a result.

Next: [move a suite from task files to drivers](moving-to-drivers.md).
