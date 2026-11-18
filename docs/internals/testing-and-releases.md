# Testing and releases

This page is for people changing pipelinegen: how its tests run, what the
docs check enforces, and how a release is cut.

## Running the tests

```sh
TEST_TMPDIR=$HOME/.cache/pipelinegen-test GOFLAGS=-trimpath go test ./...
bazel run //tools:lint   # golangci-lint, then this repository's own check, from the tree that lands
bazel run //ci:gate      # build, vet, the lint, every test and the proof, before a branch lands
```

The targets run the Go SDK and golangci-lint that `MODULE.bazel` pins by
checksum, and the lint fails when `.tool-versions` names other versions.
The `lint` suite's step runs `//tools:lint` too.

- Tests start Bazel 9.2.0 through bazelisk and run the pinned bazel-diff,
  which they fetch once into the user's cache and check against its
  checksum. `PIPELINEGEN_TEST_BAZEL_DIFF` names another binary.
  `PIPELINEGEN_TEST_GITHUB_CONNECTOR` and `PIPELINEGEN_TEST_GIT_CONNECTOR`
  name connector binaries the `internal/cli` tests serve in place of the
  pinned releases. Without
  them a test fails, it never skips; only `-short` leaves Bazel out.
- `TEST_TMPDIR` keeps Bazel's output in a directory of your own instead of
  `~/.cache/bazel`.
- Unit tests use no network: GitHub is a fake (`internal/report`), and git
  remotes are bare repositories in temporary directories.

## Fixtures

- `testdata/workspace` is a small Bazel workspace with a Go backend, browser
  specs and a whole-suite check. `testrepo.New` copies it into a new git
  repository and commits it; `testrepo.NewFrom` does the same for another
  directory. Bazel ignores `testdata/workspace` in this repository, since
  its labels are its own root's.
- `example/` is the example repository the guide uses. `internal/example`
  plans its changes the way its comments and the guide say, so the docs'
  examples are tested: a change to one test runs its slice, a shared file
  runs both, a task file runs everything.

## The docs check

`internal/docscheck` fails when a config key (`config.Keys`) has no row in
[the config reference](../reference/config.md) or a row names a key that
doesn't exist; when a command or flag (`cli.Commands`) is missing from
[the command reference](../reference/commands.md); when a command's section
says "Not in a release yet." though the command exists, or lacks it though
the command doesn't; and when a relative link or heading anchor in
`README.md` or `docs/` leads nowhere. It also holds the tables of
narrowings in the guide and the design equal to the narrowings the core
declares, with their levels. A feature is done when its reference entry
exists.

## The book's outputs

Every command output the book shows is in `docs/transcripts/`, and the
docs check fails when a page shows something else. `bazel run //ci/book
-- <scratch dir>` runs every step of the book on a fresh copy of
`example-notes/`, as a reader would, and fails when what the tool prints
differs from those transcripts; `--update` writes the new outputs into the
transcripts and the pages instead. It refuses to run unless the
`pipelinegen` on `PATH` is the release `example-notes/pipelinegen.yaml`
pins and each plugin in `$PLUGINS` is its pinned binary, so the book always
shows the pinned releases.

It is a job of its own, run nightly and on release tags, not on every
change. Its image needs:

- Bazel through bazelisk (`bazel` and `bazelisk` on `PATH`) and network
  access for Bazel's first fetch of rules_go, Gazelle and the Go SDK;
- Go 1.27.1, git and `jq`;
- Node with npm, and the Playwright browser of the `@playwright/test`
  version in `example-notes/package.json` (the
  `mcr.microsoft.com/playwright` image of that version has both);
- the pinned pipelinegen on `PATH` and the pinned plugins in a directory
  named by `$PLUGINS` (`go-sbom`, `go-correlator`, `openapi-sbom`,
  `oapi-codegen-correlator` and `concourse`);
- a free local port for the notes server (`NOTES_PORT`, 7181 by
  default).

A run takes a few minutes on a warm Bazel cache and longer on a cold one,
most of it Bazel's first build of Gazelle.

## This repository's own CI

pipelinegen runs its own CI through the pipeline it generates, from the
root `pipelinegen.yaml`: suites for lint, the Go tests, the example's two
suites, and the plugin proof (`proof`), which plans a fixture through the
released Go plugins. [proof.md](../proof.md) lists what each proof shows and where it
was shown.

## Releases

A version tag on `main` builds static linux binaries for amd64 and arm64,
the same bytes for the same commit, writes `SHA256SUMS` and attaches them to
the tag's GitHub release. How to cut one and how a pipeline fetches a pinned
version is in [releases.md](../releases.md).
