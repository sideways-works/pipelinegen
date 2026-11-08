# Safety

This page is for anyone who needs to know why a change can't slip past
the selection, and where that argument ends. Running fewer tests is only
worth it if no change that can break a test slips past the selection. The argument below rests on the rules under
"What must hold" in [design.md](../design.md#what-must-hold), and its limits
follow it.

## Why a change can't be missed

1. Every changed file is seen. Planning diffs the two commits with git,
   not with a list of paths. A changed file that no Bazel target owns makes
   the whole run select everything, so a file outside the graph can't
   change quietly.
2. The graph decides, and only the graph. bazel-diff hashes every
   target with all its inputs and dependencies. A slice runs when its
   selector's hash differs, so anything in its dependency closure that
   changed selects it. There is no hand-kept filter that could leave
   something out.
3. A test's own file always selects it. The test file is a plain input
   of its selector, never narrowed: a change anywhere in it, adding it or
   removing it runs it.
4. What the graph can't see is recorded, or runs everything. Code a
   test reaches at run time (the backend a browser test calls) is bound by
   recording, plus the static closure of what was recorded, so new methods,
   changed types, constants and initialisers reach it. Packages that defeat
   static analysis (reflection by name, `go:linkname`, cgo, build tags) are
   bound whole. Before anything is recorded, a recorded slice depends on
   everything.
5. Missing evidence never counts as executing nothing. A recording run
   whose slice left no coverage fails that slice, and its list stays as it
   was. A correlator that can't tell what a slice touched, or tests that
   ran and touched nothing at all, mark the slice, and the mark binds it to
   everything until a recording tells again. A list that exists, even an
   empty one, binds exactly its units; an empty one comes only from a
   suite that sets `empty_recordings: allow`.
6. Uncertainty selects everything. No base, a base that isn't an
   ancestor, an unknown base, a base that can't be hashed, a failed
   bazel-diff run and a changed `everything_on` file each run every slice,
   with the reason. A head that can't be planned fails the run.
7. Every file is covered or declared untested. The check refuses a
   file that no slice depends on in the commit hook, and planning finds it
   again.
8. Lists can only make plans select more in the window that matters.
   Planning unions the recordings of every run still in the refs into the
   base's lists, so a merge whose lists aren't attached yet can't narrow a
   plan. Lists shrink only from a run that selected every slice, for a
   slice that passed in it and has data. A slice that failed keeps its
   list, and a recording it left part way only adds to it.
9. The nightly runs everything. Whatever the selection left out runs
   every night, so a test that broke unnoticed shows the next morning.

## The known limits

- The `executed` width follows what ran last time. A change to a branch
  a test didn't take when it was recorded, in a function it did execute,
  selects it (the function's digest changed). A change to a function it
  never executed, reached only through a branch it didn't take, selects
  nothing under `executed`. `dependents` covers that, at the cost of
  selecting much more, and the nightly catches it.
- Undeclared reads. A test that reads a file it doesn't declare is not
  selected when that file changes. Backend tests should run where an
  undeclared read fails (`bazel test`'s sandbox); browser tests depend on
  their owner target and `pipelinegen:data` declarations.
- Things outside the repository. A changed external service, a moved
  container tag that isn't pinned by digest, a test that depends on the
  time of day: none of them is a change pipelinegen sees.
- Flaky tests look like changes that broke something, or like changes
  that didn't. Selection doesn't make them better or worse; it makes them
  run less often.
- The `untested` list is trusted. A file listed there selects nothing;
  the list is reviewed by people.
