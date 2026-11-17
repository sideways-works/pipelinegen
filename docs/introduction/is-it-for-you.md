# Is it for you?

pipelinegen pays off when a full run of your tests takes long enough that
people wait for it, and when most changes touch a small part of the code.
It asks something of your repository in return. This page lists what, so
you can decide before you spend an afternoon on it.

## What your repository needs

### A Bazel workspace

pipelinegen learns which parts of the repository a change touched from
Bazel's build graph, through a tool called bazel-diff. You don't have to
build or test with Bazel: the graph only has to describe your files. If
your repository has no Bazel yet, the step-by-step part of this book adds
it for a Go module, with Gazelle writing the build files for you ([Bazel
for your project](../tutorial/bazel.md)). For other languages the book
stops at that point and points you to Bazel's own guides.

### Git

The repository is a git repository. pipelinegen keeps its own records
(plans, what each test ran, locks) in hidden refs of that same repository,
so it needs no database and no service of its own.

### Tests you can run one file at a time

pipelinegen hands each CI task a list of test files. The task has to be
able to run just those (for `go test`, a package and a `-run` pattern; for
Playwright, a list of spec files).

## What it works with

None of these is a requirement; each decides how much pipelinegen does
for you. [What works today](what-works-today.md) has the details.

### The git host

Any git server holds the repository and its records. On GitHub,
pipelinegen also finds pull requests and posts statuses and a comment on
them; on a plain git server, the `git` connector takes review
branches as proposed changes, and `pipelinegen status` reads a run's
result.

### The CI system

pipelinegen's steps are commands a CI job calls. A runner plugin writes
the whole pipeline from the config; there is one today, for Concourse.
Another CI system needs a runner, or jobs that [run the steps by
hand](../how-to/any-ci-by-hand.md).

### Your machine

Planning, `why` and recording work without either: you can try
pipelinegen, and seed its first lists, before any CI runs it.


Which git hosts, CI systems and languages work today is on one page: [what
works today](what-works-today.md).

## Who it is for

- Repositories where the test suite is slow and grows: a backend with
  thousands of tests, a frontend with browser tests, or both in one
  repository.
- Monorepos where a change to one service shouldn't run the tests of all
  the others, and where tests cross boundaries (a browser test that calls
  the backend, a backend test that reads the database schema).
- Teams who want "only run what changed" without keeping path rules by
  hand.

## Who it is not for, yet

- A repository whose whole test run takes a minute or two. The setup isn't
  worth it.
- A repository that can't have a Bazel workspace. Without the build graph
  pipelinegen has nothing to start from.
- A CI system or git host on the "not yet" side of [what works
  today](what-works-today.md).
- A language without plugins can still use pipelinegen with the build
  graph alone: a test runs when a file of its own Bazel target changed.
  That is coarser, but it already skips everything outside the target.

## Where to go next

If you are new to the ideas, read [the concepts](../concepts/README.md)
first: they introduce every term the setup uses, on one small example. If
you would rather start doing, the [step-by-step
guide](../tutorial/README.md) takes you from nothing to a working pipeline
on your own project, and links back to the concepts where it uses one.
