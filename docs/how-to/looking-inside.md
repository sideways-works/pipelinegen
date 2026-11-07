# Looking inside

The lists and a run's state are hidden refs, so GitHub shows
none of it. Two commands read them, from a checkout of the repository with
`GITHUB_TOKEN` set.

## What a commit's lists hold

```sh
pipelinegen lists show                       # the lists HEAD uses
pipelinegen lists show --commit 0723eb0a     # another commit's
pipelinegen lists show --suite e2e           # one suite's
pipelinegen lists show --slice e2e/editor.spec.ts
```

It prints the lists as planning sees them for that commit: where they came
from (the commit's own ref, or its nearest first-parent ancestor's and how
far back), the live runs whose recordings it added, how many units each
slice's list holds, and which slices a mark binds to everything: the
correlator that couldn't tell, since when and why. With `--slice` it prints
that slice's units instead, one ID per line.

## Why a slice was selected, or left out

```sh
pipelinegen why --run pr --change pr-12 --commit 3f9c2a1e7b04 --suite e2e --slice e2e/editor.spec.ts
pipelinegen why --base origin/main --head HEAD --slice e2e/editor.spec.ts
```

The first reads the plan the run kept, which is what the run did: use it
for a slice of a CI run. `--run` is the run's name in the config,
`--change` the change as the run names it (`pr-12` for pull request 12,
the branch for a branch's run), `--commit` the commit it planned (its
first 12 characters do; left out, the change's newest kept plan) and
`--suite` the slice's suite (left out, every suite that has it). The
second plans your commits again, for commits no run planned yet or a
change you try on your machine. It plans the way the planning job would, which needs Bazel and takes as long as a
plan; with sbom or correlator plugins configured it needs them too, from
`tools/plugins/` or the directory `--plugins` names. For a selected slice
it prints the reasons, the same ones the comment shows with
`report.reasoning`:

| Kind | Meaning |
|---|---|
| `everything` | the whole run is a full one; `what` says why |
| `new-slice` | the slice didn't exist at the base, or the change's config adds it to the suite (`what` says so) |
| `test-added`, `test-removed`, `test-changed` | the test file itself, or a test in it |
| `executed` | a unit it touched changed |
| `depends` | a unit the code it touched calls or uses changed (the `dependents` width) |
| `opens` | a file of an app it opens changed |
| `target` | a Bazel target it depends on changed; `through` is the path. When the target is a rule whose own files changed (a filegroup, say), the reason names each changed file, and `through` starts at the rule |
| `input-added`, `input-removed` | a Bazel label became one of its inputs, or stopped being one (an owner's `data`, `extra_deps`, a `pipelinegen:data` line) |
| `input-unknown` | it depends on a label bazel-diff doesn't hash and nothing pins, so it runs. A label in another repository is judged by its pin as the build resolves it: a module by its version, the registries `.bazelrc` names, its overrides, and the hashes `MODULE.bazel.lock` keeps of that version when the lockfile is committed; a repository rule (`http_archive` and the like) by its sha256, integrity or commit. The same pin means the same data; another gives a `target` reason ("the pin of its repository changed"). A module without a committed lockfile rests on its registry never changing a published version: the plan says "pinned by version (no lockfile hash)" and `pipelinegen check` warns (`pinned-by-version`); committing the lockfile makes the pin exact. A local path override, a git override on a branch, an archive without a hash or an extension's repository has no pin and stays `input-unknown`, its reason naming the repository and what would pin it. |
| `text-input` | the suite's `text_inputs` say it reads this file as text: its sbom says nothing observable changed in it, which holds for code that runs it, not for a test that reads it |
| `plugin` | the config entry of a plugin that binds it, or of a driver that runs it, changed (its pin or its `config`) |
| `config` | the config changed how it runs: `what` names the part, a suite's step that takes it or a setting every slice of the suite runs with |
| `sampled` | the suite ran a sample and kept this slice |
| `base-config` | the base's config selects it: a change that edits the config can add to what runs but never take away from it |
| `cannot-tell` | a correlator couldn't tell what it touched when it last recorded, so it is bound to everything until a recording tells again |

For a slice that was left out it says why:

| Kind | Meaning |
|---|---|
| `untouched` | nothing it is bound to changed |
| `narrowed` | a narrowing left it out; the line names the narrowing level from which it would have run. A slice several narrowings left out has one line for each |
| `sampled-out` | the suite ran a sample without it; the nightly runs it |
| `removed` | the base has it and the head doesn't |

For a slice of a recorded suite it also names the correlators that observe
it (`observed by`). `--json` prints the same with each reason's `kind`. A run's plan stays
readable while `keep` keeps the run.

If you believe a slice should have run and `why` says it is untouched, the
graph or the recording misses an edge: a file the test reads without
declaring it, say. Fix the edge rather than running everything to be safe.
