# Move a suite from task files to drivers

How to replace a suite's `run_slice` and `prepare` task files with steps
run by drivers. What drivers and steps are is in
[concepts](../concepts/drivers.md). The page works through two suites of
pipelinegen's own repository: the `go` suite, whose task ran Go test
files, and the example's `smoke` suite, whose tests are shell scripts run
against a server. After it you can move a suite of your own, check that
it runs as before, and delete what it no longer needs.

What is released of drivers is in [one place](../concepts/drivers.md#what-is-released).

## What a task file did, and where each part goes

The `go` suite's batch task was a task file, `ci/go-test.yml`. It defined
a function that ran one test file and sourced a loop script,
`ci/slices.sh`, which walked the batch's slices and called the function
between `slice start` and `slice end`. A third script, `ci/result.sh`,
wrapped one `go test` and wrote the slice's result file, and a fourth,
`ci/trim-cache.sh`, kept the task's cache within 4 GB when it ended.
Every part of them has a place after the move:

| In the task files | What it does | After the move |
|---|---|---|
| `image_resource: golang 1.27.1` | the image the batch runs in | the suite's `image`, a `registry-image` resource of the root pipeline file |
| `inputs`, `outputs` | `source`, `batches`, `tools`; `result`, `coverage` | the generated batch task has them |
| `params: {GOFLAGS: -trimpath}` | plain environment for every Go command | the suite's `params`, which reach every step and every test |
| `caches`, `GOCACHE`, `GOMODCACHE`, `XDG_CACHE_HOME` under `$PWD/cache` | Go's caches and the bazel-diff the tests fetch, kept with the job | the suite's `params`, under `{cache}`, a directory kept with the task between builds; without them the go driver keeps Go's caches under its own cache directory |
| `trap 'sh source/ci/trim-cache.sh cache 4096' EXIT` | the cache within 4 GB | the go driver's `cache_mb` setting, which brings Go's build cache within it before each run |
| `PATH="$PWD/tools:$PATH"` | bazelisk for the tests that start Bazel | `pipelinegen slice run` puts the tools directory first on `PATH` for every driver and test |
| `. source/ci/slices.sh` | the loop over the batch, with `slice setup-end`, `start` and `end` | `pipelinegen slice run`, which the generated batch task calls |
| `grep -qx '//ci:race'` | the race detector for a file that asks for it | the go driver reads the same line |
| `-timeout 60m` | a slice's time limit | the go driver's `timeout` setting, a test file's limit: a bundle of files gets it once per file |
| `-cover -covermode=atomic -coverpkg=github.com/sideways-works/pipelinegen/... -tags pipelinegen_cover -coverprofile=...` | each slice's coverage in a recording run | the go driver in a recording run: its coverage covers every package of the module unless `coverpkg` names others, and `cover_tags` are the build tags only a recording adds |
| `-run "^(TestA\|TestB)$" ./pkg` | only the file's own tests | the go driver: one file per call with `batch: false`, a package per bundle with `batch: true` |
| `bash source/ci/result.sh` | the result file, failing tests from `--- FAIL:` lines | the go driver, from `go test -json` |

Nothing of it is left to write: the suite's config says which driver runs
its slices, and the driver holds the rest.

## The go suite

Before:

```yaml
suites:
  go:
    tests: ["internal/**/*_test.go", "plugin/**/*_test.go"]
    owner: "//:code"
    bind: recorded
    run_slice: ci/go-test.yml
```

After:

```yaml
plugins:
  - name: go
    kind: driver
    repository: sideways-works/pipelinegen-driver-go
    release: {version: <tag>, sha256: <the release's checksum>}
suites:
  go:
    tests: ["internal/**/*_test.go", "plugin/**/*_test.go"]
    owner: "//:code"
    bind: recorded
    image: golang
    params:
      GOFLAGS: -trimpath
      GOCACHE: "{cache}/go-build"
      GOMODCACHE: "{cache}/mod"
      XDG_CACHE_HOME: "{cache}/xdg"
    steps:
      - run: go
        with: {timeout: 60m, cover_tags: [pipelinegen_cover], cache_mb: 4096}
```

The step names the plugin entry `go`. `with` holds the driver's own
settings: what the task file wrote as flags. `timeout` is a test file's
limit, `cover_tags` the build tag go-correlator's coverage build needs,
which only a recording run adds, and `cache_mb` the cap on Go's build
cache that the trap kept. It caps the build cache alone: the module cache
and what the tests download under `XDG_CACHE_HOME` grow as they need,
where the trap dropped them too once the whole cache passed its cap. The suite's only run step needs no `take`: it
gets every slice.

`params` holds the plain environment the task file set: `GOFLAGS`, and
the caches the task kept under `$PWD/cache`, now under `{cache}`, a
directory kept with the task between builds. `XDG_CACHE_HOME` is where the
tests keep the bazel-diff they fetch.

The batch task no longer names an image, so the suite does. `image` names
a `registry-image` resource of the root pipeline file
([`concourse.include`](../reference/config.md#concourse)), here
[ci/root.yml](../../ci/root.yml):

```yaml
resources:
  - name: golang
    type: registry-image
    tags: [cloud-1]
    source: {repository: golang, tag: 1.27.1}
```

Give the resource the tags of the workers the suite runs on, so the
image is checked and fetched where its tasks run.

`pipelinegen slice run` checks the go driver's needs (`go`, at least the
version `go.mod` names) in the task's container before it runs the step.

Leave out `batch`, so it is `false`. Each test file then runs as it did
in the task: its own `go test` with only its own tests, so the move
changes nothing but who runs the command. Once the suite runs green that
way, set `batch: true`, as the tool's own go suite does ([bundle a
suite's slices](batching.md)).

`go-whole` shared the same task file. It moves the same way, with the
same steps.

## The smoke suite

The example's smoke suite tests a small server. Its slices,
`words/*_smoke.sh`, are shell scripts whose `test_` functions call the
server. Before the move it had two task files: a prepare task,
`ci/smoke-prepare.yml`, that built the server, and a batch task,
`ci/smoke-slices.yml`, that started it, waited until it answered, ran the
batch's files through a loop script, `ci/run-tests.sh`, and stopped it.

Two drivers take all of it over. The command driver, built into
pipelinegen, builds the server and starts it. The shell driver runs shell
test files the way `run-tests.sh` did: each file sourced in its own
directory, each of its `test_` functions in a shell of its own, with
`fail` to stop one with a message, and a result per function. The test
files stay; both task files and `run-tests.sh` go.

In the example's own config, [example/pipelinegen.yaml](../../example/pipelinegen.yaml),
where every path is from the example's root:

```yaml
plugins:
  - name: shell
    kind: driver
    repository: sideways-works/pipelinegen-driver-shell
    release: {version: <tag>, sha256: <the release's checksum>}
suites:
  smoke:
    tests: ["**/*_smoke.sh"]
    owner: "//{dir}:{stem}"
    bind: graph
    image: busybox
    steps:
      - prepare: command
        image: golang
        with: {run: [go, build, -trimpath, -o, "{prepared}/bin/words", .], dir: words, env: {CGO_ENABLED: "0"}}
      - setup: command
        with:
          start: ["{prepared}/bin/words"]
          env: {ADDR: "127.0.0.1:8080"}
          ready: {http: "http://127.0.0.1:8080/healthz", seconds: 60}
          export: {WORDS: "http://127.0.0.1:8080"}
      - run: shell
        with: {functions: test_}
```

The three steps do what the two task files did:

- the prepare step builds the server once per build of the suite, into
  `prepared`, in its own `golang` image, as `smoke-prepare.yml` did; the
  batches run in the suite's `busybox`, as `smoke-slices.yml` did;
- the set-up step starts it in each batch, waits until `/healthz`
  answers, and hands the tests its address as `WORDS`; pipelinegen stops
  it when the batch ends, whatever happened, as the `kill` at the end of
  `smoke-slices.yml` did;
- the run step runs each file's functions whose names start with `test_`,
  as `run-tests.sh` did: the slice's result names each failing function
  with the end of its output, and counts the file's tests.

The shell driver also runs a step's own script, inline in the config
(`script`), once per slice or once for a bundle, and judges a slice by
the script's exit status unless the script writes the slice's result.
That is how a suite whose task file ran a few commands moves without a
script file of its own; its README lists the settings.

The two images are resources of a root pipeline file, which the example
didn't have. `pipelinegen check` says so until the config names one:

```yaml
concourse:
  include: ci/root.yml
```

```yaml
# ci/root.yml
resources:
  - name: busybox
    type: registry-image
    source: {repository: busybox, tag: 1.37.0}
  - name: golang
    type: registry-image
    source: {repository: golang, tag: 1.27.1}
```

The repository's own config runs the same suite from the repository's
root, so there every path of the steps starts with `example/`.

## Check it and run it

1. `pipelinegen check` refuses a slice that no run step takes and one
   that two take, a `with` key the driver doesn't declare, and a suite
   that still names `run_slice` or `prepare` beside its steps. Fix what
   it names until it passes. (`pipelinegen pipeline` refuses steps for a
   runner that can't write their tasks.)
   A run step of the smoke suite that took only `words/upper_smoke.sh`
   would leave the other file to no step (the example's warning about
   its token, which has nothing to do with steps, left out):

   ```
   $ pipelinegen check
   pipelinegen.yaml: suite smoke: 1 slice no run step takes: words/count_smoke.sh
     fix: give a run step a take that matches them, or leave take out on the suite's only run step
   pipelinegen check: 1 finding: did not pass
   [exit 1]
   ```
2. Run the suite on your machine as CI runs it. `pipelinegen slice run
   --suite smoke` runs the prepare step, starts the server, runs every
   slice through the run step, prints each slice's result and stops the
   server; it writes no records:

   ```
   $ pipelinegen slice run --suite smoke
   shell run: ok   words/count_smoke.sh test_count
   shell run: ok   words/count_smoke.sh test_count_nothing
   slice words/count_smoke.sh: passed
   shell run: ok   words/upper_smoke.sh test_upper
   slice words/upper_smoke.sh: passed
   ```

   The go suite runs the same way; [bundle a suite's
   slices](batching.md#try-it) shows it on two files of one package.
3. `pipelinegen record --suite go` runs a suite with steps through the
   same code, so a local recording still works after the move.
4. Push it. The suite's next run selects the same slices and should give
   the same results as before, and in a recording run the same
   recordings.

## Delete what is left

Remove the task files and the scripts that nothing uses any more. In
pipelinegen's own repository every suite moved, so all of them went: the
batch tasks `ci/go-test.yml`, `ci/lint.yml` and `ci/proof/task.yml`, and
`ci/slices.sh`, `ci/result.sh` and `ci/trim-cache.sh` with them. The
example's two suites moved too: its unit suite runs its test files with
the shell driver alone, so all three of its task files and
`ci/run-tests.sh` went.
Search for a script's name before you delete it, and remove it from any
Bazel target that lists it. Then look for the deleted paths in
`everything_on`, `sample_on` and `untested`, and drop the patterns that
matched only them.
A change to a suite's steps runs the slices the changed step runs: a run
step's those it takes, a prepare or set-up step's every slice of the
suite ([a change to the config](../reference/config.md#a-change-to-this-file)).
List `pipelinegen.yaml` under `everything_on` only if every change to it
should run everything.

A suite with steps can't also name `run_slice` or `prepare`. A suite you
haven't moved keeps its task files and works as before.
