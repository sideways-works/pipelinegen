# Step 7: the suites' steps

pipelinegen's core doesn't know how your tests run: each suite's `steps`
say it, and the drivers they name do it. The config of [step
3](first-config.md) names a driver per suite. This step puts the drivers
on your machine and completes the browser suite, whose specs need a
running server. It is the one piece of CI you own; everything around it
is generated.

## Put the drivers where pipelinegen finds them

The drivers are plugins, and live beside the others in `tools/plugins/`.
Download each and check it against its pin, the drivers' checksums in a
file of their own:

```sh
curl -fsSL -o tools/plugins/go \
  https://github.com/sideways-works/pipelinegen-driver-go/releases/download/v0.1.0-rc.1/go-driver-linux-amd64
curl -fsSL -o tools/plugins/playwright \
  https://github.com/sideways-works/pipelinegen-driver-playwright/releases/download/v0.1.0-rc.2/playwright-driver-linux-amd64
chmod +x tools/plugins/go tools/plugins/playwright
```

<!-- output: sha-drivers -->
```
$ sha256sum -c tools/drivers.sums
tools/plugins/go: OK
tools/plugins/playwright: OK
```

## Go tests

The `go` suite's one step is all it needs. go-driver runs each test file's
tests with `go test -run`, makes each file's result from Go's output and,
in a recording run, builds with coverage of every package of the module
and leaves each file's coverage where go-correlator reads it. Its
settings (a time limit, build tags, the packages a recording covers) are
in [go-driver's README](https://github.com/sideways-works/pipelinegen-driver-go).

## Browser tests with Playwright, against a Go server

The specs talk to the notes server. The suite builds the server once,
with coverage, starts it for each batch, and runs the specs against it,
one spec file at a time:

```yaml
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    owner: "//e2e:e2e"
    bind: recorded
    correlators: [go-correlator, oapi-codegen-correlator]
    # the specs talk to one server for the whole batch, whose coverage
    # go-correlator takes at each spec's edges
    stack: true
    steps:
      # the server, built once with coverage and go-correlator's cover
      # package
      - prepare: command
        with: {run: [go, build, -tags, pipelinegen_cover, -cover, -covermode=atomic, "-coverpkg=example.com/notes/...", -o, "{prepared}/notes-server", ./cmd/notes]}
      # started once per batch with the seed notes; it stops when the batch
      # ends
      - setup: command
        with:
          start: ["{prepared}/notes-server", -addr, "127.0.0.1:8080", -seed, e2e/seed.json]
          env: {PIPELINEGEN_COVER_DIR: "{work}/ctl", NOTES_REQUEST_LOG: "{work}/requests.log"}
          ready: {http: "http://127.0.0.1:8080/notes", seconds: 30}
          export: {NOTES_URL: "http://127.0.0.1:8080"}
      # one spec file at a time: pipelinegen takes the server's coverage
      # between them
      - run: playwright
        with: {config: e2e/playwright.config.ts}
```

Both the prepare step and the set-up use the `command` driver, which is
built into pipelinegen and needs no entry under `plugins`. Its `run` runs
a command to its end; its `start` starts one that keeps running, `ready`
waits until it answers, `export` hands variables to the later steps and
the tests, and the command stops when the batch ends. `{prepared}` is the
directory the prepare step fills and every batch reads, `{work}` the
step's own directory for the batch (`work/command` under the batch's
directory, the path step 6 gave `request_log`).

`stack: true` says that the specs share something that keeps running, the
server, so a spec's recording is what the server ran between that spec's
start and its end. go-correlator takes the server's counters at those
edges through `cover_dir`, which goes into its entry:

```yaml
  - name: go-correlator
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-go
    release: {version: v0.1.8-rc.12, sha256: 9850db46545444bd5d4c84631c29d0481c404cf1bfd51d7401ad8cadb0b27337}
    config:
      # the running server's counters pass through here between specs
      cover_dir: work/command/ctl
```

