# Changelog

Every release of pipelinegen, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0.0 a
minor version may change the config or the protocol.

## [Unreleased]

### Changed

- Connectors are plugins in repositories of their own: GitHub's statuses, comment and pull requests go through the GitHub connector ([pipelinegen-connector-github](https://github.com/sideways-works/pipelinegen-connector-github)), and a plain git server's review branches through the git connector ([pipelinegen-connector-git](https://github.com/sideways-works/pipelinegen-connector-git)). pipelinegen pins a release of each and fetches the one a config uses, checked by its checksum, the way it fetches bazel-diff, so a config with a `github` section or a `git` connector needs no new line. A generated pipeline's bootstrap fetches it with the other plugins, with the read token; anywhere else pipelinegen downloads it from the connector's GitHub release into its cache the first time a command talks to the host, which needs github.com and a public repository. Regenerate the pipeline after upgrading. The connector holds no token: pipelinegen sends its requests, with the token of each call's purpose, to the step's API host only
- Pull request admission asks the connector's listing of open pull requests, not the one pull request: a pull request retargeted to another base reads "closed, or no longer onto `<base>`", and a pull request from a fork, or from a head repository GitHub no longer has, is never planned, by the head repository's id rather than its name
- `once_per_ready` finds an earlier head of the pull request in the run's own records, a plan written since it was last marked ready, where it read that head's commit statuses
- The first step of a two-step plan (`plan --out`) that holds the read token alone plans without asking whether the run takes the pull request: no connector call carries the read token. The second step asks with the changes token, as before
- The sweep's give-up status goes through the connector's `status` call, with the report token, or the changes token when the sweep holds no report token
- A pull request's comment: the comment kept is the oldest that starts with the run's marker, and it is updated only outside the report throttle of the run's last status, where it waited for its own last update. A run that reports no statuses updates its comment whenever its text changes
- A call to GitHub without its purpose's token in the step's environment fails, naming the purpose, where it was sent without a token

### Added

- Config: `plugins[].kind: connector`, named `github` or `git`, pins another release of that connector (`release` with `sha256` or `sums`; no `config`)
- Conformance kit: a connector fixture may say why its host has no forks (`"no_forks"`), and then checks that the changes it names in `same` run

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
