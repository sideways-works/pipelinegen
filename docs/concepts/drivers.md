# Drivers: how a batch runs

The [previous page](suites-and-slices.md) dealt a suite's selected slices
into batches, each set up once on one machine. This page says what runs a
batch: the suite's steps, each run by a driver, and how a slice reaches
the driver that runs it. After it you can read a suite's `steps`, tell a
set-up step from a run step, and say which driver runs which slice.

## What is released

This section is the one place the book says how far drivers are
released; the other pages link here.

- Drivers are part of pipelinegen: `steps`, `pipelinegen slice run`,
  `slice prepare` and `slice mark`, and the built-in command driver.
- A suite with steps needs a runner whose release has the capability
  `steps`, as every release of concourse-runner has. A runner without it
  can't write such a suite's tasks, and `pipelinegen pipeline` refuses
  the suite and says to update the runner.
- The go, shell, bazel and playwright drivers are released, each at
  v0.2.0 with the rest of the family. The command driver comes with
  pipelinegen and needs no release of its own.

## What a batch has to do

Whatever runs a batch of the notes service's `go` suite does the same
things for every slice: run the file's tests with `go test -run`, add the
coverage flags in a recording run, turn Go's output into the slice's
result, and leave its coverage where go-correlator reads it. The `e2e`
suite builds the notes server with coverage, installs the specs'
packages, starts the server, and then runs the specs with Playwright, one
at a time, taking the server's coverage between them, so each spec gets
the coverage of what it reached.

