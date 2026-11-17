# Changelog

Every release of pipelinegen, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0.0 a
minor version may change the config or the protocol.

## [Unreleased]

### Changed

- Connectors are plugins in repositories of their own: GitHub's statuses, comment and pull requests go through the GitHub connector ([pipelinegen-connector-github](https://github.com/sideways-works/pipelinegen-connector-github)), and a plain git server's review branches through the git connector ([pipelinegen-connector-git](https://github.com/sideways-works/pipelinegen-connector-git)). pipelinegen pins a release of each and fetches the one a config uses, checked by its checksum, the way it fetches bazel-diff, so a config with a `github` section or a `git` connector needs no new line. A generated pipeline's bootstrap fetches it with the other plugins, with the read token; anywhere else pipelinegen downloads it from the connector's GitHub release into its cache the first time a command talks to the host, which needs github.com and a public repository. Regenerate the pipeline after upgrading. The connector holds no token: pipelinegen sends its requests, with the token of each call's purpose, to the step's API host only
- Pull request admission asks the connector's listing of open pull requests, not the one pull request: a pull request retargeted to another base reads "closed, or no longer onto `<base>`", and a pull request from a fork, or from a head repository GitHub no longer has, is never planned, by the head repository's id rather than its name
- `once_per_ready` finds an earlier head of the pull request in the run's own records, a plan written since it was last marked ready, where it read that head's commit statuses. Records older than `keep.runs` are gone, so a pull request ready for longer than that runs again after a push
- The first step of a two-step plan (`plan --out`) that holds the read token alone plans without asking whether the run takes the pull request, a draft or a closed one too, and runs its code there as before: no connector call carries the read token. The second step asks with the changes token, as before, and writes no plan for a pull request the run leaves out
- The sweep's give-up status goes through the connector's `status` call, with the report token, or the changes token when the sweep holds no report token
- A pull request's comment: the comment kept is the oldest that starts with the run's marker, and it is updated only outside the report throttle of the run's last status, where it waited for its own last update. A run that reports no statuses updates its comment whenever its text changes
- A call to GitHub without its purpose's token in the step's environment fails, naming the purpose, where it was sent without a token

### Added

- Config: `plugins[].kind: connector`, named `github` or `git`, pins another release of that connector (`release` with `sha256` or `sums`; no `config`)
- Conformance kit: a connector fixture may say why its host has no forks (`"no_forks"`), and then checks that the changes it names in `same` run
- `compare: last_passed`: while the new core narrowing `passed-since` is on (levels 1 to 3), each slice is compared from the newer of the run's base and the newest earlier commit at which it passed in the same run with its tests run, read from the run's records within `keep.runs`. For the newest two such commits the plan finds what changed from there to the head before the sample is drawn, and leaves out a slice that passed at one of them when nothing since reaches it, under `narrowed` with the reason "compared from `<commit>`, where it last passed in run `<run>`", which `pipelinegen why` shows. A run kept red by one slice re-ran every slice the changes since its last whole pass reached, on every commit. A slice with no such pass, one that ran no tests or was not run, the sample's targets, a suite whose whole-suite checks run, and a commit after which the config changed keep the run's base

## [0.2.3] - 2026-11-17

### Added

- `pipelinegen run` plans a change as `affected` does and runs the slices it selects on this machine, as a pull request's run would: per suite, its prepare once and one batch of the selected slices, through its drivers or task files, then a summary of the slices that passed, failed and errored, exiting 1 when any did not pass. `--base` is required unless `--all`; uncommitted changes are neither planned nor run, and a line names them. `--record` records the suites bound by recording and attaches their lists to the head in the local store when every slice of the suite passed. It needs no token, host or `runs` section
- Config: `narrowings` sets the level one narrowing is off from, in place of the level its plugin or the core declares, with a required note: `narrowings: {document-units: {off_at: 3, note: "every contract's readers and per_operation declared"}}`. `off_at` is 1 to 5 and may raise or lower the declared level; a name no configured plugin declares, and that isn't one of the core's, is refused when planning. Every plan records the levels the config set, and the plan's narrowed lines, `why` for a slice a narrowing left out and the nightly's alarm say so (`off from 3 by the config (narrowings.document-units: ...); plugin openapi-sbom says 2`); the alarm also names the `narrowings` entry to lower. A change to `narrowings` counts as a change to what is selected: the change plans under both configs and runs what either selects

### Changed

- Config: `concourse.planner.bootstrap.bazelisk` is optional; without it the pipeline fetches bazelisk v1.30.0, checked against its pinned checksum
- Continuous runs: the planning job is never serial. Its builds run side by side, and each plan step records its round's start and planning in the run's records, reads the others', exits when its commit is no longer the branch's tip, and waits, at most `runs.<run>.wait_minutes`, while an older round is under way. The spec no longer sets `serial_plan`, and `PIPELINEGEN_SERIAL_PLAN` is no longer read: a pipeline set by an earlier runner, whose planning job is serial, records its rounds too, every plan of a continuous run now has `limits.plan` as its deadline, and a build of a commit whose round a build that died had started runs nothing until `limits.plan` and 10 minutes have passed (or `pipelinegen end` ends that round). The runner protocol drops the `serial_jobs` capability; a runner that still declares it is asked for nothing serial, and the conformance kit refuses a serial planning job and a step that sets `PIPELINEGEN_SERIAL_PLAN`
- Batches: every batch of a suite's build runs at once. `suites.<name>.parallel` (4 by default) is how many batches a build is dealt, and so how many run together; `batches` and `batch_minutes` deal more, as before, and all of those run at once too. `check` warns of a suite whose `batches` is more than four times its `parallel`, of one with `batch_minutes` and no `batches`, whose run of everything has no bound on its batches, and of one whose batches, more than `parallel`, take a lock one by one and whose last could wait past the lock's 4 hours, since every batch asks for its slot at once

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
