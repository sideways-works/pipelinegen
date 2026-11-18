# Step 3: a first config

pipelinegen reads one file, `pipelinegen.yaml`, at the root of the
repository. Start with the smallest one that works: a name and a
[suite](../concepts/suites-and-slices.md) per kind of test. The notes
service has two:

```yaml
pipeline: notes

plugins:
  - repository: https://github.com/sideways-works/pipelinegen-plugins
    with:
      - {name: go, kind: driver, path: driver/go, release: {version: v0.4.0, sums: 9a666c360d7f001477588e54823aa4b46df4117d853ec41e53c1d9b147a81887}}
      - {name: playwright, kind: driver, path: driver/playwright, release: {version: v0.4.0, sums: 98cec967c5d0d7823832a9c1fbad37f71d195f000a5627a9c4e3177c349fbd0b}}

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
Playwright specs. The drivers live in pipelinegen's plugins repository,
each in a directory of its kind and name (`driver/go`), and each is
released on its own. Under `plugins` a group names the repository once,
and each entry its `path` and a release, pinned by version and by the
checksum of the release's `SHA256SUMS`, the list of its files' checksums.
`pipelinegen plugin add` writes such entries for you, each named as its
plugin names itself (`go-driver`) unless `--name` gives it another
([adding a plugin](../how-to/adding-a-plugin.md)):

```sh
pipelinegen plugin add https://github.com/sideways-works/pipelinegen-plugins driver/go --name go
pipelinegen plugin add https://github.com/sideways-works/pipelinegen-plugins driver/playwright --name playwright
```

Nothing runs a test before [step 7](tasks.md), which completes the steps
(the browser specs need a server).

## Put the drivers where pipelinegen finds them

The CI pipeline downloads the pinned drivers itself. On your machine,
pipelinegen looks for them in `tools/plugins/<name>` under the
repository's root, and `pipelinegen check` asks each what settings its
steps take. One it doesn't find there it fetches from its pinned release
into your user cache, checked against the pin. To keep them in the tree
instead, download each with its release's `SHA256SUMS` beside it as
`<name>.SHA256SUMS`, keep `tools/` out of git, and check each list against
its pin, the checksums from the config in a file (`tools/drivers.sums`
here). pipelinegen then checks each driver against its line in the list
beside it before it runs it:

```sh
mkdir -p tools/plugins
curl -fsSL -o tools/plugins/go \
  https://github.com/sideways-works/pipelinegen-plugins/releases/download/driver/go/v0.4.0/go-driver-linux-amd64
curl -fsSL -o tools/plugins/go.SHA256SUMS \
  https://github.com/sideways-works/pipelinegen-plugins/releases/download/driver/go/v0.4.0/SHA256SUMS
curl -fsSL -o tools/plugins/playwright \
  https://github.com/sideways-works/pipelinegen-plugins/releases/download/driver/playwright/v0.4.0/playwright-driver-linux-amd64
curl -fsSL -o tools/plugins/playwright.SHA256SUMS \
  https://github.com/sideways-works/pipelinegen-plugins/releases/download/driver/playwright/v0.4.0/SHA256SUMS
chmod +x tools/plugins/go tools/plugins/playwright
echo '/tools/' >> .gitignore
```

<!-- output: sha-drivers -->
```
$ sha256sum -c tools/drivers.sums
tools/plugins/go.SHA256SUMS: OK
tools/plugins/playwright.SHA256SUMS: OK
```

`/tools/` keeps the downloads and the checksum file out of git; a file git
doesn't ignore is one the check asks you to cover. If your repository has
a `tools/` directory of its own, ignore only `/tools/plugins/`, as [the
worked
example](../worked-example/README.md#step-6-bind-the-go-tests-by-what-they-ran)
does, and keep the checksum files under `tools/plugins/` too.

On macOS, or for a release without a binary for your system, build a
driver from source instead (`go install
github.com/sideways-works/pipelinegen-plugins/driver/go/cmd/go-driver@v0.4.0`,
the same for playwright-driver) and copy the binary into
`tools/plugins/` under the name the config gives it.

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