The run step leaves `batch` off, so pipelinegen calls playwright-driver
once per spec file and takes the server's coverage itself before and
after each call, which is why the specs need no fixture that marks their
start and end. A suite that bundles recorded stack slices (`batch: true`)
needs that marks fixture, whose `marks.mjs` `playwright-driver marks
<dir>` writes ([playwright-driver's
README](https://github.com/sideways-works/pipelinegen-driver-playwright)).

### The server's side

The server hands its counters over while it keeps running through
go-correlator's `cover` package, the module
`github.com/sideways-works/pipelinegen-correlator-go/cover` at `v0.1.8-rc.12`, linked
into the coverage build only. A file with the build tag the prepare step
passes links it, [cmd/notes/cover.go](../../example-notes/cmd/notes/cover.go):

```go
//go:build pipelinegen_cover

package main

// A coverage build of the server links go-correlator's cover package: while
// the server keeps running across the browser specs, it hands its counters
// to pipelinegen at each spec's end and starts the next spec from zero.
import _ "github.com/sideways-works/pipelinegen-correlator-go/cover"
```

Add the module to `go.mod`:

<!-- output: go-get-cover -->
```
$ go get github.com/sideways-works/pipelinegen-correlator-go/cover@v0.1.8-rc.12 && go mod tidy
go: added github.com/sideways-works/pipelinegen-correlator-go/cover v0.1.8-rc.12
```

The file is for the prepare step's `go build`, not for Bazel: keep it out
of Gazelle's targets with a line in the root `BUILD.bazel`, and list it
under `untested` in `pipelinegen.yaml`:

```starlark
# gazelle:exclude cmd/notes/cover.go
```

```yaml
  - paths: [cmd/notes/cover.go]
    reason: links go-correlator's cover package into the e2e suite's coverage build only, which Bazel never builds
```

If the module comes to you from a private module proxy, Bazel has to
reach it too. Gazelle resolves the modules of `go.mod` with a go command
that has no git, so the module must come from a module proxy: set
`GOPROXY` to one that serves it, and keep the module out of `GOPRIVATE`
and `GONOPROXY`, which mean fetching it directly. Gazelle takes those
settings when it first fetches, so after changing them run `bazel clean
--expunge`.

With oapi-codegen-correlator, the server also logs the route of each
request it serves, to the file `NOTES_REQUEST_LOG` names; the same
[cmd/notes/main.go](../../example-notes/cmd/notes/main.go) that serves
the page shows how. When the batch ends, the server's coverage build
warns at its stop that `GOCOVERDIR` isn't set (step 8 shows it): nothing
is lost, since go-correlator took its counters at each spec's end.

### What to change for your project

1. The prepare step: `-coverpkg=example.com/notes/...` becomes your
   module's path, and `./cmd/notes` the package of your server's `main`.
2. The set-up's `start`: your server's binary from `{prepared}`, and the
   flags it needs to listen on the address and start with test data.
3. `ready`: a cheap route of yours that answers once the server is up.
4. `export`: the variable your Playwright config reads for its
   `baseURL`, here `NOTES_URL` in
   [e2e/playwright.config.ts](../../example-notes/e2e/playwright.config.ts).

## Another runner

A test tool without a driver of its own runs through the `command` driver
(`each` runs a command per slice, `all` one for the batch) or the shell
driver, whose steps are short scripts in the config and which runs shell
test files function by function ([drivers](../concepts/drivers.md)). A
suite can also name CI task files instead of steps, the older form,
which [writing a suite's tasks](../how-to/writing-tasks.md) describes.

## Check and commit

<!-- output: check-steps -->
```
$ pipelinegen check
warning: cmd/notes/main.go: judged by the plan: go-sbom may claim it, and a file an sbom plugin claims is covered through its units
  fix: nothing, if go-sbom claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested
warning: go.mod: judged by the plan: go-sbom may claim it, and a file an sbom plugin claims is covered through its units
  fix: nothing, if go-sbom claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested
warning: go.sum: judged by the plan: go-sbom may claim it, and a file an sbom plugin claims is covered through its units
  fix: nothing, if go-sbom claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested
```

Commit the config, `cmd/notes/cover.go`, `go.mod`, `go.sum` and
`BUILD.bazel`. The next step runs the steps on your machine.

Next: [step 8, see it on your machine](on-your-machine.md).
