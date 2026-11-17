# Proof

This page is for anyone who wants evidence that pipelinegen does what its
docs say. pipelinegen runs its own CI, and that CI is where the tool is
shown to work end to end: real pull requests in this repository, planned and run on
Concourse by the pipeline the tool generates. Each entry below names the
pull request and the build it was shown on. An entry whose result is
"fails" describes something the tool can't do yet.

## The setup

- `pipelinegen.yaml` at the root declares five suites: `lint` and `go` (the
  tool's own lint and `go test ./...`), the example's `unit` and
  `smoke` (see [example/pipelinegen.yaml](../example/pipelinegen.yaml)),
  and `proof`, which reruns the plugin proof below every night and
  whenever `ci/proof` changes.
  The example's slices are slices here too, so a change to the example
  exercises the tool the way a user's repository would.
- The repository has one pipeline, `pipelinegen`, written by
  `pipelinegen pipeline` from the config alone (with `ci/root.yml`
  included) and set by hand. Its `pipeline` job runs whenever the config
  changes and fails when the pipeline set is stale; the config leaves
  `concourse.self_set` off, so a person sets it again:

  ```sh
  pipelinegen pipeline --out pipeline.yml
  fly -t ci set-pipeline -p pipelinegen -c pipeline.yml -v github_token=...
  ```

  Per run it holds a planning job and one job per suite; each change is one
  build of each suite job.

- It needs one var, `github_token`: a fine-grained token on this repository
  with Contents read and write (clone; the run state and recorded
  lists under `refs/pipelinegen/`; releases), Commit statuses read and
  write, Pull requests read and write and Issues read and write (listing
  pull requests, the comment), and Metadata read.
- The pipeline never pushes to a branch, only to `refs/pipelinegen/`, so a
  ruleset on `main` needs no bypass for it. A ruleset that covers every ref
  must leave `refs/pipelinegen/*` to the token.
- Every task runs on workers tagged `cloud-1`.

## Proofs

Pull requests are opened from `proof/*` branches as drafts, marked ready for
the test and closed afterwards.

| | What | Pull request | Build | Result |
|---|---|---|---|---|
| a | A change to one test file runs that file's slice; the others show as untouched | | | pending |
| b | A change to a library runs every test file that depends on it, and only those | | | pending |
| c | A change to the build setup runs everything | | | pending |
| d | The first build with no earlier passing commit runs everything | | | pending |
| e | The comment appears when the change is planned and follows slices as they start and end, as a list and as a Mermaid chart, and renders on GitHub | | | pending |
| f | Two changes' builds of one suite job run side by side, and no step takes a lock | | | pending |
| g | Rerunning a failed suite build (Concourse's own rerun) runs exactly the failed slices again, and the comment and statuses end right | | | pending |
| h | After a passing branch run that recorded, that commit's lists ref holds what its slices touched; no commit was added and no file changed; the next plan finds the lists (the run's own refs stay until `keep` sweeps them, see n) | | | open: waits for a suite of this repository that records |
| i | An added test file is a new slice and runs, and nothing else | | | pending |
| j | A removed test file is named as removed; a test removed inside a file runs that file | | | pending |
| k | A test file moved to another path runs at its new path and is named as removed at the old | | | pending |
| l | A plan is reproducible from the commits alone: the same base and head planned twice, on different days, give the same result | | | pending |
| m | With the lists refs deleted on purpose, the next run selects every recorded slice and makes them again | | | open: waits for a suite of this repository that records |
| n | A finished run's result (`why`, the comment's data) can be read until it falls out of `keep`, and is gone after | | | pending |
| o | Three selected slices of one suite run in one task after one setup, each with its own result in the comment | | | pending |
| p | With recording on, slices that shared one server process each get their own recording, and they differ | | | open: waits for this repository's smoke suite to record |
| q | Caches are warm on a job's second build: the same suite job takes measurably less time | | | pending |
| r | A config change sets the pipeline again without losing builds in flight | | | pending |

### How each is shown

