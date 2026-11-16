# Step 6: plugins for your language

With the Bazel graph alone, a test runs when anything in its target's
build changed. Plugins bind each test file to the functions it actually
ran ([units](../concepts/units-and-sbom.md),
[links](../concepts/links-and-correlators.md) and
[recordings](../concepts/recordings-and-lists.md)). This step adds them.

## Decision: which plugins

Each domain has a pair, one plugin that lists what exists (sbom) and one
or more that say what uses it and what each test ran (correlators):

| Your code | Add | Guide |
|---|---|---|
| Go | go-sbom and go-correlator | this page |
| TypeScript, React, browser tests of a web app | ts-sbom, npm-sbom and react-correlator | [react-correlator's README](https://github.com/sideways-works/pipelinegen-correlator-react) |
| PostgreSQL migrations | pg-sbom, with sql-correlator for SQL in code or gorm-correlator for GORM | [pg-sbom's README](https://github.com/sideways-works/pipelinegen-sbom-pg) |
| An OpenAPI contract with oapi-codegen | openapi-sbom and oapi-codegen-correlator | [openapi-sbom's README](https://github.com/sideways-works/pipelinegen-sbom-openapi) |
| anything else | nothing: keep `bind: graph` and go on to [step 7](tasks.md) | |

Browser tests of a Go backend need go-correlator only to bind them to the
backend code they reach; react-correlator adds the frontend side.

## Pin the plugins

Each plugin is a release of its own repository, pinned in the config by
version and by the checksum of its Linux binary, which the release's
`SHA256SUMS` lists, as the drivers of step 3 are. The notes service is Go
with an OpenAPI contract, so it takes the Go pair and the OpenAPI pair,
listed under `plugins` before the drivers:

```yaml
plugins:
  - name: go-sbom
    kind: sbom
    repository: sideways-works/pipelinegen-sbom-go
    release: {version: v0.2.0, sha256: 9bc2115506021ab2f324258a54486951e69873a166fb43c723a8566e508fbb95}
  - name: go-correlator
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-go
    release: {version: v0.2.0, sha256: 2abdbae3cdb4ed1d04bdc926a417907e36ba01846eb05b02fd30ccb471d6a210}
  - name: openapi-sbom
    kind: sbom
    repository: sideways-works/pipelinegen-sbom-openapi
    release: {version: v0.2.0, sha256: fb9adfeebe3a4498d12f3b774c76c158026fd4e90c608121b31d067be9a7cbab}
  - name: oapi-codegen-correlator
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-oapi-codegen
    release: {version: v0.2.0, sha256: 155b4bce79efb0b098b3868014e89399f43a749f4496a7c64027b34634ac1bce}
    config:
      # the server appends the route of each request it serves here
      request_log: work/command/requests.log
      request_log_lines: patterns
      other_routes: ["GET /"]
```

`name` is yours to choose; it names the executable. The plugins find their
own files: you don't list them. `config` is a plugin's own settings, which
its README lists. oapi-codegen-correlator needs to know which API
operations a browser spec called: the server writes the route of each
request it serves to `requests.log`, and the correlator cuts that log per
spec. The path is relative to the batch's directory: step 7 starts the
server with the command driver, whose directory for the batch is
`work/command`, and tells it to write the log there. The page's own
route, `GET /`, is no API operation.

Then bind both suites by recording, and say which correlators observe
each:

```yaml
suites:
  go:
    tests: ["**/*_test.go"]
    bind: recorded
    correlators: [go-correlator]
    steps:
      - run: go
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    owner: "//e2e:e2e"
    bind: recorded
    correlators: [go-correlator, oapi-codegen-correlator]
    steps:
      - run: playwright
        with: {config: e2e/playwright.config.ts}
```

Two entries of step 4 change now. go-sbom claims `go.mod` and `go.sum`,
and lists each module they name as a unit, so they can leave
`everything_on`: a module bump then runs only the slices whose code links
that module. And `cmd/notes/main.go` is covered through go-sbom's units,
so the `untested` entry for `cmd/notes` narrows to its BUILD file:

```yaml
everything_on: [MODULE.bazel, MODULE.bazel.lock, .bazelversion, package.json, package-lock.json, "ci/**"]

untested:
  - paths: [BUILD.bazel]
    reason: holds only Gazelle's own target
  - paths: [.gitignore]
    reason: repository settings
  - paths: [cmd/notes/BUILD.bazel]
    reason: builds the server, which the browser specs run; their recordings bind its code
```

The check now leaves files a plugin claims to the plan, and says so:

<!-- output: check-plugins -->
```
$ pipelinegen check
warning: cmd/notes/main.go: judged by the plan: go-sbom may claim it, and a file an sbom plugin claims is covered through its units
  fix: nothing, if go-sbom claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested
warning: go.mod: judged by the plan: go-sbom may claim it, and a file an sbom plugin claims is covered through its units
  fix: nothing, if go-sbom claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested
warning: go.sum: judged by the plan: go-sbom may claim it, and a file an sbom plugin claims is covered through its units
  fix: nothing, if go-sbom claims it (pipelinegen plugins claims shows it); otherwise make a slice depend on it or list it in untested
```

The Go tests leave no request log, so only go-correlator observes them.
The browser specs are observed by both: go-correlator reads the coverage
of the server they talk to, and oapi-codegen-correlator the requests they
made ([step 7](tasks.md)).

## Put the plugins where pipelinegen finds them

The CI pipeline downloads the pinned plugins itself. On your machine,
pipelinegen looks for them in `tools/plugins/<name>` under the
repository's root, beside the drivers of step 3. Download each and check
it against the pin:

```sh
curl -fsSL -o tools/plugins/go-sbom \
  https://github.com/sideways-works/pipelinegen-sbom-go/releases/download/v0.2.0/go-sbom-linux-amd64
curl -fsSL -o tools/plugins/go-correlator \
  https://github.com/sideways-works/pipelinegen-correlator-go/releases/download/v0.2.0/go-correlator-linux-amd64
curl -fsSL -o tools/plugins/openapi-sbom \
  https://github.com/sideways-works/pipelinegen-sbom-openapi/releases/download/v0.2.0/openapi-sbom-linux-amd64
curl -fsSL -o tools/plugins/oapi-codegen-correlator \
  https://github.com/sideways-works/pipelinegen-correlator-oapi-codegen/releases/download/v0.2.0/oapi-codegen-correlator-linux-amd64
chmod +x tools/plugins/*
```

Then check each against its pin, with the checksums from the config in a
file (`tools/sums` here):

<!-- output: sha-plugins -->
```
$ sha256sum -c tools/sums
tools/plugins/go-sbom: OK
tools/plugins/go-correlator: OK
tools/plugins/openapi-sbom: OK
tools/plugins/oapi-codegen-correlator: OK
```

On macOS, or for a release without a binary for your system, build them
from source instead (`go install
github.com/sideways-works/pipelinegen-sbom-go/cmd/go-sbom@v0.2.0`,
the same for the others) and copy the binaries into `tools/plugins/`.

## The Go plugins' module cache

The Go plugins type-check your code, and never use the network: they read
your module's dependencies from a module cache of pipelinegen's own, under
its cache directory ([where pipelinegen keeps
things](../reference/your-machine.md)). On your machine `affected`, `why`
and `try` fill it themselves before they plan: the Go plugins' `describe`
says that `go mod download` fills it, and pipelinegen runs that once per
content of `go.mod` and `go.sum` and says so the first time. In CI, the
planner's `prepare` command does the same (step 9). Without it, the Go
correlator can't tell what the code holds, and the plan runs every
recorded slice with a warning that names a module lookup.

## See what they claim

<!-- output: claims -->
```
$ pipelinegen plugins claims
go-sbom	MODULE.bazel	directive
go-sbom	api/api.go	source
go-sbom	api/create_test.go	source
go-sbom	api/notesapi/generate.go	source
go-sbom	api/notesapi/notesapi.gen.go	generated
go-sbom	api/search_test.go	source
go-sbom	cmd/notes/main.go	source
go-sbom	go.mod	manifest
go-sbom	go.sum	lockfile
go-sbom	notes/notes.go	source
go-sbom	notes/notes_test.go	source
go-sbom	search/search.go	source
go-sbom	search/search_test.go	source
go-sbom	text/text.go	source
go-sbom	text/text_test.go	source
go-sbom	web/web.go	source
go-correlator	api/api.go	source
go-correlator	api/create_test.go	source
go-correlator	api/notesapi/generate.go	source
go-correlator	api/notesapi/notesapi.gen.go	generated
go-correlator	api/search_test.go	source
go-correlator	cmd/notes/main.go	source
go-correlator	go.mod	manifest
go-correlator	go.sum	lockfile
go-correlator	notes/notes.go	source
go-correlator	notes/notes_test.go	source
go-correlator	search/search.go	source
go-correlator	search/search_test.go	source
go-correlator	text/text.go	source
go-correlator	text/text_test.go	source
go-correlator	web/web.go	source
openapi-sbom	api/notes.yaml	contract
oapi-codegen-correlator	api/api.go	source
oapi-codegen-correlator	api/create_test.go	source
oapi-codegen-correlator	api/notes.yaml	contract
oapi-codegen-correlator	api/notesapi/generate.go	directive
oapi-codegen-correlator	api/notesapi/notesapi.gen.go	generated
oapi-codegen-correlator	api/search_test.go	source
oapi-codegen-correlator	cmd/notes/main.go	source
oapi-codegen-correlator	go.mod	manifest
oapi-codegen-correlator	notes/notes.go	source
oapi-codegen-correlator	notes/notes_test.go	source
oapi-codegen-correlator	search/search.go	source
oapi-codegen-correlator	search/search_test.go	source
oapi-codegen-correlator	text/text.go	source
oapi-codegen-correlator	text/text_test.go	source
oapi-codegen-correlator	web/web.go	source
```

**You should see** each plugin with the files it claimed and the role of
each (`source`, `manifest`).

**If you don't:** a plugin that claims nothing is either not where
pipelinegen looks (`tools/plugins/<name>`), not executable, or finds none
of its files (`include` and `exclude` on its entry widen or narrow what it
looks at).

## Plan again

<!-- output: affected-plugins -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: no remote to read the recordings from (git.remote, the github section or the checkout's origin), and no local store (`pipelinegen record` makes one): recorded slices are bound to everything
pipelinegen affected: filling the gomodcache cache for go-sbom, go-correlator: go mod download in ., into /home/you/.cache/pipelinegen/repos/notes-2afb52553fae/caches/gomodcache
e2e: 2 selected, 0 untouched
  run e2e/create.spec.ts
  run e2e/search.spec.ts
go: 5 selected, 0 untouched
  run api/create_test.go
  run api/search_test.go
  run notes/notes_test.go
  run search/search_test.go
  run text/text_test.go
```

Every slice of both suites runs: none has been recorded yet, so each is
bound to everything until a recording run tells what it executes.
`--no-lists` says not to look for recordings; without it, `affected` looks
for them in the repository's remote. A recording run is the first CI run,
or one on your machine in [step 8](on-your-machine.md). With recordings,
the same change selects only the three Go test files and the one browser
spec that ran `Words`.

Next: [step 7, the suites' steps](tasks.md).
