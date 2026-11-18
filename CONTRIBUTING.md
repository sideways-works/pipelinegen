# Contributing to pipelinegen

Thank you for wanting to help. This page says how to set up a working
copy, what every change has to pass, and how commits are written. How the
tool works inside is in [the internals](docs/internals/README.md), the
design and its reasons in [design.md](docs/design.md), and the interfaces
between the tool, its plugins and a repository in
[contracts.md](docs/contracts.md).

## Reporting a problem

Open an issue on the repository with what you ran, what you expected and
what happened, and the output of `pipelinegen version`. For a plan that
selected the wrong slices, `pipelinegen affected --json` and `pipelinegen
why --slice <name>` for the slice in question say most of what we need. A
security problem goes through the private route in
[SECURITY.md](SECURITY.md), never an issue.

## A working copy

You need Go (the version in `go.mod`), git, and
[bazelisk](https://github.com/bazelbuild/bazelisk) on your `PATH`, as
`bazel` too. The checks and the programs under `ci/` run as Bazel targets,
with the Go SDK and golangci-lint that `MODULE.bazel` pins at the versions
`.tool-versions` names.

```sh
git clone https://github.com/sideways-works/pipelinegen.git
cd pipelinegen
go build -trimpath ./cmd/pipelinegen
cp ci/commit-msg "$(git rev-parse --git-path hooks)/commit-msg"
```

## What every change passes

```sh
TEST_TMPDIR=$HOME/.cache/pipelinegen-test GOFLAGS=-trimpath go test ./...
bazel run //tools:lint            # golangci-lint, then this repository's own check
```

`//tools:lint` runs golangci-lint with the repository's `.golangci.yml` and
a cache of its own over every package, then `pipelinegen check` built from
the tree, and fails when `.tool-versions` names other versions than the
pinned ones. `bazel run //ci:gate` runs all of it, `go build` and `go vet`
first and the proof (`docs/proof.md`) last, as a branch's gate before it
lands; `ci/gate/` holds the same gate, the proof and a release build as
task files for a CI worker.

- Tests start Bazel 9.2.0 through bazelisk and run the pinned bazel-diff,
  which they download once into your user cache and check against its
  checksum. `PIPELINEGEN_TEST_BAZEL_DIFF` names another binary. A test
  without them fails rather than skips; only `-short` leaves Bazel out.
- Tests that talk to a fake GitHub or a plain git server run the GitHub
  and git connectors as a step does: downloaded from a release and checked
  by their pins. `PIPELINEGEN_TEST_GITHUB_CONNECTOR` and
  `PIPELINEGEN_TEST_GIT_CONNECTOR` name binaries built from the connectors'
  repositories, which the tests serve in place of the pinned releases.
- `TEST_TMPDIR` keeps Bazel's output in a directory of your own instead of
  `~/.cache/bazel`.
- Unit tests use no network: the git host is a fake, and remotes are bare
  repositories in temporary directories.
- A feature is done when its reference entry exists: every config key
  (`config.Keys`) and every command and flag (`cli.Commands`) has its
  section in the [config](docs/reference/config.md) and
  [command](docs/reference/commands.md) references, and
  `internal/docscheck` fails otherwise. The same check follows every link
  in the docs.
- A change to what the tool does comes with its docs: the book under
  `docs/` describes the tool as it is on `main`.

## Commits and pull requests

A commit message is one conventional subject line of at most 72
characters, such as `fix(report): keep the comment when a batch is rerun`,
with no body and no trailers. The types are `feat`, `fix`, `perf`,
`refactor`, `test`, `docs`, `build` and `ci`. The hook you installed above
checks it, and the lint suite's `subjects` slice checks the commits a pull
request brings. Keep commits small and each one passing.

Open a pull request against `main`. The repository's own pipeline, which
pipelinegen generates, runs the slices your change reaches and comments on
the pull request.

## Releases

Releases are cut by the maintainers with `bazel run //ci/release`; see
[releases.md](docs/releases.md). Every release has its entry in
[CHANGELOG.md](CHANGELOG.md).

## Licence

pipelinegen is licensed under the [Apache License 2.0](LICENSE). By
contributing you agree that your contribution is licensed the same way.