- **a**: change `example/calc/add_test.sh`: in `unit`, that file's slice
  only; `mul_test.sh`, `lib_test.sh` and `upper_test.sh` untouched; nothing
  of `smoke` or `lint`. In `go`, the example's Go tests run too, since they
  read that file; the tool's other tests stay untouched.
- **b**: change `example/calc/lib.sh`: the three test files of
  `example/calc` that use it, and not `example/text/upper_test.sh`.
- **c**: change `example/ci/root.yml`: every slice of every suite, with
  the reason `everything_on`.
- **d**: the first build of each suite job after the pipeline is set has no
  earlier passing commit: every slice, reason "nothing to compare with".
- **e**: on the pull request of (a): the comment exists before the first
  slice starts, its states move from pending to running to passed, once with
  `format: list` and once with `format: mermaid`. Rendered on GitHub and
  looked at.
- **f**: two pull requests changing `example/words/main.go`, marked ready
  together: their `smoke` builds run at once, neither waiting for the
  other, and no step of either runs `pipelinegen lock`.
- **g**: a pull request that breaks `example/calc/add_test.sh` and
  `example/calc/mul_test.sh`: those two fail, the rest pass. Rerunning the
  failed `unit` build runs those two again on the same commit and nothing
  else; the comment and the `unit` status show the rerun's results and no
  other slice changes state.
- **h**: this repository binds every suite by the graph, so none of its
  runs records; the plugin proof below shows recording and the lists on a
  fixture. Once the smoke suite binds by recording: after a passing `main`
  run of a change to `example/words/words.go`,
  `refs/pipelinegen/lists/<commit>` exists and names what each smoke slice
  touched (`count` touched `Count`, `upper` did not); `main` has no commit
  after the merged one and its tree is unchanged; the run's own refs stay
  until the sweep (n); the next
  change's plan reads those lists, or the nearest ancestor's when its own
  base has none yet. A following change to `Count` alone then runs only the
  `count` slice.
- **i**: add `example/calc/sub_test.sh`: a new slice, which runs; nothing
  else.
- **j**: delete `example/calc/lib_test.sh`: named as removed, nothing runs
  for it; separately, remove `test_mul_zero` from
  `example/calc/mul_test.sh`: that file runs.
- **k**: move `example/calc/add_test.sh` to `example/calc/sum_test.sh`: the
  new path runs, the old one is named as removed.
- **l**: plan the base and head of (a) again a day later with
  `pipelinegen affected --base B --head H --json` on a fresh cache, and
  compare with the first plan byte for byte.
- **m**: like (h), this waits for a recorded suite. Then: delete every
  `refs/pipelinegen/lists/*` on purpose, then push a change to
  `example/words/words.go`: the run selects every smoke slice (nothing
  recorded to bind by) and, once it passes, the lists ref of its commit
  exists again.
