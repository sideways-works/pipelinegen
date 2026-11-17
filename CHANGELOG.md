# Changelog

Every release of pipelinegen, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0.0 a
minor version may change the config or the protocol.

## [Unreleased]

### Added

- `run --slice <name>`, repeatable, runs only the named slices, by name or label, in every suite that has them: of the plan's selection, or of every slice with `--all`. A slice the plan didn't select runs too, and a line on stderr says so; a name no suite has exits 2 and names it

### Fixed

- `check` fetches a plugin it asks that `--plugins` (default `tools/plugins`) lacks from its pinned release, as `affected` and `run` do, checked by its pin, where it warned that the driver's steps went unchecked. A plugin pinned by `commit` that isn't there still can't be asked: the warning gives the commands that build it
- `why` plans on when the remote's lists can't be read, as `affected` and `run` do: the recorded slices are bound to everything, and a warning below the reasons says the lists couldn't be read and why. It exited 1, even for a config whose every suite is bound by the graph
- `run` without `--work` says why a slice failed in full on stderr. It named the slice's ref in the run's own store, which was removed when the run ended

## [0.3.0] - 2026-11-17

### Added

- Config: `plugins[].kind: connector`, named `github` or `git`, pins another release of that connector (`release` with `sha256` or `sums`; no `config`)
- `run` and `record` run a suite's steps in the image CI runs them in: the suite's `image` (or its prepare step's), a `registry-image` resource of `concourse.include`, pulled by its pinned digest when it has one, or else the task file's `image_resource`, with `docker` or `podman` (`--container-tool`). The build's directory, the repository's git directory (read only) and the tools are mounted at their own paths, the environment is the task's own (`PIPELINEGEN_*` and `params`, never a token), the suite's `privileged` holds, and the network is the default bridge. The steps run as root with the image's `HOME`, as on CI, and what they wrote is handed back to you when each ends, failed or stopped; `--as-me` runs them as you. In an image that isn't this machine's platform, or when this pipelinegen isn't a static Linux build, the release `concourse.planner.bootstrap.release` pins runs there. `--no-image` runs on this machine as before; with no container tool, a suite that names an image fails and says so
- `affected`, `why`, `try`, `run`, `record` and `slice run` fetch a plugin `--plugins` (default `tools/plugins`) lacks from its pinned release on GitHub, for this machine's platform or the image's, into the user cache, checked by its `sha256` or by the release's `SHA256SUMS` (`sums`), so a fresh clone needs nothing in `tools/plugins`. A driver there that isn't its pin is passed over for the pinned release. No token is needed, and a release no command used in 30 days is removed from the cache. A plugin pinned by `commit` can't be downloaded: the error names it and how to build it
- Conformance kit: a connector fixture may say why its host has no forks (`"no_forks"`), and then checks that the changes it names in `same` run
- `compare: last_passed`: while the new core narrowing `passed-since` is on (levels 1 to 3), each slice is compared from the newer of the run's base and the newest earlier commit at which it passed in the same run with its tests run, read from the run's records within `keep.runs`. For the newest two such commits the plan finds what changed from there to the head before the sample is drawn, and leaves out a slice that passed at one of them when nothing since reaches it, under `narrowed` with the reason "compared from `<commit>`, where it last passed in run `<run>`", which `pipelinegen why` shows. A run kept red by one slice re-ran every slice the changes since its last whole pass reached, on every commit. A slice with no such pass, one that ran no tests or was not run, the sample's targets, a suite whose whole-suite checks run, and a commit after which the config changed keep the run's base
- Narrowing: `build-files-by-targets` (the core's, off from 5). A `BUILD`, `BUILD.bazel` or `.bzl` file in a recorded slice's filegroups is no data file of the slice: a changed rule reaches it through that rule, which bazel-diff hashes, and a changed member list as files added or removed. Adding a unit test to an app's test lists ran every spec whose data holds the app's shell, since the shell listed its BUILD file; it now runs the specs with no list yet, and the plan names the rest under the narrowing
- Config: `narrowings` sets the level one narrowing is off from, in place of the level its plugin or the core declares, with a required note: `narrowings: {document-units: {off_at: 3, note: "every contract's readers and per_operation declared"}}`. `off_at` is 1 to 5 and may raise or lower the declared level; a name no configured plugin declares, and that isn't one of the core's, is refused when planning. Every plan records the levels the config set, and the plan's narrowed lines, `why` for a slice a narrowing left out and the nightly's alarm say so (`off from 3 by the config (narrowings.document-units: ...); plugin openapi-sbom says 2`); the alarm also names the `narrowings` entry to lower. A change to `narrowings` counts as a change to what is selected: the change plans under both configs and runs what either selects

### Changed

- Connectors are plugins in repositories of their own: GitHub's statuses, comment and pull requests go through the GitHub connector ([pipelinegen-connector-github](https://github.com/sideways-works/pipelinegen-connector-github)), and a plain git server's review branches through the git connector ([pipelinegen-connector-git](https://github.com/sideways-works/pipelinegen-connector-git)). pipelinegen pins a release of each and fetches the one a config uses, checked by its checksum, the way it fetches bazel-diff, so a config with a `github` section or a `git` connector needs no new line. A generated pipeline's bootstrap fetches it with the other plugins, with the read token; anywhere else pipelinegen downloads it from the connector's GitHub release into its cache the first time a command talks to the host, which needs github.com and a public repository. Regenerate the pipeline after upgrading. The connector holds no token: pipelinegen sends its requests, with the token of each call's purpose, to the step's API host only
- Pull request admission asks the connector's listing of open pull requests, not the one pull request: a pull request retargeted to another base reads "closed, or no longer onto `<base>`", and a pull request from a fork, or from a head repository GitHub no longer has, is never planned, by the head repository's id rather than its name
- `once_per_ready` finds an earlier head of the pull request in the run's own records, a plan written since it was last marked ready, where it read that head's commit statuses. Records older than `keep.runs` are gone, so a pull request ready for longer than that runs again after a push
- The first step of a two-step plan (`plan --out`) that holds the read token alone plans without asking whether the run takes the pull request, a draft or a closed one too, and runs its code there as before: no connector call carries the read token. The second step asks with the changes token, as before, and writes no plan for a pull request the run leaves out
- The sweep's give-up status goes through the connector's `status` call, with the report token, or the changes token when the sweep holds no report token
- A pull request's comment: the comment kept is the oldest that starts with the run's marker, and it is updated only outside the report throttle of the run's last status, where it waited for its own last update. A run that reports no statuses updates its comment whenever its text changes
- A call to GitHub without its purpose's token in the step's environment fails, naming the purpose, where it was sent without a token
- Planning opens a recorded slice's filegroups (and aliases, and the filegroups inside them) down to their files, with one Bazel query per level, cached by commit under `groups/` in the cache. A file an sbom lists units for is then replaced by the units the slice touched inside a filegroup too: with an anchor where the slice holds it, without one where the suite's recordings hold units of that sbom for files of the same directory. Editing one migration ran every spec whose stack holds the migrations; it now runs the specs that touched its tables. A file with units added or removed in such a group reaches the suite's setup part where the setup list holds its sbom's units and the setup split is on, and stays whole for every slice otherwise; a rename keeps it whole. Other files, other rules, a filegroup with a `select()` or an `output_group`, generated files and other repositories' labels stay whole, and a filegroup in which a slice gives up nothing stays one label. A reason names the filegroups its file was found through
- Planning and binding judge a data file alike: binding now keeps a file a test reads as text (`text_inputs`) or names in its `pipelinegen:data` lines, and a file whose anchor the slice's list lacks, as the walk did
- Continuous runs: the planning job is never serial. Its builds run side by side, and each plan step records its round's start and planning in the run's records, reads the others', exits when its commit is no longer the branch's tip, and waits, at most `runs.<run>.wait_minutes`, while an older round is under way. The spec no longer sets `serial_plan`, and `PIPELINEGEN_SERIAL_PLAN` is no longer read: a pipeline set by an earlier runner, whose planning job is serial, records its rounds too, every plan of a continuous run now has `limits.plan` as its deadline, and a build of a commit whose round a build that died had started runs nothing until `limits.plan` and 10 minutes have passed (or `pipelinegen end` ends that round). The runner protocol drops the `serial_jobs` capability; a runner that still declares it is asked for nothing serial, and the conformance kit refuses a serial planning job and a step that sets `PIPELINEGEN_SERIAL_PLAN`
- Batches: every batch of a suite's build runs at once. `suites.<name>.parallel` (4 by default) is how many batches a build is dealt, and so how many run together; `batches` and `batch_minutes` deal more, as before, and all of those run at once too. `check` warns of a suite whose `batches` is more than four times its `parallel`, of one with `batch_minutes` and no `batches`, whose run of everything has no bound on its batches, and of one whose batches, more than `parallel`, take a lock one by one and whose last could wait past the lock's 4 hours, since every batch asks for its slot at once

### Deprecated

- Config: `suites.<name>.run_on.serial_group`, `suites.<name>.run_on.max_in_flight` and `concourse.serial_groups` are no longer read: no job is serial, in a serial group or capped, and `check` names each one still set; a later release refuses them

## [0.2.4] - 2026-11-17

### Fixed

- Pull request runs: when the target branch moves on between a plan's two steps, the second step takes the first step's plan of the change merged into the older tip, once it finds that tip in the target's history and gets the same merge itself; it refused the plan as "not of this change" and failed the run

## [0.2.3] - 2026-11-17

### Added

- `pipelinegen run` plans a change as `affected` does and runs the slices it selects on this machine, as a pull request's run would: per suite, its prepare once and one batch of the selected slices, through its drivers or task files, then a summary of the slices that passed, failed and errored, exiting 1 when any did not pass. `--base` is required unless `--all`; uncommitted changes are neither planned nor run, and a line names them. `--record` records the suites bound by recording and attaches their lists to the head in the local store when every slice of the suite passed. It needs no token, host or `runs` section

### Changed

- Config: `concourse.planner.bootstrap.bazelisk` is optional; without it the pipeline fetches bazelisk v1.30.0, checked against its pinned checksum

### Fixed

- Pull request runs: a change that plans every slice for a reason known before any tree is read (a changed `everything_on` file, among others) now records its base in the plan. Planning in two steps refused such a plan as "not of this change", and its suites could not merge the change

## [0.2.2] - 2026-11-17

### Fixed

- Pull request runs: a planning build that ends without a plan (the run left the head out, as a draft, or its plan failed) removes the record that its planning began, in its write step (`plan --from`), so the run's sweep takes the head up after `missed_after`. The sweep left such a head to the ended build until `limits.plan` and ten minutes had passed, two hours and ten minutes by default

## [0.2.1] - 2026-11-16

### Added

- Config: `plugins[].collect_seconds` bounds a correlator's collect calls (10 by default); in a batch run by drivers a call also stops 30 seconds before the batch's time limit, and `check` refuses a recorded suite whose correlators may take more than 90 seconds together. A call stopped by its limit says so, with the call and the slice (`sql collect (slice-end of <slice>): stopped after 10s, the limit for a collect call`), where it said `signal: killed`

### Fixed

- Recording: a batch run by drivers observes its slices in its own task, where its tests ran and its correlators collected, after its last slice, and keeps each correlator's answers for the batch's end, which observes only what the batch left (a correlator that failed there, or one the batch had no time for). The batch's end runs in the pipeline's planner image, which may lack what a correlator needs (node for react-correlator), and every slice ended errored there with "exit status 127" after its tests had passed. An observation in the batch never changes a slice's result and stops two minutes before the task's limit. Before its first slice a recording batch checks the interpreter each correlator's file names on its first line, and stops at once, naming the suite, the correlator, the interpreter and the task, when the task lacks it; the batch's end says the same once, and fails only the slices it still had to observe

## [0.2.0] - 2026-11-16

The first release.

- `slices`, `check`, `affected`, `why` and `record` work on a developer's
  machine with no CI system; `seed` puts local recordings into the
  repository's store.
- Planning walks a stored graph outward from what changed, for every suite,
  and joins that with what bazel-diff says changed.
- Plugins: sboms and correlators say what the code holds and what each test
  executed; drivers run a suite's slices; runners write the pipeline;
  connectors say where the code lives (GitHub and plain git built in).
- Runs (`main`, `nightly`, continuous branch runs, pull requests) are
  declared by name in `pipelinegen.yaml`; a change to the config is judged
  by what it means for the tests.
- A run keeps its records in a few refs of the repository, and lists are
  kept by commit.
- Tokens and ssh keys are split by purpose, and a step gets only the ones it
  needs; a pull request's tasks get none that can write.
- The plugin conformance kit (`pipelinegen plugin test`) checks a plugin
  against the protocol.
- Releases carry static binaries for Linux and macOS, with `SHA256SUMS`.
