# Changelog

Every release of pipelinegen, newest first. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html); before 1.0.0 a
minor version may change the config or the protocol.

## [Unreleased]

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