- **n**: with `keep` set low for the test, a finished run's `pipelinegen
  why` and comment data still read back; after enough newer runs (or the
  period) the sweep removes its refs and `why` says it is gone.
- **o**: the change of (b) reaches three unit test files: the batch's task
  log shows one setup, then the three slices; the comment shows three
  results.
- **p**: a recording run of the smoke slices that talk to the example's
  long-running server: one server process serves them in turn, and each
  slice's recording names only what its own requests executed.
  go-correlator's `collect` takes a running server's coverage between
  slices, and it is released, but this repository still binds the smoke
  suite by the graph and records nothing for it on CI. The proof waits for
  that suite to bind by recording through the Go plugins.
- **q**: the same suite job's first and second builds on the same worker:
  the second's prepare and lint steps take less time (warm Go and lint
  caches); the two build times are written here.
- **r**: change `pipelinegen.yaml` while a suite build runs: the
  `pipeline` job reports the pipeline stale, it is set again by hand, the
  running build finishes and reports, and the next change runs on the new
  jobs.

## Shown

Build numbers below are from the repository's current pipeline.

### A release builds the same bytes on another machine

A release published by hand is built again by the pipeline's `release`
job, twice, on a CI worker, and `publish` compares the result with the
published `SHA256SUMS`: "<tag> is released with exactly these files".
Shown with a development build before v0.2.0 (`release` build 1, id
917174).

### Planning through the released Go plugins

Shown without a CI worker, on a fixture repository that the proof
program, [ci/proof](../ci/proof/main.go) with its work in
[internal/proof](../internal/proof/run.go), builds from nothing. It
downloads pipelinegen v0.2.1, go-sbom v0.2.0 and go-correlator v0.2.0
from their releases, checks each against its pinned checksum, records what the fixture's tests run,
plans every change below, and prints the two tables. It exits non-zero on
any difference from what the ladder says:

```sh
go run ./ci/proof /some/empty/dir
```

It needs git, flock, ps, Go 1.27.1 and bazelisk. The releases it plans with
are pinned in [ci/proof/pins](../ci/proof/pins), and it fetches them
through GitHub's API with `GITHUB_TOKEN`, which goes only in a request
header. A run takes about four minutes, the downloads included.

After the recording and one plan that warms the cache, the proof deals
the remaining plans to `PROOF_WORKERS` workers (3 by default; the
suite's param of the same name sets it on CI). Each plans in a worktree
of the fixture and a copy of the warm cache of its own, with a Bazel
output base of its own for the plan and one for the walk; the plugins'
module caches are shared. So at most two Bazel servers per worker run at
once, plus the first plan's two until their 120-second idle time ends,
and each server's Java heap is bounded by `PROOF_BAZEL_HEAP` (`1g` by
default, the fixture's `--host_jvm_args=-Xmx`). The
branches the plans compare are made before the workers start, and the
tables are computed once at the end, in the same order whatever worker
planned a row. A worker that fails stops only itself: the others finish,
and the run then fails with that worker's log. The safety checks run
afterwards on the first cache alone, since one of them holds its lock.

The `proof` suite runs the program in three modes, all against the same
table. The nightly plans with the pinned releases, so a failure there
points at a pinned release. A pull request or main commit that changes
`ci/proof` or the planning and recording code (`//:proved` in
[BUILD.bazel](../BUILD.bazel)) runs it with `--source`. The proof then
builds `./cmd/pipelinegen` from the commit under test and plans with it,
next to the pinned plugins. A failure there means the change broke the
table, and the log lists the rows it broke. The `newest` run, an hour
after the nightly, runs it with `--newest`, which plans with each piece's
newest release, checked against that release's `SHA256SUMS`. No branch
rule requires its status, so a release that breaks the table shows up
there before anyone pins it. Once a release passes there,
`ci/proof/pins` moves to it: the nightly plans with the pins against
today's table, so a row added after an old pin fails the nightly. A param on the suite picks the mode:
`PROOF_TOOL: source` on the `pr` and `main` runs, `PROOF_NEWEST: "true"`
on `newest`. The suite's step refuses both at once. The log's last line names the
tool the run planned with and how many rows differ from the table.

Planning walks outward. In every mode the proof also builds the tool
from its own checkout binding forward, as planning did before the walk
(the `pipelinegen_forward` build tag; the released tool has no such
switch). It plans each of the 92 plans again with that build, on a cache
of its own, and compares the two plans: the slices selected and what
each narrowing left out. A plan that fails one way must fail the other
way too. The table's last column says "same" or how the forward binding
differs, and the last line counts the plans where it differs. Any
difference fails the run. The walk reads the links a correlator
answers, so the comparison runs only when the Go correlator offers
`links`; otherwise the column says "not compared" and so does the last
line.

