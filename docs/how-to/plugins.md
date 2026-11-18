# Configuring plugins

How planning and recording use the plugins, how to add one to a
repository, and what happens when a plugin can't say what a test touched. Writing a plugin
of your own is in [writing plugins](../writing-plugins/README.md), and the
protocol with every field is [plugins.md](../plugins.md).

## What a plugin is

pipelinegen's core knows units, the links between them and what each slice
touched when it ran. It knows no language, package ecosystem or CI system.
That knowledge lives in plugins: each is an executable in a repository of
its own, released and pinned like pipelinegen itself. A plugin is a plain
program that reads files and answers in JSON. It starts no server, opens no
database and uses no network. A driver is the one exception: it runs the
tests, in the suite's batch task.

There are six kinds. These four are the ones a repository configures:

| Kind | What it says | Named for |
|---|---|---|
| sbom | what exists: units with their IDs and digests, and how they hang together (a function in its file, a module's dependencies) | what it describes |
| correlator | who touches or uses those units: links from one unit to another, and which units a test touched when it ran | the tool or framework whose conventions it follows |
| runner | the CI pipeline that runs the plan | the CI system |
| driver | how a suite's tests run: it runs a batch's slices and gives a result and a recording per slice ([drivers](../concepts/drivers.md)) | the test tool it runs |

An sbom plugin owns the units. A correlator owns none: it only links units
that sbom plugins list, and observes which of them a test touched. So the
plugins come in pairs per domain, and each pair shares a small library so
both spell an ID the same way:

| Domain | sbom | Correlators |
|---|---|---|
| Go | `go-sbom` | `go-correlator` |
| TypeScript and React | `ts-sbom`, and `npm-sbom` for the packages | `react-correlator` |
| PostgreSQL | `pg-sbom` | `sql-correlator`, `gorm-correlator` |
| OpenAPI | `openapi-sbom` | `oapi-codegen-correlator` |

The other two are a connector, for a git host
([connectors](../concepts/connectors.md)), and a store, which is a design
([the store](../concepts/the-store.md)). pipelinegen pins the GitHub and
git connectors itself and fetches the one a config uses; a `plugins` entry
of kind `connector` pins another release
([`connectors`](../reference/config.md#pinning-a-connector)).

The one runner is `concourse-runner`. The drivers are go, bazel and
playwright, and the command driver comes with pipelinegen itself. Each plugin's own README says what it
reads and which settings it takes.

## When planning uses them

Every plan goes through the configured sbom and correlator plugins. The
core asks them about both commits, builds the graph of units and links for
each, and walks it back from the units that changed to the slices whose
lists or own files reach them ([the graph and the
walk](../concepts/the-graph-and-the-walk.md)). bazel-diff adds the slices
whose Bazel inputs changed. In a recording run, the correlators also say
at the end of each slice what it touched.

A suite bound by graph never records: each of its test files depends on
its owner target, its own files' units and what it names as data. With no
sbom and no correlator plugin there is nothing to ask, and every suite is
bound by its owners' graph alone. A runner doesn't change that: it writes
the pipeline and nothing else. A config with a `bind: recorded` suite and
no sbom or correlator plugin is refused, since nothing could read its
recordings.

## How planning asks them

Planning makes the same calls for the base and for the head:

1. `describe`, once per plugin: its kind, version, the calls it answers,
   what it needs on the machine (such as `node>=22.18`), the caches it
   wants kept between runs, its narrowings, and its `candidates`, cheap
   path patterns such as `**/*.go` or `**/package.json`.
2. `detect`: the plugin reads the candidate files and claims those it has
   positive evidence for, each with a role (`source`, `contract`,
   `migration`, `generated`, `lockfile` and so on). A YAML file is an API
   contract because it has an `openapi` key, not because of its name.
   `pipelinegen plugins claims` prints what each plugin claims and in
   which role, and a plan warns of a plugin that claims nothing.
3. `units` from each sbom plugin and `links` from each correlator that
   answers them, over the files it claimed. These make the graph that
   planning walks from what changed.

Every answer is cached by content: a file's answer under its git blob hash,
a set's under the blob hashes of its files, each with the plugin's version,
checksum and config. The planning job keeps the cache in its own cache
directory and, behind it, in hidden refs of the repository
(`refs/pipelinegen/cache/`), so every worker shares it. Answers no plan
used for two weeks are dropped from the refs, and so are those of a plugin
release no longer in use. On your machine,
`affected` keeps it in your user cache only, and `why` also reads the refs
when it plans again; neither writes them. Either way a plan
sends a plugin only the files it has no answer for, and a new release of a
plugin answers afresh. The plan's
`plugins` part counts, per plugin and call, the files asked about and those
answered from the cache.

Detection is why little config is needed. You don't list a plugin's files:
it finds them. The config keeps the pin, `include` and `exclude` patterns
for the rare file detection gets wrong, and in `config` what no file can
say, such as where a run leaves its coverage.

## Adding plugins to a repository

Name each plugin in `pipelinegen.yaml` with its kind, its repository and a
release with the checksum of its `linux-amd64` file (the `.mjs` file for a
Node plugin), or a 40-character commit to build:

```yaml
plugins:
  - name: go-sbom
    kind: sbom
    repository: sideways-works/pipelinegen-sbom-go
    release: {version: <tag>, sha256: <the release's checksum>}
    config: {modules: [backend]}
  - name: go-correlator
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-go
    release: {version: <tag>, sha256: <the release's checksum>}
    config: {modules: [backend]}
  - name: concourse
    kind: runner
    repository: sideways-works/pipelinegen-runner-concourse
    release: {version: <tag>, sha256: <the release's checksum>}
```

A plugin's repository is named for its kind first: a repository
`pipelinegen-KIND-NAME`, KIND one of sbom, correlator, runner, driver or
connector, holds the program `NAME-KIND`, whose release files are
`NAME-KIND-linux-amd64` (or `NAME-KIND.mjs`) and whose source is
`./cmd/NAME-KIND`. So `sideways-works/pipelinegen-sbom-go` holds `go-sbom`,
and `sideways-works/pipelinegen-correlator-oapi-codegen` holds
`oapi-codegen-correlator`. Any other repository holds the program of its
own name. The plugins' repositories had their programs' names before, and
a config that still says `sideways-works/go-sbom` keeps working while
GitHub redirects the old name. A config that uses the new name needs a
pipelinegen and a concourse-runner released with this rule, since older
ones look for files named after the whole repository; with those, both
spellings fetch the same files and keep the same cached answers, and
pipelinegen plans the same under either.

`name` is yours to choose and names the executable. `include` and `exclude`
take path patterns that widen or narrow what the plugin claims. `config` is
passed to the plugin as it is, in every call; each plugin's README lists
its keys. A config usually has one runner, and the pipeline's settings
are under `concourse:`. With several, each run names the one that owns it
in `runs.<name>.runner`. The proof's fixture
([internal/proof/fixture.go](../../internal/proof/fixture.go)) is a complete config
with the Go pair.

In the pipeline, each job's first step fetches every plugin into
`tools/plugins/<name>` and checks its checksum, as it does for pipelinegen
itself; a mismatch fails the step. On your own machine, `pipelinegen
affected` and `pipelinegen why` look for the executables in
`tools/plugins/` under the repository's root, or in the directory `--plugins`
names.

A plugin's entry in the config (its pin and its `config` block) is part of
what every slice it binds depends on: for an sbom, the slices that bind a
unit it lists; for a correlator, the recorded slices of the suites it
observes (`suites.<name>.correlators`, or every suite that names none) and
the slices whose binding holds the near end of a link it gave or a unit of
a file it claims. So changing it is a change like any other: from
narrowing level 4 those slices run again, with the reason "the entry of plugin <name>
changed"; below it each suite they belong to runs its sample, drawn from
the recorded slices the entry reaches, on top of the slices that run for
another reason, and the plan names the rest under `plugin-entry`.
A driver's entry reaches the slices its steps run, the same way, and its
`config` block counts as the `with` of each of those steps. Listing
`pipelinegen.yaml` under `everything_on` runs every slice for any config
change, a plugin's included; you don't need it for upgrades.

Plugins never use the network, so a cache they share, such as Go's module
cache, has to be filled before planning. `concourse.planner.prepare` names
a shell command the planning step runs in the checkout first, `go mod
download` for example; it may use the network.

## Trying a new pin before merging

A pull request that changes a plugin's pin, `bazel_diff` or `bazel.bazelisk`
is planned with the pins the pipeline was made from. A proposed change
never decides which binaries its run downloads and runs (see [what a
proposed change's config can't change](secrets.md#what-a-proposed-changes-config-cant-change)).
The plan's first warning, its status and its comment name the keys it
edits and point here: "planned with the repository's own plugins: this
change edits them, and they take effect once it is merged; to try them
first, see Trying a new pin before merging (…)". `pipelinegen check` on
the change's tree still verifies that the new pins resolve, so a broken
pin fails the change.

To watch the new pins work before merging, give the branch a pipeline of
its own for a while. A branch's run is trusted, so it runs the branch's
pins:

1. Push the branch with the new pins.
2. In its checkout, copy `pipelinegen.yaml` to an uncommitted
   `trial.yaml`. In the copy, point one branch run's `trigger.branch` at
   the branch, and set `concourse.self_set: false`: otherwise the trial's
   `pipeline` job sets the repository's own pipeline over it. Leave
   `concourse.branch` as it is. The trial's runs are then not the default
   branch's, so they attach no lists the repository's plans read.
3. Make the pipeline from the copy and set it under a name of its own:

   ```
   $ pipelinegen pipeline --config trial.yaml --out trial.yml
   $ fly -t ci set-pipeline -p example-trial -c trial.yml -v github_token=...
   $ fly -t ci unpause-pipeline -p example-trial
   ```

4. The trial runs the branch's tip, and again on every push to it. When
   the pins work, destroy the trial pipeline and merge.

The trial's `pipeline` job fails while it runs, since the trial is not
the pipeline the repository's config makes. That failure is expected.

## Recording through correlators

In a recording run of a suite with `bind: recorded`, each batch asks the
correlators for two things. `collect` gathers what ran: at the end of the
batch's setup, and at the start and end of each slice, for the actions the
correlator declares. A long-running server's coverage between slices comes
this way; for a Go server, through the Go correlator's `cover` package,
linked into the server's coverage build. Then
`observe` says which units each slice touched, and that becomes the slice's
recording.

Every correlator of the config observes every recorded suite, unless the
suite names its own with `correlators`. A correlator a suite doesn't name
is never asked about its slices, so its "can't tell" can't mark them, and a
correlator that observes no recorded suite gets a warning when the config
loads. A plan lists each recorded suite's correlators as `observed_by`, and
`pipelinegen why` names them.

A suite whose batches start long-running processes says `stack: true`. In a
recording run its batches fail at the end of their setup when no
configured correlator collects, since the slices would record nothing of
the stack. [Writing a suite's
tasks](writing-tasks.md) has the batch task's side of this.

## When a plugin can't tell

A slice is bound by evidence, and without evidence it is bound to
everything:

- A slice of a recorded suite with no list yet runs on every change until
  a recording run gives it one.
- When a correlator can't tell what a slice touched, the slice gets a mark
  in place of a new list. It keeps its old list, but the mark binds it to
  everything until a later recording tells again, and every plan warns of
  it. `pipelinegen lists show` and `pipelinegen why` name the mark and the
  correlator.
- Tests that ran and touched nothing at all are taken as a correlator that
  couldn't tell, and give a mark. A suite whose tests really touch nothing
  of the tree (they check a deployed service, say) sets
  `empty_recordings: allow`, and then an empty recording is an empty list.
- A list that exists, even an empty one, binds exactly its units, the
  slice's own files and what it reads.

During planning, a unit a plugin can't describe binds its whole group (its
file, say) to every slice that reaches it. When a plugin answers "can't
tell" for a whole call, the plan warns, naming the plugin, and binds more
widely to make up for it. If an sbom can't tell which files are its own
(`detect`) or what they hold (`units`), each file it was asked about
becomes a unit of its own, digested by the file's contents. A recorded
slice that reached the sbom's namespaces, or owns one of those files,
depends on all of them, so a change to any of them selects it. If a
correlator can't tell its `links`, or a bind correlator its `bind`, every
recorded slice depends on everything, because what the plugin would have
added could reach anything. A suite bound by graph still takes those files
through its owners' graph.

A plugin that fails fails the plan, which names it.

## Writing one

[Writing plugins](../writing-plugins/README.md) has a page per kind.
