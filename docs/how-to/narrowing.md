# Narrowing

How to choose a repository's `narrowing` level; what narrowing is, is in
[concepts](../concepts/narrowing.md). Some changes reach every test the
same way: a CI task file, the build setup, the code that starts the
server, a migration, a dependency bump. Bound strictly, each runs a whole
suite. A narrowing is a narrower answer for one such cause. Each saves
runs and weakens the safety argument a little, and the nightly, which runs
everything, is the net under all of them.

Planning has the plugins' own narrowings, the sample and `sample-bound`,
and each plan says what every one of them left out. The design is in
[design.md](../design.md#what-used-to-run-everything-and-the-narrowing-ladder).

## The narrowings

The core declares these itself:

| Narrowing | What it does instead of running the whole suite | Off from level |
|---|---|---|
| `sample` | a change to code every slice ran the same way runs a sample of the suite | 3 |
| `sample-bound` | a change to what every slice reaches only through that code, or to what runs at start-up, runs a sample of the suite | 3 |
| `plugin-entry` | a changed plugin entry (a pin, a checksum, its config block) runs a sample of each suite it reaches; what it left out is named under it | 4 |
| `unobservable-edits` | a changed file whose sbom covers it whole, with every unit as it was, runs no slice of a recorded suite, only the checks that read its text | 4 |

Each plugin declares its own narrowings, each with the level it is off
from, in its `describe` answer, and its README lists them in a section
called Narrowings. For example, go-correlator's `setup-split` (off from 3)
binds what a batch's setup ran to the suite's sample rather than to every
slice, and its `unreached-new` (off from 3) lets new code nothing reaches
select nothing by itself; pg-sbom's `rows-to-readers` (off from 2) binds a
row of seed data to the code that reads it rather than to every reader of
its table, and its `migrations-by-table` (off from 5) binds a migration to
the slices whose recorded statements touch its tables; openapi-sbom's
`document-units` (off from 2) binds an API contract per operation. The
[design](../design.md#the-ladder) gives the reason for each level.

## The levels

`narrowing: <level>`, from 1 to 5, sets how much is narrowed; a suite may
set its own, and the plan uses the highest level of the config and its
suites. Each level switches off every narrowing whose "off from" is at or
below it, and a narrowing that is off falls back to the wide rule.

- Level 1 narrows the most: the fewest runs.
- Level 2 gives up the narrowings that rest on data or reflection and
  keeps the structural ones, the setup split and the sample.
- Level 3, the default, also runs whole suites for setup-only code and for
  code every slice runs the same way, and lets new code nothing reaches
  yet select the slices that link it.
- Level 4 also switches off the plugins' narrowings marked off from 4,
  which each plugin's README lists.
- Level 5 binds by declaration and by file with the full closure, and
  follows dependents in every suite.

## The sample

A sampled change runs `sample` slices of the suite (default its
`parallel`), spread over the report's groups, the same for a commit and
rotating across commits so every slice is sampled over time. Slices the
change selects on their own are always in it.

What samples: code every recorded slice of the suite ran (the server's
start, the request plumbing), code only the suite's setup ran while the
setup split is on, and with `sample-bound` what every slice reaches only
through that code or what runs at start-up (a configuration type the
start-up code reads, a variable whose initialiser runs when the package
loads). A suite needs two recorded slices before anything samples.
A changed plugin entry samples too, while `plugin-entry` is on (levels 1
to 3): each suite it reaches runs its sample, not every slice it reaches
(see [a plugin's entry](plugins.md)). Only one sample of a suite runs for
a change, whichever of these asks for it. From level 4 everything an
entry reaches runs, also when the config file is listed under
`sample_on`: those slices are the change's own, and `sample_on` samples
only the rest.
`sample_on` lists files whose change samples whatever the level;
`everything_on` still runs everything. The comment says how many slices
the sample left for the nightly, and `pipelinegen why` names the narrowing
for a slice the sample left out.

## Files that changed nothing observable

A change that only reformats a file, or edits a comment no tool reads,
leaves every unit of it as it was. When the file's sbom also says that its
units cover the file whole (`complete` in its units answer: it parsed the
file without errors, and no comment in it is one a tool reads), the change
can't alter what a test running the file observes. While
`unobservable-edits` is on, such a file selects no slice of a suite bound
by recording, through whatever Bazel edge reaches the slice: a target that
holds it, `extra_deps`, a data line. The suites bound by the graph and the
tagged checks still run on it, since a format check or a lint reads its
text. Every other path the change touched still counts, however it
reaches a label: bazel-diff hashes the head's tree once more with the
base's copies of these files in place, and a label it still finds
impacted stays, a file a module extension reads included. Only a label
impacted through these files alone is left out. That is one more hashing
for each group of slices taking the same files (`label_groups` in the
plan), and none when nothing else changed. The plan's summary ends with "1 file changed without an observable change",
and `pipelinegen why` names the file, its sbom and the narrowing for a
slice it left out.

A test file's own `pipelinegen:data` lines are such a declaration
already: the labels they name stay whole for that test.

Some recorded tests read source files as text: a documentation check that
follows references in comments, a golden test over source files, a parser
run on fixtures an sbom also claims. For them a comment or a layout change
is observable. Declare them with the suite's `text_inputs` before taking a
release of an sbom that says `complete`: such a test keeps its edge to the
file, and `by_units` doesn't narrow the file for it either. `pipelinegen
why` says so for a slice kept that way.

```yaml
suites:
  go:
    text_inputs:
      - tests: ["internal/docscheck/*_test.go"]
        paths: ["**/*.go", "docs/**"]
```

## The alarm

Every plan records its level and what each narrowing dropped. The nightly
counts its failing slices that a narrowing dropped from the branch's runs
since the last nightly that passed. When the count is above
`narrowing_alarm` (default 2), its report opens with those changes and
recommends raising the level to the narrowing's "off from". A slice that
several narrowings left out of one run is listed under each, and the
level it asks for is the lowest of theirs, the first that runs it. Nothing
changes until someone edits the config, and the report never recommends a
lower level.