A Go correlator that offers `links` needs a tool that walks typed links.
When a release changes what the tool and the Go plugins exchange, the
tool is released first, then go-sbom, then the Go correlator, and the
pins here (and an adopter's config) move the tool and the plugins
together, never the new plugins under an old tool.

#### The fixture

A Bazel workspace with one target that owns every file, and one Go module
([internal/proof/fixture.go](../internal/proof/fixture.go)):

- `app`: `Start`, which every test calls; `Config` and `defaultPort`, which
  only `Start` uses; `registry`, a variable whose initialiser reads the
  environment; `Greet`, `Total`, and `Discount`, which only the price tests
  use;
  `refund` and `rounded`, which `Total` calls only for a negative sum,
  which no test makes; `hook`, an empty function `Total` calls, and `Opt`,
  the type only `hook`'s signature names;
- `report`: `Line`, whose test also uses `github.com/google/go-cmp`, and
  which pads its line with `pad.Right`;
- `pad`: `Right`, and `clip`, which `Right` calls only for an overlong
  line, which no test makes, and which names `limits.Width`. An empty
  assembly file makes the package one go-correlator binds whole;
- `limits`: `Width`, which only `clip` names;
- `unused`: a package nothing imports;
- three test files, each a slice: `app/greet_test.go`,
  `app/price_test.go` and `report/report_test.go`.

`pipelinegen.yaml` has one suite of test files with `bind: recorded`,
`sample: 1` and `narrowing: 2`, and pins both plugins by release and
checksum.

#### The recording

The lists come from a real run. It plans every slice and deals them into
three batches. Each batch calls `slice setup-end`, then for each test file
`slice start`, `go test -cover` with that file's tests, and `slice end`.
The task writes each coverprofile into the slice's coverage directory, as
the contract says, and knows nothing of the plugins: go-correlator's
`collect` takes what is its own, and its `observe` turns it into units.
`lists attach` then writes the lists to a bare repository that stands in
for the remote (`PIPELINEGEN_GIT_URL`). Commit statuses went to a local
stand-in for GitHub's API.

| Slice | Units recorded |
|---|---|
| `app/greet_test.go` | 3: `Greet`, `Start`, `load` |
| `app/price_test.go` | 4: `Discount`, `Start`, `Total`, `load` |
| `report/report_test.go` | 5: `Greet`, `Start`, `load`, `report.Line`, `pad.Right` |

#### What each change selects

Each change is one commit on the recorded commit. A setting (the level,
`follow: dependents`) is a commit of its own underneath, so both sides of a
plan have the same config and the lists come from the nearest ancestor.
Every plan is `pipelinegen affected --plugins`; "price", "greet" and
"report" are the three test files.

| Change | Level 2 | Level 3 | Level 5 | Dependents, level 2 | Dependents, level 3 | What level 2 narrowed |
|---|---|---|---|---|---|---|
| `Discount`'s body | price | price | price | price | price | |
| `Config` and `defaultPort` | 1, sampled | all 3 | all 3 | 1, sampled | all 3 | sample-bound |
| `Start`'s body | 1, sampled | all 3 | all 3 | 1, sampled | all 3 | sample |
| `registry`'s initialiser | 1, sampled | all 3 | all 3 | 1, sampled | all 3 | sample-bound |
| `unused` | nothing | nothing | nothing | nothing | nothing | |
| go-cmp from v0.6.0 to v0.7.0 | report | report | all 3 | report | report | module-bumps |
| `app/greet_test.go` | greet | greet | greet | greet | greet | |
| `Discount` and `Start` together | price | all 3 | all 3 | price | all 3 | sample |
| a new function nothing calls | nothing | all 3 | all 3 | nothing | all 3 | unreached-new |
| `rounded`, which only the unrun `refund` calls | nothing | nothing | price | price | price | |
| `Opt`, which only the empty `hook`'s signature names | price | price | price | price | price | |
| `limits.Width`, which only the unrun `clip` in a whole package names | nothing | nothing | report | report | report | |
| `Opt` changed so that `hook(0)` no longer compiles | all 3 | all 3 | all 3 | all 3 | all 3 | |
| go-correlator's entry (its pin's version) | 1, sampled | 1, sampled | all 3 | 1, sampled | 1, sampled | plugin-entry |
| go-sbom's entry (its pin's version) | 1, sampled | 1, sampled | all 3 | 1, sampled | 1, sampled | plugin-entry |

