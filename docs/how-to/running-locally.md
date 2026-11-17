# Run it on your machine, without CI

You can see what CI would run for a change, and run it, on your own
machine. No CI system, git host or token is needed. `pipelinegen run`
plans a change and runs the slices it selects, the way a pull request's
run would. `affected` and `why` show the plan, and `record` records the
lists of what each slice executed. This page shows how, and what differs
from a CI run.

`affected`, `why`, `run` and `record` read commits, never your working
tree. Commit your change first, on a branch of its own if you like.

## What your machine needs

- **pipelinegen**, installed as in [step 2](../tutorial/install.md).
- **Bazel through bazelisk.** pipelinegen runs the command
  `bazel.bazelisk` names, `bazelisk` on your `PATH` by default, and
  bazelisk runs the Bazel version of `.bazelversion`.
- **Docker or Podman**, for the suites that name an image. Without
  either, `--no-image` runs the steps on your machine, which then needs
  the tools the images have: Go for Go tests, Node and a browser for
  browser specs.
- **The right Go first on your `PATH`**, when a Go plugin needs a newer Go
  than your default. [Where pipelinegen keeps
  things](../reference/your-machine.md#the-work-tree-and-your-toolchain)
  says why.

The plugins and drivers come by themselves. A command that runs one and
doesn't find it in `tools/plugins/<name>` (or the directory `--plugins`
names) fetches it from its pinned release into your user cache and checks
it against the pin's checksum. No token is needed. A plugin pinned by
`commit` has no release: the command stops, and its error gives the
commands that build it into `tools/plugins`. `pipelinegen check` fetches
the plugins it asks the same way; for one pinned by `commit` that isn't
there, its warning gives the commands that build it.

The first plan also downloads bazel-diff, at the release pipelinegen
pins. `affected`, `why` and `run` fill the Go plugins' module cache with
`go mod download` before they plan. In CI, the planning step's
`concourse.planner.prepare` command does that.

## See what a change would run

`pipelinegen check` says which files no slice covers. It reads BUILD
files and git and starts no Bazel, so it is quick. `pipelinegen slices`
lists every suite's slices.

`pipelinegen affected --base <ref>` plans your commit against the base,
as the planning job would, and prints the slices it selects. Use the
branch you will merge into as the base, such as `--base main`. On the
notes service, after a change to the backend's validation:

<!-- output: affected-validate -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
pipelinegen affected: filling the gomodcache cache for go-sbom, go-correlator: go mod download in ., into /home/you/.cache/pipelinegen/repos/notes-2afb52553fae/caches/gomodcache
e2e: 1 selected, 1 untouched
  run e2e/create.spec.ts
go: 1 selected, 4 untouched
  run api/create_test.go
```

`pipelinegen why` says why one slice was selected or left out:

<!-- output: why-validate-create -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 26118462acef to 8933844527a0:
  it executed api.validate during its recording (api/api.go), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

A suite bound by the graph needs nothing more. A suite bound by
recording needs lists. With none, each of its slices is bound to
everything and runs. The lists come from `pipelinegen record`, or from
`run --record`, which write them to a local store. A config that names no
remote reads that store when there is one. A config that names one (a
`github` section, or `git.remote`) reads the remote: add `--local` to
read the local store instead. When the remote can't be read, `affected`,
`run` and `why` say so and bind the recorded slices to everything; `why`
says it in a warning below its reasons.

## Run what a change selects

`pipelinegen run --base <ref>` plans like `affected` and runs only the
selected slices. On the small example of the repository, after a commit
that changes `calc/lib.sh`:

```
$ pipelinegen run --base HEAD~1
smoke: 0 selected, 2 untouched
unit: 3 selected, 1 untouched
  run calc/add_test.sh
  run calc/lib_test.sh
  run calc/mul_test.sh
prepared unit
setup ended
slice calc/add_test.sh: passed
slice calc/lib_test.sh: passed
slice calc/mul_test.sh: passed
ran at e756ea760d06:
  unit: 3 passed, 0 failed, 0 errored
```

First comes the plan, then each suite with a selected slice runs: its
prepare once, then one batch of its selected slices, through its drivers
or its task files. The last lines sum up each suite. The tests' own
output, and why a slice failed, go to stderr; without `--work`, the
failures are said there in full, as the run's records go with its
temporary directory. A failed slice is named in the summary, and `run`
exits 1:

```
ran at 9dc1d1f84ba1:
  unit: 0 passed, 1 failed, 0 errored
    failed text/upper_test.sh
