# Adding a test

This page covers suites of test files. In a tagged suite, add the file to a tagged target's `srcs` instead, as
[adding a suite](adding-a-suite.md#a-tagged-suite) shows.

## A new test file

Add the file where the suite's `tests` patterns match it, and commit it.
A file that holds a test is a slice by being there, named by its path.
Nothing is generated or checked in for it. When the suite has an `owner`
pattern, add the target it names for the new file, as the example does for
each of its test files.

With this suite:

```yaml
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts"]
```

`e2e/sharing.spec.ts` is a new slice. Its first run selects it with the
reason `new-slice`, and from then on it runs when a change reaches it.

A Go test file is a slice when it has a top-level `Test...` function of its
own. Its tests run by name: the batch compiles the package's test binary
once and runs each slice's tests with `-run`.

## A test added to a file that exists

A changed test file runs that whole file (`test-changed`); a test removed
from a file does too. A removed file runs nothing of its own any more and
shows as removed in the plan.

## Helpers

A file the `tests` patterns match that holds no test of its own is a
helper: it is never a slice, and a change to it runs every test file that
uses it. Go test files without a `Test...` function are helpers by
themselves; for other files, name them:

```yaml
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts", "e2e/**/*.ts"]
    helpers: ["e2e/support/**", "e2e/**/fixtures.ts"]
```

A file that matches but belongs to no slice of the suite at all (a config
file of the test runner, say) goes into `exclude`.

## Files a test reads

A test that reads a file at run time (a fixture, a photo, a JSON answer)
must say so, or a change to that file would run nothing. Either name the
target that holds the file in a comment near the file's top, in its first
30 lines, in any comment syntax:

```ts
// pipelinegen:data //e2e/fixtures:images
```

```go
// pipelinegen:data //testdata:notes
package notes_test
```

or name it for many files at once in the suite's `extra_deps`:

```yaml
extra_deps:
  - tests: ["e2e/help/**"]
    deps: ["//help:pages"]
```

A data line in the test file itself says that this test reads those
files, as text or whole: no narrowing takes the label for the units the
test was recorded touching, nor for a change its sbom calls unobservable.
`extra_deps` and an owner's `data` say only that the test depends on the
files, and a narrowing may bind them by their units.

Both make the target one of the test file's inputs. Either can also
name a unit by its ID, as an sbom plugin lists it (`go:app#func:Start`):
the test file is then bound to that unit and what links reach from it,
whatever its recording says. `pipelinegen check` counts only `extra_deps`
labels and owner targets so far: a file that only a comment names fails
the check until `extra_deps` or the owner target names it too.

Run backend tests where an undeclared read fails, as in `bazel test`'s
sandbox: a missing declaration then shows up as a failing test, not as a
test that never runs when its file changes.

## Before you commit

`pipelinegen check --staged` in the commit hook refuses a file that no slice
depends on and that isn't listed in `untested`. See [the check](../reference/check.md).