The fixture's config lists itself under `sample_on`, as a repository does
whose plugin entries live there. A changed entry reaches every slice, so
at levels 2 and 3, where `plugin-entry` is on, the suite runs its sample
(one slice) and the plan names the other two under `plugin-entry`; at
levels 4 and 5 all three run, as the change's own, and `sample_on` samples
nothing beside them. The two entries are also planned at level 4, which
selects all 3.

All 77 plans select what the ladder says. The dependents columns match
the executed ones except in the rows for `rounded` and `limits.Width`.
There the changed code runs only through something the tests never ran
(`refund`, `clip`): the executed width binds what the tests ran, and the
dependents width, which level 5 follows, adds what that code reaches.
`Opt` selects price at every level: go-correlator binds the types a bound
declaration names, and `hook`, which `Total` calls, is bound.
`pad.Right` on the report test's list shows that a package bound whole
still records which of its functions ran. A change that breaks the build
leaves go-correlator unable to type-check the tree: its `bind` can't
tell, so every recorded slice runs, as the last row shows.

A plan records, per narrowing, the slices it left out. The proof checks
that those are exactly the slices the plan at the narrowing's off level
selects and this one doesn't; that holds in all 22 plans with a narrowing.

What `why` says:

- the `Discount` change, `app/price_test.go`: "it executed
  go:app#func:Discount, which changed";
- the `Start` change at level 2, a slice the sample left out: "not in the
  sample: code every slice of the suite ran changed; the nightly runs it;
  it would have run at narrowing level 3 or above (the plan used 2)";
- the `Config` change, likewise, with "code every slice of the suite
  reaches only through what they all ran changed";
- the go-cmp bump at level 2, `app/greet_test.go`: "the narrowing
  module-bumps left it out: dep:go:github.com/google/go-cmp changed, which
  only the wide rule binds to these slices; it would have run at narrowing
  level 5 or above";
- the new function at level 3, `app/greet_test.go`: "it depends on the
  names nothing binds in app, which changed";
- the `unused` change: "nothing it is bound to changed".

#### Safety rules

| Rule | What happened |
|---|---|
| A slice its correlator can't tell (its coverage files deleted before `slice end`, on a new recorded commit) | a change to `Discount` selects price and greet; `affected` warns on stderr and in the plan: "go/app/greet_test.go: go-correlator couldn't tell: ... no coverage ...; bound to everything" |
| A change that edits the config can't deselect its own tests: it excludes `app/greet_test.go` from the suite and changes `Greet` | greet still runs, with the reason "selected by the base's configuration", and report runs for `Greet` |
| The same, where the config lists itself under `everything_on` | all 3, "pipelinegen.yaml changed (everything_on pipelinegen.yaml)" |
| A plan started while another process holds the cache's lock for 15 s | it waits, and plans only once the holder lets go (the proof checks that it never ends before), with the right selection |
| The same, with the holder killed (`kill -9`) after 5 s | still waiting at 5 s, done 1 s after the kill |
| The plan's work tree moved to another commit while bazel-diff hashed it | exit 1, "the work tree changed underneath the plan: <head> was checked out to hash, <its parent> was there after"; no snapshot of the head kept, and the next plan selects greet and report, as it should |

The moved work tree is made by a bazel-diff wrapper that, right after
hashing the head, checks out the head's parent in the plan's work tree, as
a second process could. The lock is held with `flock` on the cache's lock
file.

Three more rows each record on a remote and a cache of their own, so no
earlier recording makes up for them. In each, a recording says nothing
useful about the price slice, and a later change to `Discount` must still
select it:

| Recording | What happened |
|---|---|
| The batches get their tree as a symbolic link | the same lists as through real directories (3, 4 and 4 units); price is selected |
| The price slice's coverage names a module the tree doesn't hold | price is marked "can't tell" and has no list; it is bound to everything, with the warning on stderr and in the plan |
| The price slice's tests run and its coverage counts nothing | price is marked, "its tests ran and nothing was recorded", and bound to everything, with the warning |

