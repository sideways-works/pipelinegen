# Connectors

A connector tells pipelinegen where the code lives: the git host of the
repository under test. It recognises the repository from its remote URL,
lists the changes proposed for a branch (pull requests or merge requests,
with their head commit, whether they are drafts, their labels, and whether
they come from a fork), and shows the results there: a status on each
commit and one comment per proposed change. Connectors are being built;
until they land, GitHub is built into pipelinegen ([below](#today)).

Plugins come in six kinds, each answering one question:

| Kind | Question | Examples |
|---|---|---|
| sbom | what exists? | go-sbom, pg-sbom |
| correlator | how does it connect, and what did each test run? | go-correlator, sql-correlator |
| runner | where does the pipeline run? | concourse-runner |
| driver | how does a test run? | the go, bazel, playwright and command drivers ([drivers](drivers.md)) |
| connector | where does the code live? | GitHub, GitLab, Gitea |
| store | where does pipelinegen keep what it learned? | the repository's own refs ([the store](the-store.md)) |

With connectors, pipelinegen's core knows no git host. A config will be
able to name several, for example the code's host and a mirror that also
gets statuses. Without any connector, branch, timer and manual runs will
still work, with the CI system's own job results as the report and the
records kept in the repository through plain git.

A connector holds no secrets. Like every plugin it is a plain program
without network access: it describes the requests to make and names the
environment variable that holds the token, and the core sends them. A
change from a fork never runs with a token.

## Today

The connector plugin is being built. Until it lands, GitHub support is
built into pipelinegen and configured under `github:` in
`pipelinegen.yaml`. A plain git server, with review branches as proposed
changes, has the built-in `git` connector under `connectors:` (not in a
release yet). The design of the connector plugin
is in [writing a connector](../writing-plugins/connector.md). [What works
today](../introduction/what-works-today.md) says when that changes.

Next: [the store](the-store.md).
