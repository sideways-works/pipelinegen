# Adding a suite

A suite is one kind of check with one way to run it. There are two kinds:

- a suite of tests names its test files with `tests` patterns, and each
  file is a slice;
- a tagged suite names a Bazel tag, and each target carrying it is a
  slice. Use it for checks whose units aren't test files: a linter, a type
  check, a build.

A suite has one or the other.

## A tagged suite

Tag the targets and name the tag:

```starlark
# lint/BUILD.bazel
filegroup(
    name = "go",
    srcs = ["//:go_sources", "//:golangci"],
    tags = ["slice-lint"],
)
```

```yaml
suites:
  lint:
    tag: slice-lint
    run_slice: ci/lint.yml
runs:
  pr:
    suites: {lint: {}, unit: {}}
```

A slice's own files are what it checks; everything it depends on is what can
change its result. Keep the dependencies exact: a slice that depends on
nothing but its own files only runs when they change, and a slice that
depends on everything runs on every change. When the target that does the
work is another one (a `go_test` that embeds a whole library would select
the slice on every library change), tag a `filegroup` with exactly the right
inputs and name the runnable target with the tag
`pipelinegen-run://pkg:target`.

Check it with `pipelinegen slices --suite lint`, then try a change with
`pipelinegen affected --base HEAD~1 --suite lint`.

## A suite of tests

```yaml
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    helpers: ["e2e/support/**"]
    exclude: ["e2e/playwright.config.ts"]
    owner: "//{dir}:{stem}"
    bind: recorded
    durations: ci/durations/e2e.json
    image: playwright
    steps:
      - run: playwright
        with: {config: e2e/playwright.config.ts}
    parallel: 3
```

- `tests`, `helpers` and `exclude` say which files are slices, which are
  shared helpers and which belong to no slice; see
  [adding a test](adding-a-test.md).
- `owner` names the target that holds a test file's graph edges, with
  `{dir}`, `{file}`, `{base}` and `{stem}` filled from its path:
  `//{dir}:{stem}` for `e2e/editor.spec.ts` is `//e2e:editor`. Give
  each spec such a target when its imports, the files it reads and the app
  it opens differ from its neighbours'. Without `owner`, a file depends on
  its package's target.
- `bind: recorded` binds each file to what it touched when it ran, so a
  change to a backend route runs the specs that called it. What it touched
  comes from the correlators of the configured [plugins](plugins.md) (for
  the frontend, `react-correlator`), so a recorded suite needs an sbom and
  a correlator plugin.
- `steps` run the suite's tests through drivers, in the suite's `image`
  ([drivers](../concepts/drivers.md)); a suite whose tests talk to a
  server builds and starts it in steps of its own, as the [guide's step
  7](../tutorial/tasks.md) does. The older form names task files of the
  suite's own instead, `prepare` and `run_slice` ([writing a suite's
  tasks](writing-tasks.md)).
- `durations` is the file that batches are dealt by.
- `group` names a slice's group in the report (default: the leading word of
  the file's name).

## Whole-suite checks

A whole-suite check judges a suite as a whole, such as an API coverage
guard that needs every browser test to have run. It runs only in runs that
selected every slice of its suite, and whenever a change reaches a target
it names in `also_on`. It reports as the slice `whole.<name>`.

```yaml
suites:
  e2e:
    whole_suite:
      - name: api-coverage
        task: ci/api-coverage.yml
        also_on: ["//api/routes:routes"]
```

[Writing a suite's tasks](writing-tasks.md#whole-suite-checks) has the
task's side.

## Tasks, runs and the build setup

- Pin the drivers its steps name, and give its `image` a
  `registry-image` resource in the root pipeline file.
- Add the suite to the runs that should cover it, with per-run params where
  they differ.
- If its jobs need workers of their own (browsers, a big machine), place
  them in the runner's config: for Concourse, a rule such as
  `{for: suite, suite: e2e, tags: [browsers]}` under
  `plugins.<runner>.config.placement`; see
  [sharing machines](sharing-machines.md#which-workers).
- Add the files that say how its tests run and that pipelinegen doesn't
  read itself (a suite with task files: those, such as
  `ci/<suite>-*.yml`) to its `sample_on`: a change to how its tests run
  reaches every slice the same way, so a sample of the suite runs. Use `everything_on` only
  for files that can break one test and not another.
- Set the pipeline again (`pipelinegen pipeline`, then `fly
  set-pipeline`). Once the pipeline exists, its `pipeline` job fails on
  the default branch while the pipeline you set is stale, naming the
  command to set it; with `concourse.self_set: true` it sets the pipeline
  itself.