How the plan reads a stored list: a slice whose list exists is bound to
its units, its own files and its data; a slice with no list, or with a
`<slice>.cannot-tell` mark beside its list, is bound to everything, and
the plan says so.

#### The config's own changes

These rows plan a change of `pipelinegen.yaml` itself, on a commit that
adds a suite of two check scripts in no run, `lint_check.sh` and
`vet_check.sh`, each bound by the Bazel graph and run by steps: a set-up
step of the built-in `command` driver, then a run step of `command` for
lint and one of a driver entry, `shell`, for vet. The config is under
`sample_on`, at level 2. Not yet shown in a run; what each row checks:

| Change | What runs |
|---|---|
| The lint step's `with` | lint, and nothing of the go suite |
| The set-up step's `with` | lint and vet |
| The `shell` driver's pin | vet, which only that driver runs |
| A comment above the suite | nothing |
| A time limit (`minutes`) beside the lint step's change | lint, which the sample keeps, and one sampled go slice: the planner doesn't understand the limit, so the config is a `sample_on` file again |

### The outward walk against the forward binding

Planning walks outward from what changed instead of binding every slice
forward (see [internals/graph.md](internals/graph.md)). The forward
binding stays in the tests as the reference, and the walk has to select
what it selects. The comparison lives in
[internal/graph/graphtest](../internal/graph/graphtest):

- `Forward` binds every slice in the base and in the head, then compares
  what each slice's selector depends on in the two trees: its units with
  their digests, units one tree lacks, whole groups, data labels, and
  every unit for a slice bound to everything. A test holds that this
  comparison decides exactly as the deps repository does, read file by
  file, on 40 random pairs of trees.
- `Model` binds by the rules of the graph's link kinds, with one depth
  counter. Over plain links it binds unit for unit what
  `plugins.Set.Bind` binds, in both widths and both trees, on 60 random
  pairs with depths, narrowings, pins and plugins that couldn't tell.
