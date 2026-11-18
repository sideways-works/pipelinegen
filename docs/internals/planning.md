# Planning

This page is for people changing how a plan is made. `affected.Compute`
turns a base and a head commit into a plan. `pipelinegen affected` calls it
on your machine, and the planning job through `pipelinegen plan`. The page
follows the code in `internal/affected`.

## Step by step

1. Resolve the head. A head that isn't a commit is an error
   (`ErrHead`): there is nothing to run.
2. Decide whether everything runs before hashing anything
   (`everythingReason`): `--all`; no base ("nothing to compare with"); a
   base that doesn't resolve; a base that isn't an ancestor of the head; a
   changed file matching `everything_on`. Each gives its reason, and the
   plan lists every slice of the head. Such a plan binds nothing, so
   `plan` and `affected` ask `affected.Everything` before anything else
   and then load no plugin and read no lists. The head's tree gets the
   stub deps repository (`Options.Stub`, `depsrepo.Stub`) in place of
   `Prepare`'s, which is enough for Bazel to list the tagged slices.
3. Check the base out and snapshot it. `gitx.Checkout` puts the commit
   into one reused work tree under the cache (`<cache>/tree`), a detached git
   worktree, cleaned of untracked files except ignored ones. Reusing one
   directory keeps Bazel's output base warm. A snapshot holds:
   - the slices, from a Bazel query per suite for targets carrying its tag
     (`bazel.Slices`);
   - the files Bazel owns: every source file of every target, and the
     BUILD and `.bzl` files (`ownedPaths`);
   - bazel-diff's hashes and dependency edges (`Differ.Hashes`).

   Snapshots are cached by commit (and by what `Prepare` wrote, below), in
   `<cache>/snapshots`, written under temporary names first so a cut-off run
   never leaves a file the next one would trust. A base that can't be
   snapshotted makes everything run with the reason; a head that can't is
   an error.
4. Snapshot the head the same way.
5. Find the impacted targets. A changed file that neither commit's Bazel
   owns makes everything run ("no Bazel target owns it"). Otherwise
   bazel-diff's `get-impacted-targets` compares the two hash files; if it
   fails, everything runs ("bazel-diff failed").
