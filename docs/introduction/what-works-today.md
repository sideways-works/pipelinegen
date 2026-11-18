# What works today

This page is the one place that says what pipelinegen supports right now.
Everything else in the book points here.

## Platforms

pipelinegen is released as static binaries for Linux and macOS, on amd64
and arm64, with a `SHA256SUMS` file (macOS since v0.2.0; the macOS
builds are cross-built and not run in pipelinegen's own CI). Each Go
plugin's release lists the systems it is built for; the TypeScript
plugins are JavaScript files that need Node. On Windows, or for a plugin
without a build for your system, build from source with Go (see
[install](../tutorial/install.md)); CI jobs run on Linux.

## Git hosts

pipelinegen needs a git repository, not a particular host. The host
decides only where results show.

| Git host | Today |
|---|---|
| GitHub | works: branch, nightly and pull request runs, statuses, the comment, through the GitHub connector |
| a plain git server, or any host | since v0.2.0: branch, nightly and review-branch runs with the `git` connector; results stay in the repository's records, read with `pipelinegen status` |
| GitLab | as a plain git server; its merge requests, statuses and comments not yet |
| Gitea, Forgejo | as a plain git server; their pull requests, statuses and comments not yet |

On your own machine, `pipelinegen slices`, `check`, `affected`, `why` and
`record` work with any repository and no host.

Talking to a git host belongs to one of the six kinds of plugin, the
connector ([concepts](../concepts/connectors.md)). Each connector is a
plugin of the plugins repository, in a directory of its kind:
[connector/github](https://github.com/sideways-works/pipelinegen-plugins/tree/main/connector/github)
for a config's `github:` section, and
[connector/git](https://github.com/sideways-works/pipelinegen-plugins/tree/main/connector/git)
for a plain git server under [`connectors`](../reference/config.md#connectors).
Not in a release yet: pipelinegen fetches the release of the connector a
config uses that it pins, checked by its checksum, so a config needs no new
line; a `plugins` entry of kind `connector` pins another. Connectors for
GitLab and Gitea come after. This table changes as they land.

## Downloads

The book's commands download releases with `curl` from the repositories'
release pages. That works once the repositories are public; until then,
download them with an account that can read them, for example with `gh
release download <kind>/<name>/<version> -R
sideways-works/pipelinegen-plugins` for a plugin.

## CI systems

pipelinegen's steps are commands a CI job calls; a runner plugin writes
the whole pipeline for one CI system from the config, so nobody writes
those jobs by hand.

| CI system | Runner plugin |
|---|---|
| Concourse | [concourse-runner](https://github.com/sideways-works/pipelinegen-plugins/tree/main/runner/concourse) |
| others | none yet: write a runner ([writing a runner](../writing-plugins/runner.md) says what the pipeline must do), or [run the steps by hand](../how-to/any-ci-by-hand.md) in its jobs |

## Languages and tools

| Domain | sbom plugin (what exists) | Correlators (how it connects, what ran) |
|---|---|---|
| Go | [go-sbom](https://github.com/sideways-works/pipelinegen-plugins/tree/main/sbom/go) | [go-correlator](https://github.com/sideways-works/pipelinegen-plugins/tree/main/correlator/go) |
| TypeScript and React | [ts-sbom](https://github.com/sideways-works/pipelinegen-plugins/tree/main/sbom/ts), and [npm-sbom](https://github.com/sideways-works/pipelinegen-plugins/tree/main/sbom/npm) for packages | [react-correlator](https://github.com/sideways-works/pipelinegen-plugins/tree/main/correlator/react) |
| PostgreSQL | [pg-sbom](https://github.com/sideways-works/pipelinegen-plugins/tree/main/sbom/pg) | [sql-correlator](https://github.com/sideways-works/pipelinegen-plugins/tree/main/correlator/sql), [gorm-correlator](https://github.com/sideways-works/pipelinegen-plugins/tree/main/correlator/gorm) |
| OpenAPI | [openapi-sbom](https://github.com/sideways-works/pipelinegen-plugins/tree/main/sbom/openapi) | [oapi-codegen-correlator](https://github.com/sideways-works/pipelinegen-plugins/tree/main/correlator/oapi-codegen) |

Any other language works with the build graph alone, without plugins.

## The book's step-by-step guide

The [guide](../tutorial/README.md) walks a Go project end to end, Bazel
included. For a TypeScript frontend it covers the pipelinegen side and
stops where you would add Bazel's JavaScript rules, which their own docs
explain.

## Versions

pipelinegen is before its 1.0 release: a minor version can change the
config. Every change a release makes is in the
[changelog](../../CHANGELOG.md). Release candidates (`vX.Y.Z-rc.N`) are
marked as prereleases and are never the latest release.
