# Troubleshooting

The first part lists the messages you can meet while setting pipelinegen
up, in the order of the [step-by-step guide](tutorial/README.md). The
second part starts from what you see in a running pipeline: a slice that
didn't run, a run that ran everything, a step at its time limit. Each case says how to
find out what happened and what to do.

## While setting up

### `invalid config: ...`

pipelinegen reads the whole config and reports every problem at once, one
`invalid config:` line each. Unknown keys are errors, so a typo never
turns a setting off quietly. Two that surprise people:

- `github needs a repository and the name of its token var` and `concourse
  needs a repository source and a planner image`: once a config declares
  `runs`, the git host and the CI system are required. Leave `runs` out
  until you generate the pipeline ([step 9](tutorial/pipeline.md)).
- `concourse.planner needs the image's digest` and
  `concourse.planner.bootstrap needs ...`: the planner's image is pinned
  by digest, and the tool and bazelisk by version and checksum.

### `<file>: no slice depends on this file, so a change to it would run nothing`

The check found a file no test covers. Make a test depend on it, or list
it under `untested` with the reason; see [the check](tutorial/check.md).

### `pipelinegen.yaml: untested pattern "<pattern>" matches no file`

An `untested` pattern matches nothing. Files git ignores don't count, so a
pattern for an ignored directory (`tools/**`) matches nothing. Remove the
pattern or fix it.

### `<file>: //pkg:target's inputs can't be read without running Starlark ...`

The check reads BUILD files without Bazel, so it needs a target's inputs
written as strings, lists, `glob` and the like. Names loaded from other
repositories (the binaries of `rules_js` packages, say) are tools and need
no reading. A macro from another repository that globs files itself is
declared under `macros`; [the check's
reference](reference/check.md#a-macro-from-another-repository) shows how.

### `warning: MODULE.bazel: declares the pipelinegen_deps repository, ...`

And the same for `.pipelinegen` in `.gitignore` or `.bazelignore`: lines
planning no longer needs. Remove them as the warning's `fix:` line says;
[fresh clones](how-to/fresh-clones.md) has the details.

### `<file> changed and no Bazel target owns it: it is in no target yet (run gazelle)`

A new Go test file that no BUILD target holds yet. The plan runs every
slice until it has one. Run `bazel run //:gazelle` after adding a file,
and commit the BUILD files it changed.

### `no runner plugin is configured`

`pipelinegen pipeline` needs a plugin of kind `runner` in the config, and
its binary in `tools/plugins/<name>`; see [step 9](tutorial/pipeline.md).

### A plugin release has no binary for your machine

A plugin's release page lists the systems it is built for (pipelinegen's
own releases have Linux and macOS binaries). Node
plugins are one `.mjs` file for any platform. For a system a release has no binary for, build
the plugin from source ([step 6](tutorial/plugins.md)). In CI, run the
planner and the suites on Linux workers.

### Planning can't see a private Go module

Plugins never use the network, so every module the code imports must be in
Go's module cache before planning. `concourse.planner.prepare` runs a
command first (`go mod download`); for private modules give it a token
with `concourse.planner.secrets` and `concourse.planner.git_token`, and
set `GOPRIVATE` in `concourse.planner.params`. Without them the Go plugins
can't tell what the code holds, and the plan binds wide and warns.

### A Go plugin can't tell because of the Go version

A plan warns that go-correlator couldn't tell, with a message that names a
toolchain, your `go.mod` and two Go versions, and then runs more slices
than it should. The Go plugins type-check with the `go` command they find,
which must be at least as new as `go.mod`'s `go` line. They run in a copy
of your repository under pipelinegen's cache ([where pipelinegen keeps
things](reference/your-machine.md)), where a version manager may pick
another Go than in your checkout. Commit the version manager's file, or
put the right Go first on `PATH`. In CI, use a planner image with that Go.

### TypeScript plugins fail in the planning job

The TypeScript plugins need Node, and the planner's stock `golang` image
has none. Use a planner image that has both Go and Node, pinned by digest
in `concourse.planner.image` and `digest`.

## In a running pipeline

### Why did a slice run?

