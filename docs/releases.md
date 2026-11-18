# Releases

This page is for whoever cuts a release of pipelinegen or a plugin, or
pins one in a pipeline. pipelinegen and each of its plugins are released
the same way, by the release command of this repository, run in its
checkout:

```sh
bazel run //ci/release -- build   DIR TAG OUT    # build TAG of the checkout in DIR into OUT
bazel run //ci/release -- publish REPO TAG OUT   # attach OUT to TAG's GitHub release of REPO
bazel run //ci/release -- tested  REPO COMMIT CONTEXT...   # fail unless one of these statuses passed on COMMIT
bazel run //ci/release -- notice  DIR            # write the NOTICE the release of the checkout in DIR needs
bazel run //ci/release -- patch   DIR REPO TAG OUT   # tag main, build, publish and check by download
bazel run //ci/release -- tagged  SOURCE REPO OUT CONTEXT...   # the release job: tested, build and publish the head's version tag
```

The target builds the command with the Go SDK that `MODULE.bazel` pins
and runs it where `bazel run` was started, so relative paths mean what
they say. It puts that SDK first on `PATH`, so the release is built with
it too, and after it the gh that `MODULE.bazel` pins, which `tested`,
`publish` and `patch` call.

A release is made only from a commit that a run of every test passed on.
pipelinegen's everyday runs select the tests a change reaches, so only its
nightly, which selects every test, shows that all of them pass on a commit.
`tested` reads the commit's statuses and fails unless one of the contexts
named is `success`; the pipeline's `release` job asks for
`pipelinegen/nightly` before it builds. A candidate (`vX.Y.Z-rc.N`) is a
trial, not a release: a prerelease, never the latest, pinned only where it
is tried. It may come from a commit no nightly has passed yet, and the
version it leads to needs the nightly.

`build` clones the tagged commit out of DIR, so nothing uncommitted gets
in, and builds it twice in fresh directories. If the two builds differ in
a single byte, it stops. What it builds depends on the repository, and
NAME is the program it holds: a repository named `pipelinegen-KIND-NAME`,
KIND a plugin kind, holds `NAME-KIND` (`pipelinegen-sbom-go` builds
`go-sbom`), and any other repository, pipelinegen included, the program of
its own name:

| Repository | Files |
|---|---|
| a Go module with `./cmd/NAME` | `NAME-linux-amd64`, `NAME-linux-arm64`, `NAME-darwin-amd64` and `NAME-darwin-arm64`, static (`CGO_ENABLED=0`, `-trimpath`, no build id) |
| a Go module whose root package is `main` | the same, NAME being the program the module path's last element names rather than the repository's, so a renamed repository changes its name in `go.mod` alone |
| a Node package whose `build` script writes `dist/NAME.mjs` | `NAME.mjs`, one bundled file |

A Go module with `./cmd/NAME` releases every other main package under
`cmd/` beside it, each as a program of its directory's name, in the same
release and the same `SHA256SUMS`: a runner repository holding
`cmd/concourse-runner` and `cmd/piperun-runner` releases both. A
directory under `cmd/` that holds no main package is left out, and a
module with one program releases exactly what it always did.

The macOS builds are cross-built on Linux, and the release builds every
file twice and compares the bytes for them as for the others. No macOS
machine runs them in CI: a release says nothing about how they behave on
a Mac beyond that they build.

A Go module that declares `var Version = "dev"` at package level gets the
tag set into it. A Node package is built with the tag as its version.
A Node plugin's release holds the bundled command, and the files its
`package.json` names under `"pipelinegen": {"release": [paths]}`, each
under its base name and listed in `SHA256SUMS`: a file other projects take
without building, such as a test fixture, pinned by its checksum. A Go
module names such files in `.pipelinegen-release` at its root, one path a
line (`#` starts a comment), as the playwright driver does for the
`marks.mjs` a suite's own fixture imports. Each must be committed at the
tag, and the build must leave it exactly as committed; none may take the
name of a release file of its own. A package that uses the plugin's
library installs it from the tag, as a git dependency.

A plugin also gets `plugin.json`: its own answer to `describe`, with its
name, kind, protocol version, version and the runtime it needs
(`"needs": ["node>=22.18"]`). `toolchains` is left out: it says which
versions one run resolved on one machine, not what the release is. Each
other program whose name ends in a plugin kind (`-runner`, `-sbom`,
`-correlator`, `-driver` or `-connector`) gets `NAME.plugin.json` the same
way; one whose name doesn't is no plugin and is never asked. The build
fails when a plugin reports a version other than the tag, and when a
program's `version` command prints another. Every
release, pipelinegen's and each plugin's, Go or Node, also carries the
repository's `LICENSE` and `NOTICE`. `SHA256SUMS` lists every file, by
their letters and digits with case aside, as every release has listed
them.

