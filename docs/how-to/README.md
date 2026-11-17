# How-to guides

Each guide does one thing on a repository that already uses pipelinegen.
The terms are in the [concepts](../concepts/README.md) and the
[glossary](../glossary.md); every key and command is in the
[reference](../reference/config.md).

## Tests and suites

- [Add a test](adding-a-test.md): a new test file, helpers, files a test
  reads.
- [Add a suite](adding-a-suite.md): a suite of test files, a tagged suite,
  whole-suite checks.
- [Write a suite's tasks](writing-tasks.md): the batch task, the prepare
  task, result files and coverage.
- [Bundle a suite's slices, or run each alone](batching.md): the `batch`
  setting of a run step, what a bundle is for each driver, and when not
  to bundle.
- [Move a suite from task files to drivers](moving-to-drivers.md): steps
  instead of `run_slice` and `prepare`, worked on a Go suite and a suite
  of shell scripts.
- [Configure plugins](plugins.md): how planning and recording use them,
  and what happens when one can't tell.

## Trying it out

- [Estimate what you would save](estimate-savings.md): plan your past
  commits and count what would have run.

## Runs

- [Declare runs](declaring-runs.md): triggers, what a run compares with,
  params, statuses and the comment.
- [Start without a full first run](starting-narrow.md): seed the lists
  from your machine, or set a known good commit as the baseline.
- [Run it on your machine, without CI](running-locally.md): see what a
  change would run, run it, and record the lists.
- [Run it on any CI system by hand](any-ci-by-hand.md): the steps in
  order, with one run in a plain shell.
- [Choose a narrowing level](narrowing.md), the sample and the alarm.
- [Durations and batches](durations.md): how slices are dealt into
  batches.
- [Share machines](sharing-machines.md): builds side by side, and locks
  between repositories.
- [Secrets](secrets.md): the token, its least rights, and secrets for your
  tasks.
- [Packages and cached images](packages-and-images.md): the packages a
  suite's steps need, installed in their image, and an image with them
  built once.
- [Uncovered files at planning](uncovered.md): fail the run or run
  everything.

## The store

- [Look inside](looking-inside.md): `lists show` and `why`.
- [Attach lists after a merge](attaching-lists.md), for merge automation.
- [Keep history](keeping-history.md): how long runs and lists are kept.
- [Back up the store](backups.md), and what happens if it is lost.
- [Fresh clones and old setup lines](fresh-clones.md).
