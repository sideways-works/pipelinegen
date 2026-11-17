# Run it on any CI system by hand

A runner plugin writes the whole pipeline for one CI system; today there
is one, for Concourse. On any other CI system you can write the jobs
yourself: pipelinegen's steps are plain commands that read and write
plain directories and a few environment variables. This page lists them
in the order a run calls them, then shows one run of the book's notes
service done entirely in a shell, with a local bare repository as its
remote, and the real output it printed.

What it covers: a branch run that plans, deals batches, runs and records
the slices, reports them, and attaches the lists to the commit, then a
second commit whose plan selects less. What it leaves out: pull requests
(their plan runs in two steps, `plan --out` and `plan --from --gate
<dir>`, so the change's code never runs where a credential that writes
is, and their suites start only when `<dir>/gate.json` is open),
whole-suite checks, a suite's prepare steps (`slice prepare`), statuses and comments on a
git host, and continuous runs. For a continuous run, run its plan step's
builds side by side, never one at a time: each records its round for the
others to read ([continuous runs](declaring-runs.md#continuous-runs) says
how). The [contracts](../contracts.md#commands-the-generated-pipeline-calls)
list every step and every input of the generated pipeline; the [runner
page](../writing-plugins/runner.md#what-the-pipeline-must-do) says what a
complete pipeline must do.

## What a run needs

- A checkout of the commit under test, the trigger. pipelinegen reads the
  config from it and works out the run's change from it. Give each job a
  checkout of its own: the steps fetch commits into it, and two jobs
  sharing one would each see the other's fetches.
- `tools/pipelinegen` and the configured plugins in `tools/plugins/`,
  each checked against its pin.
- A cache directory that stays between runs: bazel-diff's snapshots, the
  plugins' caches and answers, and the store's scratch repository. Each
  plan trims it to `keep.cache` when it ends. With
  `PIPELINEGEN_PIPELINE_STEP=1` set, as a CI system's steps should have it,
  Bazel's output and bazelisk's downloads go there too; without it they
  stay in Bazel's own place in your home.
- bazelisk and Go on the `PATH`, as on your machine.
- Write access to the remote the config names. pipelinegen keeps every
  record of a run (the plan, each slice's state and recording, the
  lists) in hidden refs there. In this example the remote is a bare
  repository on the same disk, so no token is needed; with a git host,
  the steps read the token from the environment (see
  [secrets](secrets.md)).

## The steps, in order

| Step | Command | Reads | Writes |
|---|---|---|---|
| prepare | `GOMODCACHE=<cache>/caches/gomodcache go mod download`, in the trigger | the trigger's `go.mod` | the plugins' module cache; the plugins never use the network |
| plan | `tools/pipelinegen plan --run <run> --trigger trigger --root trigger --cache <cache>` | the trigger, the plugins under `tools/plugins`, the remote's records | the plan, in the remote's records |
| per suite: tree | `tools/pipelinegen tree --run <run> --suite <s> --trigger trigger --root trigger --out source --gate gate` | the plan | `source`, the tree under test; `gate/gate.json`, `["1"]` when the plan selected the suite; with the gate open, the suite build's start record, in the remote's records (it needs the store's token; without it the step warns, and the suite counts as begun only from its first batch's end) |
| batches | `tools/pipelinegen batches --run <run> --suite <s> --trigger trigger --root source --out batches` | the plan | `batches/<n>/slices.json` per batch, `batches/run.json` |
| per batch: run | `tools/pipelinegen slice run --cache <cache>/<suite>`, which runs the suite's steps through their drivers ([drivers](../concepts/drivers.md)); for a suite with task files, the older form, your batch script, which calls `tools/pipelinegen slice setup-end`, then `slice start <name>` and `slice end <name>` around each slice ([writing tasks](writing-tasks.md)) | `source`, `batches`, `tools` | `result/` and, for a recorded suite, `coverage/` |
| per batch: batch-end | `tools/pipelinegen report batch-end --batch <n> --result result --root source`, whatever the task did | `result/`, `coverage/` | each slice's state and recording, in the remote's records |
| suite end | `tools/pipelinegen report suite --root trigger` | the run's records | the suite's state; when the run's last suite ended, the lists of the slices that passed attached to the commit |

Every step got `PIPELINEGEN_RUN`, the run's name. From the tree step on,
the steps also got `PIPELINEGEN_SUITE` and `PIPELINEGEN_TRIGGER`, the
trigger checkout's absolute path, from which they learn the run's change
and commit; `slice run` got `PIPELINEGEN_BATCH`, the batch's number. Its
`--cache` is a directory kept between runs, where the drivers keep what
they reuse (Go's build and module caches).
Every step, `plan` included, reads the plugins from `tools/plugins` in its
own working directory, beside `tools/pipelinegen`, never from the
checkout.

## The example

The repository is the book's notes service with its Go suite only: the
browser suite needs a running server and a browser, which a plain shell
can give it too, but this page keeps to one suite. Its config is
[example-notes](../../example-notes/pipelinegen.yaml) with these changes:
no `github` and no `concourse` section (so no `image` for the suite),
`git.remote` naming the bare repository, one run, and no browser suite,
nor its driver and oapi-codegen-correlator, which observes only that
suite (so the page, which only the browser specs tested, is declared
untested):

```yaml
git:
  remote: file:///home/you/ci/remote.git

runs:
  main:
    trigger: {branch: main}
    compare: last_passed
    select: affected
    suites: {go: {}}
    record: true

untested:
  - paths: ["web/**"]
    reason: the page, which only the browser specs test, and this copy has none
```

One job, for one commit `$commit` of `main`, in a directory of its own:

```sh
mkdir -p job/tools && cd job
git clone -q /home/you/ci/remote.git trigger && git -C trigger checkout -q "$commit"
cp ~/bin/pipelinegen tools/pipelinegen
cp -r ~/plugins tools/plugins                       # checked against their pins
export PIPELINEGEN_RUN=main

(cd trigger && GOMODCACHE=/home/you/ci/cache/caches/gomodcache go mod download)
tools/pipelinegen plan --run main --trigger trigger --root trigger --cache /home/you/ci/cache

export PIPELINEGEN_SUITE=go PIPELINEGEN_TRIGGER=$PWD/trigger
tools/pipelinegen tree --run main --suite go --trigger trigger --root trigger --out source --gate gate
tools/pipelinegen batches --run main --suite go --trigger trigger --root source --out batches
for b in batches/*/; do
  b=$(basename "$b")
  mkdir "batch-$b" && cp -a source batches tools "batch-$b/"
  (cd "batch-$b" && PIPELINEGEN_BATCH=$b tools/pipelinegen slice run --cache /home/you/ci/cache/go)
  (cd "batch-$b" && tools/pipelinegen report batch-end --batch "$b" --result result --root source)
done
tools/pipelinegen report suite --root trigger
```

On a CI system, each block is a job or a step, and `cp -a` is however
the system hands one job's directories to the next. The batches can run
side by side, as the generated pipeline runs them.

### The first commit

The first run has no earlier run to compare with, so it runs every
slice:

```
$ tools/pipelinegen plan --run main --trigger trigger --root trigger --cache /home/you/ci/cache
main main: every slice: files no slice covers: cmd/notes/main.go: no slice depends on this file, so a change to it would run nothing; go.mod: no slice depends on this file, so a change to it would run nothing; go.sum: no slice depends on this file, so a change to it would run nothing
pipelinegen: cache: kept 25 MB (caches 18 MB, bazel-diff 7 MB, store 104 KB, .tool-versions 4 KB); dropped gomodcache sources 84 MB, tree 268 KB
$ tools/pipelinegen tree --run main --suite go --trigger trigger --root trigger --out source --gate gate && cat gate/gate.json
main main: go at 912e0707c211684317773417b8bc77db50a3be84
["1"]
$ tools/pipelinegen batches --run main --suite go --trigger trigger --root source --out batches && ls batches
main main: 5 slices of go in 4 batches
0
1
2
3
batches.json
run.json
started.json
```

The three files no slice covers yet are covered once the recordings say
which slices run their code. Each batch runs its slices, and its
batch-end writes their states and recordings:

```
$ cd batch-0 && PIPELINEGEN_BATCH=0 tools/pipelinegen slice run --cache /home/you/ci/cache/go
setup ended
go run: go: downloading github.com/oapi-codegen/runtime v1.9.0
go run: go: downloading github.com/apapsch/go-jsonmerge/v2 v2.0.0
go run: go: downloading github.com/google/uuid v1.6.0
go run: === RUN   TestCreate
go run: --- PASS: TestCreate (0.00s)
go run: === RUN   TestCreateNeedsATitle
go run: --- PASS: TestCreateNeedsATitle (0.00s)
go run: PASS
go run: coverage: 43.4% of statements in ./...
go run: ok  	example.com/notes/api	0.013s	coverage: 43.4% of statements in ./...
slice api/create_test.go: passed
go run: === RUN   TestWords
go run: --- PASS: TestWords (0.00s)
go run: === RUN   TestTitle
go run: --- PASS: TestTitle (0.00s)
go run: PASS
go run: coverage: 83.3% of statements in ./...
go run: ok  	example.com/notes/text	0.006s	coverage: 83.3% of statements in ./...
slice text/text_test.go: passed
$ cd batch-0 && tools/pipelinegen report batch-end --batch 0 --result result --root source
```

go-driver runs each test file's tests on their own, in a recording run
with coverage of the whole module, and leaves each file's coverage where
batch-end hands it to go-correlator.

After the last batch, the suite's end finds the whole run passed and
attaches the recordings to the commit as its lists (in a run where a slice
failed, it attaches those of the slices that passed):

```
$ tools/pipelinegen report suite --root trigger
lists of 912e0707c211: main main 912e0707c211 merged into no earlier lists (none within 200 commits); 5 slices, 5 changed
```

From a clone of the repository, `pipelinegen status` reads the run's
result from the records, and `lists show` the lists:

```
$ pipelinegen status 912e0707c211684317773417b8bc77db50a3be84
pipelinegen: main main 912e0707c211: passed (1 suite passed)
$ pipelinegen lists show --commit 912e0707c211684317773417b8bc77db50a3be84
lists of 912e0707c211: its own lists
no live runs' recordings added
go: 5 slices
  api/create_test.go 10
  api/search_test.go 12
  notes/notes_test.go 4
  search/search_test.go 3
  text/text_test.go 2
```

### The second commit

The next commit changes how `text.Words` splits words. The same job, for
that commit, plans against the first run's lists and selects only the
three test files that executed the changed code:

```
$ tools/pipelinegen plan --run main --trigger trigger --root trigger --cache /home/you/ci/cache
main main: go 3 of 5 (3: go:text#func:Words changed)
pipelinegen: cache: kept 704 MB (bazel 559 MB, caches 133 MB, bazel-diff 7 MB, snapshots 2 MB, plugins 996 KB, go 968 KB, answers 604 KB, store 364 KB, .tool-versions 4 KB); dropped bazel repository contents 478 MB, gomodcache sources 84 MB, tree 272 KB
$ tools/pipelinegen batches --run main --suite go --trigger trigger --root source --out batches && ls batches
main main: 3 slices of go in 3 batches
0
1
2
batches.json
run.json
started.json
$ cd batch-0 && PIPELINEGEN_BATCH=0 tools/pipelinegen slice run --cache /home/you/ci/cache/go
setup ended
go run: === RUN   TestSearch
go run: --- PASS: TestSearch (0.00s)
go run: PASS
go run: coverage: 65.0% of statements in ./...
go run: ok  	example.com/notes/api	0.004s	coverage: 65.0% of statements in ./...
slice api/search_test.go: passed
$ cd batch-1 && PIPELINEGEN_BATCH=1 tools/pipelinegen slice run --cache /home/you/ci/cache/go
setup ended
go run: === RUN   TestFind
go run: --- PASS: TestFind (0.00s)
go run: PASS
go run: coverage: 29.7% of statements in ./...
go run: ok  	example.com/notes/search	0.004s	coverage: 29.7% of statements in ./...
slice search/search_test.go: passed
$ cd batch-2 && PIPELINEGEN_BATCH=2 tools/pipelinegen slice run --cache /home/you/ci/cache/go
setup ended
go run: === RUN   TestWords
go run: --- PASS: TestWords (0.00s)
go run: === RUN   TestTitle
go run: --- PASS: TestTitle (0.00s)
go run: PASS
go run: coverage: 83.3% of statements in ./...
go run: ok  	example.com/notes/text	0.002s	coverage: 83.3% of statements in ./...
slice text/text_test.go: passed
$ tools/pipelinegen report suite --root trigger
lists of a47fabb18d6f: main main a47fabb18d6f merged into the lists of its parent 912e0707c211; 5 slices, 0 changed
$ pipelinegen status a47fabb18d6f30ab256490bf08640f569aaa11da
pipelinegen: main main a47fabb18d6f: passed (1 suite passed, plugins: 92 of 146 files from the cache)
```

These outputs come from a development build of pipelinegen before v0.2.0
and the plugins and drivers the book's example pins.