The `NOTICE` must be the one `notice` writes for the release: the
programs' names, its copyright and licence, and each piece of third-party
software its files include, with that software's licence and its own
notices reproduced. For Go, that is every module linked into any of the released
programs on any of the four platforms, and the Go standard library of the
toolchain; the family's own modules are named in the opening sentence
instead, under the repository that holds each now. A module that embeds
code of other forms says so too: go-pgquery's PostgreSQL parser brings
protobuf-c, xxHash and wasi-libc, whose licences `notice` takes from the
parser's sources and, for wasi-libc, fetches once from GitHub into
`$NOTICE_UPSTREAM` (`~/.cache/notice-upstream`). For Node, it is every npm
package the bundle includes, as esbuild's metafile names them for the
builds `package.json` lists under `"pipelinegen": {"notice": [options]}`
(esbuild build options; `[]` for a bundle that includes no npm package).
A package without that key is refused, since what its `NOTICE` covers
can't be told. The build refuses a `NOTICE` that differs; run `notice`,
commit what it wrote, and tag again.

`publish` checks the files against `SHA256SUMS` first. It needs a pushed
tag and a GitHub login with Contents write on the repository (`gh`'s own,
or `GH_TOKEN`). It never changes a release that exists, since configs pin
releases by checksum: when the tag is released already, it succeeds if the
release holds exactly this build and fails if it doesn't.

## Cutting a release

For this repository, a version tag (`v1.2.3`) pushed on `main` starts the
`release` job of its pipeline (`ci/root.yml`, `ci/release.yml`), which
runs `tagged` with the release command from `main`: it takes the version
tag on the tagged commit, a candidate's never, checks that the nightly
passed there, builds and publishes. The job gets bazelisk from Go's module
proxy, and Bazel builds the command without the token, which only the
command's own process gets. A repository without a
pipeline is released by hand. So is this one when the job can't run soon:
pause the `release` job, publish by hand, then unpause it, and its build
confirms the release ("released with exactly these files"). Without the
pause, the job and the hand publish can both find no release and both
try to create it.

By hand, in a fresh clone of the repository (never a working checkout),
with the commands run in a checkout of pipelinegen's `main`:

1. Check out the commit to release, detached, and make sure it is on
   `main`. For a pipelinegen release (not a candidate), `bazel run
   //ci/release -- tested sideways-works/pipelinegen <commit>
   pipelinegen/nightly` must pass: a commit the nightly hasn't run waits
   for it.
2. Run the repository's checks. For Go, `go test ./...`
   and `golangci-lint run` in the root module and again in each nested
   module released with it, since `./...` stops at a nested `go.mod`. A
   Node package runs its `test` and `lint` scripts. golangci-lint won't start
   while another run on the machine holds its lock;
   `--allow-parallel-runners` lets it run.
3. For a plugin, run the conformance kit with pipelinegen built from
   `main`: `pipelinegen plugin test <executable> --tree <fixture>`. A plugin
   that type-checks Go needs the Go version its fixture's `go.mod` asks
   for first on `PATH`. When the fixture has third-party modules, it also
   needs `--cache <dir>` with them in `<dir>/gomodcache` (run
   `GOMODCACHE=<dir>/gomodcache go mod download` in the fixture).
4. Make sure the commit has `LICENSE` and the `NOTICE` that `bazel run
   //ci/release -- notice <clone>` writes, and, in a repository
   with a `CHANGELOG.md` (pipelinegen's), the release's entry there: a
   heading `## [X.Y.Z] - <date>`, or for a candidate that heading or
   `## [Unreleased]`. The build refuses a tag without them, and releases
   `LICENSE` and `NOTICE` beside the programs. A repository without a
   `CHANGELOG.md`, as a plugin's, is released without that check.
5. Tag the commit `vX.Y.Z`, and each nested module released with it
   `SUB/vX.Y.Z` on the same commit.
6. Build with `bazel run //ci/release -- build <clone> vX.Y.Z <out>`. For a Node
   package, write down the Node version: the build uses the `node` on
   `PATH`, and only the same version is sure to give the same bytes.
7. Push the tags, then run `bazel run //ci/release -- publish owner/name
   vX.Y.Z <out>`. A `gh` login is enough, so leave `GH_TOKEN` unset instead of
   putting a token on a command line.
8. Download the release into an empty directory, run
   `sha256sum -c SHA256SUMS` there, and compare its `SHA256SUMS` with the
   build's.
9. After a release of pipelinegen, go-sbom or the Go correlator, and once
   the `newest` proof has passed with it, move
   [ci/proof/pins](../ci/proof/pins) to it (see
   [the proof](proof.md)).

`patch` takes steps 5 to 8 for a release of `main`'s head, once steps 1
to 4 hold: `bazel run //ci/release -- patch <clone> owner/name vX.Y.Z
<out>`. It fetches `origin` in the clone and refuses a `main` that doesn't
fast-forward to `origin/main` or a clone with changes not committed. It
tags `main`'s head and each nested module released with it, unless the
release's tag exists, which never moves, and pushes those tags alone, on
every run. It builds into
`<out>`, or keeps the build there from an earlier try, publishes it,
downloads the release into a directory of its own, checks the files
against their `SHA256SUMS` and that file against the build's, and prints
the checksum of `SHA256SUMS`. Run it again after a failure: it picks up
where it stopped.

A pushed tag is never moved or deleted, and a published release is never
changed. A wrong release gets a new version.

A release candidate is tagged `vX.Y.Z-rc.N` and built and published the
same way. `publish` makes it a prerelease that is not marked latest, so
a lookup of the newest release never finds it. The pipeline's `release`
job takes only `vX.Y.Z` tags, so a candidate is always published by hand.

A Node package with a private git dependency gets it through git when
`npm ci` runs, so git needs credentials for https from a credential
helper; a token never goes in the URL. Lockfiles often record `git+ssh://` URLs; a
`url.https://github.com/.insteadOf ssh://git@github.com/` rule sends those
over https. The build installs with `--ignore-scripts`, so a git
dependency's `prepare` script doesn't run: the package must build from the
files the dependency's tag holds.

## Changing what the pipeline's steps call

The generated pipeline and the repository's task scripts run the pinned
release, never `main`. So a change to the commands those steps call (a new
subcommand a batch task must call, a flag, an output it reads) ships in one
step: the code lands, a release is cut from it, and the same commit that
moves the pin to that release changes the scripts. A script that calls
something the pinned release lacks fails the batch, and a pin that moves
without its scripts can break them the same way.

Before a pin moves, compare the task scripts (`ci/` and the example's
`ci/`) at the release's tag with `main`: a script changed after the tag
may call something the release lacks. When the tool and the runner plugin
each need the other's new release (a config key the tool reads and a step
the runner writes for it), both pins move in the same commit. Then
generate the pipeline with the files of the pinned releases and check it
with `fly validate-pipeline --strict` before anyone sets it.