```

`--base` is required, so a forgotten flag doesn't run every suite whole.
`--all` runs every slice instead, and `--suite` limits the run to one
suite (repeat it for more). `--slice` limits it to one slice, by name or
label (repeat it for more): with `--all`, any slice; otherwise one the plan
didn't select runs too, and a line on stderr says it wasn't selected. With changes in your working tree, `run`
still runs, and its last line on stderr names what it left out:

```
pipelinegen run: uncommitted changes are neither planned nor run; this tests e756ea760d06 as committed, without:
  text/lib.sh
```

Without `--work`, each suite builds in a new temporary directory, removed
after. With `--work <dir>`, each suite's build goes to `<dir>/<suite>/build`
and stays: each slice's result is in `build/result/`, and a later run in
the same directory reuses its caches.

Add `--record` to record the suites bound by recording as you run them.
Each such suite's lists are attached to the commit in the local store
when every slice of it that ran passed. A suite bound by the graph runs
without, and a line on stderr says so. Without `--record`, the local
store is left as it was.

## Where the steps run

A suite that names an `image` runs its steps in that image, as on CI,
with `docker` or `podman`, whichever is on your `PATH`; `--container-tool`
picks one. An image you don't have is pulled first. A line on stderr says
where each step runs:

```
pipelinegen: suite unit's prepare runs in busybox:1.37.0 (suites.unit.image (busybox)), with docker
pipelinegen: suite unit's batch runs in busybox:1.37.0 (suites.unit.image (busybox)), with docker
```

The steps run as root, as a CI task does. What they wrote is given back
to you when they end, so the build directory stays yours. `--as-me` runs
them as you instead, except a privileged suite's. The container gets the
build's own variables and the suite's `params`, never a token or the rest
of your environment. `--no-image` runs the steps on your machine.
[In their images](../reference/commands.md#in-their-images) says what is
mounted and which pipelinegen runs inside.

## Record the lists

`pipelinegen record --suite <suite>` runs one suite bound by recording at
`HEAD`, every slice in one batch, and attaches the lists to `HEAD` in the
local store, `.git/pipelinegen/local.git`, when every slice passes.
Nothing is pushed anywhere. The [tutorial's step
8](../tutorial/on-your-machine.md) does this on the notes service, with
all the output.

`--slice` records only the slices you name, by name or label. Repeat it
for more than one; a comma doesn't separate names:

```sh
pipelinegen record --suite go --slice api/search_test.go --slice text/text_test.go
```

`record` refuses a tree with uncommitted changes, and a suite that isn't
bound by recording or that no correlator observes. `--store <dir>` writes
to another store, which `affected` and `why` don't read. `pipelinegen
seed` puts your recordings into the repository's store for CI's first
runs ([start without a full first run](starting-narrow.md)).

## Run one slice's tests directly

`run --slice` runs one slice through its suite's steps, as CI would. To
debug a failure faster, run the test runner on the slice's file yourself.
`pipelinegen slices --suite go --json` lists the tests of each Go test
file, which `go test -run` takes. On the notes service:

```sh
go test -run '^(TestTitle|TestWords)$' ./text/
```

When the suite's steps start something first, start it yourself. The
notes service's browser specs need the server:

```sh
go run ./cmd/notes -addr 127.0.0.1:8080 -seed e2e/seed.json &
npm ci
npx playwright test --config e2e/playwright.config.ts e2e/search.spec.ts
```

## What differs from CI

- No token, no git host and no CI system. Nothing posts a status or a
  comment.
- Each suite runs as one batch: no `parallel` split, no `run_on` and no
  lock. CI deals a suite's slices into several batches ([durations and
  batches](durations.md)), so a suite's setup runs once here.
- Whole-suite checks don't run. A line on stderr names the ones the plan
  selected.
- With `--no-image`, the steps run on your machine, and a server a setup
  step starts binds its port here.
- The build is a fresh checkout of the commit, as in CI. What your
  working tree has built or installed, such as `node_modules`, isn't in
  it.
- The lists of `record` and `run --record` go to the local store, which no
  clone carries. A test can take another path on your machine than on CI,
  so its list can miss what it runs there. The nightly run of everything
  replaces the lists with what CI saw.

The [command reference](../reference/commands.md) has every flag of
[`run`](../reference/commands.md#pipelinegen-run),
[`affected`](../reference/commands.md#pipelinegen-affected),
[`why`](../reference/commands.md#pipelinegen-why) and
[`record`](../reference/commands.md#pipelinegen-record), and [which
plugins a command fetches](../reference/commands.md#plugins-a-command-fetches).
