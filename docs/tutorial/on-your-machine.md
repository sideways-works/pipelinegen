# Step 8: see it on your machine

Before anything has been recorded, every slice of a recorded suite is
bound to everything, so `pipelinegen affected` selects them all (step 6).
`pipelinegen record` records a suite on your machine, the way a CI run
would: it runs the suite's steps from step 7 here, reads what each
test executed, and, when every slice passes, attaches the lists to your
`HEAD` commit in a local store inside your repository's git directory.
Nothing is pushed anywhere. Then you change the backend and watch
pipelinegen pick the one browser spec that reaches the change.

## Record

Commit first: `record` records `HEAD` and refuses a working tree with
changes. Then record each suite:

<!-- output: record-go -->
```
$ pipelinegen record --suite go
prepared go
setup ended
go run: === RUN   TestCreate
go run: --- PASS: TestCreate (<time>)
go run: === RUN   TestCreateNeedsATitle
go run: --- PASS: TestCreateNeedsATitle (<time>)
go run: PASS
go run: coverage: 43.4% of statements in ./...
go run: ok  	example.com/notes/api	<time>	coverage: 43.4% of statements in ./...
slice api/create_test.go: passed
go run: === RUN   TestSearch
go run: --- PASS: TestSearch (<time>)
go run: PASS
go run: coverage: 65.0% of statements in ./...
go run: ok  	example.com/notes/api	<time>	coverage: 65.0% of statements in ./...
slice api/search_test.go: passed
go run: === RUN   TestAddAndGet
go run: --- PASS: TestAddAndGet (<time>)
go run: === RUN   TestList
go run: --- PASS: TestList (<time>)
go run: PASS
go run: coverage: 100.0% of statements in ./...
go run: ok  	example.com/notes/notes	<time>	coverage: 100.0% of statements in ./...
slice notes/notes_test.go: passed
go run: === RUN   TestFind
go run: --- PASS: TestFind (<time>)
go run: PASS
go run: coverage: 29.7% of statements in ./...
go run: ok  	example.com/notes/search	<time>	coverage: 29.7% of statements in ./...
slice search/search_test.go: passed
go run: === RUN   TestWords
go run: --- PASS: TestWords (<time>)
go run: === RUN   TestTitle
go run: --- PASS: TestTitle (<time>)
go run: PASS
go run: coverage: 83.3% of statements in ./...
go run: ok  	example.com/notes/text	<time>	coverage: 83.3% of statements in ./...
slice text/text_test.go: passed
lists of 4c3820d1b492: local local-<time> 4c3820d1b492 merged into no earlier lists (none within 200 commits); 5 slices, 5 changed
recorded 5 slices of go at 4c3820d1b492; the lists are in the local store /home/you/notes/.git/pipelinegen/local.git, which affected and why read unless the config names a remote (then: --local)
```

<!-- output: record-e2e -->
```
$ pipelinegen record --suite e2e
prepared e2e
command setup: warning: GOCOVERDIR not set, no coverage data emitted
setup ended
playwright run: playwright-driver: npm ci in the tree's root
playwright run: 
playwright run: added 3 packages in <time>
playwright run: 
playwright run: Running 2 tests using 1 worker
playwright run: 
playwright run: [1/2] [chromium] › e2e/create.spec.ts:3:5 › adds a note
playwright run: [2/2] [chromium] › e2e/create.spec.ts:11:5 › refuses a note without a title
playwright run:   2 passed (<time>)
slice e2e/create.spec.ts: passed
playwright run: 
playwright run: Running 1 test using 1 worker
playwright run: 
playwright run: [1/1] [chromium] › e2e/search.spec.ts:3:5 › finds a note by a word in it
playwright run:   1 passed (<time>)
slice e2e/search.spec.ts: passed
lists of 4c3820d1b492: local local-<time> 4c3820d1b492 merged into its own lists; 8 slices, 3 changed
recorded 2 slices of e2e at 4c3820d1b492; the lists are in the local store /home/you/notes/.git/pipelinegen/local.git, which affected and why read unless the config names a remote (then: --local)
```