A config key the previous release requires is accepted for one more
release, with a warning that says what replaces it, and refused only in
the release after that. Then a pin and the tree that pins it are never
incompatible in both directions at once: the tree that moves the pin
can drop the key in the same commit, and the old pin still reads the new
tree.

Two keys are in that state: `concourse.vars` (`pipelinegen pipeline
--vars` lists what the pipeline needs instead) and the `learn` section
(nothing reads it any more; recorded suites bind through plugins). Each is
accepted with a warning that says it will be refused.

The steps of a suite job read the tested commit's config, and planning
reads the base's, as history: a key the running release doesn't know or
no longer takes, and a rule the file breaks, only warn there. Only the
head's config, which planning and `pipelinegen pipeline` read, is held to
every rule.

## Using a pinned version

A repository's config pins pipelinegen and every plugin by version and by
the checksum of its linux-amd64 file (the `.mjs` for a Node plugin), the
matching line of that release's `SHA256SUMS`. Or it pins the release by the
checksum of its `SHA256SUMS` itself, with `sums` in place of `sha256`: one
pin then serves every platform, a Mac's binary checked by its own line of
the pinned list:

```yaml
concourse:
  planner:
    bootstrap:
      release: {version: v0.2.0, sha256: <pipelinegen-linux-amd64's sum>}
plugins:
  - name: concourse
    kind: runner
    repository: sideways-works/pipelinegen-runner-concourse
    release: {version: v0.2.0, sha256: <concourse-runner-linux-amd64's sum>}
  - name: go
    kind: correlator
    repository: sideways-works/pipelinegen-correlator-go
    release: {version: v0.2.0, sums: <the sum of the release's SHA256SUMS>}
```

The bootstrap keeps a release's `SHA256SUMS` beside the plugin as
`<name>.SHA256SUMS`, which is how `pipelinegen check` and the hook check a
plugin pinned by `sums` before they ask it anything.

The generated pipeline's bootstrap step fetches each file and checks it.
A task of your own fetches one like this. While the repositories are
private, the request needs a token that can read their contents, sent in a
header and never in the URL:

```sh
version=v1.2.3
sha256=<the pipelinegen-linux-amd64 line of SHA256SUMS>
api=https://api.github.com/repos/sideways-works/pipelinegen

# the release's asset URL for the binary
asset=$(curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" "$api/releases/tags/$version" |
	awk -F'"' '/"url": ".*\/releases\/assets\// { url = $4 } /"name": "pipelinegen-linux-amd64"/ { print url; exit }')

curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" -H "Accept: application/octet-stream" \
	-o /usr/local/bin/pipelinegen "$asset"
echo "$sha256  /usr/local/bin/pipelinegen" | sha256sum -c -
chmod +x /usr/local/bin/pipelinegen
```

With the GitHub CLI instead: `GH_TOKEN=$GITHUB_TOKEN gh release download
"$version" -R sideways-works/pipelinegen -p pipelinegen-linux-amd64`, then
the same `sha256sum -c`.

A checksum that doesn't match fails the task. Someone with write access
can replace a release's assets, but not the checksum pinned in your
pipeline.

## Secrets and the pipeline job

Leave `concourse.self_set` off on a Concourse without a credential manager
or `var_sources`, and set the pipeline by hand; [secrets](how-to/secrets.md)
says why.

## Known gap

The image of the pull request resource type is pinned by tag only.
Concourse has no field to pin a resource type's image by digest: a
`version` on a resource type is rejected by `fly validate-pipeline
--strict`, and a plain `set-pipeline` drops it without a word. The
registry-image source takes no digest either.
