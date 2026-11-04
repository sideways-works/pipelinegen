# Step 11: pull requests

The `pr` run from step 9 starts for every ready pull request into `main`,
and again for each new commit on it. It tests the pull request merged into
`main`'s tip and compares with that tip, so it runs only what the pull
request reaches.

## Open one

Open a pull request with the edit to `Words`. Within a minute or so,
`plan-pr` plans it and the comment appears on the pull request.

> **Output to come.** The comment as GitHub shows it, while the run goes
> and when it has ended, will be filled in from a real run.

**You should see:**

- one comment by the token's account, listing the suite, the three
  selected slices and their states, and how many were left out; it is
  updated as slices start and end, and a failed slice shows its failing
  tests with the end of their output;
- statuses `pipelinegen/pr/go` and `pipelinegen/pr`.

A draft pull request starts nothing (`draft: false` in the trigger); mark
it ready to start a run.

## Require it before merging

In the repository's settings on GitHub, add a branch rule for `main` that
requires the status `pipelinegen/pr`. It is the run's status: it passes
only when every suite of the run passed, and it is set even when a suite
has nothing selected.

## After a merge

The `pr` run recorded what each selected slice executed on a merge commit
that never lands. The `main` run of the merged commit records again, so
the lists stay current without anything else. To have a pull request's
recordings attached right away, the step that merges it can run
`pipelinegen lists attach`; [attaching
lists](../how-to/attaching-lists.md) shows how.

## Rerun a failure

When a slice fails, Concourse's "rerun build" on that suite's job runs
again exactly the slices that didn't pass, and the comment follows.

Next: [step 12, the nightly](nightly.md).