`--work <dir>` keeps the build directory after the recording, with what
the steps left in it (a failed slice's output, say). Without it the build
directory is a new temporary one each time and goes away when `record`
ends. A step that builds with Bazel feels the difference: Bazel keys its
output base by the workspace's path, so each recording without `--work`
builds cold in a new path and leaves that output base behind; with the
same `--work` the next recording reuses it. A recording in a `--work`
directory used before first removes what the earlier one left (its
coverage, results, batches, what its prepare made, the drivers' work) and
says so on one line; it keeps only the caches, so nothing an earlier run
touched counts as this one's.

With Docker or Podman on your machine, the steps run in the images the
config names, as on CI ([step 9](pipeline.md)), each pulled the first
time. With `--no-image`, or a suite that names no image, they run on your
machine, which then needs what those images have: Go for the Go tests and
the server's build; Node and a Playwright browser for the specs (`npx
playwright install chromium` once; playwright-driver runs `npm ci` itself).

**You should see** each slice pass, and a last line naming the local
store.

**If you don't:**

- `the batch task failed: ...`: the steps' own output above that line,
  each line headed by its driver and step, says what was missing. A tool
  the image has and your machine doesn't is the usual cause. The steps get
  a fresh checkout of `HEAD`, so what your working tree has installed or
  built (`node_modules`, a built binary) isn't there: the steps make it
  themselves, as the prepare step builds the server and playwright-driver
  runs `npm ci`.
- A refusal that names changed files: commit them, or record from a clean
  checkout.
- A slice marked as one its correlator couldn't tell: nothing left a
  recording for it. For a browser spec, check that the server is the
  prepare step's build, with go-correlator's cover package, and that
  go-correlator's `cover_dir` is where the set-up's
  `PIPELINEGEN_COVER_DIR` points ([step 7](tasks.md)).

## Change the backend

Now edit the backend's validation, commit, and plan the commit. With no
remote configured, `affected` and `why` read the local store and say so:

<!-- output: affected-validate -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
pipelinegen affected: filling the gomodcache cache for go-sbom, go-correlator: go mod download in ., into /home/you/.cache/pipelinegen/repos/notes-986d39b68cb0/caches/gomodcache
e2e: 1 selected, 1 untouched
  run e2e/create.spec.ts
go: 1 selected, 4 untouched
  run api/create_test.go
```

<!-- output: why-validate-create -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 4c3820d1b492 to 8719e9d00ab8:
  it executed api.validate during its recording (api/api.go), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

<!-- output: why-validate-search -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/search.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/search.spec.ts was left out from 4c3820d1b492 to 8719e9d00ab8:
  nothing it is bound to changed
  observed by: go-correlator, oapi-codegen-correlator
```

No file of the page or the specs changed, yet the spec that submits the
form runs and the one that only searches doesn't. `pipelinegen run --base
HEAD~1` plans the same change and runs what it selects here, the way a
pull request's run would: the suite's prepare once, then the selected
slices, with each slice's result and a summary (see
[the reference](../reference/commands.md#pipelinegen-run)). The edit to
`Words` from step 5:

<!-- output: affected-words-recorded -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e: 1 selected, 1 untouched
  run e2e/search.spec.ts
go: 3 selected, 2 untouched
  run api/search_test.go
  run search/search_test.go
  run text/text_test.go
```

A change to the page alone runs the browser specs and no Go test:

<!-- output: affected-page -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e: 2 selected, 0 untouched
  run e2e/create.spec.ts
  run e2e/search.spec.ts
go: 0 selected, 5 untouched
```

A change to the API contract reaches the tests through the operation. Give
a note's title a maximum length in `api/notes.yaml`:

<!-- output: affected-contract -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e: 1 selected, 1 untouched
  run e2e/create.spec.ts
go: 1 selected, 4 untouched
  run api/create_test.go
```

<!-- output: why-contract-spec -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was selected from 4c3820d1b492 to 2df3bd8da6d6:
  it requested POST /notes (createNote) during its recording, which changed in this commit
  it executed notesapi.ServerInterfaceWrapper.CreateNote during its recording (api/notesapi/notesapi.gen.go)
    → which leads to POST /notes (createNote) (a link from oapi-codegen-correlator), which changed in this commit
  observed by: go-correlator, oapi-codegen-correlator
```

The spec runs twice over: it made a request to `createNote` (the request
log says so), and it executed the generated wrapper that serves
`createNote`. The Go test of the create route ran that wrapper too:

<!-- output: why-contract-test -->
```
$ pipelinegen why --base HEAD~1 --slice api/create_test.go
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
go api/create_test.go was selected from 4c3820d1b492 to 2df3bd8da6d6:
  it executed notesapi.ServerInterfaceWrapper.CreateNote during its recording (seen by go-correlator; api/notesapi/notesapi.gen.go)
    → which leads to POST /notes (createNote) (a link from oapi-codegen-correlator), which changed in this commit
  observed by: go-correlator
```

A change to the contract's documentation alone, such as an operation's
`summary`, runs nothing. A narrowing, `doc-text`, leaves documentation out
of the operations' digests, and `why` says what it left out:

<!-- output: affected-contract-docs -->
```
$ pipelinegen affected --base HEAD~1
pipelinegen affected: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e: 0 selected, 2 untouched
go: 0 selected, 5 untouched
```

<!-- output: why-contract-docs -->
```
$ pipelinegen why --base HEAD~1 --slice e2e/create.spec.ts
pipelinegen why: lists from the local store (recorded on this machine): /home/you/notes/.git/pipelinegen/local.git
e2e e2e/create.spec.ts was left out from 4c3820d1b492 to 4e52be9ca996:
  the narrowing doc-text left it out: api:docs:createNote changed, which only the wide rule binds to these slices; it would have run at narrowing level 5 (the plan used 3)
  observed by: go-correlator, oapi-codegen-correlator
```

**You should see** fewer slices selected than before the recording, each
for the reason `pipelinegen why` gives.

**If you don't:** once the config names a remote (the `github` section
from step 9 on, or `git.remote`), `affected` and `why` read recordings
from there; add `--local` to read the local store instead.

Next: [step 9, the pipeline](pipeline.md).
