# Config reference

pipelinegen reads one file, `pipelinegen.yaml`, at the root of the
repository (`--config` names another). Unknown keys are errors, so a typo
never turns a setting off quietly, and every problem in the file is
reported at once.

In the paths below, `<name>` stands for a name you choose (a suite's, a
run's) and `[]` for each item of a list. A key with no default is required
where its section says so and empty otherwise.

This page is for anyone writing a `pipelinegen.yaml`. A key marked
"Accepted, not acted on yet" is checked when the file is read and then
used by nothing.

A config another commit has (the base a change is compared with, or the
commit a suite job tests) is read as history: a key this pipelinegen
doesn't know or no longer takes, and a rule the file breaks, become
warnings instead of errors. Only a config that doesn't say which suites
and test files it had can't be read; planning against such a base runs
every slice and says so. [Releases](../releases.md) has the rule for
retiring a key.

A complete file is [example/pipelinegen.yaml](../../example/pipelinegen.yaml);
this repository's own is [pipelinegen.yaml](../../pipelinegen.yaml).

## A change to this file

The planner judges a change to `pipelinegen.yaml` by what it means for
the tests, part by part, rather than as a changed file. It compares the
base commit's config with the one the run plans with; for a proposed
change that is the change's own with the
[trusted keys](../how-to/secrets.md#what-a-proposed-changes-config-cant-change)
in it, so an edit of a suite's steps or a driver's entry selects nothing
in the pull request's run, which runs the default branch's, and selects
in the default branch's run once it is merged.

| What changed | What runs |
|---|---|
| Comments, layout, key order, a default written out | nothing |
| A suite's run step (its driver, `with`, `batch`, or `take` moving a slice to another step) | the slices that step takes, in either config, with the reason "suites.<suite>.steps[<i>] (run <driver>), which takes it, changed" |
| A suite's prepare or set-up step, `params`, `image`, `secrets`, `privileged`, `stack`, `prepare` or `run_slice`, or a run's `params` or `secrets` for it | every slice of the suite (a run's own settings only in that run) |
| A driver entry's `config` block | the slices of every step that runs the driver, as for its `with` |
| A whole-suite check's entry | that check |
| A new suite | every slice of it |
| A driver entry's pin (`release`, `commit`, `repository`) | the slices its steps run, as a changed plugin entry: from level 4 all of them; below it, while `plugin-entry` is on, each suite's sample of its recorded slices, and every slice without a list |
| An sbom's or correlator's pin or `config` block | what the entry reaches, as [plugins](../how-to/plugins.md) says |
| What selects (`tests`, `tag`, `exclude`, `owner`, `extra_deps`, `text_inputs`, `sample`, `sample_on`, `narrowing`, `narrowings`, `follow`, `untested` and the like) | what either config selects: the change plans under both and runs the union, so it never deselects its own tests; a slice the head's config adds to a suite (a test file a pattern now matches) runs as a new slice, with the reason "the configuration adds it to the suite" |
| Where results go, what is kept, how slices are dealt into batches and share machines (`github`, `git`, `store`, `keep`, `writes`, `connectors`, a `plugins` entry of kind `connector`, `limits`, `parallel`, `batches`, `batch_minutes`, `run_on`, a lock's name, slots and weight, the runs' triggers and reports) | nothing: no test can observe them |
| Anything else: a time limit (`minutes`, a correlator's `collect_seconds`), a suite's `correlators`, the runner's entry or its placement, `bazel`, `concourse.include`, the pipelinegen pin in `concourse.planner.bootstrap`, a key this pipelinegen doesn't read in the base | the config is a changed file, as below |

The slices a step's change selects run at every narrowing level, and no
sample leaves them out. When the planner understood every part that
changed, the config is no changed file of its own: it is no `sample_on`
file, and no file no target owns. A Bazel target that lists it among its
inputs (a filegroup an audit test depends on) still sees it change, and
selects as for any file, since a test that declares the file reads it.

When some part isn't understood, the config is a changed file, besides
what the parts it understood select: under `everything_on` every slice
runs, under `sample_on` each suite runs its sample, through a target that
holds it what the target reaches runs, and when nothing names it every
slice runs, as for any file no target owns. A config under `untested`
then selects nothing by itself. `everything_on` is read before the
config's parts are: a config listed there runs every slice on any change
to it, as it says.

The plan records each changed part by its key path with what it means
(its `config`), and whether every part was understood
(`config_understood`); `pipelinegen why` names the part that selected a
slice, "the config: suites.lint.steps[1] (run shell), which takes it,
changed".

## Top level

| Key | Type | Default | What it does |
|---|---|---|---|
| `pipeline` | string | (required) | The name of the repository's pipeline on Concourse, and of its job links. |
| `everything_on` | list of patterns | none | Files whose change selects every slice of every suite, with the reason "`<file>` changed (everything_on `<pattern>`)". List the build setup here: `MODULE.bazel`, `.bazelversion`, CI task files. The config needs no entry: the planner judges a change to it by what it means, and what it doesn't understand counts as a changed file ([a change to this file](#a-change-to-this-file)). Listed here, it runs every slice on any change. |
| `untested` | list | none | Groups of files no test covers, each with its reason: the owner's declaration that no test needs to run when one changes. `pipelinegen check` and planning read it. A changed file that an entry matches and no Bazel target owns selects nothing by itself, and the plan's warnings say `<file>: untested by declaration: <reason>`. A file a target owns is selected through its owner, listed or not. When a slice's recording, `extra_deps` or `pipelinegen:data` units show that a test reads a listed file, planning treats the entry as wrong: with `uncovered: fail` the run fails, otherwise a warning names the file and the slices, and they are selected through what they read. |
| `untested[].paths` | list of patterns | (required) | The files of the group. |
| `untested[].reason` | string | (required) | Why no test covers them. |
| `macros` | mapping | none | What a macro of another repository reads in the package that calls it, keyed `"@repo//pkg:file.bzl%name"`. The check can't see inside such a macro: when it globs files itself instead of taking them as an argument, declare its patterns here and every call covers its package's matches. [The check](check.md#a-macro-from-another-repository) prints each declaration with what it covered. Planning holds each call against what Bazel says its targets read: a declared file they don't read fails the plan, a file of the package they read that the declaration leaves out is a warning. |
| `macros.<name>.reads` | list of patterns | (required) | Glob patterns relative to the calling package, as the macro's own `glob()` takes them. |
| `macros.<name>.reason` | string | (required) | What the macro does with them. |
| `uncovered` | `fail` or `everything` | `everything` | What planning does when the check finds a file bound to no slice: fail the run, or run every slice with a warning. A file an sbom plugin claims is covered: planning asks the sboms' claims whenever the check finds such a file. A plan that runs every slice for another reason (no base, `select: all`, a changed file `everything_on` names) keeps that reason, and says the files no slice covers among its warnings; under `fail` they still fail it. |
| `follow` | `executed` or `dependents` | `executed` | How far a change is followed in recorded suites; see [concepts](../concepts/the-graph-and-the-walk.md#how-far-the-walk-goes). `executed` runs the test files whose recorded code, or what that code needs, changed: the fewest runs. `dependents` also runs those that executed anything reaching the change through calls or uses; on a server, where the router reaches everything, that is often the whole suite. Suites and runs may override it; at `narrowing: 5` every suite follows dependents. |
| `follow_depth` | integer | 0 (no bound) | How many steps back from the change `dependents` follows: 1 takes the change's direct users only. Fewer steps run fewer slices and miss more distant callers. |
| `narrowing` | integer 1 to 5 | `3` | How much a change's selection is narrowed: 1 narrows most, 5 least. Each level switches narrowings off, the plugins' own and the core's sample; see [narrowing](../how-to/narrowing.md). The plan uses the highest level of the config and its suites. |
| `narrowing_alarm` | integer | `2` | How many nightly failures traced to narrowed changes make the nightly's report recommend a higher level. |
| `narrowings` | map | none | Sets, by name, the level one narrowing is off from, in place of the level its plugin (or the core) declares; see [narrowing](../how-to/narrowing.md#setting-one-narrowings-level). Only a narrowing a configured plugin declares, or one of the core's, may be named: planning refuses another. Plans, `why` and the nightly's alarm say the level came from the config, beside the declarer's. |
| `narrowings.<name>.off_at` | integer 1 to 5 | required | The level the narrowing is off from: it is in force below it. It may raise or lower the declarer's level. |
| `narrowings.<name>.note` | string | required | Why the narrowing holds up to that level in this repository. |
| `rebuild_check` | `error` or `warning` | `error` | What happens when the nightly's cold rebuild of the graph differs from the cache. A run that selects every slice and records (the nightly) asks every sbom and correlator plugin about every file and set again, without reading the cache, and compares the answers with the cached ones. A difference means a plugin read something it didn't declare as an input, or the cache's keys missed a change. With `error` the run's status turns red and the plan lists each difference; with `warning` they are listed as warnings, for a repository whose plugins don't keep to their declared inputs yet. A rebuild that can't finish counts the same way. The fresh answers replace the cached ones after they are reported. Runs of pull requests never rebuild. |
| `explain_limit` | integer | `25` | How many times a plan asks bazel-diff to explain a change, one process each: only for slices the plugins' bindings can't explain on their own (a slice bound by the Bazel graph, a tagged target, a recorded slice selected through a label it depends on). Slices selected through the same label share one explanation, so a whole suite selected through one label costs one. Past the limit a slice's reason says so, and `pipelinegen why` explains it (`--run <run> --change <change> --commit <commit> --suite <suite> --slice <slice>`); the plan's log says once how many slices were left. 0 asks for none. It changes reasons only, never what runs. |
| `sample_on` | list of patterns | none | Files every test exercises the same way (task files, shared setup): a change to one runs a sample of every suite (the config itself only for a change the planner doesn't understand whole; see [a change to this file](#a-change-to-this-file)), `suites.<name>.sample` slices spread over it, with the reason "`<file>` changed (sample_on `<pattern>`)". Which slices the sample must keep depends on what else changed. When every file the change touches matches a suite's `sample_on`, the slices it reaches through those files are the sample's to stand for, those that list such a file among their Bazel inputs (in a filegroup, say) included: the sample keeps only the slices whose own test files changed and those a changed plugin entry reaches, and picks the rest. When the change touches any other file too, the sample keeps every slice the change reaches, through any file. The kept slices count toward the `sample` slices, since the file reaches them like every slice of the suite: a change that already reaches that many runs nothing more. A file whose readers must run on every change to it doesn't belong here, even when they declare it as an input: put it in a Bazel target its readers depend on, and in neither list. It acts at every narrowing level. A file listed here needs no Bazel target and counts as covered. `everything_on` wins where both match. When the change also moves a plugin's entry, the suite still runs one sample, and where `plugin-entry` is off (levels 4 and 5) the slices the entry reaches run on their own beside it, as slices the change selects. |
| `limits` | mapping | | The time limits, in minutes, of the steps that aren't a suite's own tasks. Every step of the pipeline has one: a step that hangs ends, and says which limit it reached and which key raises it, instead of holding its run pending. A suite's own tasks take `suites.<name>.minutes`. |
| `limits.plan` | integer | `120` | Each task of a run's planning job and of its sweep: the planner's prepare command and the plan together. A continuous run's planning task gets its `wait_minutes` on top, for the wait for the round in progress, which comes before the prepare; the prepare and the plan have `limits.plan` from the wait's end. |
| `limits.steps` | integer | `20` | Each of pipelinegen's own steps in a suite's job (the tree and the batches, each batch's end, the reports, a lock's release) and the pipeline job's. A step that waits for a lock gets the lock's wait, 4 hours, on top. |
| `generated` | list of mappings | none | Files other generators write, for the check to compare. Its keys belong to the check. Accepted, not acted on yet. |

Patterns are paths relative to the repository's root. `*` and the other
forms of Go's `path.Match` stay inside one directory, `**` stands for any
number of directories (none included), and a pattern ending in `/` matches
everything below that directory.

```yaml
pipeline: example
everything_on: [MODULE.bazel, .bazelversion, "ci/**"]
untested:
  - paths: [.gitignore]
    reason: repository settings
```

## bazel

How to run Bazel in the repository.

| Key | Type | Default | What it does |
|---|---|---|---|
| `bazel` | mapping | | The section. |
| `bazel.workspace` | path | `.` | The Bazel workspace's directory, relative to the repository's root. |
| `bazel.bazelisk` | string | `bazelisk` | The command that runs Bazel: a name on `PATH`, or a path. A relative path with a `/` is taken from the repository's root. |
| `bazel.startup_flags` | list of strings | none | Flags that go before every Bazel command, such as `--output_user_root=/some/dir` to keep Bazel's output in a place of your own. |

```yaml
bazel:
  workspace: .
  bazelisk: tools/bazelisk
  startup_flags: ["--output_user_root=/var/cache/bazel-ci"]
```

## bazel_diff

Which bazel-diff release to run. pipelinegen pins one and downloads it into
its cache, checking its checksum; set these only to run another.

| Key | Type | Default | What it does |
|---|---|---|---|
| `bazel_diff` | mapping | | The section. |
| `bazel_diff.version` | string | `v67.0.3` | The release's tag. Give `version` and `sha256` together. |
| `bazel_diff.sha256` | string or mapping | the pinned release's, for `linux-amd64`, `linux-arm64` and `darwin-arm64` | The SHA-256 of the binary by platform (`linux-amd64: ...`), since bazel-diff publishes no checksum list; one string is the checksum for whichever platform runs it. A download with another checksum fails, and a platform with no checksum is named. bazel-diff publishes no build for Intel Macs: there, build it and pass `--bazel-diff`. |
| `bazel_diff.url` | string | GitHub's release URL | Where to download from: a pattern whose first `%s` is the version and second the asset's name (`bazel-diff-rust-linux-amd64`, `bazel-diff-rust-macos-arm64` on a Mac: bazel-diff names macOS `macos`). For a mirror. |

```yaml
bazel_diff:
  version: v67.0.3
  sha256:
    linux-amd64: 98d71d5ffc6e03dbe14f6b7f7b132bb6c5704b189065b3fa45915bad001e8e4e
    linux-arm64: 8a3235f10b5823400d7757a390df466637837a167a6bbcc6b0d10502123b29db
    darwin-arm64: 649e623a05ddf8008f069983de153dc8bc524aec70a17d6035b926751b0cfbed
  url: https://mirror.example.com/bazel-diff/%s/%s
```

## github

Where runs report and where pull requests are listed. A pipeline of a
config with runs needs this section or `git.remote`. The config may declare
runs before it has either: `check` and `affected` work on it, and
`pipelinegen pipeline` names what is missing.

| Key | Type | Default | What it does |
|---|---|---|---|
| `github` | mapping | | The section. |
| `github.repository` | `owner/name` | (required in the section) | The repository the statuses and comments go to. |
| `github.token` | string | (required in the section) | The name of the Concourse var that holds the token, never the token. pipelinegen's own steps get it as `GITHUB_TOKEN`; a task of the repository's gets it only where `secrets` declare it. |
| `github.tokens` | mapping | every purpose takes `github.token` | The token split by purpose, each the name of a Concourse var, so a step holds only a token for what it does. A purpose left out takes `github.token`; with no `github.token`, all four are needed. pipelinegen's steps then read each from `PIPELINEGEN_<PURPOSE>_TOKEN` and never from `GITHUB_TOKEN`. |
| `github.tokens.read` | string | `github.token` | Fetches the code: the repository's commits, and private modules a task downloads. The only token a task that runs the change's code may get, so a read-only token here keeps write tokens away from it. |
| `github.tokens.changes` | string | `github.token` | Lists pull requests and reads their state. |
| `github.tokens.report` | string | `github.token` | Sets commit statuses and the pull request's comment. |
| `github.tokens.store` | string | `github.token` | Writes the records, the plugins' answers and the locks. |
| `github.status_prefix` | string | `pipelinegen` | The start of every commit status's context: `<prefix>/<run>/<suite>` per suite and `<prefix>/<run>` for the whole run. |

```yaml
github:
  repository: owner/example
  token: github_token
  tokens: {read: github_read_token}   # a read-only token for what only reads
```

## git

The repository's own remote, for a repository whose host pipelinegen
doesn't report to. Without a `github` section, the runs need it: the
records and locks live in this remote, nothing is posted anywhere, and the
CI system's own job results are the report. Branch, timer and manual runs
work this way; `pipelinegen pipeline` refuses `pull_requests` triggers,
`report.status: true` and a lock's own `repository` without the `github`
section. A local `affected` without either reads the recordings from the
checkout's `origin`, and with no `origin` reads none and says so.

| Key | Type | Default | What it does |
|---|---|---|---|
| `git` | mapping | | The section. |
| `git.remote` | URL | the `github` repository | The repository's git URL: `https`, `http`, `git`, `file` or `ssh` (also the scp form, `git@host:path`), with no password or query in it. A plain git server will do: pipelinegen asks it for nothing beyond what stock git serves. |
| `git.ssh_key` | var name, or mapping | | For an ssh remote: the name of the CI var holding the private key, never the key. One var serves both purposes; `{read: <var>, store: <var>}` gives each its own. Required with an ssh remote. |
| `git.ssh_key.read` | var name | | The key that fetches the code. |
| `git.ssh_key.store` | var name | | The key that writes the records and locks. |
| `git.known_hosts` | list of strings | | For an ssh remote: the server's host keys, as `known_hosts` lines. Required: a host key that isn't listed fails the fetch, and none is ever accepted on first use. |

```yaml
git:
  remote: https://git.example.com/group/example.git
```

Not in a release yet for pipelines: `pipelinegen pipeline` refuses an
ssh remote until the runner can hand its steps the key without the CI
system's own git resource. `affected`, `why` and `status` already reach
one, with your own ssh setup.

With an ssh remote, a pipeline step finds each purpose's key in
`PIPELINEGEN_SSH_KEY_READ` or `PIPELINEGEN_SSH_KEY_STORE`. It writes the
key to a file only its user can read, in a directory of its own outside
the work tree, and removes it when the step ends, fails or is stopped. git
then runs ssh with that key alone: no agent, no password, none of the
user's ssh config, and strict checking against `git.known_hosts`. Without
those variables (on your machine) git uses your own ssh setup. The safer
set-up is a read-only deploy key for `read` and a key that may write for
`store`, since a step that runs a proposed change's code gets the `read`
key at most. Both keys move to the store's own config once the store is a
plugin.

```yaml
git:
  remote: ssh://git@git.example.com/group/example.git
  ssh_key: {read: example_read_key, store: example_write_key}
  known_hosts:
    - git.example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA...
```

On a `last_passed` run, the base is the newest earlier commit whose run
passed. With a host, that is read from the commit statuses; without one,
from the run's records in this remote. The two can differ: the records of
runs older than `keep.runs` are gone while a status stays, and a run that
passed but couldn't attach its lists shows an error status while its
records say it passed.

That base is the run's, and while the `passed-since` narrowing is on
(levels 1 to 3) each slice may compare from a newer commit: the newest
earlier commit at which it passed in this run with its tests run, read
from the run's records in the store, so only within `keep.runs`. The plan
finds what changed from the newest two such commits to the head, and
leaves out a slice that passed at one of them when nothing from there
reaches it; `pipelinegen why` says "compared from `<commit>`, where it
last passed". A slice with no such pass keeps the run's base. See
[narrowing](../how-to/narrowing.md#slices-that-passed-since-the-base).

## connectors

Not in a release yet. `pipelinegen pipeline` builds a run that takes
changes from a connector only with a runner that can hand them on (one
that describes the `connector_changes` capability), and refuses it
otherwise. `affected`, `why` and `try` plan with it too.

Where the code and its proposed changes live, when that isn't the `github`
section. The first connector is the code's. The one there is today is
`git`, the [git connector](https://github.com/sideways-works/pipelinegen-connector-git),
for a plain git server with no host API: its proposed changes are the
branches whose names match `changes.branches`. pipelinegen lists the
remote's branches with git and the read credential, and the connector
picks the changes among them. A run takes them with a `changes`
trigger. Plain git has no drafts, labels or ready-for-review times, and
nowhere to post a status or a comment. So the config refuses a run that
filters on `draft`, `labels`, `labels_without` or `once_per_ready`, or
asks for `report.comment` or `report.status`, when its changes come from
`git`. The run's state is in its records, which `pipelinegen status` reads.

| Key | Type | Default | What it does |
|---|---|---|---|
| `connectors` | list | none | The connectors. The `github` section and `connectors` don't go together. |
| `connectors[].name` | string | (required) | The connector's name: lower case letters, digits and dashes, unique. Change ids carry it. |
| `connectors[].plugin` | `git` | (required) | The connector: `git`, the plain git connector, pinned by pipelinegen or by a `plugins` entry named `git`. |
| `connectors[].remote` | URL | `git.remote` | The repository's git URL, as `git.remote` takes it. |
| `connectors[].changes` | mapping | | Which of the remote's branches are proposed changes. |
| `connectors[].changes.branches` | list of patterns | (required for `git`) | The branches that are proposed changes, such as `review/*`. There is no default: every branch of a server with many personal ones would otherwise run. |
| `connectors[].links` | URL | none | A web page for a commit, with `{commit}`, `{branch}` or `{path}` filled in: a cgit or gitweb page. |

```yaml
git:
  remote: https://git.example.com/group/example.git
connectors:
  - name: git
    plugin: git
    changes: {branches: ["review/*"]}
runs:
  review:
    trigger: {changes: {base: main}}
```

### Pinning a connector

Not in a release yet. Each connector is a plugin of its own, which
pipelinegen runs: the GitHub connector
([pipelinegen-connector-github](https://github.com/sideways-works/pipelinegen-connector-github))
for the `github` section, and the git connector
([pipelinegen-connector-git](https://github.com/sideways-works/pipelinegen-connector-git))
for `connectors` entries of plugin `git`. pipelinegen pins a release of
each, with its checksum for every platform, and fetches the one a config
uses the way it fetches [bazel-diff](#bazel_diff): a config needs no line
for it. A generated pipeline's bootstrap fetches it with the other plugins
into `tools/plugins/<name>`, with the read token; any other machine
downloads it from the repository's GitHub release into pipelinegen's cache
the first time a command talks to the host. A binary that doesn't match
its pin never runs.

A `plugins` entry of kind `connector`, named `github` or `git`, pins
another release:

```yaml
plugins:
  - name: github
    kind: connector
    repository: sideways-works/pipelinegen-connector-github
    release: {version: v0.1.0, sums: ...}
```

The entry takes `release` (`sha256` for one platform, or `sums`) and no
`config`: where the API is comes from the step's environment
(`PIPELINEGEN_GITHUB_API`), and the code's remote and branches from the
trusted config. Like every plugin entry, a proposed change can't add one or
change its pin.

## concourse

What the generated pipelines need from Concourse. A pipeline of a config
with runs needs it; `check` and `affected` don't. A section once begun is
checked whole.

| Key | Type | Default | What it does |
|---|---|---|---|
| `concourse` | mapping | | The section. |
| `concourse.url` | URL | `PIPELINEGEN_CONCOURSE_URL` from the environment | Concourse's web address, for links to builds. Leave it out to keep the host out of the repository's files; then `pipelinegen pipeline` (by hand, or the pipeline job's steps) needs it in `PIPELINEGEN_CONCOURSE_URL`, and refuses to write a pipeline without either. No other command reads it, batch tasks included. |
| `concourse.team` | string | `main` | The team the pipelines live in. |
| `concourse.repository` | mapping | (required for a pipeline with runs) | The `source` of the git resource that fetches the code. pipelinegen sets its `branch` per run; everything else (`uri`, credentials) is yours. |
| `concourse.vars` | list of strings | (going away) | No longer read, and accepted with a warning that it will be refused: `pipelinegen pipeline --vars` lists the vars a pipeline needs. |
| `concourse.include` | path | none | A pipeline file of your own whose `resource_types`, `resources`, `groups` and `jobs` are merged into the generated pipeline (a release job, checks of the commit message). |
| `concourse.branch` | string | `main` | The default branch: the `pipeline` job runs when the config changes there, and checks the pipeline (or sets it, with `self_set`). |
| `concourse.pull_requests` | mapping | the maintained fork of telia-oss/github-pr-resource | The image (`registry-image` source) of the pull request resource type the `pull_requests` triggers use. |
| `concourse.mock` | string | `bundled` | Whether the Concourse has its bundled `mock` resource type, which a runner hands a connector's changes on with: `bundled`, or `missing`, which refuses a pipeline whose runs take a connector's changes before it is written. `fly -t <target> workers -d` lists each worker's types. |
| `concourse.self_set` | bool | false | Lets the `pipeline` job set the pipeline when the config changes. Off, the job only regenerates the pipeline and fails when it differs from the one set ("the pipeline is stale: set it"), and a person sets it. On, the job sets it with secrets kept as `((name))` references, never their values, which works only when the Concourse resolves them itself (a credential manager or `var_sources`); with plain `fly set-pipeline -v` variables, leave it off. |
| `concourse.serial_groups` | | | No longer read: no job is in a serial group. `check` names it; remove it. |
| `concourse.serial_groups.<name>.per_run` | | | No longer read, with `concourse.serial_groups`. |
| `concourse.planner` | mapping | | The image pipelinegen's own steps run in, and the tools they fetch. |
| `concourse.planner.image` | mapping | (required with runs) | A `registry-image` source of a stock image with git and certificates, such as `{repository: golang, tag: 1.27.1}`. |
| `concourse.planner.digest` | string | (required with runs) | The image's digest, `sha256:...`, so the image can't change under you. |
| `concourse.planner.params` | mapping of strings | none | Plain environment for the planner's prepare command only, set in its own shell, never for `pipelinegen plan`. The same rules as a suite's `params`. |
| `concourse.planner.secrets` | mapping of strings | none | `{ENV_NAME: var_name}` for the planner's prepare command only. They must be read-only credentials (a module or registry token): in a pull request run the prepare command shares its task with planning the change's own code, which never sees them but runs where they were. |
| `concourse.planner.git_token` | string | none | One `ENV_NAME` of `concourse.planner.secrets` that git authenticates with inside the prepare command, through a credential helper, never in a URL. |
| `concourse.planner.prepare` | shell command | none | Runs in the checkout before planning, to fill the caches the plugins read: `GOMODCACHE`, `GOCACHE` and `npm_config_cache` point into the planning job's cache, where the plugins find them (`go mod download`, say). It may use the network; plugins never do. |
| `concourse.planner.prepare_image` | string | none | A `registry-image` resource of the root pipeline file, by name, that the prepare command runs in, for a private image a task file can't name. Left out, the planner's image. `check` fails as for a suite's `image`. |
| `concourse.planner.bootstrap` | mapping | | What each job's first step fetches into its `tools` output. |
| `concourse.planner.bootstrap.repository` | `owner/name` | `sideways-works/pipelinegen` | Where pipelinegen's releases come from. The token must be able to read it. |
| `concourse.planner.bootstrap.release` | mapping | | A released pipelinegen. Give either `release` or `commit`. |
| `concourse.planner.bootstrap.release.version` | string | | The release's tag, `v1.2.3`. |
| `concourse.planner.bootstrap.release.sha256` | string | | The checksum of `pipelinegen-linux-amd64` from the release's `SHA256SUMS`; see [releases](../releases.md). Give this or `sums`. |
| `concourse.planner.bootstrap.release.sums` | string | | The checksum of the release's `SHA256SUMS` itself, in place of `sha256`: the bootstrap checks that file by it and each platform's binary by its line there. |
| `concourse.planner.bootstrap.commit` | string | | A full 40-character commit of pipelinegen to build instead, before a release exists. |
| `concourse.planner.bootstrap.bazelisk` | mapping | v1.30.0 | The bazelisk release the steps fetch. Releases up to v0.2.2 require it; later ones fetch v1.30.0 when it is left out. |
| `concourse.planner.bootstrap.bazelisk.version` | string | | bazelisk's tag, `v1.30.0`. |
| `concourse.planner.bootstrap.bazelisk.sha256` | string | | The checksum of its linux-amd64 binary. |

```yaml
concourse:
  url: https://concourse.example.com
  team: example
  repository:
    uri: https://github.com/owner/example.git
    username: x-access-token
    password: ((github_token))
  planner:
    image: {repository: golang, tag: 1.27.1}
    digest: sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2
    bootstrap:
      release: {version: v1.2.3, sha256: <the linux-amd64 line of SHA256SUMS>}
      bazelisk: {version: v1.30.0, sha256: a248ae2b2b62b5baebb748f6fba7203bff1a27e5d01b55399a8406744cb539e8}
```

## suites

One entry per suite, by name. A suite's name is lower case letters, digits
and dashes, starting with a letter.

| Key | Type | Default | What it does |
|---|---|---|---|
| `suites` | mapping | (at least one) | The suites. |
| `suites.<name>.tests` | list of patterns | none | A suite of tests: every tracked file that matches and holds a test of its own is a slice, named by its path, except in directories Bazel ignores, where no target can run it. Give `tests` or `tag`; with both, the tag wins. Planning walks the stored graph from what changed to each test file, so a change runs only the files it reaches, with no setup lines in the repository ([fresh clones](../how-to/fresh-clones.md)). |
| `suites.<name>.exclude` | list of patterns | none | Files the `tests` patterns match that belong to no slice of this suite. In a suite with a `tag`, the tagged targets whose package directory (or a file in it) matches are no slices either: `**/testdata/**` leaves out fixtures. |
| `suites.<name>.helpers` | list of patterns | none | Files the `tests` patterns match that hold no test of their own (shared steps, fixtures): inputs of the slices that use them, never slices. Go test files need none: a file without a top-level `Test...` function is a helper. |
| `suites.<name>.owner` | label pattern | the test targets that list the file | The target that holds a test file's graph edges (the app it opens, the files it reads), with `{dir}`, `{file}`, `{base}` and `{stem}` filled from the file's path, such as `//{dir}:{stem}`. A label in Bazel's short form, `//cmd/app`, names the target of the package's own name, `//cmd/app:app`, as it does in Bazel. In a recorded suite only the owner's `data` counts, since the recording binds the code. In a suite bound by the graph, a test file without an owner that no target lists, no `extra_deps` entry names and no `pipelinegen:data` line binds is refused by `pipelinegen check`; a plan that meets one anyway selects it on every change. |
| `suites.<name>.extra_deps` | list | none | More targets some test files depend on, besides their owner and the `pipelinegen:data` comments in them. |
| `suites.<name>.extra_deps[].tests` | list of patterns | | The test files. |
| `suites.<name>.extra_deps[].deps` | list of labels and unit ids | | What they depend on: targets such as `//:docs`, or units by the ID an sbom plugin lists them under, such as `go:app#func:Start`. A unit binds the test files to it and to what links reach from it. |
| `suites.<name>.text_inputs` | list | none | Files some test files of a recorded suite read as text, not only run (a test file's own `pipelinegen:data` lines count as such a declaration for the labels they name): a documentation check that follows references in comments, a golden test over source files. For them a change to comments or layout is observable, so `unobservable-edits` never leaves them out for such a change and `by_units` never narrows the file for them. Declare such tests before taking a release of an sbom that says its units cover files whole; see [narrowing](../how-to/narrowing.md#files-that-changed-nothing-observable). |
| `suites.<name>.text_inputs[].tests` | list of patterns | every test file of the suite | The test files that read them. |
| `suites.<name>.text_inputs[].paths` | list of patterns | (required) | The files they read as text. |
| `suites.<name>.durations` | path | none | The suite's durations file (seconds per test file), by which a run deals its slices into batches; see [durations](../how-to/durations.md). A file missing from it counts as the median. |
| `suites.<name>.group` | regular expression | the leading word of the file's name | Names a slice's group in the report: the expression's first group, matched against the file's path. |
| `suites.<name>.tag` | string | none | A suite of other checks (a lint, a build): every Bazel target carrying this tag is a slice, except in directories Bazel ignores (`.bazelignore`, `ignore_directories` in `REPO.bazel`), which `pipelinegen check` skips as Bazel does. No two suites share a tag. |
| `suites.<name>.run_slice` | path | (required without `steps`) | The older form of running a suite, without drivers: the suite's batch task, which sets up once and runs a batch's slices in turn; see [writing tasks](../how-to/writing-tasks.md). |
| `suites.<name>.prepare` | path | none | A task file run once per build of the suite before its batches: builds, installed packages. Its `prepared` output reaches every batch. |
| `suites.<name>.steps` | list | none | Runs the suite by drivers instead of task files: prepare steps, then set-up steps, then run steps, each naming one driver, and at least one run step. A suite has `steps` or `run_slice` and `prepare`, never both. The steps and their settings of a proposed change's run come from the trusted config. See [drivers](../concepts/drivers.md). |
| `suites.<name>.steps[].prepare` | string | none | A driver that builds once per build of the suite, in the suite's prepare task (`pipelinegen slice prepare`), into the `prepared` directory every batch reads. Only what doesn't depend on the container belongs there: a static binary, never `node_modules`. |
| `suites.<name>.steps[].setup` | string | none | A driver that starts what a batch needs before its slices run, once per batch; what it exports reaches the later steps and their tests by name. It is stopped when the batch ends, whatever happened, the last started first. |
| `suites.<name>.steps[].run` | string | none | A driver that runs the slices routed to it (`pipelinegen slice run`), a plugin entry of `kind: driver` or the built-in `command`. |
| `suites.<name>.steps[].take` | list of patterns | every slice, on the suite's only run step | The slices a run step takes: test files by path pattern, targets by `kind:<rule kind>` or a label pattern. Every slice is taken by exactly one run step, and only by a driver whose routes take it; `pipelinegen check` names the slices that aren't. |
| `suites.<name>.steps[].batch` | boolean | `false` | Bundles the slices of a batch: the run step's driver gets them in one call and runs them together, as its tool allows; off, the driver is called once per slice. Every driver takes both, and results and recordings per slice are the same either way. See [bundle a suite's slices, or run each alone](../how-to/batching.md). |
| `suites.<name>.steps[].image` | string | the suite's `image` | On a prepare step only: the image its task runs in, for a build that needs another toolchain than the tests. Set-up and run steps share the batch task's container. |
| `suites.<name>.steps[].with` | mapping | none | The driver's own settings, over its plugin entry's `config`. `pipelinegen check` refuses a key the driver doesn't declare and a value of another type; for a driver that validates its settings, such as the command driver, whatever the driver refuses at any depth, by its place (`suites.<name>.steps[<i>].with.checks[2].timout`). |
| `suites.<name>.image` | string | none | A `registry-image` resource declared in the root pipeline file (`concourse.include`), by name. The suite's prepare, batch and whole-suite tasks run in it, and the suite job gets it first; pipelinegen's own steps keep the planner's image. Left out, each task file's own image. `check` fails when the root file doesn't declare it as a `registry-image`. |
| `suites.<name>.run_on` | mapping | | How builds of different changes share the suite's jobs. Where they run is the runner's placement (see [Where steps run](#where-steps-run)); `run_on.tags` is refused. |
| `suites.<name>.run_on.serial_group` | | | No longer read: a suite's builds run side by side. `check` names it; remove it. |
| `suites.<name>.run_on.max_in_flight` | | | No longer read: a suite's builds run side by side. `check` names it; remove it. |
| `suites.<name>.params` | mapping of strings | none | Plain environment for the suite's prepare, batch and whole-suite tasks; a run's params for the suite are merged over these. No secrets: a value containing `((` is refused, and so is a key starting with `PIPELINEGEN_` or one a secret of the suite also sets. |
| `suites.<name>.correlators` | list of plugin names | every correlator entry | The correlators that observe the suite's slices: only they are asked to collect and observe in its batches, and only their "can't tell" marks a slice. A claim the repository makes: a change to what only an unlisted correlator would have seen doesn't select the suite's slices (backend tests that run no frontend code leave the frontend's correlator out). It also bounds what a change of the correlator's entry reaches: the recorded slices of the suites it observes, and in any suite only the slices whose binding holds the near end of a link it gave or a unit of a file it claims. Bind correlators still bind every slice. |
| `suites.<name>.secrets` | mapping | none | Concourse vars for one kind of the suite's tasks: `{prepare: {ENV_NAME: var_name}, batch: {...}, whole: {...}}`. Only that kind of task gets them; see [contracts](../contracts.md#params-and-secrets). |
| `suites.<name>.secrets.prepare` | mapping of strings | none | The prepare task's secrets. |
| `suites.<name>.secrets.batch` | mapping of strings | none | Every batch task's secrets. A batch needs no token of its own; declaring one shows its slices' progress live, and its tests see it. |
| `suites.<name>.secrets.whole` | mapping of strings | none | The whole-suite checks' secrets. |
| `suites.<name>.parallel` | integer | `4` | How many batches a build of the suite is dealt into, and so how many run at once: every batch of a build runs at the same time. `batches` and `batch_minutes` deal more. |
| `suites.<name>.batch_minutes` | integer | none | Deals the selected slices into batches of about this many minutes each, by the durations file (a slice without a recorded time counts at the median of those with one): the total time divided by `batch_minutes`, rounded up, but never fewer batches than `parallel` and never more than there are slices; with `batches` set, at most that many. Every batch runs at once, so without `batches` a run of everything runs as many batches at once as its time asks for: `pipelinegen check` warns of it, and `batches` bounds it. A run of everything gets many small batches, a change of a few slices few: five slices of two minutes with `parallel: 2` make two batches, 585 minutes with `batch_minutes: 18` make 33. |
| `suites.<name>.minutes` | integer | `120` | The suite's time limit, which needs no lock. Each of its tasks has it on its own: the prepare, each batch and each whole-suite check, so a batch that hangs ends while its other batches go on, and the suite's total is bounded by its batches times this, and by Concourse's own settings for the job. A suite whose lock is taken per build has it as one limit for all its work while it holds the lock, which the lease covers. `batch_minutes` is what a batch is dealt to take, never a limit; `minutes` must be at least `batch_minutes`. A batch its limit stopped errors its open slices with the limit and this key; see [troubleshooting](../troubleshooting.md#a-step-reached-its-time-limit). |
| `suites.<name>.batches` | integer | the suite's `parallel` | How many batches a build deals the suite's selected slices into, balanced by their durations; at least `parallel`. Every batch of a build runs at once, so this is also how many run together: `batches: 12, parallel: 3` runs twelve at once. More, smaller batches lose less work when a worker goes away, since a batch writes its recordings when it ends, but each takes a worker's container. `pipelinegen check` warns when it is more than four times `parallel`, and when the batches take a lock one by one (`lock.per: batch`, or a continuous run) and the last could wait past the lock's 4 hours: every batch asks for a slot at once. |
| `suites.<name>.narrowing` | integer 1 to 5 | the config's | The suite's narrowing level; the plan uses the highest level of the config and its suites. |
| `suites.<name>.sample` | integer | the suite's `parallel` | How many slices a sampled change runs (by `sample_on`, by the sample narrowings at levels 1 and 2, or by a changed plugin entry at levels 1 to 3 while `plugin-entry` is on). A slice counts toward it when the sampled change reaches it: the slices a `sample_on` file's change or a sample narrowing's change selects are part of the sample, so a change that selects that many already runs no more. A changed plugin entry's sample is drawn from the recorded slices the entry reaches, and only those of them that run anyway count: a slice that runs for another reason, one without a list say, tells nothing of the new entry, so the sample comes on top of it. A change that asks for a sample more than one way runs one sample of the suite. |
| `suites.<name>.sample_on` | list of patterns | none | Like the top-level `sample_on`, for this suite only: its own task files, say. |
| `suites.<name>.privileged` | boolean | `false` | Runs the suite's tasks privileged, for a suite that starts its own container engine. |
| `suites.<name>.stack` | boolean | `false` | The suite's batches run long-lived processes (a server, a worker) whose coverage only a correlator's `collect` can take. In a recording run, `slice setup-end` and `slice start` then fail when none of the correlators that observe the suite collects, and a collector that finds no process to ask fails the slice. |
| `suites.<name>.empty_recordings` | `allow` or `refuse` | `refuse` | What happens when a slice's tests ran and its correlators together saw nothing. With `refuse`, the recording counts as one they couldn't tell: the slice keeps its list, is marked, and is bound to everything until a recording tells again. With `allow`, the slice gets an empty list. |
| `suites.<name>.bind` | `graph` or `recorded` | `graph` | How the suite's slices are bound to code: the Bazel graph only, or also what they touched when they last ran. `recorded` needs sbom and correlator plugins, which read the recordings; a config with a recorded suite and neither is refused. A `graph` suite's test file depends on its own file and on the target that owns it, whose graph bazel-diff follows; bind correlators never see it. Planning binds through the plugins, none when none is configured. |
| `suites.<name>.follow` | `executed` or `dependents` | the top-level `follow` | This suite's width. |
| `suites.<name>.follow_depth` | integer | the top-level `follow_depth` | This suite's depth bound. |
| `suites.<name>.lock` | mapping | none | A lease the suite holds while it runs, for a machine shared with another repository; see [sharing machines](../how-to/sharing-machines.md). |
| `suites.<name>.lock.name` | string | (required in a lock) | The lock's name. Suites, and repositories naming the same `repository`, share a lock by name. |
| `suites.<name>.lock.repository` | `owner/name` | the code repository | The repository whose refs hold the lock. |
| `suites.<name>.lock.minutes` | integer | none | The old spelling of `suites.<name>.minutes`, read as it when that is unset; `check` and the plan step warn, and a config that sets both to different values is refused. A lease expires 15 minutes after the suite's time limit. |
| `suites.<name>.lock.slots` | integer | `1` | How many holders the lock admits at once. |
| `suites.<name>.lock.weight` | integer | `1` | How many of the lock's slots each holder of this suite takes at once, all or none: suites of different sizes share one lock of a machine (backend batches weight 1 on a four-slot lock, a suite that needs the whole machine weight 4). At most the lock's slots; suites that share a lock agree on its slots. A waiter that takes more slots than others leaves a mark, and lighter takers leave the slots that free to it, so it is never starved. |
| `suites.<name>.lock.per` | `build` or `batch` | `build` | What holds a slot: the suite's whole build, its batches side by side, or each batch (and each whole-suite check) on its own, in every run alike, so a slot is one batch. A continuous run takes it per batch either way. With `batch`, the suite needs no serial group for the machine the lock stands for. |
| `suites.<name>.whole_suite` | list | none | Checks that judge the suite as a whole; see [concepts](../how-to/adding-a-suite.md#whole-suite-checks). |
| `suites.<name>.whole_suite[].name` | string | (required) | The check's name. It reports as `whole.<name>`. |
| `suites.<name>.whole_suite[].task` | path | (required) | The check's task file. |
| `suites.<name>.whole_suite[].also_on` | list of labels | none | Bazel targets whose change selects the check even when not every slice runs. |
| `suites.<name>.whole_suite[].privileged` | boolean | `false` | Runs the check's task privileged. |

```yaml
suites:
  e2e:
    tests: ["e2e/**/*.spec.ts"]
    helpers: ["e2e/support/**"]
    owner: "//{dir}:{stem}"
    extra_deps:
      - tests: ["e2e/help/**"]
        deps: ["//help:pages"]
    durations: ci/durations/e2e.json
    bind: recorded
    image: playwright
    steps:
      - run: playwright
        with: {config: e2e/playwright.config.ts}
    run_on: {max_in_flight: 2}
    parallel: 3
  lint:
    tag: slice-lint
    run_slice: ci/lint.yml
    params: {GOFLAGS: -trimpath}
    lock: {name: linters, minutes: 10}
```

## runs

One entry per run, by name (lower case letters, digits and dashes). See
[declaring runs](../how-to/declaring-runs.md).

| Key | Type | Default | What it does |
|---|---|---|---|
| `runs` | mapping | none | The runs. |
| `runs.<name>.trigger` | mapping | (required) | What starts the run: exactly one of `branch`, `pull_requests`, `timer` or `manual`. |
| `runs.<name>.trigger.branch` | string | | Every new commit on this branch starts a run. With `timer` or `manual`, the branch whose tip they run on. |
| `runs.<name>.trigger.pull_requests` | mapping | | Open pull requests that match start a run for each new head. |
| `runs.<name>.trigger.changes` | mapping | | `pull_requests` by its host-neutral name, with the same keys and meaning: proposed changes from the code's [connector](#connectors) (a plain git server's review branches) or a host. Give one of the two. |
| `runs.<name>.trigger.pull_requests.base` | string | (required) | The branch the pull requests go into. |
| `runs.<name>.trigger.pull_requests.draft` | `true`, `false` or `any` | `false` | `false`: ready pull requests only; `true`: drafts only; `any`: both. A draft is work its author hasn't asked anyone to look at, so by default it isn't planned. Planning applies this and the other filters: a head that doesn't pass them is not planned. |
| `runs.<name>.trigger.pull_requests.labels` | list of strings | none | Labels a pull request must all carry. |
| `runs.<name>.trigger.pull_requests.labels_without` | list of strings | none | Labels a pull request must carry none of. |
| `runs.<name>.trigger.pull_requests.head` | pattern | any | A pattern for the head branch's name, such as `train/*`. |
| `runs.<name>.trigger.pull_requests.once_per_ready` | boolean | `false` | Tests a pull request once each time it becomes ready for review: a push to a ready pull request starts nothing until it goes back to draft and is marked ready again. |
| `runs.<name>.trigger.pull_requests.missed_after` | period | `10m` | How long an open head may go without a plan, or a planned head without any of its suite builds started (its start step run, which records it), before the run's sweep plans it or hands it to the suites again (`pipelinegen unplanned`); after each time the wait doubles, and after three the sweep leaves the head and says so on its run status. A build of the planning job that finds its head planned hands it to the suites again by the same wait (`plan --from`), a head the sweep left included, and starts no suite build for a head planned more lately. A head whose planning build began (`plan --begin`) is left to it until `limits.plan` and ten minutes have passed, or until that build ends without a plan, so this can stay at minutes even where a cold plan takes most of an hour. A suite build that waits for a worker has started nothing, so set this above the longest wait for a worker you expect: a head handed on again while its first build waits gets a second build, and both run every slice. A head planned or run twice costs a second run; one left waiting costs its author the wait. |
| `runs.<name>.trigger.pull_requests.sweep_every` | period | `1m` for a connector's changes, else the runner's | How often the run's sweep looks for changes and for heads its planning missed: a minute or more. Where no push starts the planning (a plain git server, say), it is how long a new change waits to be planned. A sweep costs one `ls-remote` of the code's remote and one read of the store. |
| `runs.<name>.trigger.timer` | mapping | | Starts the run once a day inside a window, on the tip of `trigger.branch`. |
| `runs.<name>.trigger.timer.start` | `H:MM` | (required) | The window's start. |
| `runs.<name>.trigger.timer.stop` | `H:MM` | (required) | The window's end. |
| `runs.<name>.trigger.timer.location` | time zone | UTC | The time zone of `start` and `stop`, such as `Europe/Berlin`. |
| `runs.<name>.trigger.timer.days` | list of day names | every day | The days the timer fires on, such as `[Monday, Thursday]`. |
| `runs.<name>.trigger.manual` | boolean | `false` | The run starts only by hand, on the tip of `trigger.branch`. |
| `runs.<name>.trigger.continuous` | boolean | `false` | Beside `trigger.branch` alone: every commit of the branch starts a build of the planning job, the builds side by side and never serial, and one round runs at a time: the build that decides waits while a round is in progress, and one whose commit is no longer the branch's tip runs nothing (superseded), so rounds follow each other, each of the newest commit. The runner must say it can build one. |
| `runs.<name>.idle_minutes` | integer | `limits.steps` + 2 × the largest suite `minutes` of the run + 10 | For a continuous run: how long a round may record nothing before the next build stops waiting for it, as dead. A batch records nothing until it ends, so the default covers a start step, a prepare and a batch. `pipelinegen end` ends a round you know is dead at once. |
| `runs.<name>.turn_minutes` | integer | none | No longer read; a config that sets it is warned. |
| `runs.<name>.wait_minutes` | integer | `480` | For a continuous run: how long a build waits for the round in progress to end. When it has waited that long, a build whose commit the branch moved past ends superseded; one still the newest fails, naming the round. |
| `runs.<name>.compare` | string | (required) | What the change is compared with: `merged_target` (the pull request merged into its base's tip, compared with that tip; pull requests only), `last_passed` (the newest earlier commit on the branch whose run of this name passed; with none, the newest whose run is still going, so only that one runs everything; while the `passed-since` narrowing is on, a slice that passed at a newer commit in this run is compared from there) or `none` (nothing: every slice runs). |
| `runs.<name>.select` | `affected` or `all` | (required) | Whether the run selects the slices the change reaches or every slice. |
| `runs.<name>.suites` | mapping | (at least one) | The suites the run covers. Its plan holds these alone, and its statuses, its comment and its end count only them. |
| `runs.<name>.suites.<name>.params` | mapping of strings | none | Params for this suite in this run, merged over the suite's own; the run's win. |
| `runs.<name>.suites.<name>.secrets` | mapping | none | Secrets for this suite in this run, by kind, merged over the suite's; the run's win per `ENV_NAME`. |
| `runs.<name>.suites.<name>.secrets.prepare` | mapping of strings | none | As the suite's. |
| `runs.<name>.suites.<name>.secrets.batch` | mapping of strings | none | As the suite's. |
| `runs.<name>.suites.<name>.secrets.whole` | mapping of strings | none | As the suite's. |
| `runs.<name>.suites.<name>.minutes` | integer | the suite's `minutes` | The suite's time limit in this run, for a run whose batches take longer (a nightly). |
| `runs.<name>.suites.<name>.lock_minutes` | integer | none | The old spelling of `runs.<name>.suites.<name>.minutes`, read as it when that is unset; `check` and the plan step warn, and both with different values are refused. |
| `runs.<name>.record` | boolean | `false` | Whether the run's recorded suites record what each slice touched. It changes nothing for a suite bound by graph. |
| `runs.<name>.report` | mapping | | Where the run reports. |
| `runs.<name>.report.status` | boolean | `true` | Sets one commit status per suite and one for the run. |
| `runs.<name>.report.comment` | mapping | none | Keeps one comment on the pull request for the run. Pull request runs only. |
| `runs.<name>.report.comment.format` | `list` or `mermaid` | (required in a comment) | A list of suites and slices, or a chart GitHub draws. A chart too big for GitHub falls back to the list. |
| `runs.<name>.report.reasoning` | boolean | `false` | Shows in the comment why each selected slice runs: up to three reasons per slice and a count of the rest. |
| `runs.<name>.report.flush` | period | `30s` (`5m` with `store.backend: branch`) | Read by nothing today: `slice start` and `slice end` only write the batch's journal, the batch's end flushes it, and a task that wants progress per slice runs `slice flush --every <period>` itself ([writing tasks](../how-to/writing-tasks.md#live-progress)). |
| `runs.<name>.uncovered` | `fail` or `everything` | the top-level `uncovered` | This run's setting. |
| `runs.<name>.follow` | `executed` or `dependents` | the suite's, else the top-level `follow` | This run's width, over the suite's: the main branch can follow dependents while pull requests follow what ran. |
| `runs.<name>.keep` | mapping | `keep.runs` | How long this run's runs are kept. |
| `runs.<name>.keep.last` | integer | | Keep the newest this many runs. |
| `runs.<name>.keep.for` | period | | Keep the runs of this period. |

```yaml
runs:
  pr:
    trigger:
      pull_requests: {base: main, draft: false}
    compare: merged_target
    select: affected
    suites: {unit: {}, smoke: {}}
    record: true
    report:
      comment: {format: mermaid}
  nightly:
    trigger:
      timer: {start: "3:00", stop: "4:00", location: Europe/Berlin}
      branch: main
    compare: none
    select: all
    suites: {unit: {}, smoke: {}}
    record: true
    keep: {last: 30}
```

## keep

How long runs and lists are kept; see [keeping history](../how-to/keeping-history.md).
A retention keeps the newest `last` and those younger than `for`; with
both, whatever either keeps. A period is a number and a unit: `12h`, `5d`,
`2w`. Every planning job runs the sweep that applies it.

| Key | Type | Default | What it does |
|---|---|---|---|
| `keep` | mapping | | The section. |
| `keep.runs` | mapping | `{for: 7d}` | Which finished runs keep their refs. |
| `keep.runs.last` | integer | | The newest this many runs of each run name. |
| `keep.runs.for` | period | `7d` | Runs planned within this period. |
| `keep.lists` | mapping | `{for: 90d}` | Which commits keep their lists. Each branch's newest lists are kept whatever this says. |
| `keep.lists.last` | integer | | The newest this many lists. |
| `keep.lists.for` | period | `90d` | Lists made within this period. |
| `keep.lists.commits` | integer | `200` | The lists of the default branch's last this many first-parent commits are kept whatever their age, and those of its older commits are removed whatever their age. A plan looks this many first parents back for its base's lists. When the planning job's checkout is shallow, or holds no ref of the default branch, only `last` and `for` apply. |
| `keep.cache` | size | `8GB` | How much a planning step's cache (`--cache`) may hold when the step ends: a whole number with `B`, `KB`, `MB`, `GB` or `TB`, or `0` for no cap. Over it, the step drops what is cheapest to get back first and Bazel's output last ([disk on the planning workers](../how-to/sharing-machines.md#disk-on-the-planning-workers)). Whatever the cap, the step drops the Go modules' sources extracted beside their zips and the repositories Bazel extracted from its repository cache's archives, which come back from those archives without the network. |

```yaml
keep:
  runs: {for: 5d}
  lists: {last: 500, for: 30d}
```

## writes

How pipelinegen tries a write to the store again when it fails. A push
applies all of its refs or none, so what goes again is the whole write.
A ref that moved under a write is not such a failure: the command reads
it again and applies itself again (see `store.deadline` for the branch).

A failure in passing (the connection dropped, the host was busy with other
pushes and refused, a read of the refs before the push failed) goes again
at least `retries` times, and after that until `deadline` has passed since
the first try. The first wait is `backoff`, each later one twice the one
before up to 30 seconds, and each is cut to a random length between half
of it and all of it, so writers that met don't meet again in step. A
refusal by a rule of the remote's (a hook that declines, a permission or
an authentication it lacks, a repository it doesn't serve) goes again at
most twice, since a busy host declines that way now and then. A failure
that trying again would only repeat goes again never: it is a defect of
pipelinegen and says so with the object it concerns. No retry waits past
the step's time limit, as the runner gives it (`PIPELINEGEN_LIMIT`, and
`PIPELINEGEN_DEADLINE` when set): the command ends before it and says
which limit it reached. When a write gives up, its error says how
many tries it made, over how long, why it stopped, and the last failure;
a command that needed retries says how many once, when it ends.

| Key | Type | Default | What it does |
|---|---|---|---|
| `writes` | mapping | | The section. |
| `writes.retries` | integer | `10` | How often a write that failed in passing goes again at least, 0 to 50, however long that takes within the step's limit; 0 tries once. |
| `writes.backoff` | period | `1s` | The wait before the first retry. |
| `writes.deadline` | period | `10m`, or half of `limits.steps` when that is shorter | How long after its first try a write that failed in passing keeps going again once its retries are spent. It must be shorter than `limits.steps`, the time limit of the steps that write. |

```yaml
writes: {retries: 10, backoff: 1s, deadline: 10m}
```

## store

Where pipelinegen keeps what outlives a job: the plans and states of runs,
the recorded lists, the plugins' cached answers and the leases of locks.
By default they are hidden refs of the repository (`refs/pipelinegen/*`).
With `backend: branch` they are ordinary files on one branch that shares no
history with the code, and the leases on a second, small branch beside it.

A write to the branch is a commit on the tip it read, pushed so that the
branch only moves when nobody moved it first. When another writer did, the
write reads the new tip and applies itself again; those replays don't count
against `writes.retries`, since every lost race means another write landed.
`store.deadline` bounds them.

A write reads the branch's trees and only the files it reads or replaces.
Its push is thin, as git's pushes over HTTP always are, so each new file
may go as a delta of the file it replaces: the write fetches those files
first, in one fetch, and the push itself fetches nothing. Which files it
lacks it learns from the objects it holds, never by asking git about one
it may lack, which a git before 2.45 answers by dying.

Both branches need force-pushes allowed, for the leases and for the job that
squashes the store's history. Leave them out of branch rules that require
pull requests, and out of webhooks and workflows that run on every push. A
run can't be triggered by either branch.

| Key | Type | Default | What it does |
|---|---|---|---|
| `store` | mapping | | The section. |
| `store.backend` | string | `refs` | `refs` or `branch`. |
| `store.branch` | string | `pipelinegen` | With `backend: branch`, the branch the records live on; the leases live on the same name with `-locks` added. No run may be triggered by either, since every write would start it again. With `refs` it is not used. |
| `store.deadline` | period | `10m` | How long a write keeps applying itself again after other writers moved the branch first, before the command fails. |

```yaml
store: {backend: branch, branch: pipelinegen, deadline: 10m}
```

## learn

The section of a Go binding that pipelinegen no longer has. Nothing
reads it. A config that sets it loads with a warning that it will be
refused, as [releases](../releases.md) says. A suite with `bind: recorded` binds
through sbom and correlator plugins; the plugin proof's fixture
([internal/proof/fixture.go](../../internal/proof/fixture.go)) is a worked example.
The keys are still checked when the file is read: a `learn.files` entry
without `paths` or a known `normalize` makes the config invalid.

| Key | Type | Default | What it does |
|---|---|---|---|
| `learn` | mapping | | The section. |
| `learn.modules` | list of paths | none | Read by nothing. Was: the Go modules the built-in binding loaded; go-sbom's and go-correlator's `modules` take its place. |
| `learn.files` | list | none | Read by nothing. Was: plain files the built-in binding bound as units. |
| `learn.files[].paths` | list of patterns | (required in an entry) | Read by nothing. |
| `learn.files[].normalize` | `js`, `css` or `none` | (required in an entry) | Read by nothing. |
| `learn.pure` | list of strings | none | Read by nothing. Was: functions whose calls in an initialiser kept a variable bound by its uses; go-correlator's `pure_calls` takes its place. |

## plugins

The plugins the repository uses, each an executable pinned by a release and
its checksum, or by a commit before the first release. See
[plugins](../plugins.md).

| Key | Type | Default | What it does |
|---|---|---|---|
| `plugins` | list | none | The plugins; see [the guide](../how-to/plugins.md). Planning and recording go through the sbom and correlator plugins; with none, every suite is bound by the Bazel graph. The pipeline needs a runner. |
| `plugins[].name` | string | (required) | The plugin's name in this config, unique. |
| `plugins[].kind` | `correlator`, `sbom`, `runner`, `driver` or `connector` | (required) | What the plugin is. At most one runner. A driver runs a suite's slices for its `steps`; `command` is built in and needs no entry. A connector entry, named `github` or `git`, pins the release of that connector in place of pipelinegen's own pin, and takes a `release` and no `config` ([pinning a connector](#pinning-a-connector)). |
| `plugins[].repository` | `owner/name` | (required) | Where its releases come from. A repository `pipelinegen-KIND-NAME` holds the program `NAME-KIND`, which names its release files and its `./cmd/NAME-KIND`; any other holds the program of its own name. |
| `plugins[].release` | mapping | | A released version. Give either `release` or `commit`. |
| `plugins[].release.version` | string | | The release's tag. |
| `plugins[].release.sha256` | string | | The SHA-256 of the release's `linux-amd64` file (its `.mjs` file for a Node plugin), checked before it runs. Give this or `sums`. |
| `plugins[].release.sums` | string | | The SHA-256 of the release's `SHA256SUMS`, in place of `sha256`: one pin for every platform. The bootstrap keeps the file beside the plugin as `<name>.SHA256SUMS`, and the plugin's file for the platform it runs on (`<program>-<os>-<arch>`, or its `.mjs`, the program being the one its repository holds) is checked by its line there. A machine on another platform, a Mac say, checks its own binary by the same pin. |
| `plugins[].include` | list of patterns | none | Files the plugin claims besides those it detects as its own. |
| `plugins[].exclude` | list of patterns | none | Files the plugin never claims, whatever it detects. |
| `plugins[].commit` | 40 hex | | A commit to build, before the first release. |
| `plugins[].config` | mapping | none | The plugin's own settings, passed to it in every call. A runner's holds its placement; see [Where steps run](#where-steps-run). |
| `plugins[].collect_seconds` | integer | `10` | How long each of a correlator's `collect` calls may take before it is stopped and the slice's recording fails, saying so. In a batch run by drivers a call also stops 30 seconds before the batch's time limit, so the batch still ends. A recorded suite's correlators may take 90 seconds together: a mark waits for every one of them in turn, and the playwright driver's `marks.mjs` waits 120 seconds for a mark, so `check` refuses more, naming the suite and each correlator's seconds. A change to it is a changed file, as a time limit is. Only a correlator's entry takes it. |
| `plugins[].fill` | boolean | `true` | Whether `affected`, `why` and `try` fill the caches this plugin reads before they plan on your machine, as its `describe` says. `false` leaves them as they are; `--no-fill` does the same for every plugin, for one command. A pipeline's steps never fill: there the planner's prepare does. |

### Where steps run

Which workers each step of the pipeline runs on is the runner's to say, in
its own words, under `placement` in its `config`. pipelinegen passes the
block on as it is and reads nothing inside it. It only refuses a runner
that doesn't report the capability `placement`, since that runner would
put every step on untagged workers. For concourse-runner the block is a
`default` list of tags and `rules`, tried in order, each `for` one kind of
step (`pipeline`, `planning`, `checks`, `suite`, `bookkeeping`), optionally
of one `suite` and one `run`, with its `tags`. The
[runner's README](https://github.com/sideways-works/pipelinegen-runner-concourse#placement)
has the details.

```yaml
plugins:
  - name: concourse
    kind: runner
    repository: sideways-works/pipelinegen-runner-concourse
    release: {version: v1.2.3, sha256: ...}
    config:
      placement:
        default: [linux]
        rules:
          - {for: planning, tags: []}
          - {for: suite, suite: e2e, tags: [browsers]}
```

The keys that placed steps before, `concourse.planner.tags`,
`concourse.resource_tags`, `concourse.bookkeeping` and
`suites.<name>.run_on.tags`, are refused, each with the rule that places
the same steps. A proposed change's run takes the runner's block from the
config the pipeline was made from, like the other
[trusted keys](../how-to/secrets.md#what-a-proposed-changes-config-cant-change).
