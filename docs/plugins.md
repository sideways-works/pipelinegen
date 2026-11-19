# Plugins

This page is the plugin protocol, for people writing a plugin or changing
how the core calls one. pipelinegen's core knows units, links between
units, and what a run touched. It keeps the graph, walks it from what
changed, asks bazel-diff, applies the narrowing level and the sample, and
reports. Everything that knows a language, a package ecosystem, a
database, a CI system or a git host is a plugin, an executable with
releases of its own; pipelinegen's are in one repository, a directory
each by kind
([sideways-works/pipelinegen-plugins](https://github.com/sideways-works/pipelinegen-plugins)). There are six kinds; the store (where pipelinegen
keeps what it learned) is still a design ([writing a
store](writing-plugins/store.md)), and the other five are these:

- an **sbom** plugin records what exists: the units of one domain, with
  their digests, and the structure among its own units;
- a **correlator** records who touches things: who uses which unit, inside
  a domain or across domains, and which units a test touched. It owns no
  units and no namespace;
- a **runner** knows a CI system and writes its pipeline. Only the
  Concourse runner exists.
- a **driver** knows how a test tool runs: it runs a batch's slices and
  reports a result and a recording per slice, and a set-up driver starts
  what the tests need. It is the one kind that executes the tree's code;
- a **connector** knows where the code lives: a git host's proposed
  changes, statuses and comments. It sends no requests itself; the core
  sends them for it.

An sbom plugin is named for what exists (go, ts, npm, pg, openapi); a
correlator for the tool or framework whose conventions it follows (go,
react, sql, gorm, oapi-codegen), so one inventory can have several
correlators. An sbom and its correlators share a small library so they name
things the same way:

| Domain | sbom | correlators |
|---|---|---|
| Go | go-sbom (declarations and modules) | go-correlator |
| TypeScript, JavaScript, CSS | ts-sbom, npm-sbom for packages | react-correlator |
| PostgreSQL | pg-sbom | sql-correlator, gorm-correlator |
| OpenAPI | openapi-sbom (operations and components) | oapi-codegen-correlator |

Narrowings may come from either kind: an sbom's finer units, or a
correlator's narrower links. A repository with no sbom and no correlator
works on the Bazel graph alone. It needs one runner to have a pipeline at
all.

## What every plugin shares

### A pure program

A plugin is one executable, called as `<executable> <call>`, with a JSON
request on stdin and a JSON response on stdout. Files go in and JSON comes
out: a plugin starts no server, connects to nothing, needs no database and
no container. What exists is read from files (pg-sbom parses migration
files); what a test touched is read from files the run left behind (a
coverage directory, a statement log the tests' own database wrote). The one
contact allowed with a running process is with one the test itself
started, and it goes through files too: `collect` writes a request file
that process polls, and waits for the marker file it writes once its
coverage counters are on disk. Where files can't tell, the answer is "can't tell".

A plugin parses the tree's files and never executes them: no build, no
interpreter run on the tree's content, no loading of the tree's configs as
code (a Vite config is read as text, never imported). The steps that
observe and collect run beside credentials that write, on a proposed
change's tree; a plugin that ran that tree would hand its code those
credentials.

A driver is the exception to the two paragraphs above: running the
tree's tests, and starting the servers they need, is its job. It runs only
in the suite's own prepare and batch tasks, never while planning or
observing, and pipelinegen takes every token out of its environment
([drivers](#drivers)). Its requests and its `describe` answer follow the
rules below; its exit codes and its answer to `run` are its own.

Every request carries `"protocol": 1`, a `"work"` directory for this call,
and a `"cache"` directory kept between runs that the plugin may use as it
likes. An answer too big for stdout goes into files in the work directory
as JSON Lines, named by the answer's `"files"`; a large Go backend has
about 21,000 units, for example. Each line of such a file is one record of
the answer's top-level list for that call, and the file's name says which
list; what stays on stdout and what is in files add up:

| Call | File | One line is |
|---|---|---|
| `detect` | `claims.jsonl` | a claim `{"path", "role", ...}` |
| `units` | `units.jsonl`, `links.jsonl`, `sets.jsonl` | a unit, a link, a set |
| `links` | `links.jsonl`, `sets.jsonl`, `digest_from.jsonl` | a link, a set, a `digest_from` entry |
| `bind` | `units.jsonl`, `bind.jsonl`, `unbound.jsonl` | see Bind |
| `observe` | `touched.jsonl` | a unit id |

Stderr is for people: in a batch's steps (`collect`, `observe`), what a
plugin writes there while answering appears in the batch's log, a line at
a time as `<plugin> <call>: <line>`, redacted. Plugins keep no state
between calls.

The exit code says how the call went:

| Code | Meaning |
|---|---|
| 0 | answered (the answer may be empty) |
| 3 | can't tell: for `detect` and `units`, each file asked about binds as a whole to the slices that could have reached it; for `links` and `bind`, every recorded slice depends on everything; for an observation, the slice runs on every change until it is recorded again, and the plan says so. The last lines the plugin wrote on stderr say why, and the plan's warning quotes them, redacted |
| anything else | failed: planning fails, naming the plugin |

"Can't tell" is an answer of its own, apart from a failure and from an
empty answer.

### `describe`

Every plugin answers `describe` first:

```json
{"protocol": 1, "kind": "sbom", "name": "go-sbom", "version": "v0.3.0",
 "namespaces": ["go", "dep:go"], "capabilities": ["units"],
 "candidates": ["**/*.go", "**/go.mod", "**/go.sum"],
 "inputs": {"units": "per-set", "detect": "per-file"},
 "narrowings": [
   {"name": "fields", "off_at": 2, "note": "a type binds the fields code selects; a type that reaches reflection stays whole"}
 ],
 "needs": ["go>=1.27"], "caches": ["gocache", "gomodcache"]}
```

`needs` lists the language runtime or toolchain the plugin itself runs
with (`node>=22`), and may name one whose minimum the plugin reads from the
tree (`go>=go.mod`: the `go` line of the repository's go.mod, since
type-checking needs at least that Go). Not in a release yet: the planning
job putting the needed toolchain first on `PATH` (the bootstrap fetching a
pinned runtime by checksum into `tools`); today the planner image or the
planner's prepare command has to provide it. Planning checks, before it
asks a plugin anything, that each need's program is on the `PATH` the
plugin gets, and warns once per plugin of one that isn't ("plugin go-sbom
needs go>=1.27, which this machine's PATH lacks"); the version is the
plugin's to check. `pipelinegen check` and `pipelinegen pipeline` don't
run in the planner's image and can't tell what it holds, so the first
plan that runs there is where a missing toolchain shows. A plugin
that finds an older toolchain answers "can't tell" with `"cacheable":
false`, and the plan carries a warning naming the plugin and "toolchain",
since otherwise a wrong image would quietly bind every slice of that
language wide. `toolchains` says which version the plugin itself resolved
for each need, in the directory and environment it runs in
(`"toolchains": {"go": "go1.27.1"}`): a version manager's shim picks by
the working directory's version file, so the first binary on `PATH` says
nothing. It is optional, but the core caches a `bind` answer only for a
plugin that reports a version for every need, keyed by that report, since
an answer from another toolchain could differ. `caches`
are directories the plugin wants kept between runs (the Go build and module
caches, without which a cold planner compiles every dependency's export
data); the runner keeps them with the job. `inputs` is how each call's
answer depends on its files (below). `candidates` are path patterns over
the tracked files that the plugin may own; `detect` decides.

A correlator with `collect` may name the actions it handles in `collects`
(`setup-end`, `slice-start`, `slice-end`); none named means all three. The
core starts it only for those, so a plugin whose test fixture leaves its
files behind can declare `["slice-end"]` and skip two starts per slice.
The kit checks the names and calls only what is declared. A description
doesn't change between the calls of one batch, so the slice steps may
keep the descriptions from the batch's first step and load the plugins
from them, without starting each one to describe again.

### What the step that observes can see

`observe` runs in the batch's own task once its last slice ended (a batch
run by drivers), and in the step after a batch (`report batch-end`) for
what the batch left there and for a batch of task files. That step sees
the tree (`source/` in its working directory) and the coverage output
(`coverage/`), nothing else, and an observe in the batch's task reads
only the same two. Every runner gives it these two and
only these, and a correlator may rely on them alone: a build another task
made, or a prepared directory, isn't there unless the batch copied it into
the coverage output. A correlator whose config names a path it reads while
it observes lists those keys in `observe_reads`
(`"observe_reads": ["build_root"]`). The core then refuses a value outside
the two: an absolute path, one leaving the working directory, or one under
neither `source/` nor `coverage/`. `pipelinegen check` fails on it
(`bad-config`, from the pinned `describe`). A plan that loaded the
correlator warns, or fails for a run that records a suite it observes. A
recording batch stops at `slice setup-end` and at its first `slice
start`, before any slice runs. `pipelinegen record` holds a local build to
the same rule, although its build directory holds every task's output.
The message states the rule first: `<plugin> config.<key> = "<value>": the
step that observes sees only the tree (source/) and the coverage output
(coverage/), ...`. The kit calls `observe` from a directory that holds
only the fixture's tree and its coverage, and checks the kit's config
against the same rule.

### Finding its own files

A plugin finds the files it describes or reads; the config doesn't list
them. `candidates` is cheap (extensions, names). The `detect` call gets
the candidate paths the core has no cached answer for, reads them, and
claims the ones it has positive evidence for:

```json
{"claims": [
  {"path": "api/openapi.yaml", "role": "contract", "depends": ["api/schemas/order.yaml"]},
  {"path": "api/gen/server.gen.go", "role": "generated"},
  {"path": "web/package-lock.json", "role": "lockfile"}
]}
```

Roles include `contract`, `migration`, `generated`, `directive`,
`manifest`, `lockfile` and `source`. `depends` names files a claim reads
along (a contract's `$ref`s): the core treats them as claimed by the same
plugin in the role `fragment` (unless it claims them otherwise), sends them
to its `units` with the claim, and counts their blob hashes in the claim's
set, so fragments get units and links into them resolve. Detection is per file and cached by blob hash
like any per-file answer. It is conservative: a plugin claims a file only
on positive evidence (a YAML file is a contract when it has an `openapi`
key, not because of its name), and several plugins may claim one file. The
core passes each plugin its claimed files, with their roles, in `units`,
`links` and `bind` (`"claimed": [{"path", "role"}]`). Where an output
location can be found in the tree (a bundler config names its output
directory), the plugin finds it too.

A correlator that observes may mark a claim `"recorded": true`, per file:
a recording made with it holds units of this file whenever a slice
depends on it (a script the browser or a test's Node process runs, a file
under `public/`). A change to a file without it must reach the slices
another way (a stylesheet, a config file, build tooling). Only a
correlator that observes may set it; `pipelinegen plugins claims` shows it,
and the kit checks that the plugin observes and that each fixture file it
marks is in the fixture's recording. The mark is per file because one
`.ts` file can be code the browser runs and another build tooling.

A correlator that observes the app's build may instead mark a claim
`"erased": true` (never with `recorded`): the build erases this file, a
TypeScript file that holds nothing but types. It runs in no test, no
recording holds it, and no test that exercises the built app depends on
it, so a change to it reaches only a type check. The kit checks that the
plugin observes, that no file carries both marks, and that the companion
sbom answers no unit of a file marked erased that could run (`executable`).
The limit: a build step that reads types (a schema or code generated from
TypeScript types) makes such a file a real input of that step, and the
step's own target has to name it; the claim can't know.

Run-time locations, which no file of the tree can say, go in the plugin's
`config` as paths relative to the batch task's working directory:
`build_root` (a build handed over outside the tree, laid out like it), the
statement log, the request log. Evidence that exists but can't be
attributed (a script on the app's origin that no build holds) is "can't
tell", never dropped.

So a `plugins:` entry is the pin and little more: optional `include` and
`exclude` patterns that widen or narrow what the plugin claims, and in
`config` only what no file can say (where a run leaves its coverage). Keys
that name files (documents, migration directories, module lists) are
overrides, never required. `pipelinegen plugins claims` prints what each
plugin claims and in which role, and a plan warns of a plugin with
candidates or an `include` that claims nothing in the tree. A file an
sbom claims counts as covered in planning: its units carry a change, so
it needs neither a Bazel target nor `everything_on`. The claims are the
head's: `affected` and `try` of a commit that isn't your checkout check
that commit out apart and ask the plugins there, so a past dependency
bump plans as the planning job plans it. The commit hook's
`check` runs no plugin over the tree; it asks each configured sbom only
for its `describe` answer, and only a binary whose SHA-256 is the pin's,
keeping the answer by that pin. An uncovered file its `candidates` or
`include` match is then a warning, judged by the plan.

### Pinned and verified

The config names each plugin by repository, with its directory in a
repository of several, and by a release with its checksum, by a commit
that its `manifest.yaml` builds it from, or by a zip and the zip's
checksum ([the manifest](reference/manifest.md),
[building and hosting a plugin as a zip](how-to/plugin-zip.md)). The
bootstrap fetches each into `tools/plugins/<name>` and checks it like the
tool itself:

```yaml
plugins:
  - repository: https://github.com/sideways-works/pipelinegen-plugins
    with:
      - name: go-sbom
        kind: sbom
        path: sbom/go
        release: {version: v0.4.0, sums: e12cb210141dda74d52c56e9d192a67cf735c244dffb687757f3348402cda71d}   # or commit: <sha>
        exclude: ["tools/**"]
      - name: go-correlator
        kind: correlator
        path: correlator/go
        release: {version: v0.4.0, sums: fa7fea22707363a40366dad3b6947cd8d0a427cadce9e0dbd3e4b6cda227a020}
        config: {pure_calls: [errors.New]}
      - name: concourse
        kind: runner
        path: runner/concourse
        release: {version: <tag>, sums: ...}
```

`config` is the plugin's own block, passed in every request. The answer
cache is keyed by the plugin's version, checksum and config, so a new
release answers afresh. A plugin's
entry (its version, checksum, commit or zip, and its `config` block) is a unit
of each tree, read from that tree's own config, and every slice the plugin
binds depends on it: for an sbom, a slice holding a unit it listed; for a
correlator, every recorded slice of a suite it observes (the suite's
`correlators`, every correlator when it names none), whose recording it
made, and every slice whose binding holds the near end of a link it gave
or a unit of a file it claims (a slice bound by the Bazel graph through
the units its data names, not its own files' units). A suite it
neither observes nor answered for is untouched by it. So an upgrade is
itself a change: from narrowing level 4 those slices run again with the
reason "the entry of plugin <name> changed"; below it, while `plugin-entry` is on,
each suite they belong to runs its sample instead, a unit that changed in
its own right still selects its slices, and the plan names the slices the
sample left out under `plugin-entry`.

### Caching by content

The core caches plugins' answers by content, so a plan sends a plugin only
the files it has no answer for. Base and head share nearly every file, and
so do consecutive commits: a normal plan sends a handful.

Each call's answer depends on its input in one of two ways, which
`describe` declares per call in `inputs`:

- `per-file`: the answer for a file depends on that file alone (an sbom's
  units for one TypeScript file, one API document). The core keeps
  one answer per file, keyed by the file's blob hash, the plugin's name,
  version and checksum, and the plugin's config. For a tree it asks git for
  the blob hashes and sends the plugin, in the request's `"paths"`, only
  the files it has no answer for; the plugin names each unit's file.
- `per-set`: the answer depends on a set of files together (a Go package's
  units, since a type's digest holds methods from the package's other files
  and an embedding variable's the embedded files; a type-checked link
  graph; a schema built from all migrations in order; a lockfile). It
  is cached by a hash of the set's blob hashes. A plugin may name a smaller
  set per answer (`"inputs": [paths]`: a Go package and its imports, the
  migrations directory), so one changed file doesn't invalidate the rest.

A per-set answer comes in sets, each cached by its inputs' blob hashes:

```json
{"sets": [
  {"group": "internal/notes", "inputs": ["internal/notes/notes.go", "internal/notes/store.go"],
   "whole": false, "units": [...], "links": [...], "cannot_tell": []}
]}
```

`"paths"` in a request means: answer every set that holds one of these
paths. A deleted file's set is answered too: a deleted package as an empty
set. A plugin may say any other path belongs to sets (a file a package
embeds answers the packages above it; a dependency manifest answers the
dependency set).

A set's `inputs` may name files the plugin never claimed: a file it
reads along a chain of references (a root names a context file, which
names a third) that detect, looking at one file at a time, can't know of.
The core keys the set on every input's blob, claimed or not, so a change
to the last file of the chain asks the set again, and it counts such an
input as the plugin's, in the role `fragment` (as `depends` does);
`pipelinegen plugins claims` lists it once the set is cached. A set may also list `"absent": [paths]`:
paths it looked for and didn't find (a config file probed on the way up
the tree). Its answer depends on their absence, so the core keys the set
on them, and one that appears asks the set again. A declared input inside
the tree that doesn't exist is keyed the same way, as absent. A path
outside the tree (absolute, or climbing out with `..`) can't be keyed at
all: the core takes such a set as one the plugin couldn't tell (its files
are untold, the recorded slices the plugin could bind run on any change
of theirs, the set is never cached) and the plan warns `<plugin> <call>:
the set of <group> declares <path>, which is outside the tree: bound wide
until <plugin> declares paths of the tree`. The kit is stricter: a
declared input the fixture lacks must be listed in `absent`, since one
that isn't (an import specifier without its extension) is a mistake that
keys the set on the wrong file.

Sets may share inputs: per-file sets that each read one contract name it
in each set. The core finds every set from each input it names, so one
changed file asks again about that file alone, and every set that still
holds is served from the cache.

A correlator's set may also name `"input_units": [ids]`: units an sbom
lists whose digests the answer depends on, keyed like the input files'
blobs. A Go package's links depend on what its imports declare, not on
their function bodies. So its set names one unit per imported package (the
package's declarations without bodies) instead of the imported files, and
a body-only change asks again about one package instead of every package
that imports it.

- The sboms answer first. A set is keyed on the sboms' digests, with
  their own `digest_from` resolved; a correlator's `digest_from` doesn't
  count, since it comes from the answers being keyed.
- A unit no sbom lists keys the set as absent, but only when an sbom that
  covers the unit's namespace told about every file it was asked about.
  When no sbom covers the namespace, or one couldn't tell, the set is
  asked about on every plan and never cached.
- `file:<path>` stands for the file's blob, for a set that depends on a
  file it doesn't list as an input.
- An sbom's set names no input units.
- An input unit's digest has to change whenever the set's links could. If
  giving a function its first statement turns a `bind` link to it into a
  `dependents-only` one, the unit's digest must change with that edit.
- Naming a unit as an input unit asks the set again when its digest
  changes, and nothing else, for a unit a file declares (a function, a
  type). A unit no file declares, such as a Go package's API unit, sums
  up declarations of its group that carry every change of it themselves:
  it counts among no group's unbound names and is not bound with a group
  that can't be told.

When a set's files are unchanged and only an input unit's digest has
changed, the core asks about the input of that set that the fewest sets
hold: for a Go package, its own file. Asked about a `go.mod` that every set
of the module holds, the plugin would have to answer the whole module; asked
about the package's file, it answers the sets the change made stale.

For each input and blob, the core remembers the sets that the plugin's
answers about that input held. When the plugin is asked about an input
again, its answer replaces that input's record. A set that an older answer
named, but that one of its own inputs' records no longer holds, has been
replaced: the plugin last answered about that input without it. Another
input's older record may still name it, but such a set is neither served
nor asked about.

### What an answer may depend on

A plugin runs with exactly this environment: `PATH`, `HOME`, `USER`, the
locale (`LANG`, `LANGUAGE`, `LC_*`), `TZ`, `TMPDIR`, where Go is
(`GOROOT`, `GOTOOLCHAIN`, `GOOS`, `GOARCH`), `GOPROXY=off`, and one
variable per cache its `describe` declares, pointing into the plugin's
cache directory, which the runner keeps with the job: `gocache` as
`GOCACHE`, `gomodcache` as `GOMODCACHE`, `npm` as `npm_config_cache`. No
cache is inherited, and it never gets the repository's token or
pipelinegen's own settings. A declared cache is shared by every plugin that
declares it, at `<cache>/caches/<name>` under the core's cache directory
(`gocache`, `gomodcache`, `npm`); the planner's prepare command
(`concourse.planner.prepare`) fills it before planning. Whatever else a
plugin keeps goes in its own `<cache>/plugins/<plugin>`. A plan's calls
work in `<cache>/plugin-calls-<n>`, which the plan removes when it ends,
failed or not; the next plan sweeps any that a killed plan left. The kit's
`--cache` directory takes the same per-cache layout. A Go plugin builds with `-trimpath`, or the
build cache keys on the checkout's directory and base and head compile
everything twice. Filling the module cache (which needs the network) is
the planning job's, before the plugins run; on your machine the local
commands do it, as the plugin's `fill` says (next section).

An answer depends only on its declared inputs, the plugin's version and
config, and nothing about the machine: answers are cached by content and
shared between machines. A plugin fixes what would vary (the Go pair reads
every file under one build context: linux/amd64, cgo off, the tags from its
config, the cover tag in). The environment a plugin may rely on is its
`needs` (the toolchain the bootstrap fetched) and its `caches` (kept by the
runner with the job, so the Go module cache holds the repository's
modules). When something outside the tree is missing all the same (a
module's `go.mod` not in the module cache), the plugin answers "can't tell"
for the units it affects and marks the answer `"cacheable": false`, so the
next plan asks again.
The cache lives in hidden refs of the repository, with a local directory
(a CI job's cache, a developer's cache directory) as a first level in front
of it, so a worker with a wiped cache and a second worker plan in seconds:

- One ref per plugin version and week,
  `refs/pipelinegen/cache/<plugin>/<version>-<checksum prefix>.<ISO week>`
  (`go-sbom/v1.2.0-0123456789ab.2026-W46`), points at a parentless commit
  (the checksum is the pin's when the binary is the file the pin names,
  else the binary's own, so a plugin built on a machine never shares
  answers with the pinned build)
  whose tree is fanned out by the key's hash (`ab/cdef...`), each entry one
  answer. Not in a release yet: compressing the entries.
- Planning fetches the ref (in the same fetch as the lists it needs), asks
  the plugin only for what is missing, and pushes the new entries with a
  compare-and-swap. When the ref moved meanwhile, it merges the two trees
  and tries again: two plans adding different entries both land, and a lost
  race costs only recomputing an answer, never a wrong one.
- Only a pipeline step writes the refs. On a developer's machine,
  `pipelinegen why` reads them when it plans again, and `pipelinegen
  affected` uses the local directory alone. Not in a release yet:
  `affected` reading the refs, and a `cache_push` setting that lets a
  developer's machine push.
- A plan reads this week's ref and then last week's. Whatever it uses
  from last week's, and whatever its local directory answered, goes into
  this week's ref with its next write. Every plan reads the whole graph of
  its trees, so the answers a tree names move from week to week while
  anyone plans it. The planning job's sweep removes the refs older than
  last week's: an answer no plan used for two weeks is gone, and so is a
  plugin version nobody runs any more. A machine whose local directory
  still holds an answer the refs lost puts it back the next time it uses
  it.

For a repository of a few thousand files, a plugin's ref holds a few MB of
answers before git compresses them (about 2 KB per Go file's units) and
about 1 to 2 MB fetched cold; a cold fetch takes a few seconds, a warm one
nothing. The local directory takes writes to a temporary name and renames
them, so concurrent runs are safe. Not in a release yet: bounding it by age
and size.

A plan keeps at most 64 MB of new answers in memory. The rest wait in the
local directory and go to the refs in writes of at most that size, so a
cold pass over a whole tree doesn't hold every answer at once.

A cold pass asks every plugin about every file and set and serves nothing
from the cache. It lists each answer that differs from what the cache
would have served (by plugin, call and the set's inputs, with the units,
digests, links or can't-tell marks that differ), which is how a plugin
whose answer leans on something its inputs don't name shows up.

The nightly's rebuild replaces each stored answer that differs, and in the
same write notes the key in a small ref of its own,
`refs/pipelinegen/replaced/<plugin>/<version>-<checksum prefix>`, with a
digest of the bytes it left there. A plan that finds an answer in its
local directory checks that record first (one small fetch per plugin), so
a worker that kept the wrong answer reads the replacement instead.

The cache is never trusted across plugin versions or checksums, and it is
only a cache: losing it changes nothing but time. Never cached: a run's
observations, `collect`, and answers marked `"cacheable": false`.

The plan records, per plugin and call, how many files it was asked about
and how many came from the cache, so a slow plan can be explained.

The core may assume these speeds: units or links for about 20,000 units in
a few seconds; `observe` and `collect` per slice in well under a second.

### Filling caches on your machine

In a pipeline, the planner's prepare command fills the caches the plugins
read. On your machine nothing does, so a plugin that type-checks against a
module cache would find it empty, answer "can't tell", and the plan would
bind every recorded slice to everything. A plugin can therefore say in
`describe` how its cache is filled, and `affected`, `why` and `try` run that
before they plan:

```json
"fill": {"gomodcache": {"files": ["go.mod", "go.sum"], "run": ["go", "mod", "download"]}}
```

For each tree it plans (the base's and the head's), the core runs `run` in
every directory that holds the first of `files`, with the cache's variable
(`GOMODCACHE` here) pointing at the shared cache `<cache>/caches/<name>`.
A directory whose file every plugin declaring the fill leaves out with its
`exclude` is skipped, since no answer reads that cache for it.
It runs once per content of `files` in that directory: a mark in
`<cache>/caches/.filled` keeps it from running again for the same files, in
this plan or a later one, and a commit whose `go.mod` changed gets its own
run. The first time it runs for a given content it prints the command, the
directory and the cache on stderr. When it fails, it prints the cache, the
command, where it ran and the program's last lines, and the plan's warnings
say which plugins may then not tell what depends on what is missing. It
tries again next time.

What a fill may do:

- run one program, found on `PATH`, with fixed arguments. The core runs it
  as an argument list, never through a shell;
- read the files it names in the directory it runs in, and reach the
  network to fetch what they pin into the cache, as your own `go` or `npm`
  would;
- use your environment as it is (your Go, npm and git settings, so private
  modules work as they do for you), except that every variable whose name
  ends in `TOKEN`, `SECRET`, `PASSWORD`, `PASSWD`, `_KEY` or `CREDENTIALS`
  and every `PIPELINEGEN_` variable and `GIT_SSH_COMMAND` are removed. If your git reaches
  private modules with a token from the environment, the fill can't: use
  a git credential helper or a `.netrc`, or run the command yourself. A
  failed fill says so.

What it may not do:

- name a path for its program, or a file outside the directory it runs in
  (`files` are names, not paths);
- fill a cache the plugin doesn't declare in `caches`, or one the core
  sets no variable for (`gocache`, `gomodcache`, `npm`);
- write into the tree, or change what is already in the cache when it runs
  a second time over the same files: the kit runs it twice over the
  fixture and fails the plugin if the second run changed anything.

A fill runs only in the local commands, never in a pipeline's steps, where
the planner's prepare stays in charge. `--no-fill` turns it off for one
command, and `plugins[].fill: false` for one plugin. The plugin itself still
runs with `GOPROXY=off` and no network: the fill gives it what it reads,
and the answers depend on the same pinned modules as in a pipeline.

### Conformance

`pipelinegen plugin test <executable> [--tree <dir>]` checks a plugin the
way the core calls it. Every plugin repository runs it in its own CI, on a
fixture tree of its own. It checks:

- for every plugin: `describe` keeps the protocol (kind, name, version,
  narrowings with a note and `off_at` from 2 to 5, `inputs` per-file or
  per-set, no namespace for a correlator), and a call it doesn't know, or
  one of another kind, fails without saying "can't tell";
- for an sbom or a correlator, with `--tree`: its tree calls and `observe`
  run on a copy of the fixture that also holds files which leave a marker
  when anything runs them (a Vite config with a side effect, package.json
  scripts, a `go generate` line, a pytest `conftest.py` and `setup.py`, a
  Makefile), and no marker may appear. That shows only what these files
  would show: a plugin may still run something they don't tempt;
- for a plugin with a `fill`: each one names a program and no path, files
  by name that its candidates cover, and a cache it declares; with
  `--tree`, it runs twice in every fixture directory holding its first
  file, and the second run may change nothing in the cache;
- for a runner: `pipeline` turns the kit's fixture spec into exactly one
  file, the same bytes every time. The kit reads every call of
  `pipelinegen` in it (a task's own, or a line of a script). A task the
  pipeline marks as the tool's step (`PIPELINEGEN_PIPELINE_STEP`) whose
  run names no call, one that runs the runner's own program say, the kit
  runs instead, and checks the calls it makes as it checks the ones a
  task names. It runs each in the order its job does, with the inputs
  the job's earlier tasks put out or a get fetched, empty outputs and
  caches, its params, no package installs and no network, and every tool
  the spec pins already in `tools` at its pin: the runner under test in
  `tools/plugins/<name>`, the fixture spec's runner pin made the runner's
  checksum, and every other tool a stand-in that notes each call, opens
  every gate but a decision's and writes the outputs a call names. It
  runs the tasks for the spec
  and for its variants: an older tool's spec with a lock on every suite,
  per build and per batch, a pipeline that sets itself, a config that splits the token by purpose and, for a runner with
  `connector_changes`, a pull request run whose changes come from a
  connector; a planner whose prepare command has a git token of its own
  (`git-auth`); for a runner with `steps`, suites run by drivers; and for a
  runner with `steps` and `packages`, suites with packages, one of them
  with a cached image, and planner packages, where every task the runner
  writes that runs pipelinegen installs its packages first, every cached
  image the spec names is built, and the registry's vars reach no task that
  runs the repository's code (the pushing var no job that does). It runs
  the spec's tasks twice more, once with a stand-in plugin and once with
  the runner itself pinned by `sums` and no `sha256`, the list the pin
  names already beside the tool as `tools/plugins/<name>.SHA256SUMS`: each
  task must get as far as calling the tool, or exit 0, and leave the list
  matching the pin and the tool matching its line in it, so a runner that
  checks only a `sha256` fails. Run again with each such tool changed so
  that it no longer matches its line, a task that fetches the tools itself
  (one not handed `tools` as an input) and called the tool before must
  not call it, so a runner that keeps a tool with no `sha256` unchecked
  fails too. A
  runner must report `placement`; when the config the kit is
  given has a `placement`, the pipeline written with it must be the one
  written without it but for worker tags and the gets a moved step needs
  on its worker: a get the placement adds fetches a resource the pipeline
  declares as a get of the same job already does, the same in everything
  but its name and `trigger`, under a name of its own, never triggers, and
  only a task's `image` and `input_mapping` may name it, in place of the
  get it copies. The connector variant's pipeline sweeps the changes (`unplanned`),
  plans them in two steps (`plan --out`, `plan --from`), and has no
  resource of the CI system's own pull request support. Each call names a command, subcommand and flags the tool
  running the kit has. Each step mounts every directory its call reads,
  unless an earlier call of the tool in the same task wrote it, and keeps
  every directory it writes as an output or a cache, as the contracts'
  [table of steps](contracts.md#commands-the-generated-pipeline-calls)
  lists them. The directory the tool runs from, with its plugins, may
  also be an output, for a bootstrap in the task itself. A path to the
  tool counts as a call only where a command starts; it has the token where the command writes refs or statuses,
  and only the pipeline's step gets Concourse's address. Every step of the
  table appears, and the pipeline references no var but the token, the
  declared secrets and the repository's own (`concourse.repository`,
  `concourse.include`). Every task, get, put and `set_pipeline` step has a
  time limit, a timeout of its own or on a step around it, and a step in
  a hook has its own. No step runs `pipelinegen lock` or `unlock`, even
  for a spec that holds a lock. A step that runs `report
  batch-end` or `report suite` is in no `try`, and its run, given a
  stand-in for the tool that exits 4 for a report, exits 4: a step that
  could not write the run's records fails its job. Every call of the tool gets the spec's `trusted`
  string, byte for byte, in `PIPELINEGEN_TRUSTED`. A task the
  repository's own file defines gets the secrets the spec declares and no
  other var: no token, and not `PIPELINEGEN_TRUSTED`. A suite's batch task
  that runs `slice run` gets `PIPELINEGEN_TRUSTED` and no token, and no
  driver process or child of one finds either in its environment. With the token split
  by purpose, the step that plans a change's own tree (`plan --out`) holds
  the read token and no token that writes;
- for an sbom: `detect` claims only what it was asked about, each in a
  role, and claims something; `units` answers the same every time, each
  unit once, inside the declared namespaces, with a digest or a
  `digest_from` that resolves; links have both ends, a valid `while`, a
  kind that doesn't misspell a known one, and one that holds while a
  narrowing is on names it; and an answer for some paths agrees unit by unit with the whole
  tree's, which is what makes caching by content safe;
- for an sbom and a links correlator: run through the core's own content
  cache, the answers it serves warm, and warm again after one claimed file
  changed, are the plugin's own cold answers for the same tree (sets that
  share an input included), kinds and tags of every link too. After that
  change the kit compares the cold answers before and after it, and notes,
  failing nothing, every link that came or went while neither of its ends
  changed: planning counts such a link's `from` as changed, so the plugin
  costs extra selections there. A correlator's ends are judged by the
  digests of a companion sbom (`--sbom`, its config in `--sbom-config`);
  without one the check is skipped and says why;
- for a correlator whose sets name input units, with a companion sbom: each
  edit the fixture names in its kit file (`{"edits": [{"path", "old",
  "new"}]}`, the first `old` in `path` becomes `new`) is made, and the
  tree served warm through the cache equals the cold one, and the plugin
  is asked about no more files than the sets whose input files or input
  units changed hold. Name one edit that leaves an input unit's digest
  alone (a function's body) and one that changes what the links depend
  on (a body's first statement); a fixture with input units and no edits
  fails;
- for an sbom and a links correlator that claim files by detection: the
  answer is the same when every file the plugin didn't claim, read along
  with a claim, or declare as an input of a set holds other bytes, which
  is what lets the core cache it by content; and every input a set
  declares is a file of the fixture or listed in its `absent`;
- for a correlator, only the calls its `capabilities` list: `links` or
  `bind` answer the same every time and are well formed (bind's three
  files parse, and their indexes point into the id table); `observe` says
  "can't tell" for a missing part and for an empty directory, and, given a
  recording of the fixture, touches some of its units (also when the setup's
  directory, first, has another name and doesn't exist), but never answers
  an empty list for that recording against a tree it can't map it back to;
  `collect` with nothing running succeeds;
- for a connector, every call goes through the core's own exchange, with
  every check of [what the core refuses](#what-the-core-refuses), against a
  fake host that serves the responses its fixture lists. `describe` names
  only known capabilities, needs no `store` token, names a header for
  its tokens, and declares none of the headers only the core sets.
  `identify` claims the fixture's remote and disowns one on another
  host, and neither `identify` nor `links` asks for a request. `changes`
  lists changes that each have an id, a full head commit, a base and a
  `from` of `same`, `fork` or `unknown` (`unknown` for every change
  without the `forks` capability). With `forks`, the fixture names the
  changes that come from a fork, and none of them may be reported as
  `same`, since that is what would let a fork's code run. A host with no
  forks at all, a plain git server, says why in `no_forks` instead, and the
  changes it names in `same` must be reported as `same`.
  `status`, `statuses`, `subject` and `comment` answer, for the
  capabilities that need them, with statuses in the core's four states
  and a subject on one line. Every request the connector makes must find
  a response in the fixture.

The cache checks run the plugin on copies of the fixture in the kit's work
directory, which is a temporary directory unrelated to where the fixture
lives. A plugin that runs a toolchain through a version manager gets the
version that the nearest version file above its working directory names.
In a copy, that would be the machine's default rather than the fixture's.
So before it starts, the kit copies the nearest `.tool-versions`,
`.go-version`, `.node-version`, `.nvmrc` and `.python-version` at or above
the fixture to the top of its work directory. When answers about the copies
still come back marked not cacheable, or a plan over them warns, the kit
adds a note that says how many answers were marked, quotes the warnings
(a plugin that couldn't tell says why in its own words) and lists the
toolchains that `describe` reports. Each cache check that fails points to
that note. Put the toolchain version the fixture needs in one of those
files, in the fixture or above it.

A call the plugin declares and the kit never makes is a failure, since a
check that never ran shows nothing: an sbom or a correlator run without
`--tree` fails, and so does a correlator that observes when its fixture
names no recording. `plugin test` prints a line for every check it ran,
and `skip` with the reason for one its options left out.

A connector's fixture is the `connector` section of
`.pipelinegen-kit.json`. Wherever it says `{server}`, the kit puts the fake
host's address:

```json
{"connector": {
  "remote": "{server}/team/app.git",
  "config": {"api": "{server}/api"},
  "responses": [
    {"method": "GET", "path": "/api/pulls", "query": "state=open",
     "status": 200, "body_file": "pulls.json"}
  ],
  "calls": {"changes": {"base": "main"},
            "status": {"commit": "1111...", "context": "ci/main", "state": "pending"}},
  "forks": ["pr-8"], "same": ["pr-7"]}}
```

`config` goes over the plugin's config block, so a connector whose API
lives on another host (`api_hosts`) can be pointed at the fake one.
A plain git connector's fixture has no responses: its `config` holds the
`heads` the core would list with git, and `"no_forks"` says why no change
comes from a fork.

`responses` answer by method and path, and by query when one is given; a
response listed twice for the same request answers the first request and
then the second, and the last one keeps answering after that. `calls` are each call's arguments. The tokens are canary
values, and no answer or message may carry them.

Answers in work files are read like stdout. A bind correlator's fixture
names the slices to bind in `.pipelinegen-kit.json` at the tree's root
(`{"slices": [...]}`, as in the `bind` request), and a correlator that
observes names a recording of its fixture there
(`{"observe": {"slice", "dirs": [the setup's, then the slice's parts]}}`,
paths relative to the tree). A correlator that collects names a recording command
too (`{"record": {"slice", "run": [argv], "parts": 2}}`): it runs one part
of the slice as the batch's test runner would, and leaves what the
plugin's `collect` gathers where the plugin looks for it. It runs as a
batch task does: in a directory holding the tree as `source/` (a copy of
the fixture) and the batch's coverage output as `coverage/`, with
`PIPELINEGEN_RECORD=true`. A task writes a slice's output into
`coverage/<slice>` by convention, and every part of the slice writes
there: no variable names that path, in a real task or in the kit. The kit
also sets `PIPELINEGEN_SLICE` and `PIPELINEGEN_PART` (1, 2, ...) for the
fixture to name the slice and tell its parts apart; a real task gets
neither. The kit calls `collect` and `observe` for it as the batch's
steps do: from the batch's directory, into directories named after the
plugin's entry in the fixture's config (`"name"` at the top of
`.pipelinegen-kit.json`, default the plugin file's name without its
extension), with the suite's `stack` (`"stack"` in the entry, default
true) and the plugin's config block as a pipeline gives it (`"config"` in
the entry, paths relative to the batch's directory; default the kit's
`--config`). A plugin that collects with a stack and without lists one
entry for each (`"record": [{...}, {..., "stack": false}]`); the kit
checks each. For stack mode, an entry names the batch's stack too:
`"serve": [argv]` and `"ready": "<glob under the batch's directory>"`.
The kit starts it in the batch's directory, in a process group of its
own, before the setup's end, waits for the ready file (never a fixed time;
two minutes at most), runs the parts against it, and stops it with
SIGTERM after the last collect. The check fails when the stack isn't
ready, exits before the last collect, doesn't stop within ten seconds, or
leaves a process of its group behind; whatever is left is killed.
The kit runs it `parts` times (at least two) for one slice in one batch,
calls `collect` as the core does, the setup's end into
`<coverage>/_setup/<plugin>` and each part into the directory the core
names for it (`<coverage>/<slice>/<plugin>`, then
`<coverage>/.pipelinegen/part-<k>/<slice>/<plugin>`), with `stack` set,
and observes the setup with every part. It fails when a part's directory
is empty or missing, when `collect` fails, when `observe` can't tell from
the parts together, or when the parts together tell fewer units than one
alone. The core and the kit hand plugins a tree by its
real path, with symlinks resolved, so a plugin compares the paths in a
recording with the tree's as they are. A slice "recorded, nothing
touched" needs evidence only the plugin can make, so each plugin tests that
case itself. With the kit's run, the token variable holds a canary value; a
plugin whose answers or messages carry it fails.

## sbom plugins

An sbom plugin answers `units` for a tree:

```json
{"protocol": 1, "work": "/tmp/w", "cache": "/c", "tree": "/path/to/tree", "config": {...},
 "paths": ["internal/notes/notes.go"], "claimed": [{"path": "internal/notes/notes.go", "role": "source"}]}
```

with `{"units": [...], "links": [...]}`, or per set as above. The answer
doesn't depend on the narrowing level: units a narrowing adds (field units
of a type, say) are always there, and the core binds the coarser unit
while that narrowing is off.

A **unit** is something a change can touch and a slice can depend on:
`{"id", "group", "digest", "file", "kind", "executable"}`.

- Its id is namespaced. `file:<path>` is the core's own namespace: every
  tracked file is a unit, so `file:<path>` is always a valid link end (an
  sbom's `changes` links from a migration's file to what it defines). A namespace is named for what owns its meaning and
  belongs to the sbom that describes it; correlators link into it by the
  documented ids: `go:<package dir>#func:Name` and `dep:go:<module>`
  (go-sbom), `ts:<file>#fn:name` (ts-sbom), `dep:npm:<name>` (npm-sbom),
  `pg:<kind>:<schema>.<name>` (pg-sbom: `pg:table:public.notes`,
  `pg:view:...`, `pg:function:public.f(integer, text)`, `pg:sequence:...`,
  `pg:extension:citext`, and `pg:data:public.notes` for rows the migrations
  write), `api:<operationId>` (openapi-sbom), and `file:<path>` for plain
  files (the core's own).
- Its digest changes exactly when the unit's behaviour can change (for Go,
  the printed syntax tree without comments).
- Its group (a Go package's directory, a file) lets the core bind a whole
  group at once.
- `executable` says whether coverage can ever see it run (a type or a
  constant can't).
- `entry` names a test entry function the way a test runner selects it
  (`"entry": "TestCreate"`; for Go, functions named `Test`, `Benchmark`,
  `Fuzz` or `Example` in a test file, never `TestMain`). A slice that names
  the tests it runs starts only from the entries among them.
- `"not_root": true` keeps a unit from being a starting point of the slice
  whose file declares it, for a type whose test-local twin stands for it
  there.
- `"label"` names the unit as a developer would (`api.validate`,
  `ServerInterfaceWrapper.CreateNote`, `POST /notes` for an operation).
  `pipelinegen why` prints a reason's chain in labels; a unit without one
  shows a plain form of its id. It changes no digest and no selection.
- Instead of a digest of its own, a unit may carry `"digest_from": [ids]`:
  its digest is then the combination of those units' digests. An sbom
  offers name-only aliases this way (`pg:relation:public.notes` over the
  table or the view of that name, `pg:function:public.f` over all
  overloads), and one domain's unit can take its digest from another's (a
  Go variable that embeds an API contract changes exactly when the
  contract's operations do).
- A unit of kind `toolchain` (the Go version, Node, a bundler) reaches
  everything compiled with it. Not in a release yet: the core treating
  that kind apart, so that a change to one samples every suite that
  compiles with it.
- Anything else the plugin knows about a unit (an npm package's `direct`,
  `dev`, `versions`) goes into one `"attributes"` object. The core ignores
  it and keeps it for documents and reports.
- `"cannot_tell": [{"unit", "reason"}]` works as in `links`: the units
  whose content the plugin couldn't read bind wide.
- `"complete": [paths]` names the files whose units hold everything a
  test can observe of them when they run as code. The plugin says it only
  where it knows: the file parsed without errors, and no comment or other
  text its digests leave out is one a tool reads (a pragma, a magic comment
  of a bundler, a coverage directive). A file left out is not complete. A
  file that is complete in both trees, under the same pin of the plugin,
  with every unit as it was, changed nothing observable: in a change of
  only such files it reaches no slice of a recorded suite
  (`unobservable-edits`, off from 4; see
  [narrowing](how-to/narrowing.md#files-that-changed-nothing-observable)).
  The cold rebuild compares it like the rest of the answer.

The `links` of an sbom's answer are the structure among its own units: a
view reads its tables, a module requires another, an operation refers to a
component. They follow the rules for links below.

An sbom for dependencies also answers `document`: the standard bill of
materials (CycloneDX) on request. Not in a release yet: the core calling
`document`.

Bazel already hashes each Go module and npm package it fetches, so a
lockfile is bound one way only: with an sbom configured, through its units,
and never also through `everything_on` or `sample_on`. Not in a release
yet: the check refusing a lockfile in both.

## Correlators

A correlator owns no units. Its calls are `links` or `bind`, `observe`,
`collect` and `why`.

A unit a recording or a link names in a namespace no configured sbom
lists (`dep:npm:react` with no sbom reading the lockfile) can't be told
unchanged: the slice whose list holds one runs on any change, and its
reason names the unit and says to configure an sbom for its namespace. A
slice bound to every change always says why, in one cause: it has no
recording yet; a correlator couldn't tell (named); an sbom couldn't tell
about a changed file (named); it reaches a unit that can't be told, with
the plugin that said so (the one whose `cannot_tell`, link or
`digest_from` it comes from, not the sbom that lists the unit) and its
reason; or it touched a unit no sbom lists.

Planning walks links from what changed and never asks `bind`. A
correlator that offers `bind` and not `links` (one written for planning
that bound forward) would add nothing to the walk, so the core takes it as
one that couldn't tell about the tree: every recorded slice it could bind
runs on any change, the plan warns `<name> <version> offers no links: ...;
pin a release of <name> that offers links`, and `pipelinegen check` fails
on it (`bad-config`), asking only its pinned `describe`. A correlator that
only observes (statements a test ran, say) offers neither and is fine.
What `observe` can read is fixed by the
[step that observes](#what-the-step-that-observes-can-see).

### Links

A **link** `{"from", "to", "kind", "narrowing", "while", "if_linked",
"ref"}` says that when `from` is bound, `to` is bound too. The core follows links forward for the
`executed` width and in reverse as well for `dependents`. Every kind runs
the same way, from what is bound first to what it brings along:

| Kind | from | to | Example |
|---|---|---|---|
| `uses` | the user | the used | a handler to the function it calls |
| `reads` | the reader | what it reads | a query's code to `pg:table:public.notes` |
| `writes` | the writer | what it writes | an insert's code to the table |
| `generates` | the product | its producer | `pg:data:public.notes` to the seed code that writes the rows, so a reader of the rows binds the seed |

A change to a table's definition reaches its readers and writers; a change
to seed rows reaches their readers only. Any other kind is a plain link.
A kind that spells a known one differently (`Uses`, `dependents_only`)
fails the plan: as a plain link it would be followed further than the
plugin meant.

Three typed kinds say how far a link carries, for a correlator whose
links stand in for its `bind` answer:

| Kind | At the `executed` width | At the `dependents` width | Example |
|---|---|---|---|
| `follow` | binds the target and carries on from it | the same | a reference to a type, a constant, a variable or a test function |
| `bind` | binds the target and stops there | binds it and carries on | a reference to a function without statements; a method reached through its type's method set |
| `dependents-only` | nothing | binds it and carries on | a reference to a function with statements, which binds only where it ran; an interface's implementations; a type's methods |

Planning walks the typed links, with their kinds and tags, outward from
what changed ([the stored graph and the walk](internals/graph.md)). A
correlator may also answer `bind`: a build with the `pipelinegen_forward`
tag binds forward from `bind` answers and plain links, for the plugin
proof's comparison.

A link may end at an id in two namespaces the core owns, which no sbom
lists: `group:<group>`, a group as units name it (a Go package's
directory), and `node:<namespace>:<rest>`, a junction that is no unit (a
directory's test binary `node:go:test:<dir>`, the meeting point of an
interface method and its implementations). Neither has a digest, so a
link into one changes nothing by itself; a `follow` link into a group
says the group is linked. Such an id is never a dangling end. A typed
link to any other id no plugin describes is one, as for a plain link: its
source binds wide, and the plan warns, naming the plugin and the link.

`if_linked: true` says a link holds only for slices whose binaries link
the group of `to` (an implementation exists only where its package is
linked in). The core stores the flag and, for now, treats the link as
always holding, which can only select more.

`ref: true` marks a link that comes from a reference in the source (a use
of a name, a call), whatever its kind; method-set and implementation
links never carry it. Only a reference makes a new name count as bound.
Typed links that agree in `from`, `to`, `kind`, `narrowing` and `while`
become one link. It is a reference if either was, and it carries
`if_linked` only if both did.

`narrowing` names the narrowing a link belongs to, which may be another
plugin's (the contract correlator's links carry the contract sbom's
`document-units`): its `off_at` comes from the plugin that declares it, and
a plan whose links carry a narrowing no configured plugin declares fails,
naming both. `while` says when
it holds: `"off"` (the default) for a wide link that stands while the
narrowing is off, `"on"` for a narrow link that replaces it while the
narrowing is on. A plugin always answers both, tagged, typed links
too; the core drops the ones the level rules out. So one cached answer serves every level, and the
core can report what a narrowing dropped.

`links` takes the same request as `units` and answers `{"links": [...]}`.

A correlator that sees a unit's content derive from other units (a
generator writes declarations covering every operation of a contract) says
so in the same answer: `"digest_from": [{"unit", "from": [ids],
"narrowing"}]`. The core then takes that unit's digest from the sources,
as for an sbom's own `digest_from`, so adding one operation changes only
what uses that operation. It belongs to a narrowing (off from 5), since a
hand edit to generated code is no longer seen while it is on. A set may
carry its own `digest_from` the same way, for the units of its files; it is
then cached with the set. One in the answer itself can't be tied to a
file, so a correlator that answers one is asked about every claim on every
plan.
It may also hold `"cannot_tell": [{"unit", "reason"}]` for units whose
links the correlator can't work out (SQL built at run time, say) while the
rest of its answer stands; the core binds those units wide. Not in a
release yet: counting them in the check's report.

### Bind

When the closure needs the correlator's own knowledge (Go's types, method
sets and interfaces can't cross a process boundary cheaply: a type-checked
backend is hundreds of MB in memory and tens of MB of edges), it answers
`bind` instead of plain links:

```json
{"protocol": 1, "work": "/tmp/w", "cache": "/c", "tree": "/path/to/tree", "config": {...},
 "narrowings": ["method-sets"], "widths": ["executed", "dependents"],
 "slices": [{"suite": "api", "name": "api/notes_test.go",
             "sources": ["api/notes_test.go"], "tests": ["TestCreateNote"],
             "list": ["go:internal/notes#func:Create"], "depth": 0}],
 "roots": {"api/notes_test.go": ["api:createNote"]},
 "setup": {"api": ["go:internal/server#func:Start"]}}
```

It answers `{"files": [...]}`, JSON Lines in the work directory:

- `units.jsonl`: the id table, one unit id per line; the other files name
  units by their line number;
- `bind.jsonl`: one record per slice and width, `{"suite", "slice",
  "width", "whole_groups": [group], "linked": [group], "everything",
  "units": [[index, kind, via index, [via indexes]]], "dropped": {"<narrowing>": [index]},
  "dropped_groups": {"<narrowing>": [group]}}`: the bound units (whole
  groups marked as such, so hundreds of slices stay small), why each is
  there, `everything: true` for a slice without a list, and per narrowing
  in force what the wide rule would add. `linked` lists the groups whose
  unbound names the slice depends on: the groups its binaries link, while
  `unreached-new` is off, and none while it is on. The core makes the slice
  depend on the list of those names, so a name that appears or goes
  selects it and a changed body of a name nothing binds doesn't.
  `dropped_groups` are the groups whose unbound names a narrowing's wide
  rule would make the slice depend on. `dropped` and `dropped_groups` are
  what the plan's `narrowed` and `pipelinegen why` stand on: a slice the
  plan left out while a unit in its `dropped` changed, or the unbound
  names of a group in its `dropped_groups` (as that narrowing's wide rule
  counts them), was left out by that narrowing. A unit's kind is the correlator's word,
  except `init`, which the core reads as "bound because it runs when a
  linked package starts". The fourth element, optional, lists every unit
  of the slice that brings this one along; the `sample-bound` narrowing
  moves a unit to the suite's sample only when it knows them all, so a
  unit without that list stays each slice's;
- `unbound.jsonl`: per group and width, `{"group", "width", "names":
  [ids], "narrowed": {"<narrowing>": [ids]}}`, written at every level:
  `names` are what nothing binds under the narrowings in force, and
  `narrowed` the names a narrowing in force alone counts as bound
  (`referenced-new` counts a name a bound unit references), which its
  wide rule adds back. A correlator that leaves the file out while
  `unreached-new` is on still works: the core then counts a group's
  unbound names itself, as its units no slice binds.

`setup`, when the setup split is on, is per suite what its setup ran,
which binds the suite's setup target rather than each slice; the answer
binds it as the slice `_setup` of that suite, in both widths. With the
split off the core adds the setup's units to every recorded slice's
`list` instead. `narrowings` names the narrowings in force at the plan's
level; a correlator binds under those and reports in `dropped` what each
one left out. `roots` carries
units another correlator's link made this slice's: when a
bound unit has a link into a namespace another correlator binds, the core
passes that unit to the other's `bind` as an extra root, and repeats until
nothing new comes. A correlator's roots never hold what it bound itself,
so its answer has to be the whole closure of the list and the roots. The
core asks it again in the same tree only when its request changed: a
correlator nothing new reaches is asked once per tree.

### Observe

`observe` turns what a slice left behind into the unit ids it touched. It
runs inside the batch task, where the built bundles and source maps exist:
`pipelinegen slice end` calls it for each correlator that observes the
suite (plugins
travel in `tools` to batch tasks as well as to planning), and the ids are
stored with the slice, the setup's apart.

```json
{"protocol": 1, "work": "/tmp/w", "cache": "/c", "tree": "/path/to/tree", "config": {...},
 "slice": "e2e/editor.spec.ts", "dirs": ["coverage/_setup/go", "coverage/e2e/editor.spec.ts/go"]}
```

`claimed`, when present, lists the plugin's own claims in the tree, as in
`units` and `links` (worked out once at the batch's setup end and kept for
its slices): a correlator that needs files to map what it saw (a bundler's
config, a source map) reads those, and walks nothing. A plugin reads it when
present; older ones ignore it. `collect` requests carry it too.

It answers `{"touched": ["go:internal/notes#func:Create", ...],
"setup_like": [...]}`. `dirs[0]` is always the setup's directory and
the rest are the slice's parts: a plugin tells the setup by its position,
never by its name. The setup's directory may be empty or not exist at all
(a batch whose setup recorded nothing); that is fine. A missing slice part
is "can't tell". `touched` includes what only the setup
ran, and `setup_like` is that part plus what ran only as loading (a module's top level while a page loaded, an init): with the
setup split on, the core treats it as setup, which a sample covers; with
the split off it binds the slice like the rest. Any correlator may answer
it. A suite may name the correlators that observe it
(`suites.<name>.correlators`); then only those are asked to collect and
observe its slices, and another one's "can't tell" never marks them.

An empty answer needs positive evidence of a finished recording with
nothing in it: coverage files with zero counts, an existing statement or
request log with no entries, a finished marker. A directory with no
evidence that recording happened (no files at all, as a task that forgot to
record leaves behind) is "can't tell" (exit 3), like a missing one, and the
slice runs on every change until it is recorded again; treating it as "touched nothing"
would quietly empty the slice's list. So is an observation
the correlator can't map back to the tree: a recorded script whose hash
differs from the built file, a browser whose coverage it can't read, a
script that ran transformed without a source map. Data that can't be read
or parsed exits 1: the slice errors and its list stays as it was. A
touched id that matches no unit in the tree binds nothing, so a correlator
may answer one candidate per schema on a search path without knowing the
sbom's model. Not in a release yet: counting such ids in the check's
report. A slice may leave
several parts between one start and one end (a browser batch runs one
project after another); `dirs` lists them all, after the setup's.

### Collect

`collect` manages the files a batch's slices leave behind while the batch
runs. A task writes its coverage into the slice's directory as contracts (b)
says; at the slice's end each collector moves what belongs to it into its
own `<plugin>` directory, which `observe` reads. That directory is always
a direct child of the slice's coverage directory, so a collector may move
the files it recognises from the parent into its own and must leave
everything else where it is. `pipelinegen slice setup-end`, `slice start`
and `slice end` call the `collect` of every correlator that observes the
suite, in the config's order, each bounded by the entry's
`collect_seconds` (10 by default) and, in a batch run by drivers, stopped
30 seconds before the batch's time limit:

```json
{"protocol": 1, "work": "/tmp/w", "config": {...},
 "action": "setup-end", "slice": "e2e/editor.spec.ts", "dir": "coverage/e2e/editor.spec.ts/go"}
```

`action` is one of three values. Where the test's processes write their
coverage on request, each touches a ready file in the plugin's coverage
directory every time it polls its request file; `collect` treats a ready
file untouched for 2 seconds as a process that's gone, and waits for
answers 8 seconds by default, under the core's 10. No ready file at all is
a success with nothing to do for a plain test batch, and a failure when the
request's `stack` is true (the suite's `stack: true`), so a forgotten
build tag errors the slices instead of quietly marking every one of them,
which would bind each to everything.

- `setup-end` comes once, before the batch's first slice. It closes what
  ran during setup (start-up, seeding) into `coverage/_setup/<plugin>`, so
  each correlator keeps setup apart from what a slice's own steps ran. The
  setup split and the sample depend on it.
- `slice-start` marks where a slice's part begins (an offset in a statement
  log, say).
- `slice-end` writes what the slice touched into `dir`: the part of a log
  since `slice-start`, or the coverage counters a test's own process wrote
  when asked through its request file.

A failed `collect` errors the slice and names the correlator. Collectors
may rely on the batch model: a batch runs its slices one after another
against its one stack, never two at once, so a log or a counter set read
between slices belongs to exactly one slice.

### Why

Not in a release yet: the core calling `why`; `pipelinegen why` gives its
reasons from the plan without it.

`why` explains a link: `{"from", "to"}` in, `{"path": [{"unit", "note"}]}`
out, for `why` and the plan's reasons.

### Small correlators

The protocol is meant to make narrow ones cheap: links found by scanning
for a key in two languages, observations read from a log. Some that fit:

- events: a message subject's producers and consumers, linked through the
  constant that names the subject;
- message catalogs: a change to one key's text selects the tests that
  render that key, through the translation calls in Go and in React;
- seed and scenario data per record: rows, which matter more than tables;
- webhook fixtures, by event type.

## What the core does with them

For each tree it plans (base and head, with the same lists), the core:

1. asks every sbom for `units` and every correlator for `links`, from the
   cache where it can, and keeps the answers as the stored graph;
2. compares the two trees' units by digest and walks the links backwards
   from the units that changed to the slices whose roots they reach: the
   units a slice touched last time, its own files' units and the units its
   data names; at `follow: executed` along the link kinds that bind, at
   `dependents` along every kind;
3. asks bazel-diff, and adds the slices whose Bazel inputs that no plugin
   covers changed (an input that is a file an sbom has units for counts
   through those units).

Tracked paths that a filesystem folding case or normalising Unicode (a
Mac's, by default) holds as one file, `README.md` beside `readme.md` or a
composed `café.md` beside its decomposed form, go to no plugin: what a
plugin reads there for one may be the other's bytes. A change to one is
planned as a change to a file no plugin claims, on every machine alike,
and the plan names the pair once. Without Unicode's tables the core
compares names conservatively: two names that only look alike outside
ASCII may be taken as one too, which plans them wide and never misses a
real pair.

### A dependency on a file that has units

Tests often depend on a whole file whose parts an sbom describes: every
backend test's build reaches the embedded migrations, tests read or embed
an API contract, a Go variable embeds files. A file has units when an
sbom's `detect` claims it with `"by_units": "<narrowing>"`; its units name
it as their `file`, and a link of kind `changes` from `file:<path>` to each
unit says what the file defines.

A file whose units are all as they were, and which its sbom says its
units cover whole (`complete`), comes before that: it reaches no
recorded slice through a label that nothing else the change touched
reaches, with or without a list, since nothing in it changed for anyone.

While the narrowing is on, the core replaces a recorded slice's
dependency on such a file by the units of that file the slice was
observed to touch, plus the file's whole unit (`api:<file>`, say) where a
correlator says the reader takes the file whole. Which dependency, and for
which slices, depends on whether the claim names an anchor:

A recorded slice's filegroups are opened down to their files first (see
[narrowing](how-to/narrowing.md#files-inside-filegroups)), so a file
inside a filegroup, or a filegroup inside it, is judged like a label
naming the file.

- With `"anchor": "<unit id>"`, a unit of the sbom's own with a digest
  that never changes, which a recorder puts in a slice's list whenever it
  ran for that file (for a message catalog, `locale:<catalog dir>`): every
  path to the file is replaced for a slice whose list, or its suite's
  setup part, holds the anchor. That is a label naming the file, a
  filegroup holding it, `extra_deps` and an owner's `data`; inside
  another rule (a genrule, say), bazel-diff, run once more on the head
  with the base's copies of the files a group of slices takes, says which
  labels anything else that changed reaches (`label_groups` in the plan
  counts those runs). A slice without the anchor keeps every path whole:
  its recording doesn't cover the file.
- Without an anchor, a label naming the file is replaced in a suite whose
  recordings hold units of the sbom that lists the file's units, as
  judged from the slices' lists. A file found inside a filegroup is
  replaced only where those recordings hold units the sbom lists for
  files of the same directory. In a suite whose correlators record none,
  a list can't say which units a slice used, so the label stays whole,
  and the plan warns once per suite and narrowing
  (`by-units-unobserved`). Through other rules the file stays whole.

A file added, removed or renamed in such a group reaches the suite's
setup part, where the setup list holds units its sbom lists for that
directory and the setup split is on, or else every slice that holds it.

Some paths are never replaced. A test file's own `pipelinegen:data` lines
say that this test reads those files, so their labels stay whole; so do
the files the suite's `text_inputs` say a test reads as text. An anchor
proves that the recorder ran, not that every read of the file went
through it: a unit test that reads an imported catalog object outside the
recorded lookups holds the anchor and still reads the file whole, and is
declared in `text_inputs`. Not in a release yet: replacing a link into
`file:<path>` for a slice that holds the anchor.

An embedding variable's digest comes from the units it embeds through
`digest_from`, on the same narrowing. While the narrowing is off, the
dependency is the whole file. The narrowings are the sbom's:
`migrations-by-table` (off from 5) for migrations, `document-units` (off
from 2) for contracts. A slice without a list keeps the whole file.

A link to a unit that doesn't exist in the tree is dangling: a naming
convention that didn't hold. Planning binds its `from` unit wide, as if the
narrowing the link stands for were off, and the plan says why; planning
never treats it as unlinked. Not in a release yet: the check reporting
dangling links per plugin. The ladder comes from what
the plugins declare: a narrowing's `off_at` is the lowest level that
switches it off, and the core refuses a narrowing without one.

### Verifying the selection

Not in a release yet.

"Everything needed and nothing more" can't be proven for arbitrary code. So
every run that selects everything compares, per slice, what it touched
(every correlator's observation) with what it was bound to:

- A unit touched but not bound is a hole. The run fails, naming the plugins
  and the units.
- A unit bound, changed in a period, and never changing a result counts
  toward over-selection, reported per plugin and narrowing.

## Runners

A runner turns the config's runs and suites into one CI system's pipeline.
It owns everything specific to that system: task files and their inputs and
outputs, steps repeated over a list known only at run time, caches, hooks that report a broken build, the bootstrap step, and
the repository's own parts merged in.

- `describe` adds what the CI system can do: steps repeated over a runtime
  list, caches that stay with a job, a native rerun. A runner also
  says, in `cache_scopes`, how widely each cache its pipeline keeps is
  shared, by the cache's name: `job` (one job's), `pipeline` (every job's)
  or `worker` (every job's on a worker). The plan's cache holds answers
  and records that later plans read, so a pull request run's plan cache
  shared beyond its job would let a proposed change leave what a trusted
  plan reads.
- `pipeline` takes the spec of the config (`plugin.Spec`: runs and suites
  as the core reads them, and the CI settings only the runner interprets)
  and answers the pipeline: `{"files": ["pipeline.yml"]}`. It may add
  `"notices"`, a list of one-line strings for what a person should know
  of the pipeline short of a refusal (a job its placement sends to
  untagged workers, say), left out when empty. `pipelinegen pipeline`
  prints each once on stderr, `pipelinegen pipeline: runner <name>:
  <notice>`; `pipelinegen check` reports each as a `runner-notice`
  warning, which fails nothing; the kit checks that each is one line.
- A run whose changes come from a connector has `connector` (its name)
  and `sweep_every` on its trigger. A runner that builds the chain for it
  says so with the capability `connector_changes`, and lists in
  `resource_types` the CI system's own types the chain needs (Concourse's
  bundled `mock`). `pipelinegen pipeline` refuses such a run with a runner
  without the capability, and when the config's `concourse.mock` says the
  Concourse lacks a type the runner needs.
- The spec's `ssh_keys` name, by purpose (`read`, `store`), the CI var of
  each ssh key, when a remote is reached over ssh. A runner with the
  capability `ssh_keys` gives each pipelinegen step the key of each
  purpose it gives the token of, in `PIPELINEGEN_SSH_KEY_<PURPOSE>`, and
  none to a task of the repository's. `pipelinegen pipeline` refuses a
  config with an ssh remote for a runner without the capability.
- Where each step runs is the runner's to say, in its own words: the
  spec carries no worker tags, and the runner reads its placement from its
  own config block (the `config` of its entry, in every call's request).
  A runner reports the capability `placement` to say it does;
  `pipelinegen pipeline` refuses a runner without it, which would put
  every step on untagged workers. A placement moves steps between workers
  and changes nothing else in the pipeline but the gets a moved step
  needs fetched on its worker. The runner refuses a placement
  it can't follow (an unknown kind of step, a run or suite the spec lacks,
  a rule an earlier one makes unreachable) when asked for the pipeline,
  and `pipelinegen check` asks it when it is at hand.
- The spec's `planner_packages`, `images` and each suite's `packages` say
  what the steps need installed and which cached images to build and run
  in, with the keys the core worked out. A runner that installs them, and
  builds and runs in the images, reports the capability `packages`; one
  without it ignores the fields and writes the pipeline it always did,
  and `pipelinegen pipeline` and `check` say the steps run without the
  packages. The fields are in the contracts'
  [packages in the spec](contracts.md#packages-in-the-spec), and what a
  runner does with them in [writing a runner](writing-plugins/runner.md#packages-and-cached-images).
- The spec's `trusted` is a string the runner passes on without reading
  it. Every task that runs pipelinegen itself gets it, byte for byte, in
  `PIPELINEGEN_TRUSTED` (`plugin.EnvTrusted`). No task that runs the
  repository's own scripts gets it. The batch task of a suite with steps
  gets it, because pipelinegen is that task's entry point and takes it out
  of every driver's environment before a test runs ([drivers](#drivers)). It holds the keys that say where runs
  read and write, which credential serves which purpose and which binaries
  a step runs, and a proposed change's steps use them in place of the
  change's own (see
  [what a proposed change's config can't change](how-to/secrets.md#what-a-proposed-changes-config-cant-change)).
  It names vars and pins and holds no secret, so it can stand in the
  pipeline as a literal. The same config always gives the same string.

`pipelinegen pipeline` calls the configured runner. The pipeline's steps
call the core's commands, which know no CI system: `plan`, `tree`,
`batches`, `slice start` and `slice end`, `report` and `lists`.
Each runner documents how its system hands a batch task its inputs.

## Drivers

What is released of drivers is in [one place](concepts/drivers.md#what-is-released).

A driver runs a suite's tests. It is the one kind that executes the
tree's code: given slices, it invokes one test tool for them, and a driver
may also prepare what a suite's batches share and start what a batch's
tests need. A suite names its drivers as an ordered list of `steps`
(prepare, set-up, run); `pipelinegen slice run`, in the batch task, runs
the list and hands each run step the batch's slices routed to it.
[Writing a driver](writing-plugins/driver.md) has each call's request and
answer.

- `describe` adds `verbs` (the calls it answers: `prepare`, `setup`,
  `run`), `routes` (what it can run: `files` patterns or Bazel `kinds`),
  `apart` (whether a bundle still leaves one recording per slice),
  `recording_forms`, and `settings` (each key a step's `with` may give,
  with its type), and `results` (`"exit"` for a driver that can only
  judge by an exit code, as the command driver's `each` does), and
  `validates` (whether it answers `validate`), besides
  `needs`, `caches` and `capabilities` (protocol 1 defines none). A driver
  reads one JSON value from stdin and never waits for its end
  (`plugin.ServeDriver` in Go), and ignores fields and event kinds it
  doesn't know.
- `run` takes the slices, `batch`, `record`, the step's settings as
  `config`, the set-ups' variables as `env`, the task's `deadline` (its
  time limit, the suite's `minutes`), and
  per slice the directories for its extras and its recording; in a
  recording run of a stack suite also `marks`. It answers
  JSON lines, a `start` (optional) and an `end` per slice as they happen;
  an `end` carries `passed`, `tests`, `failures`, `seconds`, `recorded`
  and `replayed`. Exit 0 means the driver did its job, whatever the tests
  did.
- `prepare` takes the same request without slices, plus `prepared`, the
  directory it fills, and answers by its exit code.
- `setup` takes the same request without slices, writes one line
  `{"env": {...}}` when what it started is ready, and keeps running until
  pipelinegen closes its stdin at the batch's end (then SIGTERM, then
  SIGKILL to its process group).
- `validate`, for a driver that says `validates`, takes `steps`, each a
  verb and its settings, and answers one result per step with its
  `problems`, each a `place` in the settings (`checks[2].timout`) and the
  `problem`. It is the check every verb makes of its settings before it
  runs anything, without the machine: `pipelinegen check` calls it.
- `render`, for a driver whose `describe` names `renders`, writes a
  suite's run step in a CI system's own steps, so that system runs the
  test tool itself instead of pipelinegen's `slice run`. A runner calls it
  while it writes the pipeline, never a step. `renders` lists each
  `target` (a CI system, such as `piperun`) with `versions`, a semver
  range of the target's versions, and `plugins`, the target's own plugins
  the steps use with a range each. The request names the target and its
  version, the suite, its run step and set-ups as the config writes them,
  the `shape` (`pool` or `batch`), `record`, `observed_by`, `image`,
  `packages`, `params`, and the names of the batch's `secrets`, never
  their values. The answer is the target's `steps`, how they report each
  slice's start and end (`marks`: `protocol` or `file`), the directory of
  their per-slice `results`, and the `caches` they mount, by name.
  [Rendering](writing-plugins/driver.md#rendering) has the shapes.

The rules every driver keeps, which the kit checks:

- `batch` is the kind's setting, the same on every run step, and no driver
  refuses either value. With `false`, pipelinegen calls `run` once per
  slice; with `true`, once per run step, and the driver bundles the slices
  as its tool allows. Results and recordings per slice are the same either
  way.
- A slice's result comes from this invocation's own record of it
  (per-test events of `go test -json` or Playwright's JSON reporter,
  Bazel's `testResult` build event), never from an exit code alone or an
  earlier batch's files. A slice with no record of its own is not passed.
  A slice whose code didn't build is failed with the build error and not
  run again. The command driver's `each` without a result file is the one
  exception.
- A driver never runs a slice again by itself, and holds its `deadline`
  itself.
- Nothing forces re-execution: no `-count=1`, Bazel's result cache on. A
  replayed result is a result, and its stored recording the recording.
  The one exception is a stack suite (`"stack": true` in every request):
  its slices are never replayed, since the servers they talk to are no
  input of the tool's cache.
- No `-race` or `-count=1` by default: they are settings, off. A test file
  asks to run alone with a line `//ci:alone`, and `//ci:race` means alone
  with the race detector; the driver reads the line.
- A recording is one per slice, in the tool's own form (a Go coverage
  profile, LCOV), never a whole bundle's handed to each slice. A recording
  run outside a stack suite bundles only for a driver that says `apart`.
  Given `marks` (a
  recording run of a suite with `stack: true`), a driver runs the
  bundle's slices one at a time and calls `pipelinegen slice mark start`
  and `stop` at each boundary, which return once the stack's coverage for
  the slice is collected.
- A slice a bundle left without an `end` is run again in a call of its
  own, and errored if it still has none.

A driver comes from the batch task's `tools` input, fetched and checked
like every plugin, and `slice run` refuses one whose checksum isn't the
trusted pin and checks every step's `needs` against the image first.
The batch task of a suite with steps gets `PIPELINEGEN_TRUSTED`, which
`slice run` needs to read the steps from the trusted config, and no token.
pipelinegen is that task's entry point and takes every token and
`PIPELINEGEN_TRUSTED` out of each driver's environment before any test
runs, so no driver and no test finds them; the string holds no secret
anyway, since it names vars, pins and refs. A driver gets the suite's
`secrets.batch` (`secrets.prepare` in a prepare step) and nothing else,
and the core never calls one while planning. In a proposed change's run the steps and their settings come
from the trusted config, like the plugin entries.

`pipelinegen check` refuses, from the config and the pinned drivers'
descriptions: a suite with both `steps` and task files; a step whose
driver has no entry or doesn't answer the step's call; a slice no run step
takes, or that two take; a `take` outside the driver's `routes`; for a
driver that validates (the command driver does), whatever its `validate`
refuses, at any depth of the settings and named by its place in the
config (`suites.lint.steps[2].with.checks[2].timout: unknown key; the keys
of a check are name, run, timeout, need`); for any other driver, a `with`
key it doesn't declare, or a value of another type; `batch` or
`take` on any step but a run step; a prepare or set-up step after a run
step; a suite with steps and no run step; a recorded suite whose run driver and correlators
share no recording form (a warning while a correlator declares no forms).
A suite with several run steps names a `take` on each. `pipelinegen
pipeline` refuses a suite with steps for a runner without the capability
`steps`.

## Connectors

Not in a release yet: the core calls its connectors as plugins. The GitHub
connector ([connector/github](https://github.com/sideways-works/pipelinegen-plugins/tree/main/connector/github))
serves a config's `github` section, and the git connector
([connector/git](https://github.com/sideways-works/pipelinegen-plugins/tree/main/connector/git))
a plain git server under `connectors`. The core pins a release of each and
fetches the one a config uses, checked by its checksum.

A connector tells pipelinegen where the code lives. It lists the proposed
changes a run tests, reports commit statuses and keeps a comment on each
change, and builds web links to a commit, a file or a change. A
repository can have several connectors: one holds the code, and runs report
to every connector that is set to report. Without a connector, only branch,
timer and manual runs work.

A connector talks to a host's API, but it never touches the network itself
and never sees a token. It answers each call with the requests it wants
sent, written as data. The core checks those requests, adds the token, sends
them, and hands back the responses as files. The connector then either asks
for more or gives its answer. A connector is third-party code, so it never
holds a token it could send somewhere the core didn't agree to.

### Calls

| Call | Arguments | Answer | Token |
|---|---|---|---|
| `identify` | `remote` | `mine`, `repository`, `web`, `git_remote`, `head_ref` | none: it answers from the remote and its config |
| `changes` | `base`, `filters` | `changes`: a list of changes | changes |
| `status` | `commit`, `context`, `state`, `text`, `url` | nothing | report |
| `statuses` | `commit` | `statuses`: the newest status per context | report |
| `comment` | `change`, `marker`, `body` | nothing | report |
| `subject` | `commit` | `subject` | report |
| `links` | `commit`, `file`, `change` | a URL for each, or nothing | none: it sends no requests |

`head_ref` is the ref a change's head is fetched by, with `{id}` standing
for its number (`refs/pull/{id}/head`). When it is empty, the core fetches
the head branch and checks that it holds the commit. A change is
`{"id", "number", "base", "head", "head_branch", "draft", "labels",
"from", "updated", "ready_since"}`. `id` is the change's kind and number
on the host (`pr-7`, `mr-3`), and the core puts the connector's name in
front of it. `filters` are the run's pull request filters. The connector
may use them to list less, but the core applies them again to whatever
comes back. One of them asks for something rather than narrowing:
`ready_since: true` is set only for a run that tests a change once per
ready, and only then does the connector tell each change's `ready_since`,
since that may cost a request per change.

`from` says where a change comes from: `same` (the code's own repository),
`fork` or `unknown`. Only a change known to be `same` runs. `unknown`, and
any other value, counts as a fork, so code nobody vouched for never gets a
pipeline's credentials. A connector without the `forks` capability reports
every change as `unknown`.

`state` is one of the core's four: `pending`, `success`, `failure` and
`error`. The connector maps them to its host's names. `comment` keeps one
comment per change: it updates the oldest one whose body starts with
`marker` (on a line of its own, or alone), creates it when there is none,
and deletes the others that start with it. A comment that only quotes the
marker further down is someone else's and stays.

### Rounds

A call goes in rounds. Each round starts the connector once, as for any
other plugin, with this on stdin:

```json
{"protocol": 1, "work": "/w/changes", "config": {},
 "args": {"base": "main", "filters": {"draft": false}},
 "responses": [
   {"id": "page-1", "status": 200, "headers": {"Link": "<...>; rel=\"next\""},
    "body_file": "page-1.body"}
 ]}
```

and answers either with requests:

```json
{"requests": [
  {"id": "page-2", "method": "GET",
   "url": "https://api.example.com/repos/o/r/pulls?page=2",
   "headers": {"Accept": "application/json"},
   "auth": {"purpose": "changes"}}
]}
```

or with `{"done": <the call's answer>}`. The `config` is the connector's
own block of the config, with the code's remote added as `remote`, so
every call knows which repository it is about. The core fills that block
from the trusted config and the step's environment: the GitHub connector's
`api` is `PIPELINEGEN_GITHUB_API`, or GitHub's own. A plain git server has
no API to ask, so for the git connector the core lists the remote's
branches with git and its read credential, and puts them in the block as
`heads` (branch name to commit), beside the config's `branches` patterns
and `links`; the git connector answers from them without a request. The
core runs a change only at a branch git listed, at the commit git saw. A connector keeps no state between
rounds, so every round carries the call's arguments again, along with every
response from every round before it, in order. A body is a file in the
call's work directory, which every round shares, named by `body_file`. Following pages is just more
rounds. `describe`, `identify` and `links` answer `done` in the first
round, and the core refuses any request they make.

A request without `auth` goes out without a token. With `auth`, the core
puts the token for that purpose in the header `describe` names. The core
also owns retries: on a server error, or on one of the host's rate limits,
it waits and tries again, and the connector sees only the last response.
When a request still fails at the network level after its tries, the call
fails.

### `describe`

A connector's `describe` adds these to the common fields:

```json
{"protocol": 1, "kind": "connector", "name": "example-connector",
 "version": "v1.0.0",
 "capabilities": ["changes", "statuses", "comment", "draft", "labels",
                  "ready_since", "forks", "head_ref"],
 "api_hosts": ["https://api.example.com"],
 "request_headers": ["Accept"], "response_headers": ["Link"],
 "auth": {"header": "Authorization", "scheme": "Bearer"},
 "purposes": ["read", "changes", "report"], "git_user": "x-access-token",
 "limits": {"status_text_max": 140, "comment_max": 65536, "markdown": true},
 "states": {"failure": "failed"}}
```

- `capabilities` say what the host can tell. `draft`, `labels` and
  `ready_since` are the ones a run's filters need: a config whose run
  filters on drafts, labels or `once_per_ready` fails validation when its
  connector lacks the matching capability, because otherwise the run would
  test changes it means to skip.
- `api_hosts` are hosts besides the code remote's that requests may go to.
  The config has to name each one again before any request goes there.
- `request_headers` are the headers a request may set as literals.
  `response_headers` are the ones handed back; the core drops the rest.
- `auth` is where the token goes: `Authorization: Bearer <token>`, or
  `{"header": "PRIVATE-TOKEN"}` for a header holding the token alone.
- `purposes` are the purposes the connector needs a token for. The config
  maps each to a CI var. `read` is for git fetching the code, never for an
  API request. The `store` purpose belongs to the store, never to a
  connector.
- `git_user` is the user name a git fetch over https sends along with the
  read token.
- `limits` are what the host accepts: the longest status text and comment,
  and whether comments render Markdown and Mermaid. `states` map the core's
  states to the host's names where they differ.
- `accelerators` name, per runner, a resource of the CI system that notices
  a new change sooner than the sweep does. The runner reads it; the core
  doesn't.
- `fallback` is the request that sets a status, one per state, with
  `{commit}`, `{context}`, `{text}` and `{url}` filled in when the pipeline
  is made. A hook that reports a broken build sends it without running
  pipelinegen. It is checked like any other request.

### What the core refuses

The core checks every request before it sends anything, and fails the call
on the first one that breaks a rule:

- The URL is `https` (or `http` only for a host the config names), with no
  user name or password in it. Its scheme and host (with port) are the code
  remote's as `identify` found it, or one of `api_hosts` that the config
  also names.
- Headers are only the ones `request_headers` declares. `Authorization`,
  `Cookie`, `Host` and anything starting with `Proxy-` are refused as
  literals, in any case, even when declared, since only the core puts a
  credential on a request.
- `auth.purpose` is the call's own purpose from the table above. A call that
  reports can't carry the token that lists changes. None can carry the
  store's, or the read token: that one is git's, the only credential a task
  running a proposed change's code may hold, so it never reaches the API. A request whose purpose has no token in the config is refused,
  not sent without one.
- Each id is unique within the call and made of letters, digits, `.`, `_`
  and `-`.
- A call has at most 10 rounds, 20 requests per round and 100 requests in
  all. A request or response body over 10 MB fails the call instead of
  being cut short.
- A redirect is followed only to the same scheme and host. Any other
  redirect fails the request, and the token never goes along to another
  host, whatever header holds it.

## What stays in the core

- the config and its validation;
- Bazel and bazel-diff;
- listing slices;
- the stored graph and the walk over it;
- the plugins' answer cache;
- the narrowing level, the sample and the alarm;
- plans, refs, statuses and the comment;
- sending a connector's requests, with the tokens, retries and the checks
  on where they go;
- the commands a pipeline's steps call;
- the file-level check;
- the hole and over-selection comparison (not in a release yet), and the
  conformance kits;
- the lists in refs and their merge (observed unit ids per slice, whatever
  the namespace).
