# Writing a driver

This page is for people writing a driver. A driver runs a suite's tests:
given some slices, it invokes one test tool for them (`go test`, `bazel
test`, `playwright test`), reports one result per slice, and in a
recording run leaves one recording per slice for the suite's correlators.
A driver may also prepare what a suite's batches share and start what a
batch's tests need. What a driver is for a repository is in [the
concepts](../concepts/drivers.md); the fields are in
[plugins.md](../plugins.md#drivers). After this page you can write a
driver, run it in both of the modes every driver must have, and check it
with the kit.

What is released of drivers is in [one place](../concepts/drivers.md#what-is-released).

## Calls

A driver is one executable, called as `<executable> <call>` with a JSON
request on stdin, like every plugin
([the shape of a plugin](README.md#the-shape-of-a-plugin)), with one
difference: it reads one JSON value from stdin and never waits for its
end, since a set-up's stdin stays open until the batch ends. A Go
driver's `main` is `plugin.ServeDriver`, which does this; `plugin.Serve`
is for the other kinds. Ignore event kinds and fields you don't know, in
what you read, as the core ignores them in what you write, so additions
to protocol 1 break nothing. A driver answers up to six calls:

- `describe`: as for every plugin, plus the calls it answers (`verbs`),
  what it can run (`routes`), whether a bundle keeps its slices'
  recordings apart (`apart`), the forms its recordings take, what it
  needs on the machine, and the settings it accepts.
- `prepare`: fills the suite's `prepared` directory, once per build of the
  suite.
- `setup`: starts what a batch needs, says when it is ready and what later
  steps get in their environment, and keeps running until the batch ends.
- `run`: runs the slices it is given and reports each slice's end as it
  happens.
- `validate`: checks steps' settings, without running anything, for a
  driver that says `validates` (see [settings](#settings)). It is no verb
  of a step.
- `render`: writes a suite's run step in a CI system's own steps, for a
  driver whose `describe` names `renders` (see [rendering](#rendering)).
  It is no verb of a step either.

A driver answers any subset: the go driver answers `run` only, the
command driver and the shell driver all three. `pipelinegen check` refuses a step that names a
driver for a call it doesn't answer.

go-driver, built from source, answers:

```json
{"verbs":["run"],"routes":{"files":["**/*_test.go"]},"apart":true,"recording_forms":["go-coverprofile"],"settings":{"cache_mb":"int","cover_tags":"list","coverpkg":"list","flags":"list","modcache_mb":"int","module":"string","must_run":"map","race":"bool","tags":"list","timeout":"duration"},"protocol":1,"kind":"driver","name":"go-driver","version":"dev","capabilities":null,"needs":["go"]}
```

go-driver says `apart` since an acceptance run on pipelinegen's own go
suite: of its 252 slices, 249 covered the same blocks bundled as with one
`go test` per file, and the other three vary from run to run in either
mode, as running them again showed.

`routes` says what the driver can run: path patterns for file slices
(`files`), or Bazel rule kinds for target slices (`kinds`). A run step's
`take` outside them is refused. `settings` names every key a step's
`with` may give it, with its type (`string`, `bool`, `int`, `duration`,
`list` or `map`); any other key, or a value of another type, is refused.
A driver that says `validates` is asked about its settings instead, at
every depth ([settings](#settings)).
`results` is empty for a driver that reads per-test or per-target
records, and `"exit"` for one that can only judge by an exit code.
Protocol 1 defines no `capabilities`. `recording_forms` names the forms
of your recordings, and a correlator names the forms it reads in
`observes_forms`: a recorded suite whose run driver and correlators share
no form is refused.

Check your `needs` yourself too: when what you find on the machine falls
short, fail at once with one line that names the need. pipelinegen checks
every step's needs in the container the step runs in before it runs
anything; `pipelinegen check` can't look inside an image.

## Running slices

A `run` request, as `pipelinegen slice run --suite smoke` sent it on a
developer's machine to a stand-in driver that kept its requests, for the
example's smoke suite bundled (`batch: true`), with its server started by
a set-up step. The temporary paths are shortened, and the stand-in's own
parameter is left out of `env`:

```json
{"protocol": 1,
 "work": "/tmp/pipelinegen-slice-run-1/capture",
 "cache": "/home/you/.cache/pipelinegen/slice-run/drivers/capture",
 "tree": "/home/you/example", "workspace": ".",
 "tools": {"bazelisk": "bazelisk", "pipelinegen": "pipelinegen"},
 "suite": "smoke", "batch": true, "record": false,
 "env": {"WORDS": "http://127.0.0.1:8080"},
 "prepared": "/tmp/pipelinegen-slice-run-1/prepared",
 "slices": [
   {"name": "words/count_smoke.sh", "sources": ["words/count_smoke.sh"],
    "extras": "/tmp/pipelinegen-slice-run-2/words/count_smoke.sh"},
   {"name": "words/upper_smoke.sh", "sources": ["words/upper_smoke.sh"],
    "extras": "/tmp/pipelinegen-slice-run-2/words/upper_smoke.sh"}]}
```

Fields left empty are left out. In a batch task the request also has
`config` (the step's settings), `deadline`, and in a recording run
`record: true` and each slice's `recording`; a stack suite's requests
carry `"stack": true`, and in a recording run `marks`. A Go slice carries
its `tests`, and a target slice its `label`, `target` and `kind`.

- `config` is the step's `with` merged over the plugin entry's `config`.
- `env` holds, by name, the suite's `params` and batch secrets and what
  the set-up steps exported. They are in your own environment too; a tool
  that hides the environment from tests must pass each by name (the bazel
  driver passes each with `--test_env`). The tools directory is first on
  your `PATH`. In a batch's steps your environment also holds
  `PIPELINEGEN_COMMIT`, the commit the run runs, which `env` doesn't
  carry ([what a driver sees](../contracts.md#params-and-secrets)); leave
  it, and any `PIPELINEGEN_` variable you don't know, to your tests.
- `deadline` is when the task's time limit, the suite's `minutes`, runs
  out. Hold it yourself: shortly before it, stop your tool (the bazel
  driver interrupts the Bazel client), and give no `end` to the slices
  left without a result. A slice's own limit is a
  setting of yours, such as the go driver's `timeout`.
- Each slice carries what you run: a file slice its `sources` and, for Go,
  its top-level `tests`; a target slice its `label`, its `target` (run it
  when set, else the label) and its rule `kind`, with `tests` as the
  filter in that kind's syntax (for `go_test`, a regular expression). A
  filtered slice of a rule kind whose filter syntax you don't know ends
  failed with that reason; never run it unfiltered.
  `extras` is the slice's directory for files a whole-suite check reads,
  `files` the directory for its failure files ([failure
  files](#failure-files)), and `recording`, in a recording run only, the
  directory its recording goes into.
- Install what your tool needs yourself, in the batch's container, with
  its package cache under `cache`: the playwright driver runs `npm ci`
  when `node_modules` doesn't match the lockfile, unless a step sets
  `install: false`. Never rely on `prepared` for it, which another
  container may have filled.
- `work` is yours for the batch, kept across your calls in it, and
  `cache` is kept on the machine between batches and builds: keep there
  what makes the next batch fast (the bazel driver keeps Bazel's output
  user root and repository cache there, and its server stays up).
- `marks` comes only in a recording run's bundle of a stack suite, or of
  a suite with correlators ([recording with marks](#recording-with-marks)).

The answer is JSON lines on stdout, one per event, written as it happens,
so a batch's log shows what finished and a killed driver has still
reported every slice that ended before it died:

go-driver's answer to a bundle of two files of `internal/durations`,
whose tests passed:

```json
{"slice":"internal/durations/durations_test.go","event":"end","passed":true,"tests":2}
{"slice":"internal/durations/record_test.go","event":"end","passed":true,"tests":1}
```

A failed slice's `end` adds `failures`, each a `test` and the `tail` of
its output, and may add `files`; `seconds`, `recorded` and `replayed` say
how long it took, whether it left a recording and whether the tool
replayed it from its cache.

`start` is optional; every slice needs its `end`. What you write on stderr
is the tests' output, and goes into the batch's log as `<driver> run:
<line>`. Exit 0 when you did your job, whether or not tests failed; any
other exit code says the driver failed, and the slices it ended keep their
results.

## Both modes

Every driver runs with `batch: false` and with `batch: true`, and may
refuse neither. `batch` is a setting of the kind, the same word with the
same meaning on every driver's run step, so you don't declare it among
your settings.

- With `batch: false`, pipelinegen calls `run` once per slice, with a
  request of one slice and `"batch": false`, and notes the slice's start
  and end around each call. You may use the flag: the go driver then runs
  a plain `go test -run` for the file, as a task file did.
- With `batch: true`, pipelinegen calls `run` once per run step with all
  the slices routed to it, and you combine them into as few invocations as
  your tool allows. What one invocation runs is a bundle.

What may go into one bundle is yours to decide, with one rule: bundling
may not change what a test sees. The go driver bundles by package,
because a package's test files share one test binary in any ordinary `go
test`, and never puts two packages in one process. The bazel driver
bundles a batch's targets into one `bazel test`, where each test still
runs in its own process. The playwright driver bundles a batch's spec
files into one `playwright test`. If your tool has no cheaper way to run
several slices, running them one by one is a correct answer to `batch:
true`.

A test file that holds the line `//ci:alone` runs in an invocation of its
own in both modes, and one with `//ci:race` runs alone with the race
detector. You read the line; there is no second way to ask.

The results and recordings you report per slice must be the same in both
modes. The kit runs your driver both ways on the same slices and compares
them ([testing](#testing)).

## One result per slice

Take each slice's result from this invocation's own record of it, never
from an exit code alone or from files an earlier batch left: a bundle
that exits 1 has slices that passed. Use per-test events where the tool
gives them (`go test -json`, Playwright's JSON reporter), and otherwise
the per-target record: Bazel's `testResult` build event, with its
status, whether it was cached, and its outputs, never `test.xml`, which
rules_go leaves empty under `bazel coverage`. Give each failing test the
last lines of its own output, so the report shows why. A slice with no
record of its own is not passed. A slice whose code didn't build ends
failed with the build error.

Write an `end` only for a slice your invocation gave a result or a build
failure. Never run a slice again yourself: running a slice again alone is
pipelinegen's ([a bundle that dies](#a-bundle-that-dies)).

Force nothing to run again: no `-count=1`, Bazel's result cache on. A
result the tool replays from its cache is a result, and its stored
recording is the recording; say so with `"replayed": true`. The one
exception is a request with `"stack": true`, which a stack suite's
requests carry in every run: its slices talk to servers that are no
input of the tool's cache, so never let the tool replay them (the bazel
driver adds `--nocache_test_results`, the go driver `-count=1`).

The command driver can't read per-test events from a command it doesn't
know. It is the one exception: it takes the result file the command
wrote, or else the exit code, and says so in its description
(`"results": "exit"`).

## Failure files

What a failed test leaves to show why, a screenshot or a trace, is lost
with the batch's container unless your `end` names it. Copy it into the
slice's `files` directory (create it; it may not exist) and name each
file in the end's `files`, by its path in that directory, with a short
label a person reads:

```json
{"slice":"e2e/checkout.spec.ts","event":"end","failures":[{"test":"checkout pays","tail":"..."}],
 "files":[{"path":"checkout-pays/test-failed-1.png","label":"screenshot of checkout pays"}]}
```

pipelinegen keeps them with the slice's state when the slice failed or
errored, within the caps of `keep.files`, and refuses a path with a `..`
part, an absolute one, and one that reaches outside the directory through
a symlink ([failure files](../contracts.md#failure-files)). Name files
only for a slice that didn't pass, and only what helps a person see why:
the kit refuses an end that names a file for a passed slice, or one that
isn't a regular file inside the directory. A request without `files`
comes from an older pipelinegen that keeps none: copy nothing then.

The playwright driver names each failed test's `screenshot`,
`error-context` and `trace` attachments, from Playwright's own JSON
report. The go driver names none: a Go test's failure is its output,
which the end's `tail` already holds, and a test that writes files of
its own knows best which matter. The command driver passes the
directory as `{files}`, and the result file a step writes names its
files from there.

## One recording per slice

In a recording run, each slice's recording goes into its `recording`
directory, in your tool's own form: a Go coverage profile, an LCOV file.
Convert nothing; the correlators read those forms. Never hand every slice
of a bundle the same recording of the whole bundle: the correlators would
bind every file of it to what any of them ran, and those files would be
selected together on every change from then on.

When your bundles keep the recordings apart, say `"apart": true`, and
only once you have shown it on real tests. The go driver's recording
bundle builds each package's test binary once with `go test -c` and runs
it once per file exactly as `go test` runs a binary (the binary in a
directory of its own with Go's coverage directory beside it, and the
environment `go test` gives it), with that file's `-test.run` and its own
`-test.coverprofile`. A plain `go test -c` binary run another way lost the
coverage of child processes that tests start with a cut-down
environment, which is why the layout matters. Without `apart`, a recording run calls you
once per slice even with `batch: true`, except in a stack suite, whose
bundles are marked instead (next section). With `apart`, a recording
run's bundle of a suite with correlators is marked too.

## Recording with marks

In a suite with `stack: true`, the servers the tests talk to keep running
across the batch, and in a recording run pipelinegen collects their
coverage for each slice at its boundaries. A suite's correlators collect
at each slice's start and end in any suite (a statement log, a request
log, a stack's counters), and those calls must come at the slice's real
edges, never after it ran: so a recording run's bundle of a suite that
names correlators is marked too, stack or not, wherever it bundles. The
request then carries `marks`:

```json
"marks": {"command": ["/b/tools/pipelinegen", "slice", "mark"], "dir": "/b/work/marks"}
```

Run the bundle's slices one at a time, never two at once, since one stack
serves them all and its coverage couldn't be told apart; say once on
stderr that you run the bundle serially. At each boundary call, and wait
for:

```sh
/b/tools/pipelinegen slice mark start e2e/create.spec.ts   # before the slice's first test
/b/tools/pipelinegen slice mark stop e2e/create.spec.ts    # after its last test ended
```

Each call returns once the running `pipelinegen slice run` has collected
the stack's coverage for the slice that ended and noted the next one's
start. It is bounded (each collector's `collect_seconds`, 10 by default,
and a margin) and
fails loudly when nothing answers. The call that runs such a bundle, and
no other call, also has `PIPELINEGEN_MARKS` in its environment: the
bundle's marks directory, the request's `marks.dir`, made afresh for the
bundle and removed when it ends. A hook inside the tests may run the
command, or, when it can't start a process, do the exchange with files in
that directory: write `<dir>/<n>.json`, holding
`{"mark": "start", "slice": "..."}` or `"stop"`, by rename, and wait for
`<dir>/<n>.done` or `<dir>/<n>.failed`. Results still come on your stdout
as `end` events.

A hook that gets no answer fails closed: no answer to a start, and the
slice's tests don't run and it fails; no answer to a stop, and it fails.
Where your tool runs tests elsewhere (Bazel's remote execution) and can't
reach the marks, fail those slices with that reason rather than run them
unmarked.

Every driver that ships holds at boundaries: the go driver marks around
each run of the built test binary; the playwright driver runs the bundle
with one worker and a fixture that marks at each spec file's start and
end; the bazel driver runs one test at a time with `--local_test_jobs=1`
and wraps each test action with `--run_under` in a command that marks;
the command driver's `all` command marks itself, with `{mark}`.

The playwright driver can't mark from outside the browser tests, so it
hands the work to the suite's coverage fixture, which runs in the
Playwright worker. In a recording run it sets `PIPELINEGEN_COVERAGE`, the
batch's coverage directory (a spec file's recording goes into
`<that>/<spec file>`), and `PIPELINEGEN_TREE`, the tree the spec files
are named from. In a marked bundle it runs one worker and passes
`PIPELINEGEN_MARKS`, which only that call has, on to the tests; every
other call, a spec file run again alone after the bundle among them, has
no `PIPELINEGEN_MARKS`. react-correlator's coverage fixture marks each
spec file by itself when `PIPELINEGEN_MARKS` is set. A suite with a fixture of its own imports `marks.mjs`,
which `playwright-driver marks <dir>` writes into `<dir>`, and calls
`markStart(slice)` before a spec file's first test and `markStop(slice)`
after its last; without `PIPELINEGEN_MARKS` both do nothing.

A slice of a marked bundle that ends without its start mark (a replayed
result, or a driver or fixture that doesn't mark) had nothing collected
at its edges: in the recording run it errors, saying so, never counted as
having touched nothing. The reason names the driver that must mark, and
`batch: false` on the run step, which gives each slice a call of its own
and real edges without marks. The released drivers that need more than
the tool: a recorded browser suite through the playwright driver needs a
fixture that marks (react-correlator's, or the suite's own with
`marks.mjs`) or `batch: false`, and the command driver's `all`
form needs `{mark}`; its `each` form is marked around by the driver
itself.

## A bundle that dies

A bundle can die before all its slices end: a panic, a test that calls
`os.Exit`, a time limit, a killed process, a Bazel server that went away.
Don't recover yourself: when `run` returns, pipelinegen runs every slice
without an `end` again in a call of its own, and reports it only after
that. A slice that still has no `end` is errored, with your last lines on
stderr. A missed test is a bug, and so is a lost result.

## Prepare and set-up

`prepare` gets the same request without slices, plus `prepared`, the
directory to fill. It runs once per build of the suite, in the suite's
prepare task, and answers by exiting: 0, or a failure that fails the
suite's build.

`setup` gets the same request without slices. It starts what the batch
needs, and when that is ready it writes one line:

```json
{"env": {"WORDS": "http://127.0.0.1:8080"}}
```

Then it keeps running. The variables reach every later step's request
and the tests those steps start. When the batch ends, whatever happened,
pipelinegen closes the set-up's stdin, then sends SIGTERM to its process
group, then SIGKILL; the last set-up started is stopped first. Stop what
you started cleanly, and exit, when stdin closes, since a coverage build
writes its counters only when it exits. Nothing a set-up starts can
outlive its batch.

pipelinegen waits 10 seconds for the set-up to exit once its stdin
closed, and 10 more after SIGTERM, before SIGKILL.

## Settings

A driver's own settings come from the step's `with`, merged over the
plugin entry's `config`, and are declared in `describe`. Offer what
adopters used to write into their task files, and default to the plain
run: the go driver offers `race` and extra `flags`, and adds neither the
race detector nor `-count=1` unless a suite sets them. Hold a slice's own
time limit yourself (`go test -timeout`), and give an invocation of
several slices that limit once per slice, as the go driver and the shell
driver do, so bundling never tightens it; the request's `deadline` is the
limit of the whole task.

Never ignore a setting. A key you don't know, at the top of the settings
or inside an item of a list or a map (`checks: [{name: lint, timout:
5m}]`), a value of another type, a key with no value, and a value that
would read as left out (a timeout of 0, an empty name) leave a step
running without what its author wrote, unseen. Refuse them in every verb
before you run anything, and name the place and what exists there:
`settings: checks[2].timout: unknown key; the keys of a check are name,
run, timeout, need`. `plugin.DecodeSettings` decodes settings into a
struct that way, by its `json` tags at every depth; a struct with a
`SettingNoun` method names its keys in the refusal ("a check"). Return
your own refusals as a `*plugin.SettingError` with the place.

Give the same check to `pipelinegen check` with `Validate` among
`plugin.DriverHandlers`: a function of a verb and settings that returns
the refusal, the one your verbs return. `describe` then says
`"validates": true`, and the check calls `validate` once for all of a
driver's steps, with each step's verb and settings as a run gets them,
and reports each refusal by its place in the config. Keep it to what it
can tell without the machine: no tool looked up on `PATH`, no file of
the tree read, no cache directory, since the check runs where the config
is edited. The check keeps the answer by the driver's pin and the
request, so a config that didn't change costs no call; where the driver's
binary isn't there and no answer is kept, it checks the top-level keys
against `settings` and warns `undescribed-driver`. A tool that doesn't
know `validates` ignores it, and a driver without it is checked against
`settings` alone, as before.

The request and its answer:

```json
{"protocol":1,"steps":[{"verb":"run","settings":{"checks":[{"name":"lint","run":"make lint","timout":"5m"}]}}]}
```

```json
{"steps":[{"problems":[{"place":"checks[0].timout","problem":"unknown key; the keys of a check are name, run, timeout, need"}]}]}
```

## Rendering

A runner normally runs a suite's drivers through `pipelinegen slice run`
in a container step. A CI system with test plugins of its own can run the
test tool itself, and a driver that knows how says so in `describe`:

```json
{"name": "playwright", "kind": "driver", "version": "v0.4.0",
 "renders": [
   {"target": "piperun", "versions": ">=1.2 <2", "plugins": {"playwright": ">=1.0 <2"}},
   {"target": "github-actions", "versions": "*"}
 ]}
```

`target` names the CI system. `versions` is a semver range of the
target's versions the driver renders for: comparators separated by spaces
must all hold (`>=1.2 <2`), `||` separates alternatives, `!=1.3.2`
excludes one version, a bare version is exactly that one, and `*` is any;
a pre-release matches only a range that names a pre-release of the same
version, as npm reads ranges. `plugins` names the target's own plugins
the rendered steps use, each with the range of its versions the steps
work with. A range that doesn't parse fails `describe`'s check, and so
does a target named twice. `plugin.ServeDriver` refuses a `describe` with
`renders` from a driver that has no `Render` handler.

The runner calls `render` with one suite's step:

```json
{"target": "piperun", "target_version": "1.2.0",
 "suite": "e2e", "step": {"run": "playwright", "with": {"config": "e2e/playwright.config.ts"}},
 "setup": [{"setup": "command", "with": {"start": ["{prepared}/shop"], "ready": {"http": "http://127.0.0.1:8080/health"}}}],
 "shape": {"kind": "pool"},
 "record": true, "observed_by": ["go-correlator", "react-correlator"],
 "image": "playwright", "packages": ["postgresql-client-17"],
 "params": {"PROJECTS": "phone-dark,desktop-light"}, "secrets": ["STRIPE_KEY"]}
```

`shape` is `pool` when the target learns a batch's slices only as it
expands the step and feeds them in chunks, so the driver writes a
template, not a list of slices; `batch` is a fixed list, for a target
without pools. `secrets` are names only: the pipeline is written long
before any value exists, and the rendered steps name each secret for the
target to fill in. The answer:

```json
{"steps": [{"uses": "playwright", "with": {"config": "e2e/playwright.config.ts"}, "secrets": ["STRIPE_KEY"]}],
 "marks": "protocol", "results": "results",
 "caches": {"npm": "/root/.npm"}}
```

`steps` are the target's own steps, each a JSON object of the target's
schema. `marks` says how the steps report where each slice starts and
ends: `protocol`, through the target's plugin protocol, or `file`, in a
marks file the target reads after the step. `results` is the directory,
inside the step's own, that holds one result file per slice in a batch's
result format. `caches` are the caches the steps mount, by name, at
absolute paths.

The runner decides per driver whether to render or to fall back to
`slice run`, with `plugin.RenderTarget.Choose` in Go. It renders when
three things hold: the driver's range for the target admits the target
version the runner writes for, the runner's own range for the driver (if
it restricts which versions it renders) admits the driver's version, and
every plugin in `plugins` is pinned in the target's config at a version
its range admits. Otherwise it falls back, and the pipeline's notices name
the first of them that failed: `go-driver v0.4.0 renders piperun >=1.3
<2; this runner targets 1.2.0: fallback`. A missing renderer never fails
a pipeline.

## Where a driver runs

A driver runs in the suite's own tasks, on the test machine: `prepare` in
the prepare task, `setup` and `run` in the batch task, with the tree under
test as `tree`. The core never calls a driver while planning. A driver
comes in the task's `tools` input as `tools/plugins/<name>`, fetched by
the bootstrap like every plugin and never built from the tree, and
`pipelinegen slice run` refuses one whose checksum isn't the trusted
pin. pipelinegen
takes every token and the trusted config out of a driver's environment;
a driver gets the suite's `secrets.batch` (`secrets.prepare` for a
prepare step) and nothing else. In a proposed change's run the steps and
their settings come from the trusted config, not from the change.
Everything else in [what every plugin
shares](../plugins.md#what-every-plugin-shares) holds, except that a
driver executes the tree's code: that is its job.

## Testing

`pipelinegen plugin test <executable> --tree <fixture>` checks a driver
as `slice run` calls it, as for every plugin ([testing](README.md#testing)).
The fixture's `.pipelinegen-kit.json` has a `driver` section: the
`slices` to run (at least two, so there is a bundle), whether to `record`
them, the run step's `config`, a set-up's settings as `setup`, and a
prepare step's as `prepare`:

go-driver's own fixture, `driver/testdata/mod/.pipelinegen-kit.json` in
its repository, a small module whose `kid` package starts a child
process:

```json
{"driver": {
  "record": true,
  "slices": [
    {"name": "calc/add_test.go", "sources": ["calc/add_test.go"], "tests": ["TestAdd", "TestAddSub"]},
    {"name": "calc/mul_test.go", "sources": ["calc/mul_test.go"], "tests": ["TestMul"]},
    {"name": "kid/kid_test.go", "sources": ["kid/kid_test.go"], "tests": ["TestChild"]},
    {"name": "kid/other_test.go", "sources": ["kid/other_test.go"], "tests": ["TestOther"]}
  ]
}}
```

And the kit on it:

```
$ pipelinegen plugin test go-driver --tree driver/testdata/mod
ok    describe answers the protocol
ok    refuses no-such-call
ok    refuses units
ok    run answers every slice, one by one and bundled, not recording
ok    one by one and bundled give each slice the same result, not recording
ok    run answers every slice, one by one and bundled, recording
ok    one by one and bundled give each slice the same result, recording
ok    one by one and bundled leave each slice the same recording
ok    a recorded stack suite's bundle marks each slice's start and stop, one slice at a time
ok    a recorded stack suite's bundle, marked, gives each slice its result on its own
ok    a recorded bundle of a suite with correlators marks each slice's start and stop, one slice at a time
ok    a recorded bundle of a suite with correlators, marked, gives each slice its result on its own
ok    no answer or message carries the token
```

The kit then checks:

- the `describe` answer: its verbs, the types of its settings, `results`;
- that `run` answers every slice, one by one and bundled, and gives each
  slice the same result both ways; with `record`, the same again in a
  recording run, and that each slice leaves the same recording on its own
  and bundled (for a driver that says `apart`);
- with `record`, the marks: a stack suite's bundle, and for a driver that
  says `apart` a bundle of a suite with correlators, each mark each
  slice's start and then its stop, in pairs, and give the same results as
  each slice on its own (a driver without `apart` is called once per slice
  in a recording run of a suite without a stack, so that bundle isn't
  checked);
- for a driver with `setup`, that it gets ready and stops when its stdin
  closes;
- for a driver with `prepare`, that it exits 0 given the fixture's
  `prepare` settings and leaves something in the `prepared` directory;
- for a driver that says `validates`, that `validate` takes the fixture's
  settings of each verb it declares, refuses them with a key added that
  no driver takes and names that key, and that each verb refuses the same
  settings, naming the key, before it answers anything.

The playwright driver's fixture, `driver/testdata/web`, is a project of
three spec files whose tests need no browser, with a coverage fixture of
its own that writes a stand-in recording and marks through `marks.mjs`;
the kit on it:

```
$ pipelinegen plugin test playwright-driver --tree driver/testdata/web
ok    describe answers the protocol
ok    refuses no-such-call
ok    refuses units
ok    run answers every slice, one by one and bundled, not recording
ok    one by one and bundled give each slice the same result, not recording
ok    run answers every slice, one by one and bundled, recording
ok    one by one and bundled give each slice the same result, recording
ok    one by one and bundled leave each slice the same recording
ok    a recorded stack suite's bundle marks each slice's start and stop, one slice at a time
ok    a recorded stack suite's bundle, marked, gives each slice its result on its own
ok    a recorded bundle of a suite with correlators marks each slice's start and stop, one slice at a time
ok    a recorded bundle of a suite with correlators, marked, gives each slice its result on its own
ok    no answer or message carries the token
```

For a driver that names `renders`, the `driver` section also has a
`render` section: per target the `versions` to render for, each inside
the declared range since a range alone names none, and the `shapes`
(both when left out), and the suite's `step`, `setup`, `record`,
`observed_by`, `image`, `packages`, `params` and `secrets` as a request
carries them:

```json
{"driver": {"slices": [...], "render": {
  "targets": {"piperun": {"versions": ["1.2.0", "1.9.3"], "shapes": ["pool"]}},
  "step": {"run": "go-driver", "with": {"packages": "./..."}},
  "secrets": ["STRIPE_KEY"]}}}
```

The kit calls `render` for each target, version and shape, and checks
that each answer is a render answer and nothing else, with at least one
step, each a JSON object, `marks` one of the two, `results` a relative
directory and each cache at an absolute path. Each request names the
secrets with the kit's canary in their variables, which the driver never
gets: neither the request nor any answer may carry it. A driver without
`renders` is never asked to render.

A driver that declares `run`, `setup` or `prepare` and whose fixture has
no `driver` section fails the kit, and so does one that declares
`prepare` and whose `driver` section has no `prepare`: those verbs never
ran.

## Releasing

Release a driver like any plugin, with `bazel run //ci/release` from this
repository ([releases.md](../releases.md)): static binaries, the
driver's `describe` answer as `plugin.json`, and `SHA256SUMS`.
Repositories pin a driver by version and checksum in an entry with
`kind: driver`. The command driver is part of pipelinegen itself: a step
names it as `command`, with no entry.