A suite can write all of that itself, as CI task files and shell scripts
([writing a suite's tasks](../how-to/writing-tasks.md)). Every repository
that runs Go tests then writes the same loop, the same flags and the same
result file, and copies them from repository to repository. A driver is
that knowledge, written once.

## Drivers

A driver is a plugin that knows how one test tool runs: the go driver
runs `go test`, the bazel driver `bazel test`, the playwright driver
`playwright test`. Given some slices, a driver runs them, reports one
result per slice (passed, or each failing test with the end of its
output) and, in a recording run, leaves one recording per slice in the
form the suite's correlators read. The command driver, built into
pipelinegen, runs commands the suite names: it builds and starts a server,
or runs test scripts of the repository's own. The shell driver runs steps
written as shell, inline in the config, and shell test files function by
function, so a suite needs no script file of its own.

Like every plugin, a driver is a release pinned by version and checksum
in `pipelinegen.yaml`; the command driver needs no entry. Unlike the other
kinds, a driver executes the tree's code: that is its job. It runs only in
the suite's own tasks on the test machine, never while planning, and gets
no token: only the secrets the suite declares for its tasks reach it. In a
proposed change's run, the steps and the drivers' settings are read from
the trusted config, as the plugins are, so a change can't swap the
command that runs its own tests ([secrets](../how-to/secrets.md)).

## A suite's steps

A suite that uses drivers lists its steps instead of naming task files:

```yaml
suites:
  go:
    tests: ["**/*_test.go"]
    bind: recorded
    image: golang
    steps:
      - run: go
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    owner: "//e2e:e2e"
    bind: recorded
    stack: true
    image: playwright
    steps:
      - prepare: command
        image: golang
        with: {run: [go, build, -cover, -covermode=atomic, "-coverpkg=example.com/notes/...", -tags, pipelinegen_cover, -o, "{prepared}/notes-server", ./cmd/notes]}
      - setup: command
        with:
          start: ["{prepared}/notes-server", -addr, "127.0.0.1:8080", -seed, e2e/seed.json]
          ready: {http: "http://127.0.0.1:8080/notes", seconds: 30}
          export: {NOTES_URL: "http://127.0.0.1:8080"}
      - run: playwright
        batch: true
        with: {config: e2e/playwright.config.ts}
```

Run on a developer's machine with a browser at hand, the suite's steps
build the server, start it, run both specs in one `playwright test` and
stop the server again; the server, built with coverage, notes at its stop
that this run doesn't record:

```
$ pipelinegen slice run --suite e2e
command setup: warning: GOCOVERDIR not set, no coverage data emitted
playwright run: 
playwright run: Running 3 tests using 2 workers
playwright run: 
playwright run: [1/3] [chromium] › e2e/create.spec.ts:3:5 › adds a note
playwright run: [2/3] [chromium] › e2e/search.spec.ts:3:5 › finds a note by a word in it
playwright run: [3/3] [chromium] › e2e/create.spec.ts:11:5 › refuses a note without a title
playwright run:   3 passed (1.1s)
slice e2e/create.spec.ts: passed
slice e2e/search.spec.ts: passed
```

To record this suite, go-correlator also needs to know where the running
server keeps its coverage, oapi-codegen-correlator needs the server's
request log, and either the run step leaves `batch` off, so pipelinegen
takes the server's coverage between spec files itself, or the specs mark
their own start and end. [Step 7 of the
guide](../tutorial/tasks.md#browser-tests-with-playwright-against-a-go-server)
records the notes service so.

The list is the suite's: every slice of the suite is run by the same
steps, in this order.

- A prepare step runs once per build of the suite, before its batches, and
  fills the `prepared` directory every batch gets: here, the server built
  once, in an image with Go, since the suite's own image has the browsers.
- A set-up step runs once per batch, on the batch's machine, before any
  slice: it starts what the batch's tests need and lives until the batch
  ends. What it exports (`NOTES_URL`) reaches the later steps, and the
  tests they run, by name.
- A run step gets the batch's slices routed to it, and runs them.

Each driver installs what its own tool needs, in the batch's container:
the playwright driver runs `npm ci` when `node_modules` doesn't match the
lockfile, with npm's cache kept between builds. Anything else a batch
needs once before its servers start (packages of another directory, a
migration) is a command set-up's `run`, which finishes before its
`start`. `prepared` carries only what doesn't depend on the container,
such as a static binary, never `node_modules`.

A driver refuses a setting it would otherwise ignore, before it runs
anything: a key it doesn't read, at any depth, a value of another type or
with none, and a value that would read as left out. The command driver
reads `run`, `dir` and `env` in a prepare step; `run`, `start`, `stop`,
`ready`, `export`, `dir` and `env` in a set-up; `each`, `all`, `dir` and
`env` in a run step. It refuses any other key, a `stop` or `ready`
without a `start`, a `ready` that names none or more than one of `http`,
`tcp` and `file`, a `ready.seconds` below 1, a variable name in `env` or
`export` that is no variable's name, and a `dir` outside the tree.
`pipelinegen check` says the same before anything runs, by the setting's
place in the config, for the command driver and every driver that
validates its settings ([writing a driver](../writing-plugins/driver.md#settings)).

A driver sees `PIPELINEGEN_COMMIT`, the commit the run runs, and so do
the tests that inherit its environment (Bazel's inherit none). A test
that depends on the commit, such as one that reads git history, reads
it, and Go's test cache then runs it again for every commit. A test that
doesn't read it is unaffected.

There is no tear-down step. When the batch ends, pipelinegen stops every
set-up, the last started first, whatever happened before: a failed slice
or a set-up that never became ready still stops the server, which then
writes its coverage.

The `go` suite needs nothing built or started, so its list is one run
step. A server restarted for every spec would make each spec's coverage
the server's whole run. With a set-up step the server starts once per
batch and keeps running across its specs, so each spec's share of the
server's coverage is taken at the spec's end, by go-correlator's collect; that is what `stack: true` says, and why the
server is built with go-correlator's `pipelinegen_cover` tag ([coverage for
recorded suites](../how-to/writing-tasks.md#coverage-for-recorded-suites)).
A stack suite's specs are also never replayed from a test tool's cache:
the server they talk to is no input of that cache, so a stored pass could
outlive a change to the server's code.

A suite with steps names its `image`, since no task file names one any
more: the suite's tasks run in it, and a prepare step may name another.
Each is a `registry-image` resource of the root pipeline file, which
`pipelinegen check` looks for ([moving a suite to
drivers](../how-to/moving-to-drivers.md#the-smoke-suite) shows one).
Set-up and run steps share the batch task's one container. Before a task
runs anything, pipelinegen checks each step's driver against its pin and
the driver's needs (`go`, `node`) in the container, and stops before
anything runs when one is missing, naming the suite, the driver and the
need: the suite's image must hold it. The suite's `params` and its batch
secrets reach every step and the tests they start, and the tools
directory comes first on their `PATH`.

## Routing

A suite may have more than one run step: Go test files run by the go
driver and shell test scripts run by a command, in one suite. Each run
step's `take` says which slices it gets, by what they are:

```yaml
suites:
  unit:
    tests: ["**/*_test.go", "scripts/**/*_test.sh"]
    steps:
      - run: go
        take: ["**/*_test.go"]
      - run: command
        take: ["scripts/**/*_test.sh"]
        with: {each: [sh, "{source}"]}
```

- A file slice is taken by a path pattern, such as `**/*_test.go`.
- A target slice, in a tagged suite, is taken by its rule kind,
  `kind:go_test`, or by a label pattern, such as `//services/...`.
- A suite's only run step without `take` gets every slice.

Routing is config, never a guess. `pipelinegen check` refuses a slice that
no run step takes and a slice that two take, naming them, and a `take`
outside what the step's driver says it can run. The rule kind of a target
slice is found while planning and handed to the batch, so routing in the
batch task needs no Bazel query. So every slice has exactly
one driver before anything runs.

[Moving a suite to drivers](../how-to/moving-to-drivers.md#check-it-and-run-it)
shows what `check` prints for a slice no run step takes.

## Bundled or one by one

A run step's `batch` setting says whether its driver runs the slices it
was given together or each on its own. With `batch: true` the go driver
runs the selected files of one package as one `go test`, a bundle; with
`batch: false`, the default, each file is a `go test` of its own, as a
task file runs it. Every driver takes both values. Either way each slice
keeps its own result and its own recording, and selection doesn't change.
The `e2e` suite bundles too: in a recording run its bundle runs one spec
at a time and waits at each spec's end while the server's coverage for
that spec is collected.

In a recording run, a bundle of a suite that names correlators goes to its
driver with marks, stack or not, so that each correlator collects at a
slice's real start and end, never after the slice ran. A slice that ends
without its start mark errors, and says which driver or fixture has to
mark it; `batch: false` on the run step gives each slice a call of its
own, and real edges without marks. The go driver marks around each test
binary it runs, the bazel driver around each test (not under remote
execution, where it fails the slices instead), and the shell driver runs
each slice alone in a recording run; a recorded browser suite through the playwright driver
needs a coverage fixture that marks (react-correlator's, or the suite's
own with `marks.mjs`) or `batch: false`, and
the command driver's `all` form needs `{mark}`. [Bundle a suite's slices, or run each alone](../how-to/batching.md) says
what a bundle is for each driver and when not to bundle.

## Who calls the drivers

The batch task the generated pipeline runs for a suite with steps calls
one command, `pipelinegen slice run`. It reads the batch, starts the
set-up steps, hands each run step the slices routed to it, writes a result
per slice (and in a recording run the slice's recording) as a task file of
the repository's would, and stops the set-ups. The repository writes no
task file and no shell for the suite.

The same command runs a suite's steps on your machine, on your working
tree, and prints each slice's result; [bundle a suite's
slices](../how-to/batching.md#try-it) shows it on two test files of one
package, one by one and bundled.

A suite that names `run_slice` and `prepare` task files keeps working as
before. A suite has either steps or task files, not both.

Next: [units and sbom plugins](units-and-sbom.md).
