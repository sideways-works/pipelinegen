# Suites, slices and batches

## Suite

A suite is one kind of test with one way to run it: the Go tests, the
browser tests, a linter. You declare suites in `pipelinegen.yaml`. A suite
of tests names its test files with patterns and the steps that run them:

```yaml
suites:
  go:
    tests: ["**/*_test.go"]
    steps:
      - run: go
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    owner: "//e2e:e2e"
    steps:
      - run: playwright
        with: {config: e2e/playwright.config.ts}
```

Each step names a driver, a plugin that knows how Go tests, Bazel targets
or Playwright specs run; the [next page](drivers.md) is about them. A
suite can name CI task files of its own instead, the older form
([writing a suite's tasks](../how-to/writing-tasks.md)).

A check that isn't a test file, such as lint, is a suite too. It names a
Bazel tag instead of patterns, and every target carrying the tag is one of
its slices.

## Slice

A slice is what pipelinegen selects, runs and reports on its own. In a
suite of tests, every file that matches the patterns and holds a test of
its own is a slice, named by its path. The notes service has two suites,
`go` with five slices and `e2e` with two:

<!-- output: slices -->
```
$ pipelinegen slices
e2e e2e/create.spec.ts
  e2e/create.spec.ts
e2e e2e/search.spec.ts
  e2e/search.spec.ts
go api/create_test.go
  api/create_test.go
go api/search_test.go
  api/search_test.go
go notes/notes_test.go
  notes/notes_test.go
go search/search_test.go
  search/search_test.go
go text/text_test.go
  text/text_test.go
```

Each line names the suite and the slice; the indented line lists the
slice's own files.

A file that matches the patterns but holds no test of its own is a helper:
shared fixtures, test utilities. It is never a slice; a change to it
reaches every slice that uses it. A Go test file without a top-level
`Test...` function is a helper on its own; for other languages the suite's
`helpers` patterns name them.

In a tagged suite, every Bazel target with the tag is a slice, named by
its label with dots: `//lint:go` becomes `lint.go`.

## Batch

Running each slice in a CI task of its own would pay for the task's setup
(a checkout, a compile, a database, a started server) once per test file.
So pipelinegen deals the selected slices of a suite into a few batches,
and each batch sets up once and runs its slices one after the other. A
suite's `parallel` setting caps the number of batches (4 by default), the
batches of a build run at once, and the slices are balanced by how long
each took before.

When the edit to `Words` selects three slices, they go into up to three
batches; when a one-line fix selects one slice, one batch runs and pays
for one setup. Making every test file a slice therefore costs no extra
setups.

Within a batch, a suite's driver may also run several slices in one
invocation of the test tool, a bundle, while each slice keeps its own
result ([bundled or one by one](drivers.md#bundled-or-one-by-one)).

Next: [drivers: how a batch runs](drivers.md).