`pipelinegen why --run <run> --change <change> --commit <commit> --suite <suite> --slice <slice>` for a CI run's kept plan, or `pipelinegen why --base <base>
--head <head> --slice <slice>` to plan the commits again on your machine
([looking inside](how-to/looking-inside.md#why-a-slice-was-selected-or-left-out)
says when to use which), prints each reason as a chain:
what the slice executed during its recording, each link the walk
followed with the plugin that gave it, and what changed. `--ids` adds the
units' ids. A slice that runs on every change names its one cause; see
[below](#a-slice-runs-on-every-change).

### A slice I expected did not run

Find out:

1. Plan the same change on your machine. For a pull request, compare its
   head with the target's tip; for a commit on a branch, with the commit
   its run compared with:

   ```sh
   pipelinegen affected --base origin/main --head HEAD --json
   ```

The slice is either under `selected` with its reasons or under
`untouched`.
2. `pipelinegen why --run pr --change pr-12 --commit <commit> --suite <suite> --slice <slice>` gives the same
   for the run itself, and `pipelinegen lists show --slice <name>` shows
   what the slice was recorded touching.

On the default branch's own run, a slice the base's config had in the
suite and the commit's config excludes or no longer has doesn't run there,
and the plan's warnings say so: `<slice>: the base's configuration had it
in <suite>; this commit's configuration excludes it, so it no longer runs
there`. `affected` and `why --base` plan as a pull request does, so they
still select it, with the reason `base-config`.

What to do: an untouched slice means nothing it depends on changed, by the
graph and by its recording. If it should have run, an edge is missing:

- it reads a file it doesn't declare: add a `pipelinegen:data` line or an
  `extra_deps` entry ([adding a
  test](how-to/adding-a-test.md#files-a-test-reads));
- it reaches code through something the graph can't see: give its owner
  target the dependency;
- it runs code its recording doesn't hold (a branch it didn't take last
  time): that is the `executed` width; `follow: dependents` also follows
  what the code it touched calls or uses, at the cost of selecting more.

Fix the edge. Don't make the run select everything to be safe: the nightly
runs everything anyway and will show a test that broke unnoticed.

### Everything ran

Find out: the plan's first line, the comment's heading or the suite's
status says why: `every slice: <reason>`. `pipelinegen affected --base
<base> --head <head>` prints the same line.

What to do, by reason:

| Reason | What to do |
|---|---|
| `nothing to compare with` | Expected on a branch's first run, and on a branch run with no earlier run that passed or is still going. The next run compares with this one. |
| `every slice runs in this run` | Expected: the run selects all (`select: all`, a nightly). |
| `files no slice covers: ...` | Planning's check found files no slice depends on. Cover them or list them in `untested`; see [uncovered files](how-to/uncovered.md). |
| `the base's configuration can't be read, ...` | The base's `pipelinegen.yaml` doesn't say which suites and test files it had, so what it selected can't be known. Runs on later commits compare with a later base. |
| `the base's configuration can't plan, ...` | Planning under the base's `pipelinegen.yaml` failed (a finding of its check, say). A pull request's run selects every slice; the default branch's run plans under its own config alone and says so in a warning. Runs on later commits compare with a later base. |
| `the base <x> is unknown` | The base commit isn't in the clone: fetch it (`git fetch origin <base>`). |
| `the base <x> is no ancestor of the head` | The head was rebased or force-pushed past the base. The next run compares with a base on its history. |
| `<file> changed (everything_on <pattern>)` | Expected: the build setup changed. Narrow the pattern if it names more than the setup. |
| `<file> changed and no Bazel target owns it` | Give the file a target (for a new Go file, `: it is in no target yet (run gazelle)` follows: run gazelle and commit what it writes), list it in `everything_on` if it is build setup, or in `untested` with the reason if no test needs to run when it changes. |
| `the base <x> could not be hashed: ...` | The base doesn't build its graph (a broken BUILD file at the base). Runs on later commits compare with a later base. |
| `bazel-diff failed: ...` | Read the message; a Bazel or bazel-diff failure is never taken as "nothing changed". |
| `the change reaches every slice` (one suite) | Something every slice depends on changed: a shared helper, the app every spec opens. Expected, or a sign the graph is too coarse there. |

### A slice runs on every change

Find out: `pipelinegen why --run <run> --change <change> --commit <commit> --suite <suite> --slice <slice>` names the one cause. Each cause
and what to do:

| `why` says | What to do |
|---|---|
| `it has no recording yet, so it is bound to every unit the plugins list` | Expected until a recording run, in CI or `pipelinegen record` on your machine, gives it a list. |
| `nothing binds it but its own file (no owner target, no Bazel target holding the file, no extra_deps entry, no pipelinegen:data line), so a change to what it imports would select nothing; it runs on every change until the config binds it` | Give the suite an `owner` pattern, declare a Bazel target that holds the test files, or bind the suite by recording with a correlator. `pipelinegen check` names the suite and its files. |
| `<correlator> couldn't tell what the recorded slices touched in one of the trees (its warning says why), so every slice it binds runs` | Read the plan's warning for that correlator: often a toolchain or a module cache it needs ([troubleshooting above](#a-go-plugin-cant-tell-because-of-the-go-version)). |
| `<plugin> couldn't tell about <file>, which changed, and the slice reaches what it lists` | The plugin couldn't read that file; its warning says why. Fix the file or the plugin's config, or accept the wide run for this change. |
| `it reaches <unit>, which can't be told (<plugin>: <why>), in the group of <unit>, which changed` | A plugin can't follow that unit (code built at run time, a package that doesn't build, a reference to something no plugin describes). `<plugin>` is the one that said so, which may be a correlator rather than the sbom that lists the unit; its README's "What it can't see" says what helps. |
| `it touched <unit>, which no configured sbom lists: configure an sbom that lists <kind> units, or every change runs it` | Add the sbom plugin for that kind of unit (npm-sbom for `dep:npm:` units, say). |
| a mark: a correlator couldn't tell what the slice touched in its last recording | `pipelinegen lists show --slice <name>` shows the mark and its reason, such as "3 tests ran and nothing was recorded". Fix what the reason names (often the task left no coverage) and record again. A suite whose tests touch nothing of the tree sets `empty_recordings: allow`. |

### A step says locks are gone

Find out: a suite job's step prints `pipelinegen lock: locks are gone`,
and `pipelinegen check` says `suites.<name>.lock is no longer read`.

What to do: nothing breaks; the step passes and takes nothing. Remove
`lock` from the suite (move its `minutes` to the suite's own `minutes`) and
set the pipeline again, so it has no lock steps before the release that
drops the command.

### A step reached its time limit

Find out: the slices the step left open are errored, and each says why.
A batch's slices say `batch <n> stopped while slice <name> ran` and then
one of these:

- `after 125 minutes: its time limit of 120 minutes (suites.e2e.minutes)
  stopped it`: the limit ended the batch. A pipeline generated with locks
  per batch knows this exactly, from the moment its lock step ran.
- `14 minutes after the suite began: not its time limit`: the batch's
  task died on its own; its build log says why.
- `its time limit is 120 minutes (suites.e2e.minutes): the build log says
  timeout exceeded when that was the cause`: a batch's end
  only knows when the suite's work began, so it can't tell more. Concourse
  writes `timeout exceeded` in the step's log when the limit ended it.

A whole-suite check that left no result says `the whole-suite check
<name> stopped before it wrote its result` with its limit and the key that
raises it; its build log tells whether the limit stopped it. Slices that
never started say `suite <name> stopped before these slices
reported` with the same kind of reason, and `suite <name>'s work never
began` when the step before the batches (the tree, the batches) didn't
finish. One of pipelinegen's own steps ends two minutes before its
limit and says `stopped at its step's time limit of 20 minutes
(limits.steps)`.

What to do: a batch that needs longer gets a higher `minutes` for its
suite, or for one run (`runs.<run>.suites.<suite>.minutes`, for a nightly).
Smaller batches (`batch_minutes`) keep each one well inside the limit. A
step that hangs is the defect to fix: raising a limit only makes it hang
longer.

### The comment is stale

Find out: the comment is rebuilt from the run's refs at every flush of a
batch's journal: when the batch ends, and, in a task that runs `slice
flush --every <period>` in the background, every period. Compare the comment with the suite statuses
on the commit: both come from the same refs.

What to do:

- A slice that shows as running long after its build ended had its task
  killed before its end step: the batch's `ensure` marks it errored once
  that step runs, and the suite's end marks every slice that never
  reported as errored.
- A comment that belongs to an older head stays as it was; each run of a
  pull request keeps its own comment, found by the hidden marker `<!--
  pipelinegen:<run>:<number> -->`.
- Rerunning the suite's build in Concourse rewrites it.

### A recording failed

A slice whose task left no coverage, or only empty coverage, doesn't fail:
its correlator can't tell from that, so the slice gets a mark and runs on
every change (see the case above). The usual causes are binaries built
without coverage (for Go, `-cover -covermode=atomic -coverpkg=...` and the
`pipelinegen_cover` tag), `GOCOVERDIR` not set for them, or a server
killed hard that wrote nothing. Give every binary the stack starts a
SIGTERM handler, and stop the stack before the batch ends.

A slice that passed shows as errored with a recording error when the
recording itself went wrong. Find out from the slice's failure text in the
comment, or the batch task's log:

- a server's counters are missing for one slice: the correlator's
  `collect` couldn't get them. For a Go server, the Go correlator's README
  lists what it needs: its `cover` package linked into the coverage build,
  the `pipelinegen_cover` tag, `-covermode=atomic`, and the same directory
  in `cover_dir` and in the server's `PIPELINEGEN_COVER_DIR`.
- a correlator failed: the text names it and its error.
- "the slice never ended in its batch": the task was killed before the
  slice's `slice end`.

Either way its list stays as it was, so selection stays safe; rerun the
build once the task is fixed.

### The check fails on a file I don't think needs a test

Find out: `pipelinegen check` names the file and the finding.

What to do: if a test reads it or a slice should depend on it, make the
slice depend on it (see [the
check](reference/check.md#the-findings-and-their-fixes)). If no test can
cover it (prose, repository settings, a file only people read), add it to
`untested` with the reason:

```yaml
untested:
  - paths: ["design/**/*.fig"]
    reason: design sources, read by people only
```

`untested` is a list people review, not a way past the hook: a change to a
file that is only on it runs nothing.