- `Compare` sorts each slice into a class and counts the classes. Four
  are accepted, each naming its cause:
  - the walk alone selects it through a link that only one tree has (the
    walk counts that link's source as changed);
  - the walk alone selects it at the dependents width through an
    implementation junction;
  - it is a suite's sample target, selected by the walk alone in a width
    where the walk selects every slice the sample is drawn from, so the
    sample adds no test;
  - only where a test asks for it, the walk alone selects it through a
    unit the sample takes whose links moved, kept as a source because
    more links moved than the walk compares tree by tree.

  Anything else fails, in either direction. So does a path that isn't
  made of the trees' links. A left-out path is checked against the trees
  with that narrowing's links put back. A slice selected through a
  plugin has one path of one step and no root: a `pin` step to the
  plugin's node, or an `everything` step to the node of a correlator
  that couldn't tell.

[internal/graph/walk_equiv_test.go](../internal/graph/walk_equiv_test.go)
runs that comparison on 24 generated cases. Together they cover plain,
`follow`, `bind` and `dependents-only` links, group nodes and junctions,
both widths, narrowings on and off with what each leaves out, units in one
tree only, slices without a list, units and correlators that can't tell,
changed pins, depths and a group every list reaches. On those cases the
walk must select exactly what the oracle selects, with no class accepted:
a generated unit's links change only with the unit, and the model joins
implementations as the walk does. The accepted classes are for plans with
real plugins, which can break that rule; one test makes a link appear
between two unchanged units and expects that slice in the class, link
named. A guard fails when the cases stop telling slices apart.

The sample is compared in
[internal/cli/sample_equiv_test.go](../internal/cli/sample_equiv_test.go)
against `toSample`, the split planning uses today, with and without
`sample-bound`, on 32 cases that each have a setup slice too. The sample
takes a changed unit every slice ran out of each slice, but not what that
unit links. So when a change moves those links, a slice that bound the
old target only through it, or binds the new one only through it, binds
something else and is selected. The walk does the same, and a three-unit
case holds it. Past 64 moved ends the walk keeps the unit as a change of
its own and selects every slice that binds it. That is more than the
split, never less, and a case with 70 moved ends holds it.

`Model` is held to the Go correlator itself in
[internal/cli/anchor_equiv_test.go](../internal/cli/anchor_equiv_test.go).
On every tree the plugin proof plans, it binds the recorded slices twice:
once through the correlator's `bind` call and the plain links, as
planning does today, and once through the typed links the same
correlator answers. Both must bind the same, unit for unit, in both
widths. It runs on a proof's work directory
(`PIPELINEGEN_TEST_ANCHOR_WORK`). It skips while the pinned correlator
declares no `links`.

With development builds of go-sbom and go-correlator before v0.2.0, it
bound all 70 trees of the proof (13 changes on five settings, and the
commits under them) the same both ways: no binding differed, in either
width, at any level.

Two rows cover a backend handler and the browser tests that reach it
through the API contract. They run on the synthetic graph, since the
fixture has no frontend:

| Case | Executed width | Dependents width |
|---|---|---|
| A browser test whose list holds the operation it requested and the handler's units it ran; the handler changes, or the operation does | selected | selected |
| A frontend test against a stand-in backend: its list holds the operation, no backend unit; the handler changes | not selected | selected, one step back from the operation to the handler that implements it |

The second row is right as it stands: a test that never ran the
handler can't change its result when the handler does, and the
dependents width (or level 5) still runs it.

`follow_depth` counts one number of steps over both kinds: a typed link
followed at the dependents width and a plain link taken against its
direction, so a path that mixes the two kinds counts every step.
`TestOneDepthCounterOverBothKindsOfStep` shows a case: under a depth of
one, a call and then a step back to a table that uses the called function
add up to two steps, and the table is not bound. No plan on this page sets
it.

## Measured

### GitHub's limits on a comment and on Mermaid in it

Measured on pull request #1 and read from GitHub's Mermaid viewer bundle
`5b99bd9a2037271fa4cf`.

- A comment body may hold up to 262,144 bytes of UTF-8, although the error
  message says "maximum is 65536 characters": 262,144 bytes of ASCII or of
  two-byte letters went through, 262,145 and 262,146 bytes were refused with
  422, for a new comment and for an edit alike. 65,536 characters therefore
  always fit, since no character is longer than four bytes.
- GitHub renders Mermaid in its viewer (`viewscreen.githubusercontent.com`)
  with Mermaid's default limits: `maxTextSize` 50,000 characters
  of chart (over it, the chart becomes a red box "Maximum text size in
  diagram exceeded") and `maxEdges` 500 (the flowchart parser throws "Edge
  limit exceeded" when adding the 501st edge). The fallback to the list at
  400 edges or 40,000 characters stays clear of both.

The comment limit was measured through the API. The Mermaid limits are read
from the code of GitHub's viewer, not yet seen rendered: the charts under
pull request #1 (400, 499, 500 and 501 edges; 41,794 to 59,794 characters)
wait for someone logged in to look at them.

The same changes, planned without Concourse, are the cases of
`internal/example`'s tests, which run on every change to the tool.

### What binding forward costs against the walk's budget

`TestBindingForwardMissesTheBudget` binds a tenth of a large backend:
3,700 units in packages of ten, about 30,000 links, and 100 recorded
slices of 130 units, both trees and both widths. That made about 1.6
million allocations and held 476 to 544 MB of heap above what was live
before, and took 5 to 11 s on the shared build machine. The walk's budget on
the same trees is 250,000 allocations and 64 MB (`TestTheWalkKeepsItsBudget`).
Both are counted work, which a busy machine doesn't change. Time is held
only as a ratio: four times the trees may take the walk at most eight times
as long, measured back to back and tried three times, and not under the
race detector. At the full size, 37,000 units, about 300,000 links and
1,000 slices of 1,300 units, the walk must stay under a second and 512 MB;
that test runs with `PIPELINEGEN_WALK_SCALE=full` on a quiet machine,
since the forward binding can't follow there.