6. Select per suite. A slice runs when its label is impacted; the rest
   are untouched. A suite whose every slice runs says so ("the change
   reaches every slice"). A whole-suite check runs when every slice of its
   suite does, or when one of its `also_on` targets is impacted. Slices the
   base had and the head lacks are `removed`. With `follow: dependents` a
   test file is selected by its `.dependents` selector. While the sample is
   on, an impacted `_sample.<suite>`, `_sample-bound.<suite>` or setup
   target runs a sample (`sampled.go`): `sample` slices, the slices
   `mustRun` keeps among them, the rest picked by `internal/sample` with
   the head commit as seed. `mustRun` keeps every slice selected on its
   own, except for a `sample_on` sample of a change that touches only
   `sample_on` files: then only the slices whose own files changed and
   those a changed plugin entry reached. When planning binds through plugins, `Narrowed` then names, per
   narrowing, the slices left out whose dropped units changed.

   Before that, the `Unobservable` hook names two kinds of changed files
   a recorded slice may take for no reason of a label's. While
   `unobservable-edits` is on: those that changed nothing observable,
   modified in place, `complete` by their sbom in both trees under the
   same pin, told about with nothing it can't tell, and with the same
   units and digests (`unchangedWhole` in the cli). While a file's
   `by_units` narrowing is on: those claimed with the same anchor in both
   trees, which a slice takes when its list or its suite's setup part
   holds the anchor; the walk binds it to the units of them it touched.
   A slice never takes a file its suite's `text_inputs` say it reads, and
   a label its own `pipelinegen:data` lines name stays a reason (the
   `Declared` hook). In a recorded suite, an impacted input label is then
   no reason when nothing else that changed reaches it
   (`unobservable.go`). With nothing else changed that holds without a
   question. Otherwise, once for each group of slices taking the same
   files, the head's work tree gets the base's copies of them
   (`hashWithout`), bazel-diff hashes it in the same output base and lists
   what differs from the base's hashes, and the head's copies go back; a
   label in that list stays. Whatever bazel-diff counts as a change counts,
   a file a module extension reads included, so no list of special files
   is needed. The hashes go into a `without-*` directory of the cache,
   removed when the hashing ends, and by the next plan (which holds the
   cache's lock) when a plan died first. The plan counts those hashings in
   `label_groups`. When
   bazel-diff fails there, every label stays, with a warning. The slices
   left out get a `narrowed` entry naming the files, the labels and the
   narrowing.
7. Give reasons for each selected slice (`reasons.go`): first its own
   test files (added, removed, changed, or the whole slice new), then up to
   three root causes from bazel-diff's `explain`, nearest first, skipping
   the slice's own files and BUILD files. A root cause that `Why` knows
   (the bindings Prepare wrote, which may name the unit behind a cause)
   becomes `executed` or `depends`; any other is `target`, with
   bazel-diff's path to the slice. bazel-diff hashes a rule's own files
   into the rule, so the deepest cause it names is a rule, such as a
   filegroup one of whose files changed: `filesBehind` asks Bazel once per
   rule which of its files the change touched, and the reason names each
   of them, with the rule as the first step of its path. A walked slice
   selected by one of its input labels (`walkedReasons`) has the label
   explained: its root causes don't depend on the slice, so each label is
   explained once per plan and every slice selected through it gets the
   same reasons. `explain_limit` counts those explanations, not the slices
   behind them.
8. Group the reasons (`causes.go`): a reason two or more selected slices
   of a suite share, other than their own test files, becomes one of the
   suite's `causes`, with how many slices it selects, the largest first.
   The walk's reasons group by the unit that changed, since the path back
   to each slice's recording differs. The plan's log line names each
   suite's two largest causes and counts the slices none of them covers
   (`e2e 241 of 241 (241: e2e/reporter.ts changed, through
   //e2e:runner)`), then each narrowing that left slices out and how many
   (`e2e 34 of 241 (33: ...) [plugin-entry left out 207]`), and the
   comment names the causes once instead of under every slice.

The plan's JSON is in [contracts.md](../contracts.md#d-run-state-temporary-refs).

## The deps tree and the check

`Options.Prepare` is the hook where planning writes, into the work tree,
what Bazel must see before it is queried: the deps tree of
`@pipelinegen_deps`, holding each test file's generated selector and the
digests of what it is bound to ([binding](binding.md)). Its key joins the
commit in the snapshot cache, so a changed binding is a new snapshot. So
do the config and `ToolKey` (the pipelinegen build, by its version and its
executable's hash, and the bazel-diff binary's hash): a cache kept across
an upgrade never mixes snapshots hashed by different tools, which would
make every slice differ. One plan holds a cache at a time (a lock file in it): plans
share its work tree, and a second one writing there while the first hashes
would cache a snapshot of a half-written tree. After hashing, the plan checks that the
work tree is still the commit and that the deps repository still has the
key Prepare returned (`Written`); if either changed, the plan fails and
keeps nothing. Each snapshot counts the deps repository's targets it
hashed: fewer than both selectors of every test file plus every unit's
digest (`Expect`) fails a fresh snapshot, and makes a cached one hash
again, with a line in the plan's `warnings`. When
the tree holds `.pipelinegen/deps`, bazel-diff hashes it with
`--fineGrainedHashExternalRepos @pipelinegen_deps`, so one changed digest
impacts only the selectors that depend on it; without that flag it would
impact every slice.

`pipelinegen plan` sets `Prepare` only when the workspace's `MODULE.bazel`
names the `pipelinegen_deps` repository. Then the deps tree is written for
base and head alike, both bound with the base's lists, so lists that differ
alone select nothing. It goes through the plugin path
([plugins.md](../plugins.md#what-the-core-does-with-them)), with no plugins
at all when none is configured, so that a graph suite binds by its own file
and its owner either way. That is the forward path, which only a build
with the `pipelinegen_forward` tag takes (the plugin proof compares it with
the walk). A release build walks
instead: one walk of the stored graph from what changed selects each test
file ([the stored graph and the walk](graph.md)), no deps tree is written,
and the repository needs no setup lines; the check warns of lines an older
setup left.

Before planning, `plan` runs the check on the head commit
(`check.Commit`, the same `check.Find` that `pipelinegen check` calls in
the commit hook) and, by
`uncovered`, fails the run or selects everything with the findings as a
warning. It also sweeps old runs and lists by `keep`.

When the change edits the config, `withBase` first reads what the edit
means (`config.MeaningOf`, compared with the config the run plans with,
the trusted keys in it for a proposed change): each changed key path
with its meaning, the slices whose steps or settings differ
(`SliceRuns`), the drivers whose pin changed (`Drivers`). Both plans get
it as `Options.Meaning`. `configSelect` (`internal/affected/configured.go`)
selects the slices that run differently, kept by every sample, and the
slices a changed driver runs, through `plugin-entry` as a changed plugin
entry; while every part is understood, `beyondConfig` takes the config out
of the changes `sample_on` reads and `impactedTargets` takes it for owned.
Bazel's targets that hold the file see it change all the same.

Then `plan`, `affected` and `why` plan under
the base's config as well, read as history (`config.ParseHistory`, whose
problems become warnings), and unite the two plans (`withBaseConfig`): a
slice only the base's config selects has the reason `base-config`. A
trusted run unites them with `affected.UnionTrusted`: a slice the base's
config selects is added only when the head's plan of the suite has it
among its slices (`SuitePlan.Slices`), and a suite only the base's config
has isn't added at all; each one left out is a warning of the plan, which
says whether the head's config excludes the slice or no longer has it. A
proposed change keeps the whole union (`affected.Union`). A base
config that can't plan (it can't say which suites and test files it had,
or planning under it fails, on a finding of the check, say) never stops
the plan: a trusted run (the default branch's own) plans under its own
config alone, and the plan's warnings say so and why; any other run, and
`affected` and `why` on your machine, select every slice with that
reason, so a proposed change can't deselect its own tests by breaking the
base's config.

Planning binds every suite of the config. `plan` then keeps, after the
union with the base's plan, only the suites the run names in the head's
config (`ofRun` in `internal/cli/run.go`): those are the suites the run has
jobs for, and a suite it doesn't name would be waited for and never end. A
proposed change's config gets, with the other trusted keys
(`config.WithTrusted`), every suite the trusted config names for the run,
so it can't take a suite out of its own run.
The readers of a kept plan do the same by the config of their tree
(`config.RunCovers`; `planFile.ofRun` in `internal/report`, applied as the
snapshot is read and in the narrowing's net), so a kept plan with every
suite in it counts the run's suites alone. `affected`,
`try` and `why` planning again make a plan for no run, which keeps every
suite.

## Test files as slices

For a suite with `tests` and no `tag` (`Suite.Implicit`), the slices aren't
queried by tag: `TestFileSlices` lists the tracked files of the work tree
matching the patterns, minus `exclude` and helpers (a Go file without a
top-level `Test...` function, read by `internal/gotests`; any other file
matching `helpers`). Each slice's label is its selector in the deps
repository, `@pipelinegen_deps//slices:<path>`, which is what bazel-diff
reports as impacted (labels in the deps repository are normalised,
whatever form of its name Bazel prints). Its name is its path, and `tests`
carries a Go file's test names.

On the forward path the selectors are written by `Prepare`. Without it,
nothing could select a test file, so a suite of test files selects every
file, with the reason "no selectors for the test files: learn's stub did
not run". A release build selects test files by the walk and writes no
selectors.

## Why one work tree

bazel-diff hashes the workspace in a directory, and Bazel keeps an output
base per directory. Checking base and head out into the same directory, one
after the other, lets the second query reuse the server and most of the
analysis, and the snapshot cache makes a commit planned before free.

## The cache a step leaves

A planning step's cache outlives the step, and Concourse pays for it in
files: when a task that declares a cache ends, the worker copies the whole
cache directory into a new volume, file by file. `trimCache`
(`internal/cli/cachetrim.go`) runs when `plan` or `unplanned` ends. It
drops what no later build uses (the work tree, the rebuild's tree, the
claims trees, the snapshots but those used and the newest four), then the
extracted copies of archives the cache keeps (`dropExtracted`), and only
then trims to `keep.cache` by size, the cheapest part to get back first.

The extracted copies are two. Go keeps each module twice in its module
cache: the zip under `cache/download`, and the module's files extracted
into `<path>@<version>`. A command that reads a package of a module whose
directory is missing extracts it again from the zip, with `GOPROXY=off` as
the plugins run, so a module goes when its zip is there and stays when it
isn't. Bazel keeps a repository's archive in its repository cache
(`content_addressable`) and, from Bazel 8, the extracted repository in
`contents`, which the output base's `external` links to. A Bazel that finds
a link's target gone fetches the repository again, from the archive when
the repository cache has it, and a warm server does the same. A
repository without an archive there (`git_repository`, a download without
a checksum) comes over the network again, which is slower and never
different. On the tool's own tree, with Bazel's output kept, these two are
more than half of the cache's files, nearly all of them Bazel's.

`TestGoReadsTheSameModulesAfterTheTrim` holds the Go half to the same
packages, directories and export data after the trim, with no network;
`TestAPlanStartsBazelWarmFromItsCache` holds the Bazel half to no new
download, with the network closed, for a new build and for a step's second
plan on a warm server, and to the same selection as a cold plan.
