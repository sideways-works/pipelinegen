# Batches

`internal/batch` and `internal/durations`, called by `pipelinegen batches`.
The user's side is in
[durations and batches](../how-to/durations.md).

## Why batches

A slice is selected and reported on its own, but it doesn't run on its own.
Setting up
a stack (database, seed, servers) costs the same for one test file as for
twenty, so slices are dealt into a few batches, each one task with one
setup. A full run costs `parallel` setups and a small change one, so test
files can be slices without paying a setup each.

## Dealing

`batch.Deal(slices, n)`:

1. Every slice without a recorded time gets the median of those with one
   (one second when none has).
2. Slices are sorted longest first, ties by name.
3. Each goes into the batch with the least time so far, ties to the lower
   batch.
4. Each batch's slices are sorted by name, and the batches by their first
   slice. Empty batches are left out, so there are never more batches than
   slices.

The same slices give the same batches in any order, so a rerun of the same
plan deals the same way, and a plan is reproducible.

## Running a batch with drivers

For a suite with `steps`, the batch task runs one command, `pipelinegen
slice run`. It reads `batches/<n>/slices.json`, routes each slice to the
one run step whose `take` matches it (the rule kind of a target slice is
in `slices.json`, written by `batches` from planning's Bazel query, so the
batch task runs no query), starts the set-up steps and waits for each to
be ready, and does what `slice setup-end` does. Then it calls each run
step's driver: with `batch: false` once per slice, between the slice's
start and end; with `batch: true` once with all the step's slices. Each
`end` event becomes `result/<slice>.json` and the journal's end, in
process, by the same code as `slice start` and `slice end`. Slices
without an `end` when a call returns are run again, each in a call of its
own; one that still has none is errored with the driver's last lines.
Last it stops the set-ups, the last started first, and exits non-zero
when a slice didn't pass. The ensure stays `report batch-end`.

In a recording run a bundle is used only when the driver says `apart`;
otherwise the command calls the driver per slice and says once why. In a
recording run of a `stack: true` suite the driver runs the bundle's
slices one at a time and holds at each slice's end while the command
collects the stack's coverage for it.

A mark is exchanged through files in the marks directory: whoever marks
(`pipelinegen slice mark`, or a hook in the tests) writes `<id>.json` by
rename and waits for `<id>.done`, or for `<id>.failed` holding why; ids
sort in the order the marks were made. `slice run` takes each mark as
`slice start` and closing a part do for a task file of the repository's,
and is the journal's only writer.

The command is `internal/cli/driven.go`; `internal/driver` calls the
drivers, reads their streams, runs again apart what a bundle left without
a result and holds the marks, and has the built-in command driver.
`internal/config/steps.go` reads and validates the steps, and
`pipelinegen check` routes every slice of a suite with steps and checks
the steps against the pinned drivers' descriptions
(`internal/cli/stepscheck.go`). The kit's checks of a driver are in
`internal/kit/driver.go`.

## The durations file

`durations.File`: seconds per test file path, one number or a map per
parameter set. `Seconds` sums a file's parameter sets (or those named);
`Update` takes a run's measured times, keeps the parameter sets and files
not measured, and drops files that no longer exist. It is written one file
per line, sorted, rounded to a tenth of a second, so an update's diff shows
only what changed. A missing file reads as empty.

The readers turn `go test -json` output into seconds per test (then per
file, given which file declares each test) and Playwright's JSON report into
seconds per spec file and project.
