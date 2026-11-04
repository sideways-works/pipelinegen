# Step 3: a first config

pipelinegen reads one file, `pipelinegen.yaml`, at the root of the
repository. Start with the smallest one that works: a name and a
[suite](../concepts/suites-and-slices.md) per kind of test. The notes
service has two:

```yaml
pipeline: notes

plugins:
  - name: go
    kind: driver
    repository: sideways-works/pipelinegen-driver-go
    release: {version: v0.1.0-rc.1, sha256: 7783cecb1cf9192cb679de805a2273fd4d4df02ffd4bf019465cc040946f7049}
  - name: playwright
    kind: driver
    repository: sideways-works/pipelinegen-driver-playwright
    release: {version: v0.1.0-rc.2, sha256: 002364b0a8f48dcf8404143afd8a6c1e9bb3ca1686483ff365b1eb4a7a1624fd}

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

`pipeline` names the pipeline pipelinegen will generate. The suite `go`
takes every file ending in `_test.go` as a test file. The suite `e2e`
takes the browser specs, and `owner` names the target from step 1 that
says what they depend on; a Go test's owner is its `go_test` target, found
without a setting.

`steps` say how a suite's tests run. Each names a
[driver](../concepts/drivers.md), a plugin that runs one kind of test:
go-driver runs Go test files with `go test`, playwright-driver runs
Playwright specs. Each driver is a release of its own repository, pinned
under `plugins` by version and by the checksum of its Linux binary.
Nothing runs a test before [step 7](tasks.md), which completes the steps
(the browser specs need a server) and puts the drivers on your machine.

Patterns are paths from the repository's root: `*` stays inside one
directory and `**` crosses any number of them.

## Decision: which suites

One suite per kind of test that runs differently:

- Go tests: `tests: ["**/*_test.go"]`.
- Playwright specs: `tests: ["e2e/**/*.spec.ts"]`, with `helpers` for the
  shared fixtures under the same directory (`helpers:
  ["e2e/support/**"]`).
- Jest or Vitest tests: `tests: ["web/**/*.test.ts"]`.
- A check that isn't a test file, such as lint: a tagged suite, `tag:
  slice-lint`, and the tag on the Bazel targets that run it ([adding a
  suite](../how-to/adding-a-suite.md)).

In a monorepo with several services, one suite per language is usually
enough; the slices still run only where a change reaches them. Split a
suite only when the tests need different CI setups (one starts a database,
the other doesn't).

A test file under a directory Bazel ignores (`.bazelignore`, or
`ignore_directories` in `REPO.bazel`) is no slice, since no Bazel target
can hold it. Leave out other files with `exclude: ["examples/**"]`.

## List the slices

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

**You should see** each of your test files as a slice of its suite, with
its own files indented below it. Files that hold no test of their own
(helpers) are not listed as slices.

**If you don't:**

- A test file is missing: its path doesn't match the pattern. Patterns are
  relative to the root and `*` doesn't cross a `/`; use `**`.
- Files you didn't expect appear (fixtures under `testdata`, examples):
  add `exclude` patterns to the suite.
- `config ...: invalid config: ...`: the message names each problem in the
  file at once. Unknown keys are errors, so a typo never turns a setting
  off quietly.

Commit the file, then go on.

Next: [step 4, the check](check.md).
