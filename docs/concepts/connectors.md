# Connectors

A connector tells pipelinegen where the code lives: the git host of the
repository under test. It recognises the repository from its remote URL,
lists the changes proposed for a branch (pull requests or merge requests,
with their head commit, whether they are drafts, their labels, and whether
they come from a fork), and shows the results there: a status on each
commit and one comment per proposed change. Each connector is a plugin in
a repository of its own ([below](#today)).

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

Two connectors exist, each a plugin:

- the GitHub connector,
  [pipelinegen-connector-github](https://github.com/sideways-works/pipelinegen-connector-github),
  which a config's `github:` section in `pipelinegen.yaml` uses: it lists
  the pull requests, sets the statuses and keeps the comment;
- the git connector,
  [pipelinegen-connector-git](https://github.com/sideways-works/pipelinegen-connector-git),
  for a plain git server with review branches as proposed changes, under
  `connectors:`.

Not in a release yet: pipelinegen pins a release of each and fetches the
one a config uses, checked by its checksum, the way it fetches bazel-diff,
so a config that used GitHub before needs no new line. In a generated
pipeline the bootstrap fetches it with the other plugins; elsewhere
pipelinegen downloads it into its cache the first time a command talks to
the host. A `plugins` entry of kind `connector` pins another release
([pinning a connector](../reference/config.md#pinning-a-connector)).

Config for other connectors, and several connectors at once, is the
design in [writing a connector](../writing-plugins/connector.md). [What
works today](../introduction/what-works-today.md) says when that changes.

Next: [the store](the-store.md).
