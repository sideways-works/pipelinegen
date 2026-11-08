# Binding by recording

This page is for people changing how pipelinegen binds slices to the code
they ran: what the core does with the correlators' recordings. Planning
walks the stored graph from what changed ([graph.md](graph.md)); the deps
repository this page also describes is how a build with the
`pipelinegen_forward` tag binds slices, for the proof's comparison.
Calling the plugins is in
[plugins.md](../plugins.md#what-the-core-does-with-them), how
go-correlator binds Go in
[design.md](../design.md#binding-slices-to-the-code-they-run), and the
formats in
[contracts.md](../contracts.md#b-coverage-the-correlators-input).

## Why the graph alone isn't enough

A browser test starts the whole backend, so by the Bazel graph it depends on
all of it, and every backend change would select every browser test. A
backend test of one package links half the repository. Recording what each
test file touched, and binding it to that, is what makes a change to one
handler run the test files that called it and nothing else.

## From coverage to a recording

The batch task leaves per slice what the language's tooling wrote (for Go,
a `GOCOVERDIR` or coverprofiles), plus what the setup ran
(`coverage/_setup/`). At the slice's end each correlator that observes the
suite has its `collect` take its part, and `observe` turns it into the
units the slice touched, by the ids the sbom plugins give them
(`go:api/notes#func:Create`). What only the setup ran, or ran only as
loading, is kept apart: it binds every slice of the suite, or, with the
setup split on, a setup target of the suite's own. A correlator that finds
no evidence of a recording marks the slice: its list is kept as it was, and
it runs on every change until it is recorded again. Missing coverage never
counts as a slice that touched nothing.

## The repository's layout and size

`internal/depsrepo` writes the repository, `.pipelinegen/deps` (the
`@pipelinegen_deps` repository, ignored by git and Bazel's wildcards), for
base and head alike, from the units and slices the plugins produce. It
treats unit ids as opaque strings, and a unit's digest is the one its
plugin gave.
Each group (a package's directory, a file's root) is a package under `g/`
holding one digest file per unit and a filegroup `all`. Each slice gets a
selector in `slices/BUILD.bazel` that lists the digest files of its units
and its data labels. A slice that holds 8 or more units of one group points
to a filegroup of them inside that group instead. A unit a slice lists that
the tree lacks gets a file under `absent/`, so the selector still loads and
changes when the unit comes back.

The selectors file grows with the slices times the units each one names
outside a group of its own: roughly 80 bytes per label. With 1,000 slices
of 200 to 600 units spread at random over 2,000 groups, the worst case for
grouping, it came to 31 MB. Bazel loaded it in 2.6 s on a warm server, and
bazel-diff took 11.5 s to hash the workspace against 9.8 s without the
repository. Writing it took 2.1 s, and an unchanged write 0.16 s, since
the writer compares a key of its input with the one beside the repository.
Slices that run code in a few packages, the usual case, name fewer labels
and get a smaller file.

At about 5,000 slices that file would pass 150 MB, and loading one package
of that size is slow. Two splits would fix it without changing what
selects what:

- per suite, `slices/<suite>/BUILD.bazel`, which keeps every selector's
  label readable but needs the selector label to carry the suite;
- per prefix of the encoded slice name, `slices/<two hex digits of its
  hash>/BUILD.bazel`, 256 packages of about 600 KB each, which keeps the
  label's form and only changes its package.

Either way the label of a slice's selector changes, so planning and the
stub have to name it the same way. None of this is built:
the repositories pipelinegen serves today have well under 5,000 slices.

## The closure and the widths

A slice depends on the digests of what it touched and of their closure
over the links the plugins report: what a unit needs without running it
(a type, a constant, a method with no statements). The core follows links
and never reads code.

- `executed`: what the slice touched and its closure. This selects the
  fewest slices.
- `dependents`: the links followed backwards as well, from what the slice
  touched, to `follow_depth` steps. It covers
  branches the recorded run didn't take, at the cost of selecting more; on
  a backend whose router reaches everything, it selects every slice.

## Units no slice is bound to

Per suite: a changed unit that existed when the lists were recorded and
that no slice touched or reaches selects nothing; a new unit that no
slice's closure reaches selects every slice of the suite that can load it
(narrowed by a correlator's `unreached-new` where that is on).

## Lists and their lookup

What every slice recorded on a branch lives in `refs/pipelinegen/lists/<commit>`
([refs](refs.md)). Planning against base B reads lists(B), or the nearest
first-parent ancestor's within a bounded walk, for both trees, and adds the
recordings of every run whose refs still exist: that closes the window
between a merge and its lists, and can only select more. Base and head are
generated with the same lists, so a plan is a function of the two commits.
