# Narrowing

Some changes reach every test, strictly speaking. In the notes service,
every API test calls `Handler` to set up its routes, so a change to
`Handler` reaches all of them. In a large repository the same holds for
the code that starts the server, the request plumbing, a CI task file, a
database migration, a dependency bump. Run strictly, each of those runs a
whole suite.

Often a few tests tell you as much as all of them. If a change to the
server's start-up breaks it, every browser test fails the same way, and
the first one that fails already says so. A narrowing is a rule that runs
fewer tests for one such cause, accepting a small risk that the nightly
run then covers.

## Everything on and sample on

Two lists in the config handle files outside any plugin's view:

- `everything_on` names files whose change runs every slice of every
  suite: the build setup, the Go toolchain version. In the
  notes service, a change to `go.mod` runs all seven slices, with the
  reason `go.mod changed (everything_on go.mod)`.
- `sample_on` names files every test exercises the same way, such as the
  task file that runs a batch. A change to one runs a sample of each suite
  instead: `sample` slices, spread over the suite. The slices the change
  selects count toward them. When the change touches only such files, the
  sample stands for every slice it reaches through them, a slice that
  lists one as a Bazel input included, and keeps only those whose own test
  files changed.

The config itself needs neither: the planner reads what a change to it
means (a suite's step, a driver's pin, a setting) and runs what that
changes, and only a part it doesn't understand makes the config a changed
file ([a change to the config](../reference/config.md#a-change-to-this-file)).

Choose by what a break would look like. A file whose change breaks every
test the same way belongs in `sample_on`. A file that could break one test
and not another (a toolchain version, a module bump outside the graph)
belongs in `everything_on`. A file whose readers must run on every
change to it, such as one an access audit reads, belongs in neither: a
Bazel target its readers depend on selects them. A file in both lists
runs everything.

## Narrowings and levels

Besides those two lists, pipelinegen and its plugins declare narrowings of
their own. Two of pipelinegen's own are about the sample: a change to code
that every recorded slice of a suite ran the same way, or to what every
slice reaches only through that code, runs a sample of the suite rather
than all of it. Another, `build-files-by-targets`, keeps a BUILD file a
slice's filegroups list from selecting the slice by itself: a change to a
rule in it still selects through that rule. The plugins add theirs, each listed in its README;
go-correlator, for example, binds what only a batch's setup ran to the
suite's sample rather than to every slice.

You choose how much to narrow with one number, `narrowing`, from 1 to 5.
Each narrowing says from which level it is off, and level N turns off
every narrowing whose "off from" is N or lower; a narrowing that is off
falls back to the strict rule.

| Level | What is off |
|---|---|
| 1 | nothing: every narrowing is on, so the fewest slices run |
| 2 | the narrowings that rest on data or reflection rather than on code, such as binding one row of seed data to the code that reads it |
| 3 (the default) | also the sample, the split between a batch's setup and its slices, and new code that nothing reaches yet selecting nothing |
| 4 | also the plugins' narrowings marked off from 4; each plugin's README lists its narrowings with their levels |
| 5 | every narrowing: binding by declaration and by file with the full closure, and following dependents in every suite |

The [narrowing guide](../how-to/narrowing.md) lists each narrowing with
the level it is off from. A repository may set one narrowing's level
itself, under `narrowings`, with a note saying why; plans, `why` and the
nightly's alarm then name the config as the level's source, beside the
level its plugin gives.

## The alarm

Every plan records what each narrowing left out. The nightly counts how
many of its failures were in slices a narrowing left out of the branch's
runs since the last good nightly. When that count passes `narrowing_alarm`
(2 by default), its report says so and recommends the level at which those
slices would have run. Nothing changes on its own; someone decides and
edits the config.

Next: [Bazel and the plugins side by side](bazel-and-plugins.md).
